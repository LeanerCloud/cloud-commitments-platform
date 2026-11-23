// Package scheduler handles scheduled recommendation collection.
package scheduler

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/concurrency"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/oidc"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

// STSClient is the minimal AWS STS surface the scheduler uses to discover
// the Lambda's own AWS account ID for the ambient-path account-tagging fix
// (issue #604). Mirrors purchase.STSClient deliberately — both packages
// need the same single call, and cross-package imports would tangle the
// purchase ↔ scheduler dependency.
type STSClient interface {
	GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// SchedulerConfig holds configuration for the scheduler.
type SchedulerConfig struct { //nolint:revive // exported: doc comment style intentional
	ConfigStore     config.StoreInterface
	PurchaseManager ManagerInterface
	EmailSender     email.SenderInterface
	ProviderFactory provider.FactoryInterface
	CredentialStore credentials.CredentialStore
	OIDCSigner      oidc.Signer
	AssumeRoleSTS   credentials.STSClient
	STSClient       STSClient
	// AzureOptions, when set, is refreshed after a collection run in which
	// Azure succeeded, so the Azure commitment-options probe row gets filled.
	AzureOptions azureOptionsRefresher
	// BastionSTSFactory builds the STS client a bastion-mode account's target
	// role is assumed with, signed with the bastion account's credentials.
	// Without it bastion accounts fail to resolve (issue #263).
	BastionSTSFactory credentials.STSClientFactory
	// AmbientAWSCreds are the host credentials. Only a bastion that is the
	// host account itself (role_arn with no role ARN) uses them.
	AmbientAWSCreds aws.CredentialsProvider
	DashboardURL    string
	OIDCIssuerURL   string
	IsLambda        bool
}

// CollectResult holds the result of collecting recommendations.
//
// SuccessfulProviders + FailedProviders track per-provider outcomes so the
// persistence layer (UpsertRecommendations) can scope the delete-stale
// clause to providers that actually ran, and the frontend banner can
// surface the specific failures.
type CollectResult struct {
	FailedProviders     map[string]string `json:"failed_providers,omitempty"`
	SuccessfulProviders []string          `json:"successful_providers,omitempty"`
	Recommendations     int               `json:"recommendations"`
	TotalSavings        float64           `json:"total_savings"`
}

// ManagerInterface defines the purchase manager methods used by scheduler.
type ManagerInterface interface {
	ProcessScheduledPurchases(ctx context.Context) (*purchase.ProcessResult, error)
	SendUpcomingPurchaseNotifications(ctx context.Context) (*purchase.NotificationResult, error)
	// FireScheduledDelayedPurchases fires purchase_executions in status=scheduled
	// whose scheduled_execution_at is in the past (Gmail-style pre-fire delay,
	// issue #291 wave-2). Called on the "fire_scheduled_purchases" scheduler tick.
	FireScheduledDelayedPurchases(ctx context.Context) (*purchase.FireResult, error)
}

// Scheduler handles scheduled tasks.
type Scheduler struct {
	ambientAWSCreds aws.CredentialsProvider
	config          config.StoreInterface
	email           email.SenderInterface
	providerFactory provider.FactoryInterface
	credStore       credentials.CredentialStore
	oidcSigner      oidc.Signer
	assumeRoleSTS   credentials.STSClient
	stsClient       STSClient
	azureOptions    azureOptionsRefresher
	purchase        ManagerInterface
	bastionSTS      credentials.STSClientFactory
	alertRetry      map[string]bool
	dashboardURL    string
	oidcIssuerURL   string
	cacheTTL        time.Duration
	alertRetryMu    sync.Mutex
	collecting      atomic.Bool
	isLambda        bool
}

// defaultCacheTTL is the fallback when CUDLY_RECOMMENDATION_CACHE_TTL is
// unset or invalid. 6h is shorter than the default 1-day cron so
// opportunistic refresh closes the gap when users are active.
const defaultCacheTTL = 6 * time.Hour

// NewScheduler creates a new scheduler.
func NewScheduler(cfg SchedulerConfig) *Scheduler {
	factory := cfg.ProviderFactory
	if factory == nil {
		factory = &provider.DefaultFactory{}
	}

	return &Scheduler{
		config:          cfg.ConfigStore,
		purchase:        cfg.PurchaseManager,
		email:           cfg.EmailSender,
		dashboardURL:    cfg.DashboardURL,
		providerFactory: factory,
		credStore:       cfg.CredentialStore,
		oidcSigner:      cfg.OIDCSigner,
		oidcIssuerURL:   cfg.OIDCIssuerURL,
		assumeRoleSTS:   cfg.AssumeRoleSTS,
		bastionSTS:      cfg.BastionSTSFactory,
		ambientAWSCreds: cfg.AmbientAWSCreds,
		stsClient:       cfg.STSClient,
		azureOptions:    cfg.AzureOptions,
		isLambda:        cfg.IsLambda,
		cacheTTL:        cacheTTLFromEnv(),
	}
}

// IsLambda reports the Lambda mode this scheduler was constructed with
// (SchedulerConfig.IsLambda). It never re-reads the environment: the value is
// owned by whoever built the scheduler, so callers can assert that the mode
// they threaded in is the one that gates background goroutines (issue #110).
func (s *Scheduler) IsLambda() bool {
	return s.isLambda
}

// cacheTTLFromEnv parses CUDLY_RECOMMENDATION_CACHE_TTL using
// time.ParseDuration. Falls back to defaultCacheTTL on parse error (with
// a warning) so a mis-set env doesn't take the feature offline.
func cacheTTLFromEnv() time.Duration {
	v := os.Getenv("CUDLY_RECOMMENDATION_CACHE_TTL")
	if v == "" {
		return defaultCacheTTL
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		logging.Warnf("CUDLY_RECOMMENDATION_CACHE_TTL invalid (%q); using default %s", v, defaultCacheTTL)
		return defaultCacheTTL
	}
	return d
}

// CollectRecommendations fetches recommendations from all configured cloud providers.
// Persists results to the recommendations cache so read handlers can serve
// from SQL instead of re-fetching live.
//
// Bookkeeping: clears last_collection_started_at on exit (success or
// failure) so the frontend polling loop can detect completion, but only if
// ownerToken is non-empty and still matches last_collection_owner_id
// (issue #261 compare-and-clear guard). The scheduler is invoked either by
// the cron EventBridge rule or by an async self-invoke from the POST
// /api/recommendations/refresh handler. In the async case,
// MarkCollectionStarted has already set last_collection_started_at and
// returned the token that ownerToken carries here (in Lambda, re-stamped to
// an invocation-scoped token once the run won the task advisory lock, see
// ClaimCollectionInvocation, issue #84); kickColdStartCollection's
// background collect does too (issue #106), for the same reason. The cron
// case calls with an empty ownerToken since it never wins a marker of its
// own (it coordinates via the advisory lock instead, see
// HandleScheduledTask), and the clear is skipped entirely rather than
// clearing unconditionally,
// which previously let a cron run wipe a concurrent user-triggered run's
// marker. This deferred clear is the ONLY place that touches started_at:
// persistCollection's SetRecommendationsCollectionError call (below, on a
// provider failure) intentionally leaves started_at alone, so a tokenless
// run hitting a routine provider error cannot wipe another run's marker
// either.
func (s *Scheduler) CollectRecommendations(ctx context.Context, ownerToken string) (*CollectResult, error) {
	logging.Info("Collecting recommendations from cloud providers...")

	// Clear last_collection_started_at on exit so the frontend knows the
	// collection has finished. Use a fresh background context with a short
	// timeout: the request ctx may already be canceled by the time the
	// defer runs (e.g. caller deadline expired during a slow collect),
	// which would cause ClearCollectionStarted to fail and leave the
	// "in flight" marker until the 5-min auto-recovery window kicks in.
	defer func() {
		clearCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.clearCollectionStartedBestEffort(clearCtx, ownerToken)
	}()

	// Get global config
	globalCfg, err := s.config.GetGlobalConfig(ctx)
	if err != nil {
		s.recordCollectionStoppedBestEffort(collectionStoppedLoadingSettings, err)
		return nil, err
	}

	// Attach a shared semaphore to ctx so every leaf goroutine in the
	// recommendations-collection fan-out tree (AWS service, Azure service,
	// GCP region×service) acquires one slot before issuing its cloud-API
	// call and releases it after. This bounds aggregate concurrent IO across
	// the whole tree at CUDLY_MAX_PARALLELISM (default 20) regardless of how
	// nested the dispatch is — without it, peak concurrency multiplies
	// through the nested fan-outs and can exhaust Lambda memory before work
	// completes (observed with a 512 MB function in dev). Intermediate
	// dispatchers (provider, account, GCP region) do NOT acquire — they only
	// launch sub-goroutines — so no goroutine can deadlock by holding a
	// permit while waiting for sub-permits.
	maxParallelism := concurrency.MaxParallelismFromEnv()
	sem := semaphore.NewWeighted(int64(maxParallelism))
	ctx = concurrency.WithSharedSemaphore(ctx, sem)
	logging.Infof("Recommendations collection: aggregate parallelism cap = %d", maxParallelism)

	// Collect recommendations from each enabled provider concurrently, tracking
	// per-provider outcomes so persistence can scope stale-row eviction to
	// (provider, account) pairs that actually ran. A partial collection (e.g.
	// one of three Azure subscriptions failed) preserves the failed pairs'
	// previous-cycle rows instead of wiping the whole provider's slice.
	//
	// Provider-level fan-out under errgroup. Each goroutine returns nil to the
	// group so a single provider's failure does not cancel siblings — matches
	// the previous loop's `continue`-on-error behavior. Per-provider results
	// are written into a map under a single mutex; the merge then walks
	// EnabledProviders in config order so successfulProviders ordering is
	// deterministic regardless of goroutine completion order. After Wait, ctx
	// cancellation is propagated. No concurrency cap — the universe is at
	// most 3 providers.
	allRecommendations, totalSavings, successfulProviders, successfulCollects, failedProviders, partial, err := s.collectAllProviders(ctx, globalCfg)
	if err != nil {
		s.recordCollectionStoppedBestEffort(collectionStoppedIncomplete, err)
		return nil, err
	}

	logging.Infof("Collected %d recommendations with $%.2f/month potential savings",
		len(allRecommendations), totalSavings)

	heldBefore, err := s.expiredAzureHolds(ctx)
	if err != nil {
		logging.Errorf("Could not read expired Azure pricing holds before persisting, alert skipped this cycle: %v", err)
	}
	s.persistCollection(ctx, allRecommendations, successfulCollects, failedProviders, partial)
	s.alertNewlyExpiredHolds(ctx, heldBefore)
	// Send notification if we have recommendations
	if len(allRecommendations) > 0 && totalSavings > 0 {
		// Sort by savings descending to show top recommendations in email
		sort.Slice(allRecommendations, func(i, j int) bool {
			return allRecommendations[i].Savings > allRecommendations[j].Savings
		})

		data := email.NotificationData{
			DashboardURL: s.dashboardURL,
			TotalSavings: totalSavings,
		}
		for i := range allRecommendations {
			rec := allRecommendations[i]
			if len(data.Recommendations) >= 10 { // Limit to top 10 in email
				break
			}
			data.Recommendations = append(data.Recommendations, email.RecommendationSummary{
				Service:        rec.Service,
				ResourceType:   rec.ResourceType,
				Engine:         rec.Engine,
				Region:         rec.Region,
				Count:          rec.Count,
				MonthlySavings: rec.Savings,
			})
		}

		if err := s.email.SendNewRecommendationsNotification(ctx, data); err != nil {
			logging.Errorf("Failed to send notification: %v", err)
		}
	}

	s.refreshAzureOptionsBestEffort(ctx, successfulProviders)

	return &CollectResult{
		Recommendations:     len(allRecommendations),
		TotalSavings:        totalSavings,
		SuccessfulProviders: successfulProviders,
		FailedProviders:     failedProviders,
	}, nil
}

// collectAllProviders fans out provider collection (AWS / Azure / GCP) under
// errgroup. Each goroutine returns nil so a single provider's error does not
// cancel siblings; per-provider results are written into a map under a single
// mutex. After Wait, ctx cancellation is propagated. The merge then walks
// EnabledProviders in config order so successfulProviders / successfulCollects
// / allRecommendations ordering is deterministic regardless of goroutine
// completion order — keeps existing tests stable. No concurrency cap; the
// universe is at most 3 providers.
//
// Extracted from CollectRecommendations to keep that function under the
// project's gocyclo gate (.golangci.yml min-complexity: 15) after the
// errgroup + post-Wait ctx.Err() block was added.
func (s *Scheduler) collectAllProviders(ctx context.Context, globalCfg *config.GlobalConfig) ( //nolint:gocritic // tooManyResultsChecker: multiple returns intentional
	allRecommendations []config.RecommendationRecord,
	totalSavings float64,
	successfulProviders []string,
	successfulCollects []config.SuccessfulCollect,
	failedProviders map[string]string,
	partial *config.RecommendationsCollectionPartial,
	err error,
) {
	failedProviders = map[string]string{}

	var (
		mu       sync.Mutex
		outcomes = make(map[string]providerOutcome, len(globalCfg.EnabledProviders))
	)

	g, gctx := errgroup.WithContext(ctx)

	for _, providerName := range globalCfg.EnabledProviders {
		providerName := providerName // capture per-iteration
		g.Go(func() error {
			logging.Infof("Collecting recommendations from %s...", providerName)
			recs, succeededAccountIDs, gap, perr := s.collectProviderRecommendations(gctx, providerName, globalCfg)
			mu.Lock()
			outcomes[providerName] = providerOutcome{
				recs:                recs,
				succeededAccountIDs: succeededAccountIDs,
				gap:                 gap,
				err:                 perr,
			}
			mu.Unlock()
			return nil // error isolation: per-provider failures don't cancel siblings
		})
	}

	// Wait for all goroutines. g.Wait() always returns nil because every
	// goroutine returns nil. After Wait, propagate ctx cancellation so
	// callers can distinguish "all providers completed" from "the parent
	// ctx was canceled mid-fan-out".
	if waitErr := g.Wait(); waitErr != nil {
		// Goroutines return nil to isolate per-provider failures; non-nil is unexpected.
		logging.Warnf("collectAllProviders: errgroup.Wait returned unexpected error: %v", waitErr)
	}
	if cerr := ctx.Err(); cerr != nil {
		return nil, 0, nil, nil, nil, nil, cerr
	}

	// Deterministic merge: walk EnabledProviders in config order so
	// successfulProviders ordering is independent of goroutine completion
	// order — keeps existing tests stable.
	for _, providerName := range globalCfg.EnabledProviders {
		out, ok := outcomes[providerName]
		if !ok {
			continue
		}
		if out.err != nil {
			logging.Errorf("Failed to collect %s recommendations: %v", providerName, out.err)
			_, failedProviders[providerName] = purchase.SummarizeFailure(out.err)
			continue
		}
		successfulProviders = append(successfulProviders, providerName)
		successfulCollects = append(successfulCollects, expandSuccessfulCollects(providerName, out.succeededAccountIDs)...)
		for i := range out.recs {
			rec := out.recs[i]
			totalSavings += rec.Savings
		}
		allRecommendations = append(allRecommendations, out.recs...)
		partial = appendPartialProvider(partial, providerName, out.gap)
	}
	return allRecommendations, totalSavings, successfulProviders, successfulCollects, failedProviders, partial, nil
}

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

// resolveEffectiveCacheTTL returns the effective stale-while-revalidate TTL
// and whether background auto-refresh has been explicitly disabled (value 0).
// It prefers the DB-configured RecommendationsCacheStaleHours; falls back to
// s.cacheTTL (env-var / compile-time default) when the DB read fails.
func (s *Scheduler) resolveEffectiveCacheTTL(ctx context.Context) (ttl time.Duration, disabled bool) {
	ttl = s.cacheTTL
	globalCfg, gcErr := s.config.GetGlobalConfig(ctx)
	if gcErr != nil || globalCfg == nil {
		// DB read failed OR returned (nil, nil) — fall back to the env-var /
		// default TTL with refresh enabled rather than panicking on the
		// nil deref below. (nil, nil) is a defensive case; the Postgres
		// store currently never returns it, but a future store impl or a
		// test mock might.
		return ttl, false
	}
	if globalCfg.RecommendationsCacheStaleHours == 0 {
		// Explicit 0 = operator opted out of automatic background refresh.
		return 0, true
	}
	return time.Duration(globalCfg.RecommendationsCacheStaleHours) * time.Hour, false
}

// suppressionKey is the 6-tuple used to group suppressions. Within a group,
// each row's variant (availability zone, platform, tenancy; issue #816)
// decides whether it applies to a given rec (PurchaseSuppression.AppliesTo).
type suppressionKey struct {
	accountID, provider, service, region, resourceType, engine string
}

// suppressionAgg aggregates the active suppression rows that apply to one
// rec: cumulative count, earliest expiry (drives the badge's
// "Xd remaining"), and the execution whose suppression contributed
// the most (drives the badge deep-link).
type suppressionAgg struct {
	earliestExpiresAt       time.Time
	primaryExecutionCreated time.Time
	primaryExecutionID      string
	suppressedCount         int
	primaryExecutionContrib int
}

// add folds one suppression row into the aggregate.
func (a *suppressionAgg) add(sup *config.PurchaseSuppression) {
	a.suppressedCount += sup.SuppressedCount
	if sup.ExpiresAt.Before(a.earliestExpiresAt) {
		a.earliestExpiresAt = sup.ExpiresAt
	}
	// "Most contribution" selects the biggest single suppression
	// row; ties go to the most-recently-created row so a fresh
	// cancel of an older peer doesn't leave the badge pointing at
	// a stale execution.
	if sup.SuppressedCount > a.primaryExecutionContrib ||
		(sup.SuppressedCount == a.primaryExecutionContrib && sup.CreatedAt.After(a.primaryExecutionCreated)) {
		a.primaryExecutionID = sup.ExecutionID
		a.primaryExecutionCreated = sup.CreatedAt
		a.primaryExecutionContrib = sup.SuppressedCount
	}
}

// applySuppressions subtracts active purchase_suppressions from each
// rec's count and annotates the rec with the SuppressedCount /
// SuppressionExpiresAt / PrimarySuppressionExecutionID fields that
// drive the "recently purchased" badge on the frontend. Recs whose
// remaining count hits 0 are dropped entirely.
//
// A read failure returns no recs alongside the error, so a caller that
// dropped the error would still show nothing rather than the un-suppressed
// list (issue #111).
func (s *Scheduler) applySuppressions(ctx context.Context, recs []config.RecommendationRecord) ([]config.RecommendationRecord, error) {
	sups, err := s.config.ListActiveSuppressions(ctx)
	if err != nil {
		return nil, err
	}
	if len(sups) == 0 {
		return recs, nil
	}
	index := aggregateSuppressions(sups)
	return applySuppressionIndex(recs, index), nil
}

// aggregateSuppressions groups a raw suppression list by 6-tuple. The
// per-rec aggregate is built in applySuppressionIndex, because which rows
// of a group apply depends on the rec's variant.
func aggregateSuppressions(sups []config.PurchaseSuppression) map[suppressionKey][]config.PurchaseSuppression {
	index := make(map[suppressionKey][]config.PurchaseSuppression, len(sups))
	for i := range sups {
		sup := sups[i]
		k := suppressionKey{
			accountID: sup.AccountID, provider: sup.Provider,
			service: config.CanonicalSuppressionService(sup.Provider, sup.Service),
			region:  sup.Region, resourceType: sup.ResourceType,
		}
		k.engine = config.CanonicalSuppressionEngine(k.provider, k.service, sup.Engine)
		index[k] = append(index[k], sup)
	}
	return index
}

// matchingSuppressions aggregates the rows of rec's 6-tuple group that apply
// to rec's variant: rows for the same availability zone, platform and
// tenancy, plus legacy rows with no variant (a wildcard over the 6-tuple).
// A rec whose Details cannot be read matches every row of its group, the
// pre-#816 behavior. Returns nil when no row applies.
func matchingSuppressions(rec *config.RecommendationRecord, group []config.PurchaseSuppression) *suppressionAgg {
	var recVariant *config.SuppressionVariant
	if v, err := rec.SuppressionVariant(); err == nil {
		recVariant = &v
	}
	var a *suppressionAgg
	for i := range group {
		if !group[i].AppliesTo(recVariant) {
			continue
		}
		if a == nil {
			a = &suppressionAgg{earliestExpiresAt: group[i].ExpiresAt}
		}
		a.add(&group[i])
	}
	return a
}

// applySuppressionIndex walks recs, subtracts the count of the
// suppressions that apply to each rec, drops recs that go to 0 or below,
// and annotates the survivors with the badge fields.
func applySuppressionIndex(recs []config.RecommendationRecord, index map[suppressionKey][]config.PurchaseSuppression) []config.RecommendationRecord {
	// Allocate a fresh backing array so callers that hold a reference to
	// the original recs slice do not see mutations (05-M1).
	out := make([]config.RecommendationRecord, 0, len(recs))
	for i := range recs {
		rec := recs[i]
		accountID := ""
		if rec.CloudAccountID != nil {
			accountID = *rec.CloudAccountID
		}
		k := suppressionKey{
			accountID: accountID, provider: rec.Provider,
			service: config.CanonicalSuppressionService(rec.Provider, rec.Service),
			region:  rec.Region, resourceType: rec.ResourceType,
		}
		k.engine = config.CanonicalSuppressionEngine(k.provider, k.service, rec.Engine)
		a := matchingSuppressions(&rec, index[k])
		if a == nil {
			out = append(out, rec)
			continue
		}
		rec.Count -= a.suppressedCount
		if rec.Count <= 0 {
			continue // fully covered by recent purchases; hide
		}
		rec.SuppressedCount = a.suppressedCount
		expiry := a.earliestExpiresAt
		rec.SuppressionExpiresAt = &expiry
		primary := a.primaryExecutionID
		rec.PrimarySuppressionExecutionID = &primary
		out = append(out, rec)
	}
	return out
}

// kickColdStartCollection single-flights the FIRST-EVER collection
// (freshness.LastCollectedAt == nil) cluster-wide via MarkCollectionStarted
// -- the same marker the explicit refresh endpoint coordinates through --
// instead of every concurrent cold reader racing its
// own uncoordinated CollectRecommendations call on its own short request
// context (issue #106). The previous synchronous call ran on ctx, which the
// HTTP layer caps at 30s (internal/server/http.go); a real sweep routinely
// takes longer, so collectAllProviders returned ctx.Err() before
// persistCollection ever ran, last_collected_at never advanced, and every
// subsequent reader repeated the same doomed collect -- a self-sustaining
// storm hammering Cost Explorer with duplicated fan-outs.
//
// The frontend already drives a properly-coordinated first collection on
// page load (triggerAutoRefreshIfStale -> POST /api/recommendations/refresh,
// which uses this same marker and, on Lambda, a real async self-invoke
// dispatched through HandleScheduledTask's advisory lock). This function is
// a best-effort kick for non-browser API callers that read the cache
// directly without ever hitting that endpoint.
//
// On non-Lambda runtimes the winning caller runs the collection on a
// detached background context sized for a real sweep, mirroring
// maybeKickBackgroundRefresh's stale-cache refresh below, gated by the same
// per-process s.collecting guard so the two background paths can never
// double-run concurrently. On Lambda a goroutine started inside a request
// handler freezes the instant this invocation's response is sent (see
// maybeKickBackgroundRefresh), so there is no way to make progress from
// inside this invocation; this function returns immediately WITHOUT ever
// calling MarkCollectionStarted, rather than winning the marker only to
// release it straight back -- that mark-then-clear round trip does no work
// and briefly opens a false-409 window on the refresh endpoint for nothing.
// The cache is populated by whichever runs first: the scheduled cron
// (already advisory-locked) or the frontend's auto-refresh-on-open flow.
//
// The cron never calls MarkCollectionStarted (it single-flights through the
// advisory lock instead), so a kick here can overlap at most one in-flight
// cron collect. That overlap is bounded: the marker still limits cold
// readers to one kick cluster-wide, and s.collecting to one per process.
func (s *Scheduler) kickColdStartCollection(ctx context.Context) {
	if s.isLambda {
		return
	}
	token, ok, err := s.config.MarkCollectionStarted(ctx)
	if err != nil {
		logging.Warnf("cold-start collection: failed to mark started: %v", err)
		return
	}
	if !ok {
		// Another cold reader or an explicit refresh already has a
		// collection in flight.
		return
	}
	if !s.collecting.CompareAndSwap(false, true) {
		// A stale-cache background refresh already claimed the per-process
		// slot; release the DB marker so the refresh endpoint (or the next
		// reader) is not blocked by a marker nothing is actively working.
		s.clearCollectionStartedBestEffort(ctx, token)
		return
	}

	logging.Info("Recommendations cache is empty; triggering single-flighted background cold-start collect")
	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	go func() {
		defer cancel()
		defer s.collecting.Store(false)
		defer func() {
			if r := recover(); r != nil {
				logging.Errorf("cold-start collection panic: %v", r)
			}
		}()
		if _, err := s.CollectRecommendations(bgCtx, token); err != nil {
			// CollectRecommendations already surfaces errors via
			// recommendations_state.last_collection_error, so just log
			// locally here for operator visibility.
			logging.Errorf("cold-start collection: %v", err)
		}
	}()
}

// maybeKickBackgroundRefresh spawns a detached CollectRecommendations
// goroutine when the cache is stale AND the runtime can safely run
// background goroutines (i.e. not Lambda). The atomic.Bool guard
// single-flights concurrent callers so only one refresh fires per TTL
// window. Recovers from panics so one bad collect can't crash the
// long-lived process.
//
// effectiveTTL is the caller-resolved stale threshold (from DB config
// RecommendationsCacheStaleHours, or the env-var default if unconfigured).
func (s *Scheduler) maybeKickBackgroundRefresh(freshness *config.RecommendationsFreshness, effectiveTTL time.Duration) {
	if s.isLambda {
		return
	}
	if freshness.LastCollectedAt == nil {
		// kickColdStartCollection already handles this case.
		return
	}
	if time.Since(*freshness.LastCollectedAt) < effectiveTTL {
		return
	}
	if !s.collecting.CompareAndSwap(false, true) {
		// Another refresh is already in flight.
		return
	}

	logging.Infof("Recommendations cache is stale (age > %s); triggering background refresh", effectiveTTL)
	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	go func() {
		defer cancel()
		defer s.collecting.Store(false)
		defer func() {
			if r := recover(); r != nil {
				logging.Errorf("background recommendations refresh panic: %v", r)
			}
		}()
		if _, err := s.CollectRecommendations(bgCtx, ""); err != nil {
			// CollectRecommendations already surfaces errors via
			// recommendations_state.last_collection_error, so just log
			// locally here for operator visibility.
			logging.Errorf("background refresh: %v", err)
		}
	}()
}
