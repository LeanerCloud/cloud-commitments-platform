package scheduler

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// appendPartialProvider adds providerName's incomplete-sweep summary to
// partial, allocating it on first use. A complete provider (nil gap) leaves
// partial unchanged, so a collection whose every sweep was complete yields
// nil and persistCollection leaves the column cleared.
func appendPartialProvider(partial *config.RecommendationsCollectionPartial, providerName string, gap *sweepGap) *config.RecommendationsCollectionPartial {
	if gap == nil {
		return partial
	}
	if partial == nil {
		partial = &config.RecommendationsCollectionPartial{}
	}
	partial.Providers = append(partial.Providers, gap.toProviderSweep(providerName))
	return partial
}

// clearCollectionStartedBestEffort clears last_collection_started_at on the
// scheduler's exit path. Best-effort — a failure here is logged but does not
// prevent returning the collection result. Extracted so CollectRecommendations
// stays under the cyclomatic-complexity gate.
//
// A caller that never won MarkCollectionStarted (the cron) passes an empty
// ownerToken and by design owns no marker to clear; this is an explicit,
// logged skip, not a silent fallback.
func (s *Scheduler) clearCollectionStartedBestEffort(ctx context.Context, ownerToken string) {
	if ownerToken == "" {
		logging.Debugf("skipping collection-started clear: caller holds no owner token (cron run)")
		return
	}
	if err := s.config.ClearCollectionStarted(ctx, ownerToken); err != nil {
		logging.Errorf("failed to clear collection started: %v", err)
	}
}

// persistCollection writes the collection results to the recommendations
// cache via UpsertRecommendations, which:
//
//   - Upserts each row by natural key so re-collected recommendations
//     reuse the same PK across runs (stable IDs for frontend).
//   - Evicts stale rows (collected_at < $now) but ONLY for the
//     (provider, account) pairs that successfully collected in this run.
//     Failed pairs' rows stay visible so the dashboard isn't blanked out
//     by transient cloud-API failures, even when one of several accounts
//     under the same provider succeeded.
//
// On any failure (including partial), the collection error is additionally
// recorded in recommendations_state so the frontend banner renders while
// the user still sees the valid rows we managed to upsert.
//
// The rows and last_collection_partial commit in one transaction: a complete
// collection clears the previous run's marker, a non-nil partial replaces it
// (issue #87). A failed marker write rolls the rows back too, so the Azure
// pricing hold cannot be lost while the held rows stay.
//
// After a successful write it prunes the rows of (provider, account) pairs
// that have not collected for CUDLY_STALE_RECOMMENDATION_RETENTION_DAYS
// (default 30), best-effort, so a removed provider or a disabled account
// stops showing instead of staying forever (issue #120). A failed write skips
// the prune.
func (s *Scheduler) persistCollection(ctx context.Context, recs []config.RecommendationRecord, successfulCollects []config.SuccessfulCollect, failedProviders map[string]string, partial *config.RecommendationsCollectionPartial) {
	if err := s.upsertWithMarker(ctx, recs, successfulCollects, partial); err != nil {
		logging.Errorf("Failed to persist recommendations: %v", err)
		if setErr := s.config.SetRecommendationsCollectionError(ctx, collectionStoppedSaving); setErr != nil {
			logging.Errorf("Failed to record write error: %v", setErr)
		}
		return
	}

	if len(failedProviders) > 0 {
		if err := s.config.SetRecommendationsCollectionError(ctx, joinProviderErrors(failedProviders)); err != nil {
			logging.Errorf("Failed to record collection error: %v", err)
		}
	}

	s.pruneStaleRecommendationsBestEffort(ctx)
}

// Messages recorded when a collection ends before persistCollection runs. They
// are shown to users as the refresh outcome, so they carry no internal detail;
// the cause is logged.
const (
	collectionStoppedLoadingSettings = "recommendations collection stopped before saving: could not load settings"
	collectionStoppedIncomplete      = "recommendations collection stopped before saving: it was canceled or ran out of time"
	collectionStoppedSaving          = "recommendations collection stopped before saving: could not save the collected recommendations"
)

// recordCollectionStoppedBestEffort records why a collection ended without
// persisting, before the deferred marker clear runs (#1679). Without it the
// in-flight marker clears with the previous run's (usually empty) error and a
// refresh that collected nothing reads as a success. It uses its own context
// because the run's context may already be done.
func (s *Scheduler) recordCollectionStoppedBestEffort(msg string, cause error) {
	logging.Errorf("Recommendations collection stopped before saving: %v", cause)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.config.SetRecommendationsCollectionError(ctx, msg); err != nil {
		logging.Errorf("Failed to record collection error: %v", err)
	}
}

// upsertWithMarker writes the rows and the collection's partial marker in one
// transaction. Both store methods merge the previous Azure pricing hold under
// the state row lock, so a complete collection (nil partial) keeps it too.
func (s *Scheduler) upsertWithMarker(ctx context.Context, recs []config.RecommendationRecord, successfulCollects []config.SuccessfulCollect, partial *config.RecommendationsCollectionPartial) error {
	if partial == nil {
		return s.config.UpsertRecommendations(ctx, time.Now().UTC(), recs, successfulCollects)
	}
	return s.config.UpsertRecommendationsWithPartial(ctx, time.Now().UTC(), recs, successfulCollects, partial)
}

// joinProviderErrors formats a "provider: err; provider: err" string for
// the state table's last_collection_error column.
func joinProviderErrors(failed map[string]string) string {
	parts := make([]string, 0, len(failed))
	for p, msg := range failed {
		parts = append(parts, fmt.Sprintf("%s: %s", p, msg))
	}
	sort.Strings(parts) // deterministic ordering for test stability
	return strings.Join(parts, "; ")
}
