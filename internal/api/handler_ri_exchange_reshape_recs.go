package api

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-lambda-go/events"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// reshapeRequestParams groups parsed query parameters for getReshapeRecommendations.
type reshapeRequestParams struct {
	region       string
	threshold    float64
	lookbackDays int
}

// parseReshapeParams parses the threshold, lookback_days, and region query
// parameters for getReshapeRecommendations in a single call, reducing the
// number of error-check branches in the handler to keep it within the
// gocyclo limit.
func parseReshapeParams(params map[string]string) (reshapeRequestParams, error) {
	threshold, err := parseThresholdParam(params)
	if err != nil {
		return reshapeRequestParams{}, err
	}
	lookbackDays, err := parseLookbackDaysParam(params)
	if err != nil {
		return reshapeRequestParams{}, err
	}
	return reshapeRequestParams{
		threshold:    threshold,
		lookbackDays: lookbackDays,
		region:       params["region"],
	}, nil
}

// getReshapeRecommendations orchestrates fetching convertible RIs + utilization
// and returns reshape recommendations.
func (h *Handler) getReshapeRecommendations(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "view", "purchases")
	if err != nil {
		return nil, err
	}

	// Validate input before resolving the account, so a malformed request
	// makes no STS or account lookup.
	p, err := parseReshapeParams(req.QueryStringParameters)
	if err != nil {
		return nil, err
	}

	// Apply the session's allowed_accounts scope. The resolved account also
	// keys the utilization cache and scopes the recs lookup below.
	cloudAccountID, inScope, sErr := h.resolveReshapeAccountScope(ctx, session)
	if sErr != nil {
		return nil, sErr
	} else if !inScope {
		return &ReshapeRecommendationsResponse{Recommendations: []exchange.ReshapeRecommendation{}}, nil
	}

	cfg, err := h.loadAWSConfigWithRegion(ctx, p.region)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	// Normalize region: when the caller omits ?region=, loadAWSConfigWithRegion
	// resolves a default from the AWS SDK chain but the local string stays
	// empty — which would scope the RI utilization cache and the recs lookup
	// unscoped, leaking alternatives from other regions onto the reshape page.
	// Adopt the resolved region so every downstream consumer sees the same
	// value the AWS clients are actually talking to.
	if p.region == "" {
		p.region = cfg.Region
	}

	ec2Client := h.buildReshapeEC2Client(cfg)
	instances, err := ec2Client.ListConvertibleReservedInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list convertible RIs: %w", err)
	}

	recsAdapter := h.buildReshapeRecsClient(cfg)
	fetch := func(fetchCtx context.Context, days int) ([]riUtilization, error) {
		return recsAdapter.GetRIUtilization(fetchCtx, days, p.region)
	}
	cacheKey := config.RIUtilizationCacheKey{CloudAccountID: cloudAccountID, Region: p.region, LookbackDays: p.lookbackDays}
	utilData, err := h.getRIUtilizationCache().getOrFetch(ctx, cacheKey, riUtilizationCacheTTL, riUtilizationCacheStaleTTL, fetch)
	if err != nil {
		return nil, fmt.Errorf("failed to get RI utilization: %w", err)
	}

	riInfos, utilInfos, excluded := convertToExchangeTypes(instances, utilData)
	// Cross-family alternatives are sourced from the cached AWS Cost
	// Explorer purchase recommendations table in Postgres — no per-rec
	// DescribeReservedInstancesOfferings fan-out, no hand-curated
	// peer-family allowlist. The lookup is scoped to the running AWS
	// account (when registered) so a multi-tenant deployment can't
	// surface another tenant's recs. Empty resolved account ID means
	// "no scope filter" for ambient-credentials deployments where
	// CloudAccount registration hasn't happened yet; a real ListCloudAccounts
	// error aborted the request at the scope check above instead of silently
	// falling through to an unscoped query that could match the wrong
	// tenant's recs.
	currencyCode := firstNonEmptyCurrency(instances)
	lookup := purchaseRecLookupFromStore(h.config, cloudAccountID)
	offered := OfferedInstanceTypes(ctx, p.region, ec2Client.GetValidResourceTypes)
	recs := exchange.AnalyzeReshapingWithRecs(ctx, riInfos, utilInfos, p.threshold, p.region, currencyCode, lookup,
		exchange.WithOfferedInstanceTypes(offered))

	resp := &ReshapeRecommendationsResponse{Recommendations: recs, ExcludedRIs: excluded}
	h.attachReshapeStaleness(ctx, resp)
	return resp, nil
}

// attachReshapeStaleness populates the RecsStaleness and RecsCollectedAt
// fields on resp from the recommendations_state table. Non-fatal: a read
// error is logged and reported as hard-stale, so the reshape table itself is
// unaffected by a DB read-side failure but the UI never shows unknown data as
// fresh.
func (h *Handler) attachReshapeStaleness(ctx context.Context, resp *ReshapeRecommendationsResponse) {
	freshness, err := h.config.GetRecommendationsFreshness(ctx)
	if err != nil {
		// Unknown freshness is hard-stale, like the cold-start case below: an
		// empty RecsStaleness would tell the UI the data is fresh.
		logging.Warnf("getReshapeRecommendations: could not check recs freshness (reporting hard-stale): %v", err)
		resp.RecsStaleness = "hard"
		return
	}
	resp.RecsCollectedAt = freshness.LastCollectedAt
	if freshness.LastCollectedAt == nil {
		// Cold start: cache was never populated — treat as hard-stale so the
		// banner fires on a fresh deployment rather than silently hiding it.
		resp.RecsStaleness = "hard"
	} else {
		resp.RecsStaleness = classifyRecsAge(time.Since(*freshness.LastCollectedAt))
	}
}

// firstNonEmptyCurrency returns the first RI CurrencyCode set, or "" when none
// is, so the response omits currency_code instead of claiming USD (#361).
func firstNonEmptyCurrency(instances []ec2ConvertibleRI) string {
	for i := range instances {
		inst := instances[i]
		if inst.CurrencyCode != "" {
			return inst.CurrencyCode
		}
	}
	return ""
}

// ReshapeRecommendationsResponse holds reshape recommendations.
//
// RecsStaleness is empty when the underlying Cost Explorer cache is
// fresh, "soft" when it is older than reshapeSoftStaleThreshold (12 h),
// and "hard" when it is older than reshapeHardStaleThreshold (24 h).
// RecsCollectedAt carries the raw timestamp so the frontend can build
// its own relative-time label ("last collected 23h ago").
type ReshapeRecommendationsResponse struct {
	RecsCollectedAt *time.Time                       `json:"recs_collected_at,omitempty"`
	RecsStaleness   string                           `json:"recs_staleness,omitempty"`
	Recommendations []exchange.ReshapeRecommendation `json:"recommendations"`
	// ExcludedRIs lists RIs left out of cost-based analysis (and so have no
	// recommendation) because their monthly cost could not be computed.
	ExcludedRIs []ExcludedRI `json:"excluded_ris,omitempty"`
}

// reshapeSoftStaleThreshold is the age at which the reshape recs banner
// transitions to "soft" warning: data may be up to 12 h old.
const reshapeSoftStaleThreshold = 12 * time.Hour

// reshapeHardStaleThreshold is the age at which the reshape recs banner
// transitions to "hard" warning: data is more than 24 h old.
const reshapeHardStaleThreshold = 24 * time.Hour

// classifyRecsAge maps a data age to the staleness label surfaced in
// ReshapeRecommendationsResponse.RecsStaleness. The zero duration
// (cold-cache path: no LastCollectedAt) is treated as "hard" so the
// banner fires on a fresh deployment rather than silently hiding it.
func classifyRecsAge(age time.Duration) string {
	switch {
	case age >= reshapeHardStaleThreshold:
		return "hard"
	case age >= reshapeSoftStaleThreshold:
		return "soft"
	default:
		return ""
	}
}
