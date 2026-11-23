package api

import (
	"context"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

// getRecommendationDetail returns the per-id drill-down payload backing
// the Recommendations row-click drawer. Surfaces the data that's already
// computed server-side (provider/service for provenance, savings + count
// for the confidence bucket) on a per-id GET so the listing payload
// stays compact for the common case where the drawer never opens.
//
// Contract documented in issue #44 + RecommendationDetailResponse in
// types.go. Gated by view:recommendations permission. Returns errNotFound
// on unknown id (404) and on ids that exist but belong to an account the
// caller is not allowed to see (matches the existence-disclosure pattern
// used by handler_accounts.go's account lookup).
//
// usage_history is intentionally empty in this first pass: the collector
// pipeline does not yet persist time-series utilization per
// recommendation. Surfacing the missing field as an empty slice (rather
// than a 501) keeps the drawer functional today and means the day the
// collector starts populating it, the frontend automatically picks it
// up. The empty-slice case is documented in known_issues/28.
func (h *Handler) getRecommendationDetail(ctx context.Context, req *events.LambdaFunctionURLRequest, id string) (*RecommendationDetailResponse, error) {
	// Authn/permission gate runs first so an unauthenticated caller
	// can't probe id-shape validation to learn anything about the
	// endpoint's existence.
	session, err := h.requirePermission(ctx, req, "view", "recommendations")
	if err != nil {
		return nil, err
	}

	if id == "" {
		return nil, NewClientError(400, "recommendation id is required")
	}

	// GetRecommendationByID bypasses the account-override filter so that a
	// deep-linked URL to a rec that has been hidden by an account override
	// still resolves. Suppressions are still applied: a fully-suppressed rec
	// (actively dismissed) returns nil and we 404 as before. See issue #214.
	rec, hiddenBy, err := h.scheduler.GetRecommendationByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch recommendation: %w", err)
	}
	if rec == nil {
		return nil, errNotFound
	}

	// Tenant-scoping gate: an authenticated user must not be able to read recs
	// from accounts outside their allowed set, even via the detail endpoint.
	// The existence-disclosure-safe check (match before reveal) is preserved:
	// we checked GetRecommendationByID first and only reach here on a hit, so
	// the caller still cannot distinguish "doesn't exist" from "wrong tenant"
	// via a timing or wording difference.
	visible, err := h.filterRecommendationsByAllowedAccounts(ctx, session, []config.RecommendationRecord{*rec})
	if err != nil {
		return nil, err
	}
	if len(visible) == 0 {
		return nil, errNotFound
	}

	resp := h.buildRecommendationDetail(ctx, &visible[0])
	if len(hiddenBy) > 0 {
		resp.HiddenBy = hiddenBy
	}
	return resp, nil
}

// buildRecommendationDetail assembles the drawer payload from a single
// recommendation record + the global freshness state. The freshness
// lookup is best-effort; on error the provenance note degrades to its
// no-window form so the drawer still renders.
func (h *Handler) buildRecommendationDetail(ctx context.Context, rec *config.RecommendationRecord) *RecommendationDetailResponse {
	return &RecommendationDetailResponse{
		ID:               rec.ID,
		UsageHistory:     []UsagePoint{},
		ConfidenceBucket: confidenceBucketFor(rec.Savings, rec.Count),
		ProvenanceNote:   h.provenanceNoteFor(ctx, rec),
	}
}

// confidenceBucketFor mirrors the heuristic that previously lived
// client-side in frontend/src/recommendations.ts. Centralizing it on
// the server lets future provider-specific tuning land in one place
// without a frontend deploy. Thresholds intentionally match the original
// shim 1:1 so the drawer label doesn't visibly shift on rollout.
//
//	high   = savings >= exchange.ConfidenceHighSavingsUSD AND count >= exchange.ConfidenceHighMinCount
//	medium = savings >= exchange.ConfidenceMediumSavingsUSD
//	low    = otherwise
//
// The cut-offs come from pkg/exchange, which scores the reshape ranking the
// badge accompanies, so the two cannot drift apart (#343).
//
// A count below 1 (missing) is clamped to 1, so savings alone decide between
// medium and low. This deliberately differs from the exchange ranking, which
// returns a neutral score for a zero count: a badge must always show a
// bucket, while a ranking score should stay neutral on missing data.
func confidenceBucketFor(savings float64, count int) string {
	if count < 1 {
		count = 1
	}
	if savings >= exchange.ConfidenceHighSavingsUSD && count >= exchange.ConfidenceHighMinCount {
		return "high"
	}
	if savings >= exchange.ConfidenceMediumSavingsUSD {
		return "medium"
	}
	return "low"
}

// provenanceNoteFor returns the short "Derived from … last collected …"
// string the drawer renders verbatim. Format mirrors the wording the
// frontend previously assembled inline so the rollout doesn't visibly
// change the drawer copy. Timestamp is RFC3339 UTC; the frontend can
// re-format relative if it wants — keeping the wire format absolute
// avoids ambiguity around the user's locale.
func (h *Handler) provenanceNoteFor(ctx context.Context, rec *config.RecommendationRecord) string {
	provider := providerDisplayName(rec.Provider)
	base := fmt.Sprintf("%s %s recommendation APIs", provider, rec.Service)

	freshness, err := h.config.GetRecommendationsFreshness(ctx)
	if err != nil || freshness == nil || freshness.LastCollectedAt == nil {
		return base + "."
	}
	ts := freshness.LastCollectedAt.UTC().Format(time.RFC3339)
	return fmt.Sprintf("%s · last collected %s", base, ts)
}

// providerDisplayName converts the lowercase provider slug used in
// storage to the canonical display casing used in user-facing strings.
// Mirrors the equivalent helper in frontend/src/recommendations.ts.
func providerDisplayName(provider string) string {
	switch provider {
	case "aws":
		return "AWS"
	case "azure":
		return "Azure"
	case "gcp":
		return "GCP"
	default:
		return provider
	}
}
