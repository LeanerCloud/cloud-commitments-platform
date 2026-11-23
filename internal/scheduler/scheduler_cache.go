package scheduler

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// ListRecommendations reads cached recommendations from the store, applying
// the given filter as SQL WHERE clauses. Previously this did live cloud
// API calls on every request (2–10s per provider); now it's a pure DB
// read, typically under 100ms.
//
// Order of operations:
//  1. Read freshness.
//  2. Cold-start (LastCollectedAt==nil): single-flight a background collect
//     via kickColdStartCollection rather than blocking this read on it (issue
//     #106) -- the frontend already drives a coordinated first collection on
//     page load (triggerAutoRefreshIfStale -> POST /recommendations/refresh),
//     so this is a best-effort kick for non-browser API callers only.
//  3. Read from the cache (empty on a genuine cold start, until the kicked
//     collection or the frontend's refresh populates it).
//  4. Stale-while-revalidate: if the cache is older than cacheTTL AND we
//     aren't on Lambda, kick off a background CollectRecommendations so
//     the NEXT read sees fresh data. Lambda skips this (goroutines freeze
//     between invocations); the scheduled cron is Lambda's refresh path.
//
// Steps 2 and 4 run only when ctx carries WithBackgroundCollection, which the
// API sets for callers with unrestricted account scope. Both collect across
// every account, so a scoped reader gets a pure cache read (issue #627).
func (s *Scheduler) ListRecommendations(ctx context.Context, filter config.RecommendationFilter) ([]config.RecommendationRecord, error) {
	logging.Info("Reading recommendations from cache...")

	freshness, err := s.startCacheRead(ctx)
	if err != nil {
		return nil, err
	}

	recs, err := s.config.ListStoredRecommendations(ctx, filter)
	if err != nil {
		return nil, err
	}

	recs, err = s.applySuppressions(ctx, recs)
	if err != nil {
		// Fail closed (issue #111). Suppressions are the duplicate-purchase
		// guard: without them a just-bought commitment shows at its full
		// pre-purchase count with no "recently purchased" badge, inviting
		// the same purchase a second time. A failed page is the safe side.
		return nil, fmt.Errorf("apply suppressions: %w", err)
	}

	recs, err = s.applyAccountOverrides(ctx, recs)
	if err != nil {
		// Overrides are a display filter, not a spend guard, so they keep
		// the over-show trade-off: a missing global ServiceConfig or an
		// override-table read failure leaves the user looking at
		// un-overridden recs, not a blank page. Issue #196.
		logging.Errorf("failed to apply account overrides; returning un-filtered recs: %v", err)
	}

	s.finishCacheRead(ctx, freshness)
	return recs, nil
}

// startCacheRead is steps 1 and 2 of ListRecommendations (and of
// ScanRecommendations): read freshness and, for a caller allowed to trigger
// collection, kick the cold-start collection.
func (s *Scheduler) startCacheRead(ctx context.Context) (*config.RecommendationsFreshness, error) {
	freshness, err := s.config.GetRecommendationsFreshness(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to read recommendations freshness: %w", err)
	}
	if BackgroundCollectionAllowed(ctx) && freshness.LastCollectedAt == nil {
		s.kickColdStartCollection(ctx)
	}
	return freshness, nil
}

// finishCacheRead is step 4 of ListRecommendations (and of
// ScanRecommendations): the stale-while-revalidate kick. Background refresh is
// only attempted on non-Lambda runtimes; the DB config read is skipped
// entirely on Lambda so GetGlobalConfig is not called on the hot read path
// (Lambda has no persistent goroutines).
func (s *Scheduler) finishCacheRead(ctx context.Context, freshness *config.RecommendationsFreshness) {
	if !BackgroundCollectionAllowed(ctx) || s.isLambda {
		return
	}
	ttl, disabled := s.resolveEffectiveCacheTTL(ctx)
	if disabled {
		return
	}
	s.maybeKickBackgroundRefresh(freshness, ttl)
}

// ListRecommendationsStrict is ListRecommendations for the money path: the
// same stored rows, suppressions and account overrides, but an override read
// failure is returned instead of logged (a suppression read failure fails
// both). Over-showing an override-hidden rec is fine for a page; on a
// purchase it would buy what the operator excluded.
// It has no cold-start or background-refresh side effects: an empty cache
// reads as empty and the caller retries on a later tick.
func (s *Scheduler) ListRecommendationsStrict(ctx context.Context, filter config.RecommendationFilter) ([]config.RecommendationRecord, error) {
	recs, err := s.config.ListStoredRecommendations(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list stored recommendations: %w", err)
	}
	recs, err = s.applySuppressions(ctx, recs)
	if err != nil {
		return nil, fmt.Errorf("apply suppressions: %w", err)
	}
	recs, err = s.applyAccountOverrides(ctx, recs)
	if err != nil {
		return nil, fmt.Errorf("apply account overrides: %w", err)
	}
	return recs, nil
}

// GetRecommendationByID fetches a single recommendation by its application-
// level ID (the id field in the stored payload), bypassing the account-override
// filter so deep-linked URLs to override-hidden recs still resolve.
//
// Suppressions are still applied: a rec whose remaining count has been
// suppressed to zero is treated as gone (returns nil, nil, nil) so the caller
// can render a 404 rather than a "hidden" banner for actively-dismissed recs.
// A suppression read failure is returned (issue #111) rather than serving the
// rec at its un-suppressed count.
//
// hiddenBy is non-nil (one or more reason strings) when the rec exists and
// passes the suppression check, but would be dropped by the account-override
// filter. Possible reasons: "enabled=false", "engine", "region",
// "resource_type", "min_count" (overrideDimensions). The caller renders a
// "hidden by your override" banner.
//
// Returns (nil, nil, nil) when the rec is genuinely absent or fully suppressed.
func (s *Scheduler) GetRecommendationByID(ctx context.Context, id string) (rec *config.RecommendationRecord, hiddenBy []string, err error) {
	recs, err := s.config.ListStoredRecommendations(ctx, config.RecommendationFilter{ID: id})
	if err != nil {
		return nil, nil, fmt.Errorf("GetRecommendationByID: store lookup: %w", err)
	}
	if len(recs) == 0 {
		return nil, nil, nil
	}

	// Apply suppressions. A fully-suppressed rec becomes genuinely absent from
	// the caller's perspective (the suppression is intentional user action).
	recs, err = s.applySuppressions(ctx, recs)
	if err != nil {
		// Fail closed, as ListRecommendations does (issue #111).
		return nil, nil, fmt.Errorf("GetRecommendationByID: suppression check: %w", err)
	}
	if len(recs) == 0 {
		return nil, nil, nil
	}
	found := &recs[0]

	// Check whether the account-override filter would drop this rec. This is
	// a read-only call — we never drop it here, only report the reasons.
	resolved, resolveErr := config.ResolveAccountConfigsForRecs(ctx, s.config, recs)
	if resolveErr != nil {
		// Non-fatal: if the override check fails we surface the rec without
		// a hidden_by marker (over-show is the safer default).
		logging.Errorf("GetRecommendationByID: override resolution failed; returning rec without hidden_by: %v", resolveErr)
		return found, nil, nil
	}
	accountID := ""
	if found.CloudAccountID != nil {
		accountID = *found.CloudAccountID
	}
	cfg := resolved[config.AccountConfigKey(accountID, found.Provider, found.Service)]
	if cfg != nil {
		hiddenBy = overrideHiddenReasons(found, cfg)
	}

	return found, hiddenBy, nil
}
