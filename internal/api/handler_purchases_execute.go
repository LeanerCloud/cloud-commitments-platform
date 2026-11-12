package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
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

// recTotalCommitment returns a single recommendation's full committed
// spend: the upfront cost plus the recurring monthly cost multiplied by
// the term length in months. UpfrontCost and MonthlyCost are both totals
// for the rec's Count (AWS Cost Explorer reports them per-batch, not
// per-instance -- see recommendationToOffering), so they sum directly
// with no per-instance scaling needed. rec.Term is in years (AWS/Azure/GCP
// RI/SP standard; see exchange_lookup.go), converted to months with the
// same *12 convention used throughout this package. A nil MonthlyCost
// (all-upfront commitments have no recurring charge) or a non-positive
// Term (can't be converted to months) contribute nothing to the recurring
// leg, matching the Term<=0 handling elsewhere in this package. On the web
// execute path the two cost fields are the store-derived values written by
// priceRecommendationsFromStore.
func recTotalCommitment(rec *config.RecommendationRecord) float64 {
	return rec.TotalCommitment()
}

// requireNonZeroCommitment guards against a purchase batch whose computed
// total commitment (see recTotalCommitment) is exactly zero even though it
// targets real, count>0 resources. A genuine RI/Savings Plan/commitment
// purchase always carries SOME committed spend -- either an upfront cost
// (all/partial-upfront) or a recurring monthly charge (no-upfront); a batch
// that totals to exactly $0 is not "unconstrained", it is malformed or
// adversarial data (e.g. a no-upfront rec submitted with monthly_cost
// omitted or zeroed out to slip under a MaxPurchaseAmount cap -- the same
// evasion shape recTotalCommitment was introduced to close). Per this
// repo's no-silent-fallback-on-money-paths convention, a zero total must
// fail loud rather than be silently treated as "no constraint applies"
// (MaxPurchaseAmount==0 reads as uncapped to matchPurchaseAmountConstraint).
// sets is assumed non-empty whenever recs is (callers already reject empty
// recommendation batches before reaching this check).
func requireNonZeroCommitment(sets []auth.PermissionConstraints) error {
	if len(sets) > 0 && sets[0].MaxPurchaseAmount == 0 {
		return NewClientError(400, "recommendation batch has zero total commitment (no upfront or recurring cost); refusing to evaluate purchase constraints against it")
	}
	return nil
}

// normalizeCapacityPercent defaults an absent/zero capacity_percent to 100
// and rejects anything outside [1, 100]. capacity_percent is audit-only but
// still bounded: a value outside the range is a client bug worth surfacing
// rather than silently clamping. Extracted so validateExecutePurchaseRequest
// stays under the gocyclo threshold.
func normalizeCapacityPercent(execReq *ExecutePurchaseRequest) error {
	if execReq.CapacityPercent == 0 {
		execReq.CapacityPercent = 100
	}
	if execReq.CapacityPercent < 1 || execReq.CapacityPercent > 100 {
		return NewClientError(400, fmt.Sprintf("capacity_percent must be between 1 and 100, got %d", execReq.CapacityPercent))
	}
	return nil
}

// validateExecutePurchaseRecommendations runs the per-rec #643 boundary
// validation over every rec in a web execute request, then the one-account-
// per-batch rule (#1902), returning the first failure. On success it also
// returns the payment-option coercions that occurred (nil-free, in rec order)
// so the response can surface them to the caller (#1503 follow-up). Extracted
// so validateExecutePurchaseRequest stays under the gocyclo threshold.
func validateExecutePurchaseRecommendations(recs []config.RecommendationRecord) ([]PaymentAdjustment, error) {
	var adjustments []PaymentAdjustment
	for i := range recs {
		adjustment, err := validatePurchaseRecommendation(&recs[i], i)
		if err != nil {
			return nil, err
		}
		if adjustment != nil {
			adjustments = append(adjustments, *adjustment)
		}
	}
	// One plan-less execution targets exactly one cloud account (#1902).
	// Same rule the executor enforces in resolveSingleAccountProvider, applied
	// here so the batch is refused before an execution row exists, an
	// approval email goes out, or a History row lands as failed.
	if _, err := purchase.SingleCloudAccountIDFromRecs(recs); err != nil {
		return nil, NewClientError(400, err.Error())
	}
	return adjustments, nil
}

// finalizePurchaseStatus flips an execution's stored status to "failed" if
// the approval email couldn't send, and returns the status string the API
// response should carry. Returns "pending" when email_sent is true.
//
// The write is guarded on the row still being "pending" (issue #1232). A
// session approve, cancel or pause can land while the email is being sent;
// an unguarded write would then stamp "failed" over a row that was already
// approved and bought, which makes it eligible for retry and a second
// purchase. A lost guard is a no-op: the row keeps the status the other
// writer gave it, and that status is what the response carries.
//
// Any other save error is returned as a 500 (issue #54). The row then
// still reads "pending" with no error text and a valid approval token while
// no approval email exists, so answering "pending" would make it look like
// a healthy request waiting on its approver. The error names the execution
// so the caller can cancel it.
func (h *Handler) finalizePurchaseStatus(ctx context.Context, execution *config.PurchaseExecution, emailSent bool, emailReason string) (string, error) {
	if emailSent {
		return "pending", nil
	}
	failed := *execution
	failed.Status = "failed"
	failed.Error = emailReason
	err := h.config.SaveExecutionIfStatus(ctx, &failed, []string{"pending"})
	switch {
	case err == nil:
		execution.Status, execution.Error = failed.Status, failed.Error
		return "failed", nil
	case errors.Is(err, config.ErrExecutionNotInExpectedStatus):
		logging.Warnf("execution %s left pending while its approval email was being sent; not marking it failed: %v", execution.ExecutionID, err)
		return h.currentExecutionStatus(ctx, execution.ExecutionID), nil
	default:
		logging.Errorf("AUDIT LOSS: failed to mark execution %s as failed after email send error (%s): %v", execution.ExecutionID, emailReason, err)
		return "", emailFailureNotRecordedError(execution.ExecutionID, emailReason, err)
	}
}

// currentExecutionStatus re-reads an execution's stored status for a response
// whose in-memory copy went stale. Falls back to "pending" (the status the row
// was created with) when the read fails.
func (h *Handler) currentExecutionStatus(ctx context.Context, executionID string) string {
	row, err := h.config.GetExecutionByID(ctx, executionID)
	if err != nil || row == nil {
		logging.Errorf("failed to re-read execution %s status: %v", executionID, err)
		return "pending"
	}
	return row.Status
}

// validateAndTotalRecommendations validates each recommendation and returns
// totals. Runs on store-derived recs for the web path and on the persisted
// row for retry; the negative and $10M guards stay as the sanity check for
// both.
func validateAndTotalRecommendations(recs []config.RecommendationRecord) (upfront, savings float64, err error) {
	const maxAmount = 10_000_000 // $10M sanity cap
	for i := range recs {
		rec := recs[i]
		if rec.UpfrontCost < 0 {
			return 0, 0, NewClientError(400, fmt.Sprintf("recommendation %d has negative upfront cost: %.2f", i, rec.UpfrontCost))
		}
		if rec.Savings < 0 {
			return 0, 0, NewClientError(400, fmt.Sprintf("recommendation %d has negative savings: %.2f", i, rec.Savings))
		}
		upfront += rec.UpfrontCost
		savings += rec.Savings
	}
	if upfront > maxAmount {
		return 0, 0, NewClientError(400, fmt.Sprintf("total upfront cost %.2f exceeds maximum allowed (%.2f)", upfront, float64(maxAmount)))
	}
	if savings > maxAmount {
		return 0, 0, NewClientError(400, fmt.Sprintf("total estimated savings %.2f exceeds maximum allowed (%.2f)", savings, float64(maxAmount)))
	}
	return upfront, savings, nil
}

// derefStringOrEmpty returns the string a pointer points to, or "" when nil.
// Used to convert *string creator IDs to the bare string needed for hashing
// without adding a branch to the caller.
func derefStringOrEmpty(s *string) string {
	if s != nil {
		return *s
	}
	return ""
}
