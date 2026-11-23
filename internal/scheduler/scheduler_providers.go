package scheduler

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
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
