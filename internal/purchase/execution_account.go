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
