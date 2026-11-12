package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

// ExecutePurchaseRequest represents the request to execute purchases.
type ExecutePurchaseRequest struct {
	// Recommendations are matched against the stored recommendation set on
	// (provider, cloud_account_id, service, region, resource_type, engine,
	// term, payment). Only count, recommended_count and selected are taken
	// from the request; id, details and every cost field are replaced from
	// the stored row scaled by count (priceRecommendationsFromStore, #1905).
	Recommendations []config.RecommendationRecord `json:"recommendations"`
	// CapacityPercent is what fraction (1..100) of the originally-
	// recommended counts the user chose in the bulk Purchase flow.
	// Audit-only: the Recommendations slice already carries scaled
	// counts, so backend math ignores this field for purchase work.
	// 0 / absent defaults to 100 ("full capacity").
	CapacityPercent int `json:"capacity_percent,omitempty"`
	// ExecuteMode controls whether this request bypasses the approval
	// email and executes the purchase immediately. The only accepted
	// non-empty value is "direct"; any other value is treated as the
	// default approval-required flow. The handler re-checks the
	// execute-any/execute-own RBAC gate before honoring "direct",
	// even if the session already passed the execute:purchases gate in
	// validateExecutePurchaseRequest, so a client that sets this field
	// without the privilege receives a 403 rather than silent fallback.
	ExecuteMode string `json:"execute_mode,omitempty"`
}

// validateExecutePurchaseRequest handles the permission check, body parse,
// and recommendation-list bounds + scope checks. Extracted so executePurchase
// itself stays linear and under the gocyclo threshold. The returned
// PaymentAdjustment slice carries any payment-option coercions performed by
// the per-rec validation, for the response to surface to the caller (#1503
// follow-up); it is empty/nil when every rec was already canonical.
func (h *Handler) validateExecutePurchaseRequest(ctx context.Context, req *events.LambdaFunctionURLRequest) (ExecutePurchaseRequest, *Session, []PaymentAdjustment, error) {
	// Refuses a creator-less submit under 4-eyes before anything is
	// persisted, on both the approval-email and direct-execute paths (#677).
	session, err := h.requirePurchaseCreatingPermission(ctx, req, "execute", "purchases")
	if err != nil {
		return ExecutePurchaseRequest{}, nil, nil, err
	}
	execReq, err := parseExecutePurchaseBody(req.Body)
	if err != nil {
		return ExecutePurchaseRequest{}, nil, nil, err
	}
	// Scope: reject the whole request if any recommendation targets an
	// account outside the session's allowed_accounts. Safer than silently
	// dropping the out-of-scope ones — the user explicitly chose those
	// recommendations; a partial execution would misrepresent intent.
	// Runs before per-rec content validation so an out-of-scope request is
	// rejected as 403 regardless of the rec's Term/Payment/Count contents.
	if err := h.validatePurchaseRecommendationScope(ctx, session, execReq.Recommendations); err != nil {
		return ExecutePurchaseRequest{}, nil, nil, err
	}
	// Per-rec Provider/Service/Term/Payment/Count validation at the API
	// boundary so a malformed client-supplied rec (e.g. Term:7, Payment:"foo",
	// negative Count) is rejected here rather than reaching the cloud SDK at
	// execute time (#643). This is scoped to the web execute path only — the
	// retry path replays recs from an already-validated execution and must
	// not be re-gated by the same rules.
	adjustments, recErr := validateExecutePurchaseRecommendations(execReq.Recommendations)
	if recErr != nil {
		return ExecutePurchaseRequest{}, nil, nil, recErr
	}
	// Cross-check the audit-only capacity_percent against the scaled rec
	// counts so the persisted execution can't claim a capacity that
	// disagrees with what was actually purchased (#647). Skipped per-rec
	// when the rec carries no recommended_count.
	if err := validateCapacityConsistency(execReq.Recommendations, execReq.CapacityPercent); err != nil {
		return ExecutePurchaseRequest{}, nil, nil, err
	}
	// Price every rec from the stored recommendation set, then enforce the
	// per-permission Constraints (MaxPurchaseAmount, Providers, Services,
	// Regions, AccountIDs) on the granting execute:purchases permission
	// (SEC-01, issue #1141) against those store-derived values. Runs after
	// the per-rec validation above so provider and payment tokens are
	// canonical before the identity match, and after the scope check so an
	// out-of-scope account is refused as 403 before any pricing lookup. The
	// client's own upfront_cost / monthly_cost / savings / details are never
	// used (issue #1905): a purchaser under a MaxPurchaseAmount cap could
	// otherwise state any number and buy at list price.
	if err := h.priceAndEnforcePurchaseConstraints(ctx, session, execReq.Recommendations); err != nil {
		return ExecutePurchaseRequest{}, nil, nil, err
	}
	return execReq, session, adjustments, nil
}

// parseExecutePurchaseBody decodes an execute request, bounds its
// recommendation count to 1..1000 and normalizes capacity_percent. It runs
// after the permission check and before every scope and content check.
func parseExecutePurchaseBody(body string) (ExecutePurchaseRequest, error) {
	var execReq ExecutePurchaseRequest
	if err := json.Unmarshal([]byte(body), &execReq); err != nil {
		return ExecutePurchaseRequest{}, NewClientError(400, "invalid request body")
	}
	const maxRecommendations = 1000
	if len(execReq.Recommendations) == 0 {
		return ExecutePurchaseRequest{}, NewClientError(400, "no recommendations provided")
	}
	if len(execReq.Recommendations) > maxRecommendations {
		return ExecutePurchaseRequest{}, NewClientError(400, fmt.Sprintf("too many recommendations: %d (max %d)", len(execReq.Recommendations), maxRecommendations))
	}
	if err := normalizeCapacityPercent(&execReq); err != nil {
		return ExecutePurchaseRequest{}, err
	}
	return execReq, nil
}

// enforcePurchaseConstraints builds the per-recommendation
// auth.PermissionConstraints sets (purchaseConstraintSets) and enforces them
// against session's action:purchases permission, first rejecting a batch
// whose total commitment computes to zero (requireNonZeroCommitment).
// Shared by every money-mutating purchases call site (direct-execute request
// validation, the retry path, execute-any/own, approve-any/own and
// runPlannedPurchase) so all of them re-derive and check the exact same
// Constraints against the exact same zero-commitment guard (SEC-01, issue
// #1141; adversarial review follow-up to #1210 -- the retry path previously
// skipped this check entirely; issue #60 review follow-up -- approve-any/own
// and runPlannedPurchase previously skipped requireNonZeroCommitment, so an
// execution whose recs summed to $0 read as an uncapped MaxPurchaseAmount).
// action is threaded through (not hardcoded) because callers check
// different verbs: "execute" for the web submit and retry paths and
// runPlannedPurchase, "execute-any"/"execute-own" for direct-execute,
// "approve-any"/"approve-own" for session approve. recs carry store-derived
// or persisted-at-submit-time costs depending on the caller; none of these
// call sites ever price from client-supplied numbers. MaxPurchaseAmount is
// USD, so a batch with any rec not provably priced in USD is checked by
// requireUncappedPurchaseGrant instead of by its raw amount (issue #361).
func (h *Handler) enforcePurchaseConstraints(ctx context.Context, session *Session, action string, recs []config.RecommendationRecord) error {
	constraintSets := purchaseConstraintSets(recs)
	if err := requireNonZeroCommitment(constraintSets); err != nil {
		return err
	}
	if currency := nonUSDPurchaseCurrency(recs); currency != "" {
		return h.requireUncappedPurchaseGrant(ctx, session, action, constraintSets, currency)
	}
	return h.requirePermissionConstraints(ctx, session, action, "purchases", constraintSets)
}

// purchaseConstraintSets builds one auth.PermissionConstraints per
// recommendation in a web execute request, for the SEC-01 constraint
// enforcement in validateExecutePurchaseRequest. Single-value Provider/
// Service/Region/AccountIDs lists make the auth service's any-overlap
// matcher equivalent to strict containment, so a batch cannot pass on the
// strength of one in-scope rec while another rec is out of scope.
// MaxPurchaseAmount carries the batch's TOTAL commitment (upfront cost plus
// the full recurring obligation over the term, see recTotalCommitment) on
// every set, not just the upfront cost. A no-upfront or partial-upfront
// commitment's UpfrontCost is legitimately $0 or small, so capping on
// upfront alone let a batch with an arbitrarily large recurring commitment
// evade the cap using entirely honest data (adversarial review follow-up
// to #1210). AccountIDs is ALWAYS populated: a rec without a CloudAccountID
// carries unattributedAccountConstraint so an AccountIDs-constrained
// permission denies it (the auth matcher treats an empty request-side list
// as satisfied, so omitting the dimension would fail open). Session-level
// allowed_accounts scoping is independently enforced by
// validatePurchaseRecommendationScope.
func purchaseConstraintSets(recs []config.RecommendationRecord) []auth.PermissionConstraints {
	var totalCommitment float64
	for i := range recs {
		totalCommitment += recTotalCommitment(&recs[i])
	}
	sets := make([]auth.PermissionConstraints, 0, len(recs))
	for i := range recs {
		rec := &recs[i]
		accountID := unattributedAccountConstraint
		if rec.CloudAccountID != nil && *rec.CloudAccountID != "" {
			accountID = *rec.CloudAccountID
		}
		sets = append(sets, auth.PermissionConstraints{
			Providers:         []string{rec.Provider},
			Services:          []string{rec.Service},
			Regions:           []string{rec.Region},
			AccountIDs:        []string{accountID},
			MaxPurchaseAmount: totalCommitment,
		})
	}
	return sets
}
