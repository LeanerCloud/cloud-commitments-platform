package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	awsrecommendations "github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
	azureprovider "github.com/LeanerCloud/cloud-commitments-go/providers/azure"
	gcpprovider "github.com/LeanerCloud/cloud-commitments-go/providers/gcp"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/execution"
	"golang.org/x/sync/errgroup"
)

// collectProviderRecommendations collects recommendations from a specific
// provider. The succeededAccountIDs return value is the per-account roster
// that completed in this run; the scheduler threads it into
// []config.SuccessfulCollect for account-scoped eviction. For the AWS
// ambient path (no registered accounts) the slice contains a single empty
// string sentinel that expandSuccessfulCollects translates into a nil
// CloudAccountID. The *sweepGap is non-nil when some account's sweep was
// incomplete (its rows are kept; see tolerateIncompleteSweep). Provider-level
// errors propagate unchanged.
//
// A name that is not one of the known providers (e.g. "AWS" or "azure " left
// in EnabledProviders) is an error, so the merge records it as a failed
// provider and persistCollection writes last_collection_error. Returning nil
// here reported a provider that was never contacted as a clean, successful
// collection of zero recommendations (issue #111).
func (s *Scheduler) collectProviderRecommendations(ctx context.Context, providerName string, globalCfg *config.GlobalConfig) ([]config.RecommendationRecord, []string, *sweepGap, error) {
	switch providerName {
	case "aws":
		return s.collectAWSRecommendations(ctx, globalCfg)
	case "azure":
		return s.collectAzureRecommendations(ctx, globalCfg)
	case "gcp":
		return s.collectGCPRecommendations(ctx, globalCfg)
	default:
		return nil, nil, nil, fmt.Errorf("unknown provider %q (valid: aws, azure, gcp)", providerName)
	}
}

// ambientEvictionRoster builds the succeeded-account roster for an ambient
// (unregistered-credential) collection. An incomplete sweep yields an empty
// roster, so UpsertRecommendations evicts nothing for it and the
// subscriptions the sweep never queried keep their previous rows -- see
// tolerateIncompleteSweep and issue #1652.
//
// accountID is "" when the host identity did not resolve to a registered
// cloud_accounts row; that empty-string sentinel is what
// expandSuccessfulCollects maps to a nil CloudAccountID.
func ambientEvictionRoster(complete bool, accountID string) []string {
	if !complete {
		return nil
	}
	return []string{accountID}
}

// expandSuccessfulCollects converts the per-account ID slice returned by
// each provider collect into the typed []SuccessfulCollect the persist
// layer expects. The empty-string sentinel is translated into a nil
// CloudAccountID so the eviction's account_key join uses uuid.Nil
// (matching the AWS ambient-credential path).
func expandSuccessfulCollects(providerName string, accountIDs []string) []config.SuccessfulCollect {
	out := make([]config.SuccessfulCollect, 0, len(accountIDs))
	for _, id := range accountIDs {
		sc := config.SuccessfulCollect{Provider: providerName}
		if id != "" {
			id := id // pin
			sc.CloudAccountID = &id
		}
		out = append(out, sc)
	}
	return out
}

// collectAWSRecommendations fans out across all enabled AWS accounts.
// If no accounts are registered and CUDly runs on AWS, it falls back to
// ambient credentials (backward compatibility with single-account setups).
// Returns the merged recommendations + the IDs of accounts that succeeded
// (or [""] for the ambient path so the caller can synthesize a nil
// CloudAccountID for eviction).
func (s *Scheduler) collectAWSRecommendations(ctx context.Context, globalCfg *config.GlobalConfig) ([]config.RecommendationRecord, []string, *sweepGap, error) {
	accounts, err := s.enabledAccounts(ctx, "aws")
	if err != nil {
		return nil, nil, nil, err
	}

	// Backward-compatible fallback: no registered accounts → ambient credentials
	if len(accounts) == 0 {
		recs, gap, err := s.collectAWSAmbient(ctx, globalCfg)
		if err != nil {
			return nil, nil, nil, err
		}
		// Issue #604: if the Lambda's STS identity matches a registered
		// (possibly disabled) cloud_accounts row, tag every rec with that
		// account's UUID so the approve modal renders the account name
		// instead of `(ambient)`. Best-effort — STS or store errors must
		// NOT fail the collection; we just keep the pre-fix nil tagging
		// in those cases.
		if acctID := s.resolveAmbientHostAccountID(ctx); acctID != "" {
			recs = s.tagAccount(recs, acctID)
			return recs, ambientEvictionRoster(!gap.holdsEviction(), acctID), gap.forAccount().withHeld(acctID), nil
		}
		return recs, ambientEvictionRoster(!gap.holdsEviction(), ""), gap.forAccount(), nil
	}

	recs, outcome := fanOutPerAccount(ctx, "AWS", accounts, func(ctx context.Context, acct config.CloudAccount) ([]config.RecommendationRecord, *sweepGap, error) {
		return s.collectAWSForAccount(ctx, globalCfg, acct)
	})
	if outcome.FailedCount == len(accounts) && len(accounts) > 0 {
		return nil, nil, nil, errAllAccountsFailed("AWS", outcome)
	}
	return recs, outcome.SucceededAccountIDs, outcome.Gap, nil
}

// accountOutcome is the per-provider summary returned by
// fanOutPerAccount alongside the merged recommendations slice. The
// caller uses this to decide whether the provider's collection
// succeeded overall: if every registered account failed
// (FailedCount == len(accounts) > 0) the caller should surface an
// error so the provider lands in failedProviders + the freshness
// banner shows the failure. Partial success (some accounts succeeded)
// stays a success at the provider level — the per-account [ERROR]
// logs are the operator signal for the failed minority.
//
// SucceededAccountIDs lists the IDs of accounts whose collection
// completed IN FULL; the scheduler threads these into the
// []SuccessfulCollect slice that drives UpsertRecommendations'
// account-scoped eviction. Order is not preserved — the eviction
// query treats it as a set.
//
// IncompleteAccountIDs lists accounts that returned usable rows from a
// sweep that did not cover every subscription it was asked to. They are
// counted in SucceededCount (their rows are kept and upserted, and they
// must not trip the all-accounts-failed guard) but are deliberately
// absent from SucceededAccountIDs, because eviction is keyed on the
// account while a sweep fails per subscription; evicting on a partial
// sweep deletes the un-queried subscriptions' previous rows. See
// tolerateIncompleteSweep and issue #1652.
//
// Gap is the merged incomplete-sweep summary across IncompleteAccountIDs
// (nil when every succeeded account swept completely), surfaced in
// recommendations_state.last_collection_partial (issue #87).
type accountOutcome struct {
	Gap                  *sweepGap
	LastErr              string
	SucceededAccountIDs  []string
	IncompleteAccountIDs []string
	SucceededCount       int
	FailedCount          int
}

// fanOutPerAccount runs fn concurrently across accounts, bounded by the
// shared CUDLY_MAX_ACCOUNT_PARALLELISM env var. Per-account errors are
// logged and the run continues — the stale-row preservation in the
// UpsertRecommendations write path (see commit "parallelise collection")
// keeps those accounts' older rows visible. Order is not preserved; the
// SQL read in ListStoredRecommendations re-sorts anyway.
//
// Returns the merged recommendations + an accountOutcome the caller
// uses to detect "all accounts failed" → return error → provider
// flagged as failed in CollectRecommendations.
func fanOutPerAccount(
	ctx context.Context,
	providerLabel string,
	accounts []config.CloudAccount,
	fn func(ctx context.Context, acct config.CloudAccount) ([]config.RecommendationRecord, *sweepGap, error),
) ([]config.RecommendationRecord, accountOutcome) {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(execution.ConcurrencyFromEnv())

	// One mutex guards both the recs accumulator AND the outcome
	// counters — the same critical section, no second mutex needed.
	var mu sync.Mutex
	var all []config.RecommendationRecord
	var outcome accountOutcome

	for i := range accounts {
		acct := accounts[i]
		g.Go(func() error {
			recs, gap, err := fn(gctx, acct)
			if err != nil {
				if isAccountPermissionError(providerLabel, err) {
					// Operator-fixable misconfiguration (missing IAM role
					// on the deploy SA): log at WARN so a single
					// misconfigured account doesn't drown out other log
					// signals. The collection still counts as a per-account
					// failure — provider-success semantics are unchanged.
					logging.Warnf("%s account %s (%s) permission gap (operator action needed: grant required IAM role): %v",
						providerLabel, acct.Name, acct.ExternalID, err)
				} else {
					logging.Errorf("%s account %s (%s): %v", providerLabel, acct.Name, acct.ExternalID, err)
				}
				mu.Lock()
				outcome.FailedCount++
				outcome.LastErr = fmt.Sprintf("account %s (%s): %v", acct.Name, acct.ExternalID, err)
				mu.Unlock()
				return nil // never fail the whole group — partial is fine
			}
			mu.Lock()
			all = append(all, recs...)
			// A partially-swept account counts as succeeded -- its rows are
			// kept and upserted, and it must not trip the all-accounts-failed
			// guard -- but it is deliberately withheld from
			// SucceededAccountIDs, the roster that authorizes stale-row
			// eviction. See tolerateIncompleteSweep for why (issue #1652).
			outcome.SucceededCount++
			if gap.holdsEviction() {
				outcome.IncompleteAccountIDs = append(outcome.IncompleteAccountIDs, acct.ID)
			} else {
				outcome.SucceededAccountIDs = append(outcome.SucceededAccountIDs, acct.ID)
			}
			outcome.Gap = mergeSweepGaps(outcome.Gap, gap.forAccount().withHeld(acct.ID))
			mu.Unlock()
			return nil
		})
	}
	if waitErr := g.Wait(); waitErr != nil {
		// Goroutines return nil to isolate per-account failures; non-nil is unexpected.
		logging.Warnf("fanOutPerAccount: errgroup.Wait returned unexpected error: %v", waitErr)
	}
	return all, outcome
}

// isAccountPermissionError reports whether err from a per-account
// collection call represents an operator-fixable IAM permission gap
// (rather than a genuine collector error). Used by fanOutPerAccount to
// downgrade the log severity from ERROR to WARN — see issue #57: a
// single misconfigured GCP account otherwise emits an [ERROR] line on
// every scheduler tick (~15 min) and drowns out other log signals.
//
// Currently dispatches per provider:
//   - "GCP": gcpprovider.IsPermissionError (HTTP 403 / gRPC PermissionDenied)
//   - other providers: false (existing ERROR behavior preserved until
//     analogous predicates are added for AWS/Azure)
func isAccountPermissionError(providerLabel string, err error) bool {
	if err == nil {
		return false
	}
	switch providerLabel {
	case "GCP":
		return gcpprovider.IsPermissionError(err)
	default:
		return false
	}
}

// errAllAccountsFailed wraps an accountOutcome's LastErr so callers
// (the per-provider collect funcs) can surface a single error to
// CollectRecommendations when every registered account failed.
func errAllAccountsFailed(providerLabel string, outcome accountOutcome) error {
	return fmt.Errorf("%s: all %d accounts failed; last error: %s",
		providerLabel, outcome.FailedCount, outcome.LastErr)
}

// tolerateIncompleteSweep converts an incomplete recommendation collection
// into a warning and a nil error, so the caller keeps the recommendations that
// WERE collected instead of discarding them.
//
// Every recommendation-fetch site in this file has the shape
// `recs, err := ...; if err != nil { return nil, ... }`, which throws the
// results away. That is correct for a real error, but for a partial sweep it
// would turn one flaky subscription out of fifty into a total collection
// outage -- strictly worse than the silent under-collection that
// PartialSubscriptionFailureError exists to prevent. Routing every site
// through this helper keeps the policy in one place rather than relying on
// each call site to remember it.
//
// The failed subscription IDs are named in the log so an operator can tell an
// under-collected sweep from a genuinely shrinking savings opportunity. Any
// other error is returned unchanged, preserving the existing fail-loud
// behavior.
//
// The gap return value is what stops a tolerated sweep from being mistaken
// for a whole one. It is nil for a complete sweep (and for a hard error, which
// callers handle via err first); non-nil only for a partial sweep, and callers
// must keep such an account OUT of the succeeded-account roster: that roster
// drives UpsertRecommendations' stale-row eviction, which deletes with
//
//	DELETE FROM recommendations
//	 WHERE collected_at < $1 AND (provider, account_key) IN (…)
//
// keyed on the CUDly account UUID, while a sweep fails one SUBSCRIPTION at a
// time. Reporting a partial sweep as succeeded therefore deletes the
// previous-cycle rows of every subscription the sweep never queried, and
// their savings silently read as "nothing to buy" rather than "collection
// incomplete" (issue #1652). That is data loss, not a display gap.
//
// Withholding eviction is deliberately all this does. The rows that WERE
// collected are still upserted by natural key, and the account is not marked
// failed -- failing it would blank the whole account's dashboard over one
// flaky subscription, the opposite failure and the one the succeeded-account
// roster exists to prevent.
//
// The gap is also how the incompleteness leaves the logs (issue #87): it is
// threaded up through fanOutPerAccount and collectAllProviders into
// recommendations_state.last_collection_partial, which the freshness API
// serves, so the dashboard can tell 1-of-10 coverage from full coverage. It
// is sanitized (counts and reason categories); the failed subscription IDs
// and raw errors stay in this WARN log only. See sweepGap.
func tolerateIncompleteSweep(providerName string, err error) (*sweepGap, error) {
	var incomplete *awsrecommendations.IncompleteRecommendationsError
	if errors.As(err, &incomplete) {
		logging.Warnf("%s recommendations incomplete: failed_details=%d failed_scopes=%d; %v; keeping collected rows and withholding stale-row eviction",
			providerName, incomplete.FailedDetails, incomplete.FailedScopes, incomplete)
		return gapFromAWSIncomplete(incomplete), nil
	}
	partial := azureprovider.AsPartialSubscriptionFailure(err)
	if partial == nil {
		return tolerateSkippedRecommendations(providerName, err)
	}
	skipped := 0
	if sk := partial.Skips(); sk != nil {
		skipped = sk.SkippedCount()
	}
	logging.Warnf(
		"%s recommendations incomplete: %d of %d subscriptions succeeded; not queried: %s; %d recommendation(s) skipped "+
			"(keeping the %d that did, and holding back stale-row eviction so the un-queried subscriptions' previous rows survive)",
		providerName, partial.Succeeded, partial.Attempted,
		strings.Join(partial.FailedSubscriptionIDs(), ", "), skipped, partial.Succeeded)
	return gapFromPartialSubscriptions(partial), nil
}
