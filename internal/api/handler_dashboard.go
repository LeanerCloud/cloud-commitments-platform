// Package api provides the HTTP API handlers for the CUDly dashboard.
package api

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

func (h *Handler) getDashboardSummary(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (*DashboardSummaryResponse, error) {
	// Dashboard aggregates recommendations + purchase history. Gate on
	// view:recommendations — the closest existing resource. No dedicated
	// "dashboard" resource in the permission model.
	session, err := h.requirePermission(ctx, req, "view", "recommendations")
	if err != nil {
		return nil, err
	}
	provider, err := providerFilterParam(params["provider"])
	if err != nil {
		return nil, err
	}

	accountUUIDs, accountExternalIDsByProvider, err := h.resolveDashboardAccountScope(ctx, session, params)
	if err != nil {
		return nil, err
	}

	// Resolve the account scope once for both the background-collection
	// decision (issue #627) and the recommendations post-filter.
	scope, err := h.getAccountScope(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get allowed accounts: %w", err)
	}

	recommendations, err := h.scheduler.ListRecommendations(h.recommendationsReadContext(ctx, session, scope), config.RecommendationFilter{
		Provider: provider,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get recommendations: %w", err)
	}

	recommendations = filterDashboardRecommendations(scope, recommendations)

	// Issue #196 — cap the headline "potential savings" by the per-account
	// coverage target. Without this scaling the dashboard always reports
	// the un-overridden total, which is misleading once a per-account
	// override has narrowed the user's intended commitment.
	//
	// The resolver is best-effort: a lookup error logs and falls through
	// to the un-scaled total, mirroring the over-show-vs-under-show
	// trade-off ListRecommendations makes for account overrides.
	coverageByKey := h.resolveCoverageByAccountKey(ctx, recommendations)

	totalSavings, byService := summarizeRecommendationsWithCoverage(recommendations, coverageByKey)
	targetCoverage := h.resolveTargetCoverage(ctx)
	counts, committedMonthly, ytdSavings, currentSavingsByService, err := h.calculateCommitmentMetrics(ctx, provider, accountUUIDs, accountExternalIDsByProvider)
	if err != nil {
		// Issue #72: purchase history is a hard dependency of the commitment
		// KPIs, same as recommendations are of the savings KPIs above. Fail
		// the request rather than ship $0 committed / 0 commitments, which
		// would be indistinguishable from a real empty portfolio.
		return nil, err
	}

	// Populate CurrentSavings on each per-service bucket from actual
	// purchase_history commitments. Services with recommendations but no
	// active commitments correctly keep CurrentSavings=0 (absent from
	// currentSavingsByService). summarizeRecommendationsWithCoverage only
	// sets PotentialSavings; CurrentSavings is exclusively owned here.
	for svc, monthlySavings := range currentSavingsByService {
		entry := byService[svc]
		entry.CurrentSavings = monthlySavings
		byService[svc] = entry
	}

	return &DashboardSummaryResponse{
		PotentialMonthlySavings:     totalSavings,
		TotalRecommendations:        len(recommendations),
		ActiveCommitments:           counts.Active,
		ExcludedLegacyEstimateCount: counts.ExcludedLegacy,
		CommittedMonthly:            committedMonthly,
		CurrentCoverage:             h.calculateCurrentCoverage(totalSavings, committedMonthly),
		TargetCoverage:              targetCoverage,
		YTDSavings:                  ytdSavings,
		ByService:                   byService,
	}, nil
}

// resolveDashboardAccountScope parses the account filter params, resolves
// them to the dual-column purchase-history filter inputs (cloud_accounts
// UUIDs and their cloud-provider external account numbers), and then
// intersects that caller-supplied scope with the session's allowed_accounts
// via intersectAccountFilterScope.
//
// Both are needed because purchase_history rows carry either identifier
// independently and the top-bar chip emits the UUID, so matching only
// cloud_account_id dropped every NULL-cloud_account_id row (issue #701/#498).
//
//   - account_ids (plural, UUIDs): resolved to UUID + external ids via
//     resolveAccountFilterIDs. Takes precedence over the legacy singular param.
//   - account_id (singular legacy): a top-bar chip UUID for current callers or a
//     raw external number for pre-UUID callers; resolved via
//     resolveSingleAccountFilterIDs.
//
// Issue #99: an EARLIER version of this function returned the caller-supplied
// filter as-is and left the allowed_accounts fallback to a guard in the
// caller that only fired when NO filter was supplied — so a restricted
// session that supplied an explicit (possibly out-of-scope) account_ids/
// account_id filter bypassed the session's allowed_accounts entirely, with no
// downstream post-filter to catch it (unlike handler_inventory.go's
// analogous no-filter-only retrofit, which happens to be saved by its own
// separate filterPurchaseHistoryByAllowedAccounts post-filter). The
// intersection now lives here, in the resolver, so every caller gets it for
// free rather than having to remember a guard or a post-filter.
func (h *Handler) resolveDashboardAccountScope(ctx context.Context, session *Session, params map[string]string) (uuids []string, externalIDsByProvider map[string][]string, err error) {
	parsedUUIDs, parseErr := parseAccountIDs(params["account_ids"])
	if parseErr != nil {
		return nil, nil, NewClientError(400, parseErr.Error())
	}

	var filterUUIDs []string
	var filterExternalIDsByProvider map[string][]string
	if len(parsedUUIDs) > 0 {
		// UUID-based multi-account filter — takes precedence over legacy param.
		filterUUIDs, filterExternalIDsByProvider = h.resolveAccountFilterIDs(ctx, parsedUUIDs)
	} else {
		// No plural UUID filter: fall back to the legacy singular param.
		filterUUIDs, filterExternalIDsByProvider = h.resolveSingleAccountFilterIDs(ctx, params["account_id"])
	}

	return h.intersectAccountFilterScope(ctx, session, filterUUIDs, filterExternalIDsByProvider)
}

// resolveAllowedAccountScope resolves a restricted session's allowed_accounts
// into the dual-column purchase-history filter inputs so dashboard commitment
// metrics and the inventory endpoints (fetchCommitmentRecords) never include
// accounts the session can't access (issue #956). It lists
// the cloud accounts, keeps those the session scope allows (AccountScope.Allows),
// and resolves their UUIDs through resolveAccountFilterIDs (same code path as an
// explicit filter).
//
// Returns (nil, nil, nil) for unrestricted / admin sessions so the caller keeps
// the all-accounts behavior. A restricted session that matches no account
// resolves to a non-nil-but-empty UUID set (a sentinel that selects no rows),
// so a scoped user with zero accessible accounts sees zeroed KPIs rather than
// everyone's data.
func (h *Handler) resolveAllowedAccountScope(ctx context.Context, session *Session) (uuids []string, externalIDsByProvider map[string][]string, err error) {
	allowed, err := h.getAccountScope(ctx, session)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if allowed.AllowsAll() {
		return nil, nil, nil
	}
	accounts, err := h.config.ListCloudAccounts(ctx, config.CloudAccountFilter{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list cloud accounts: %w", err)
	}
	// Non-nil empty slice: a sentinel meaning "scoped to zero accounts" so the
	// dual-column predicate matches no rows (never falls back to all-accounts).
	allowedUUIDs := []string{}
	for i := range accounts {
		a := accounts[i]
		if allowed.Allows(a.ID, a.Name) {
			allowedUUIDs = append(allowedUUIDs, a.ID)
		}
	}
	uuids, externalIDsByProvider = h.resolveAccountFilterIDs(ctx, allowedUUIDs)
	return uuids, externalIDsByProvider, nil
}

// filterDashboardRecommendations applies the session's allowed_accounts filter
// (already resolved into allowed) to the recommendations list before
// aggregation so scoped users don't see cross-account totals.
// Admin/unrestricted sessions pass through unchanged.
func filterDashboardRecommendations(allowed auth.AccountScope, recs []config.RecommendationRecord) []config.RecommendationRecord {
	if allowed.AllowsAll() {
		return recs
	}

	filtered := make([]config.RecommendationRecord, 0, len(recs))
	for i := range recs {
		if recs[i].CloudAccountID == nil {
			continue
		}
		id := *recs[i].CloudAccountID
		if allowed.Allows(id, "") {
			filtered = append(filtered, recs[i])
		}
	}
	return filtered
}

// summarizeRecommendationsWithCoverage is the dashboard reducer that
// respects per-account, per-service coverage overrides. Each rec's
// contribution is scaled by min(coverage, 100) / 100 looked up in
// coverageByKey via config.AccountConfigKey(account, provider, service).
// Pass coverageByKey=nil to disable scaling (every rec counts fully).
//
// Variants of the same physical-resource cell are deduped to a single
// representative before summing (see bestVariantPerCell) so the per-(term,
// payment) fan-out does not over-report savings; details in the function body.
//
// Recs without a CloudAccountID and recs whose triple has no entry in the
// map all count at full weight — this matches the pre-#196 behavior for
// un-configured accounts. Zero-coverage configs are excluded from the map
// by resolveCoverageByAccountKey (issue #201) so they also fall through to
// full weight rather than silently zeroing the headline.
//
// Coverage > 100 is capped at 100 so a misconfigured override cannot
// inflate the headline "potential savings" beyond the raw rec total.
//
// CONTRACT: every rec's Savings field represents 100%-coverage potential
// savings. All three upstream APIs satisfy this contract:
//   - AWS Cost Explorer GetReservationPurchaseRecommendation returns a
//     recommended quantity sized for ~100% coverage of historical demand
//     (cmd/helpers.go ApplyCoverage comment, issue #215 audit).
//   - Azure Consumption Reservation Recommendations (NetSavings) returns
//     the savings from purchasing the full recommended quantity.
//   - GCP Recommender PrimaryImpact.CostProjection returns the projected
//     savings from the recommended committed-use discount.
//
// The read-side scaling here (by the operator-configured coverage %) is
// therefore correct: it projects "how much would I save if I only bought
// RIs to cover X% of my instances" against the 100%-coverage baseline
// that every provider gives us. Verified by TestSummarizeRecommendationsWithCoverage_100PctContract.
//
// Note: CurrentSavings is NOT set here. It represents committed/realized
// savings from active purchase history, populated separately in
// getDashboardSummary via aggregateActiveCommitmentsPerService. A service
// with recommendations but no active purchases correctly ships
// current_savings: 0 (issue #1031).
func summarizeRecommendationsWithCoverage( //nolint:gocritic // unnamedResult: return names would conflict with body locals
	recs []config.RecommendationRecord,
	coverageByKey map[string]float64,
) (float64, map[string]ServiceSavings) {
	// Dedupe to one representative variant per physical-resource cell BEFORE
	// summing. After PR #195's per-(term, payment) fan-out, a single physical
	// resource produces up to 6 rec rows (2 terms x 3 payments). Those rows are
	// mutually-exclusive ALTERNATIVES, not additive: the operator can only buy
	// one commitment for that resource. Summing every variant inflated both the
	// headline total and each by_service[svc].PotentialSavings by ~6x. The
	// headline KPI already avoids this on the frontend by grouping per cell
	// (frontend/src/recommendations.ts cellKey / groupRecsByCell, and the
	// dashboard.ts:139-142 comment about "~6x inflation of summing every
	// variant"); this reducer is the backend equivalent.
	representatives := bestVariantPerCell(recs, coverageByKey)

	var total float64
	byService := make(map[string]ServiceSavings)
	for idx := range representatives {
		rep := representatives[idx]
		scaled := rep.scaled
		total += scaled
		svc := byService[rep.rec.Service]
		svc.PotentialSavings += scaled
		// CurrentSavings is populated by getDashboardSummary from actual
		// purchase_history commitments (calculateCommitmentMetrics). It is NOT
		// derived from recommendations here: doing so would fabricate
		// CurrentSavings == PotentialSavings for services with no active
		// commitments (i.e. recommend-but-never-purchased). Services absent
		// from currentSavingsByService correctly default to 0 per the
		// dashboard.ts contract.
		byService[rep.rec.Service] = svc
	}
	return total, byService
}

// cellRepresentative is the chosen variant for one physical-resource cell plus
// its already-computed coverage-scaled savings (cached so callers don't re-run
// scaledSavings).
type cellRepresentative struct {
	rec    config.RecommendationRecord
	scaled float64
}

// recCellKey composes the physical-resource cell key for a recommendation:
// (provider, cloud_account_id, service, region, resource_type, engine). Recs
// sharing this key are per-(term, payment) ALTERNATIVES for the same resource,
// not additive entries.
//
// Cross-language duplication note: this MUST stay aligned with the frontend
// cellKey in frontend/src/recommendations.ts, which joins the same six fields
// with "|" in this order: provider, cloud_account_id, service, region,
// resource_type, engine. The two implementations must bucket the exact same
// variants so the backend by_service rollup and the frontend per-cell grouping
// agree. A nil CloudAccountID maps to the empty segment, matching the
// frontend's nullish-coalescing of cloud_account_id to an empty string.
func recCellKey(rec config.RecommendationRecord) string {
	account := ""
	if rec.CloudAccountID != nil {
		account = *rec.CloudAccountID
	}
	return strings.Join([]string{
		rec.Provider,
		account,
		rec.Service,
		rec.Region,
		rec.ResourceType,
		rec.Engine,
	}, "|")
}

// bestVariantPerCell collapses recs to one representative per physical-resource
// cell, keeping the variant with the MAXIMUM coverage-scaled savings. Max (not
// sum) is correct because the variants are mutually-exclusive alternatives, and
// the best realistic single purchase is the per-cell potential a user expects.
// Order of first-seen cells is preserved for deterministic aggregation.
func bestVariantPerCell(
	recs []config.RecommendationRecord,
	coverageByKey map[string]float64,
) []cellRepresentative {
	indexByCell := make(map[string]int, len(recs))
	reps := make([]cellRepresentative, 0, len(recs))
	for i := range recs {
		rec := recs[i]
		scaled := scaledSavings(rec, coverageByKey)
		key := recCellKey(rec)
		if idx, ok := indexByCell[key]; ok {
			if scaled > reps[idx].scaled {
				reps[idx] = cellRepresentative{rec: rec, scaled: scaled}
			}
			continue
		}
		indexByCell[key] = len(reps)
		reps = append(reps, cellRepresentative{rec: rec, scaled: scaled})
	}
	return reps
}

// scaledSavings returns rec.Savings * min(max(coverage, 0), 100) / 100 when
// a coverage entry exists for the rec's (account, provider, service) triple.
// Otherwise returns rec.Savings unchanged.
func scaledSavings(rec config.RecommendationRecord, coverageByKey map[string]float64) float64 {
	if rec.CloudAccountID == nil || coverageByKey == nil {
		return rec.Savings
	}
	coverage, ok := coverageByKey[config.AccountConfigKey(*rec.CloudAccountID, rec.Provider, rec.Service)]
	if !ok {
		return rec.Savings
	}
	if coverage <= 0 {
		return 0
	}
	if coverage >= 100 {
		return rec.Savings
	}
	return rec.Savings * coverage / 100
}

// resolveCoverageByAccountKey returns a map of AccountConfigKey -> resolved
// coverage% for every (account, provider, service) triple represented in
// recs. Lookup errors degrade gracefully to a nil map (no scaling applied
// → un-overridden behavior).
//
// A configured coverage of zero stays in the map, so scaledSavings shows zero
// savings for it: 0% means "buy nothing" (#523). Only a triple with no
// configured coverage at all is omitted, and scaledSavings then falls through
// to full savings, matching the pre-#196 behavior for un-configured accounts
// (#201).
func (h *Handler) resolveCoverageByAccountKey(ctx context.Context, recs []config.RecommendationRecord) map[string]float64 {
	if len(recs) == 0 {
		return nil
	}
	out, err := config.ResolveConfiguredCoverageForRecs(ctx, h.config, recs)
	if err != nil {
		logging.Errorf("dashboard: failed to resolve per-account configs for coverage cap; using un-scaled totals: %v", err)
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// resolveTargetCoverage returns the configured default coverage or 80% when
// no global config is set or the configured value is zero (a value-typed
// field cannot tell an unset 0 from a configured one).
func (h *Handler) resolveTargetCoverage(ctx context.Context) float64 {
	globalCfg, err := h.config.GetGlobalConfig(ctx)
	if err != nil {
		logging.Warnf("resolveTargetCoverage: could not load global config, using default 80%%: %v", err)
	}
	if globalCfg != nil && globalCfg.DefaultCoverage > 0 {
		return globalCfg.DefaultCoverage
	}
	return 80.0
}

// getUpcomingPurchases enumerates pending purchase_executions joined to
// their parent PurchasePlan for display. Mirrors the data flow in
// getPlannedPurchases (handler_purchases_planned.go) so the dashboard widget and
// the Plans page walk the same canonical "what's about to happen" set.
//
// The widget previously enumerated plans and synthesized one row per plan
// from plan.NextExecutionDate. That was wrong because action endpoints
// (DELETE /api/purchases/planned/{id}, pause, resume, run) all target
// purchase_executions.execution_id, not purchase_plans.id; the Cancel
// button could only fire correctly when the row in front of the operator
// IS a real execution. PR #207 worked around it by routing Cancel to
// api.deletePlan(planID), but that deleted the whole plan — too
// aggressive for "skip this scheduled run". This version emits actual
// execution rows so Cancel can target just the planned purchase via
// DELETE /api/purchases/planned/{id}.
//
// The optional provider, account_ids and legacy account_id query parameters
// narrow the rows like the summary route does (issue #713, see
// parseUpcomingFilter); they never widen the session's allowed_accounts.
func (h *Handler) getUpcomingPurchases(ctx context.Context, req *events.LambdaFunctionURLRequest) (*UpcomingPurchaseResponse, error) {
	session, err := h.requirePermission(ctx, req, "view", "purchases")
	if err != nil {
		return nil, err
	}
	// Issue #713: the topbar provider/account chips, validated before any read.
	filter, err := h.parseUpcomingFilter(ctx, session, req.QueryStringParameters)
	if err != nil {
		return nil, err
	}

	executions, err := h.config.GetPendingExecutions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get pending executions: %w", err)
	}

	plans, err := h.config.ListPurchasePlans(ctx, config.PurchasePlanFilter{})
	if err != nil {
		return nil, fmt.Errorf("failed to get purchase plans: %w", err)
	}
	planMap := make(map[string]*config.PurchasePlan, len(plans))
	for i := range plans {
		planMap[plans[i].ID] = &plans[i]
	}

	scope, err := h.getAccountScope(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	upcoming, err := h.upcomingInScope(ctx, scope, filter, executions, planMap)
	if err != nil {
		return nil, err
	}
	return &UpcomingPurchaseResponse{Purchases: upcoming}, nil
}

// upcomingInScope builds the upcoming rows for the executions whose plan still
// exists, that the session scope can see, and that pass the caller's filter,
// in input order.
func (h *Handler) upcomingInScope(ctx context.Context, scope auth.AccountScope, filter upcomingFilter, executions []config.PurchaseExecution, planMap map[string]*config.PurchasePlan) ([]UpcomingPurchase, error) {
	// Memoises GetPlanAccounts, mirroring getPlannedPurchases.
	planAccounts := make(map[string][]config.CloudAccount)

	var upcoming []UpcomingPurchase
	for i := range executions {
		exec := executions[i]
		plan := planMap[exec.PlanID]
		if plan == nil {
			// Orphaned execution (plan was deleted but cleanup missed it).
			// Hide rather than crash; cleanup is a separate concern.
			continue
		}
		ok, err := h.isExecutionInScope(ctx, scope, &exec, planAccounts)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		row := upcomingFromExecution(plan, &exec)
		ok, err = filter.matches(ctx, h, &row, &exec, planAccounts)
		if err != nil {
			return nil, err
		}
		if ok {
			upcoming = append(upcoming, row)
		}
	}
	return upcoming, nil
}

// firstServiceConfig returns the ServiceConfig for the lexicographically first
// key in plan.Services, giving a deterministic result regardless of map
// iteration order. Returns the zero value when the map is empty (both callers
// tolerate empty provider/service strings — they fall back to blank display
// fields rather than panicking).
func firstServiceConfig(plan *config.PurchasePlan) config.ServiceConfig {
	if len(plan.Services) == 0 {
		return config.ServiceConfig{}
	}
	keys := make([]string, 0, len(plan.Services))
	for k := range plan.Services {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return plan.Services[keys[0]]
}

// upcomingFromExecution projects a (plan, execution) pair onto the
// UpcomingPurchase response. Uses the first service entry from the plan as
// representative — the response shape doesn't support multi-service plans.
// Step number comes from the execution row directly (already stamped by the
// scheduler at instance-create time).
func upcomingFromExecution(plan *config.PurchasePlan, exec *config.PurchaseExecution) UpcomingPurchase {
	display := planDisplayFor(plan)
	svc := display.svc
	return UpcomingPurchase{
		Providers:        display.providers,
		ExecutionID:      exec.ExecutionID,
		PlanID:           plan.ID,
		PlanName:         plan.Name,
		ScheduledDate:    exec.ScheduledDate.Format("2006-01-02"),
		Provider:         svc.Provider,
		Service:          svc.Service,
		StepNumber:       exec.StepNumber,
		TotalSteps:       plan.RampSchedule.TotalSteps,
		EstimatedSavings: exec.EstimatedSavings,
		CreatedByUserID:  exec.CreatedByUserID,

		HasRecommendations: len(exec.Recommendations) > 0,
	}
}

// getPublicInfo returns public information about the CUDly instance (no auth required).
// No rate limiting — this is hit by Terraform deployment checks and the frontend on every page load.
// Sensitive identifiers (API key secret URL, deployment AWS account ID) are intentionally
// absent here; they live on the authenticated GET /api/info/deployment endpoint (#633).
func (h *Handler) getPublicInfo(ctx context.Context, req *events.LambdaFunctionURLRequest) (*PublicInfoResponse, error) { //nolint:unparam // req is part of router handler signature; reserved for future rate-limit or header inspection
	// Check if admin exists
	adminExists := false
	if h.auth != nil {
		exists, err := h.auth.CheckAdminExists(ctx)
		if err == nil {
			adminExists = exists
		}
	}

	return &PublicInfoResponse{
		Version:     "1.0.0",
		AdminExists: adminExists,
	}, nil
}

// getDeploymentInfo returns sensitive deployment identifiers for authenticated callers.
// Requires at least AuthUser (enforced by the router). The two fields it returns
// expose the AWS account ID and the Secrets Manager ARN path — neither should be
// reachable without a valid session (#633).
func (h *Handler) getDeploymentInfo(ctx context.Context, _ *events.LambdaFunctionURLRequest) (*DeploymentInfoResponse, error) { //nolint:unparam // error return is part of router handler interface
	// Build the AWS Console deep-link to the Secrets Manager secret.
	var apiKeySecretURL string
	if h.secretsARN != "" {
		// Extract region from ARN: arn:aws:secretsmanager:region:account:secret:name
		parts := strings.Split(h.secretsARN, ":")
		if len(parts) >= 4 {
			region := parts[3]
			apiKeySecretURL = fmt.Sprintf("https://%s.console.aws.amazon.com/secretsmanager/secret?name=%s&region=%s",
				region, h.secretsARN, region)
		}
	}

	// Resolve the host AWS account ID so the frontend can distinguish a
	// legitimate ambient-credential execution (cloud_account_id IS NULL
	// because the rec targets the CUDly Lambda's own account) from an
	// orphan execution whose account was deleted (issue #608). The call
	// is best-effort: non-AWS deployments and STS transient failures
	// return "" and the frontend falls back to the "Account deleted"
	// warning label, which is safe.
	deploymentAWSAccountID, awsIDErr := h.resolveAWSAccountID(ctx)
	if awsIDErr != nil {
		logging.Debugf("getDeploymentInfo: best-effort AWS account ID resolution failed: %v", awsIDErr)
	}

	return &DeploymentInfoResponse{
		APIKeySecretURL:        apiKeySecretURL,
		DeploymentAWSAccountID: deploymentAWSAccountID,
	}, nil
}
