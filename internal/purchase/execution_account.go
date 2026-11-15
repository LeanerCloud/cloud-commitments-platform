package purchase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/execution"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/providerfactory"
)

// Skip-row notes, shared with the stored-error allowlist (errnotes.go).
const (
	skippedAccountRetryNote   = "the run ended before this account's purchase started, so nothing was submitted; retry it to buy it"
	skippedAccountNoRetryNote = "the run ended before this account's purchase started, so nothing was submitted; this purchase type cannot be retried automatically, so start a new purchase for this account if it is still wanted"
)

// skippedAccountNote words the skip row by whether Retry accepts it. The row
// must already be failed and NoSubmission; it inherits the root's
// RetryAttemptN, so a retried root's skip rows stay refused and point at a
// new purchase instead.
func skippedAccountNote(row *config.PurchaseExecution) string {
	if RetryRefusalReason(row) != "" {
		return skippedAccountNoRetryNote
	}
	return skippedAccountRetryNote
}

// skippedAccountRows builds a failed row for each account the fan-out skipped
// before executeForAccount ran (the run context ended first), so the acked
// partial run does not silently drop its purchases (issue #1672). Nothing was
// submitted for it, so the row carries NoSubmission. Only the acked partial
// path calls this: a flat failure finalizes the root as failed, and a root
// Retry re-runs every account. It only builds the rows; executeAndFinalize
// saves them after the root, on the root's terminal-save deadline.
func skippedAccountRows(scoped map[string]*config.PurchaseExecution, results []execution.Result[bool], started *sync.Map) []*config.PurchaseExecution {
	var rows []*config.PurchaseExecution
	for _, r := range results {
		if r.Err == nil {
			continue
		}
		if _, ran := started.Load(r.AccountID); ran {
			continue
		}
		row := newAccountExecution(scoped[r.AccountID], r.AccountID)
		row.Status = "failed"
		row.NoSubmission = true
		row.Error = skippedAccountNote(&row)
		logging.Warnf("execution %s: account %s skipped before it started: %v", row.ExecutionID, r.AccountID, r.Err)
		rows = append(rows, &row)
	}
	return rows
}

// persistFailedAccountExecution writes a per-account execution row that the
// caller has already stamped with Status="failed" + Error, and joins any save
// failure with the originating cause via errors.Join so neither error is
// silently dropped (the row is as much audit trail as a success row; issue
// #1184 / COR-10). Extracted from executeForAccount so the new branch keeps
// that function under the gocyclo:10 budget.
func (m *Manager) persistFailedAccountExecution(ctx context.Context, acctExec *config.PurchaseExecution, account config.CloudAccount, cause error) error {
	// Stamped before the save so the row that lands carries the proof; a failed
	// save loses the row and strips the class below.
	markRefusedAccountRow(acctExec, refusedBeforeSubmission(cause))
	if saveErr := m.saveAccountRow(ctx, acctExec); saveErr != nil {
		// The cause is joined as text: a lost audit row means the outcome is
		// unknown, so a before-submission class on the cause must not survive
		// into the fan-out proof (errors.As walks every Join branch).
		return errors.Join(
			fmt.Errorf("AUDIT LOSS: failed to save execution record for account %s: %w", account.ID, saveErr),
			errors.New(cause.Error()),
		)
	}
	return cause
}

// applyAccountOutcome stamps the final Status / Error / CompletedAt on acctExec
// from the per-rec purchase outcome and reports whether the run was a partial
// success and whether any rec committed.
//
// A per-account run can be a partial success (#642): some recs committed to
// purchase_history (Purchased=true) while others failed. Marking the row
// "failed" in that case is wrong (it hides the real commitments and invites a
// re-approve that double-buys), so record "partially_completed" instead and let
// the caller still send the confirmation for the recs that did purchase.
//
// committed is the gate executeMultiAccount uses to distinguish a partial run
// from a flat failure (#1014): true when any rec purchased.
func applyAccountOutcome(acctExec *config.PurchaseExecution, purchaseErrors []string) (partial, committed bool) {
	committed = anyRecPurchased(acctExec.Recommendations)
	partial = len(purchaseErrors) > 0 && committed
	switch {
	case partial:
		now := time.Now()
		acctExec.Status = "partially_completed"
		// Append so any audit-gap note already stamped by
		// aggregatePurchaseOutcomes (issue #621) survives alongside the
		// per-rec failure list.
		acctExec.Error = appendErrNote(acctExec.Error, strings.Join(purchaseErrors, "; "))
		acctExec.CompletedAt = &now
	case len(purchaseErrors) > 0:
		acctExec.Status = "failed"
		acctExec.Error = appendErrNote(acctExec.Error, strings.Join(purchaseErrors, "; "))
	default:
		now := time.Now()
		acctExec.Status = "completed"
		acctExec.CompletedAt = &now
	}
	return partial, committed
}

// partialPurchaseError is the sentinel returned by the single-account
// execution path when at least one rec purchased and at least one failed
// (#642). finalizeExecution detects it via errors.As and records the row as
// "partially_completed" rather than "failed", so the real commitments stay
// visible and a re-approve can't double-buy them. The successful recs'
// confirmation email has already been sent by the time this error surfaces.
type partialPurchaseError struct {
	errors []string
}

func (e *partialPurchaseError) Error() string {
	return "some purchases failed (partial success): " + strings.Join(e.errors, "; ")
}

// getMaxAccountParallelism is a thin alias over the shared
// execution.ConcurrencyFromEnv so the purchase manager and the scheduler
// both honor the same CUDLY_MAX_ACCOUNT_PARALLELISM override.
func getMaxAccountParallelism() int {
	return execution.ConcurrencyFromEnv()
}

// resolveAccountProvider returns a *ProviderConfig with a pre-authenticated provider
// for the given account. Returns an error if credential resolution fails -- callers
// must NOT fall back to ambient credentials on error.
func (m *Manager) resolveAccountProvider(ctx context.Context, account config.CloudAccount) (*provider.Config, error) {
	t0 := time.Now()
	logging.Infof("purchase[resolveAccountProvider]: resolving credentials for provider=%s account=%s",
		account.Provider, account.ID)
	var cfg *provider.Config
	var err error
	switch account.Provider {
	case "aws":
		cfg, err = m.resolveAWSProvider(ctx, account)
	case "azure":
		cfg, err = m.resolveAzureProvider(ctx, account)
	case "gcp":
		cfg, err = m.resolveGCPProvider(ctx, account)
	default:
		return nil, fmt.Errorf("credentials: unknown cloud provider %q for account %s", account.Provider, account.ID)
	}
	if err != nil {
		logging.Errorf("purchase[resolveAccountProvider]: credential resolution failed for provider=%s account=%s after %s: %v",
			account.Provider, account.ID, time.Since(t0), err)
		return nil, err
	}
	logging.Infof("purchase[resolveAccountProvider]: credentials resolved for provider=%s account=%s in %s",
		account.Provider, account.ID, time.Since(t0))
	return cfg, nil
}

func (m *Manager) resolveAWSProvider(ctx context.Context, account config.CloudAccount) (*provider.Config, error) {
	t0 := time.Now()
	logging.Infof("purchase[resolveAWSProvider]: resolving AWS credentials for account=%s authMode=%s",
		account.ID, account.AWSAuthMode)
	if account.AWSAuthMode != "access_keys" && m.assumeRoleSTS == nil {
		return nil, fmt.Errorf("credentials: STS client not configured for non-access_keys mode (account %s)", account.ID)
	}
	awsCreds, err := credentials.ResolveAWSCredentialProviderWithOpts(ctx, &account, m.credStore, m.assumeRoleSTS,
		credentials.AWSResolveOptions{
			AccountLookup:    credentials.AccountLookupFromStore(m.config),
			STSClientFactory: m.bastionSTS,
			AmbientProvider:  m.ambientAWSCreds,
			AmbientSTS:       m.stsClient,
		})
	if err != nil {
		logging.Errorf("purchase[resolveAWSProvider]: failed for account=%s after %s: %v",
			account.ID, time.Since(t0), err)
		return nil, fmt.Errorf("credentials: resolve AWS for account %s (%s): %w", account.ID, account.Name, err)
	}
	logging.Infof("purchase[resolveAWSProvider]: AWS credentials resolved for account=%s in %s",
		account.ID, time.Since(t0))
	return &provider.Config{Name: "aws", AWSCredentialsProvider: awsCreds}, nil
}

// resolveSingleAccountProvider derives per-account credentials for the
// single-account execution path. Returns (nil, purchaseTarget{}, nil) when no
// account can be identified (ambient credentials are used in that case), or
// when no recs are selected (approval-only flows where credentials are not
// needed).
//
// The second return value carries the resolved target account's ExternalID —
// the provider-appropriate account identifier (AWS account number, Azure
// subscription, GCP project) that the caller stamps onto purchase_history
// (#646) — and its AWS partition (issue #1194). The ExternalID is "" when no
// target account could be identified, signaling the caller to fall back to
// the ambient AWS STS identity.
//
// The account is taken from exec.CloudAccountID when set (plan-with-single-
// account executions), or derived from the shared cloud_account_id of the
// SELECTED recommendations (direct-execute purchases where PlanID is empty and
// exec.CloudAccountID is nil). When the selected recs span more than one
// account, or mix attributed and unattributed recs, this returns
// errAmbiguousAccountScope: the single-account path cannot honor such a
// batch and must never fall back to ambient credentials for it (#1902).
//
// A non-nil error means a target account was identified but could not be
// resolved (lookup failed, the account does not exist, or credentials could
// not be derived); the caller must NOT fall back to ambient credentials on
// error, as that would purchase/stamp against the wrong account (#646).
func (m *Manager) resolveSingleAccountProvider(ctx context.Context, exec *config.PurchaseExecution) (*provider.Config, purchaseTarget, error) {
	// Skip credential resolution when nothing is selected. Approval-only
	// flows may carry an account ID on the recs solely for the contact-email
	// gate; attempting resolution there would fail on accounts with no
	// provider field set and add a pointless DB round-trip.
	if len(selectedIndices(exec.Recommendations)) == 0 {
		return nil, purchaseTarget{}, nil
	}

	cloudAccountID := exec.CloudAccountID
	if cloudAccountID == nil {
		var scopeErr error
		cloudAccountID, scopeErr = SingleCloudAccountIDFromRecs(exec.Recommendations)
		if scopeErr != nil {
			return nil, purchaseTarget{}, fmt.Errorf("execution %s: %w", exec.ExecutionID, scopeErr)
		}
	}
	if cloudAccountID == nil {
		return nil, purchaseTarget{}, nil
	}

	account, err := m.config.GetCloudAccount(ctx, *cloudAccountID)
	if err != nil {
		return nil, purchaseTarget{}, fmt.Errorf("credential resolution failed for account %s: %w", *cloudAccountID, err)
	}
	if account == nil {
		// A target account was identified but does not exist in config.
		// Returning ("") here would let the caller fall back to ambient AWS
		// credentials and purchase/stamp against the wrong account (#646), so
		// surface an error instead; the contract above forbids ambient
		// fallback once a target account ID is known.
		return nil, purchaseTarget{}, fmt.Errorf("credential resolution failed for account %s: account not found", *cloudAccountID)
	}
	provCfg, err := m.resolveAccountProvider(ctx, *account)
	if err != nil {
		return nil, purchaseTarget{}, fmt.Errorf("credential resolution failed for account %s: %w", *cloudAccountID, err)
	}
	return provCfg, purchaseTarget{externalID: account.ExternalID, awsPartition: account.AWSPartition()}, nil
}

func (m *Manager) resolveAzureProvider(ctx context.Context, account config.CloudAccount) (*provider.Config, error) {
	t0 := time.Now()
	logging.Infof("purchase[resolveAzureProvider]: resolving Azure credentials for account=%s authMode=%s",
		account.ID, account.AzureAuthMode)
	if account.AzureAuthMode != "managed_identity" && m.credStore == nil {
		return nil, fmt.Errorf("credentials: credential store required for non-managed_identity Azure account %s", account.ID)
	}
	azCred, err := credentials.ResolveAzureTokenCredentialWithOpts(ctx, &account, m.credStore, credentials.AzureResolveOptions{
		Signer:    m.oidcSigner,
		IssuerURL: m.oidcIssuerURL,
	})
	if err != nil {
		logging.Errorf("purchase[resolveAzureProvider]: token credential resolution failed for account=%s after %s: %v",
			account.ID, time.Since(t0), err)
		return nil, fmt.Errorf("credentials: resolve Azure for account %s (%s): %w", account.ID, account.Name, err)
	}
	azProv, err := providerfactory.NewAzure(account.AzureSubscriptionID, azCred)
	if err != nil {
		logging.Errorf("purchase[resolveAzureProvider]: Azure provider construction failed for account=%s after %s: %v",
			account.ID, time.Since(t0), err)
		return nil, fmt.Errorf("credentials: create Azure provider for account %s (%s): %w", account.ID, account.Name, err)
	}
	logging.Infof("purchase[resolveAzureProvider]: Azure credentials resolved for account=%s in %s",
		account.ID, time.Since(t0))
	return &provider.Config{ProviderOverride: azProv}, nil
}

func (m *Manager) resolveGCPProvider(ctx context.Context, account config.CloudAccount) (*provider.Config, error) {
	t0 := time.Now()
	logging.Infof("purchase[resolveGCPProvider]: resolving GCP credentials for account=%s authMode=%s",
		account.ID, account.GCPAuthMode)
	if account.GCPAuthMode != "application_default" && m.credStore == nil {
		return nil, fmt.Errorf("credentials: credential store required for non-ADC GCP account %s", account.ID)
	}
	gcpTS, err := credentials.ResolveGCPTokenSourceWithOpts(ctx, &account, m.credStore, credentials.GCPResolveOptions{
		Signer:    m.oidcSigner,
		IssuerURL: m.oidcIssuerURL,
	})
	if err != nil {
		logging.Errorf("purchase[resolveGCPProvider]: token source resolution failed for account=%s after %s: %v",
			account.ID, time.Since(t0), err)
		return nil, fmt.Errorf("credentials: resolve GCP for account %s (%s): %w", account.ID, account.Name, err)
	}
	if gcpTS == nil {
		// ADC mode: no explicit token source -- use ambient credentials.
		logging.Infof("purchase[resolveGCPProvider]: GCP ADC mode for account=%s (ambient credentials) in %s",
			account.ID, time.Since(t0))
		return nil, nil
	}
	gcpProv, err := providerfactory.NewGCP(account.GCPProjectID, gcpTS)
	if err != nil {
		logging.Errorf("purchase[resolveGCPProvider]: GCP provider construction failed for account=%s after %s: %v",
			account.ID, time.Since(t0), err)
		return nil, fmt.Errorf("credentials: create GCP provider for account %s (%s): %w", account.ID, account.Name, err)
	}
	logging.Infof("purchase[resolveGCPProvider]: GCP credentials resolved for account=%s in %s",
		account.ID, time.Since(t0))
	return &provider.Config{ProviderOverride: gcpProv}, nil
}
