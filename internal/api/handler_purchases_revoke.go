package api

// handler_purchases_revoke.go implements POST /api/purchases/{purchaseId}/revoke
// which lets a session-authenticated user revoke a completed purchase while it
// is still within the provider's free-cancel window (issue #290).
//
// Per-provider support:
//
//   - Azure reservations: return via armreservations.ReturnClient within a 7-day
//     window. The button is shown in the History UI for Azure rows inside the
//     window. Requires CalculateRefund first (to get the session ID) then Return.
//
//   - AWS EC2 RIs / Savings Plans: AWS does not expose a direct cancel API for
//     purchased RIs. Revocation requires an AWS Support case
//     (support:CreateCase). That flow is deferred to Phase 2 (#291). For now the
//     endpoint returns 422 and the frontend hides the button for AWS rows, per
//     the constraint: "if a provider has no cancel API, the button must be hidden".
//
//   - GCP commitments: no free-cancel window. Button hidden for GCP rows.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	armreservations "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/jackc/pgx/v5"
)

// revokeQuoteEpsilon is the tolerance (in currency units) for the
// TOCTOU-divergence check: if Azure's actual refund on Return diverges from
// the user-consented expected_refund_amount by more than this, the revoke is
// rejected with 422 so the user can re-quote and confirm.
const revokeQuoteEpsilon = 0.01

// azureRefundSafetyMargin is subtracted from the local window-close time before
// presenting the revoke button or accepting a revoke request. Azure's 7-day
// window has a hard edge: a reservation returned in the last few minutes of the
// window occasionally fails with RefundPolicyViolated due to clock skew between
// CUDly's clock and Azure's. Shrinking the local window by 1 hour eliminates
// the tail of clock-skew failures at the edge (issue #290 Finding #3).
//
// The safety margin applies only to the local pre-flight check -- the value
// stored in purchase_history.revocation_window_closes_at is the unmodified
// Azure deadline so operators can see the true expiry.
const azureRefundSafetyMargin = 1 * time.Hour

// revokeQuoteResult is the JSON body returned by
// GET /api/purchases/revoke/calculate/{id}.
type revokeQuoteResult struct {
	RefundCurrency string  `json:"refund_currency"`
	QuotedAt       string  `json:"quoted_at"`
	RefundAmount   float64 `json:"refund_amount"`
}

// revokeConfirmBody is the JSON body expected on
// POST /api/purchases/{purchaseId}/revoke.
// ExpectedRefundAmount and ExpectedRefundCurrency are the quote the user
// consented to (from GET /revoke/calculate), used for TOCTOU-divergence
// detection. Both are required for Azure revocations.
type revokeConfirmBody struct {
	ExpectedRefundAmount   *float64 `json:"expected_refund_amount"`
	ExpectedRefundCurrency string   `json:"expected_refund_currency"`
}

// AzureRevocationWindowDays is the number of days after purchase within which
// Azure reservations are eligible for a return (refund). Per Azure docs:
// https://learn.microsoft.com/azure/cost-management-billing/reservations/exchange-and-refund-azure-reservations
// Aliases config.AzureRevocationWindowDays so the purchase-write path and this
// endpoint share a single source of truth for the window length.
const AzureRevocationWindowDays = config.AzureRevocationWindowDays

// azureReturnClient is the narrow interface over armreservations.ReturnClient
// used by the revoke handler. Extracted for test injection.
type azureReturnClient interface {
	Post(ctx context.Context, reservationOrderID string, body armreservations.RefundRequest, options *armreservations.ReturnClientPostOptions) (armreservations.ReturnClientPostResponse, error)
}

// azureCalculateRefundClient is the narrow interface over
// armreservations.CalculateRefundClient used to obtain the session ID required
// before calling ReturnClient.Post.
type azureCalculateRefundClient interface {
	Post(ctx context.Context, reservationOrderID string, body armreservations.CalculateRefundRequest, options *armreservations.CalculateRefundClientPostOptions) (armreservations.CalculateRefundClientPostResponse, error)
}

type azureRevokeClientFactory struct {
	newCredential            func() (azcore.TokenCredential, error)
	newCalculateRefundClient func(azcore.TokenCredential) (azureCalculateRefundClient, error)
	newReturnClient          func(azcore.TokenCredential) (azureReturnClient, error)
}

// revokePurchaseResult is the JSON body returned on a successful revocation.
type revokePurchaseResult struct {
	Status     string `json:"status"`
	RevokedAt  string `json:"revoked_at"`
	RevokedVia string `json:"revoked_via"`
}

// revokeReconcilePendingResult is the JSON body returned (HTTP 200, like every
// handler result) when the Azure refund succeeded but the subsequent DB write
// failed after all retries. The frontend reads the "code" field and shows a
// non-retryable toast ("Refund issued. We will reconcile your audit shortly.")
// with no retry button (issue #290 Finding #6).
type revokeReconcilePendingResult struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	AzureReturned bool   `json:"azure_returned"`
}

// Statuses and copy for a revoke whose Azure Return is not proven accepted
// (issues #1522, #1529).
const (
	revokeCodeReconcilePending       = "RECONCILE_PENDING"
	revokeCodePendingConfirmation    = "REVOCATION_PENDING_CONFIRMATION"
	revokePendingConfirmationMessage = "We could not confirm whether the refund for this purchase went through. Check the reservation in the Azure portal before trying again: if it still shows as active after about 90 minutes, you can revoke again."

	// revokeRetryAfterSeconds is how long a client should wait before asking
	// again: the detached Return (revokeReturnTimeout, 3m) plus the writes
	// that record it (revokePersistTimeout, 30s) is 3m30s, so by 5m the
	// request that holds the claim has either recorded the Return or given
	// up; the finalize_revocations sweep (30m stale window) is the backstop.
	revokeRetryAfterSeconds = 300
)

// revokePendingConfirmationResult is the JSON body for a revoke whose Azure
// Return is not proven accepted: another request is still posting it, or an
// earlier one failed without a definite answer. It has no azure_returned
// field on purpose, so it cannot claim a refund it cannot prove. Like
// revokeReconcilePendingResult it is a result body, so the HTTP status is the
// handler's normal 200 (the "207" in comments is descriptive; nothing sets it).
type revokePendingConfirmationResult struct {
	Code              string `json:"code"`
	Message           string `json:"message"`
	RetryAfterSeconds int    `json:"retry_after_seconds"`
}

func newRevokePendingConfirmation() *revokePendingConfirmationResult {
	return &revokePendingConfirmationResult{
		Code:              revokeCodePendingConfirmation,
		Message:           revokePendingConfirmationMessage,
		RetryAfterSeconds: revokeRetryAfterSeconds,
	}
}

// revokeMarkRetryBackoffs are the sleep durations between consecutive
// MarkPurchaseRevoked attempts after the first failure (1s, 3s, 9s).
var revokeMarkRetryBackoffs = []time.Duration{
	1 * time.Second,
	3 * time.Second,
	9 * time.Second,
}

// revokePersistTimeout bounds the writes that record an Azure Return Azure has
// already accepted (issue #1525), and the in-flight clear after a failed
// Return. They run detached from the request, so a client disconnect or the
// app server's 30s per-request timeout (internal/server/http.go) right after
// the Return does not lose the record. 30s covers the whole retry schedule:
// four attempts of revokePersistAttemptTimeout plus 13s of backoff across
// revokeMarkRetryBackoffs (29s), while still capping how long a hung database
// can hold the handler after its caller has gone. The waits between attempts
// honor it too (issue #1527). If it runs out, the result is the same
// RECONCILE_PENDING as exhausted retries.
const revokePersistTimeout = 30 * time.Second

// revokePersistAttemptTimeout bounds one persist attempt (stamp the Return,
// then mark the purchase revoked: two single-row UPDATEs by primary key), so
// a first attempt stuck on a hung connection cannot use up the whole
// revokePersistTimeout and leave the retries nothing (issue #1527). Both
// writes are idempotent, so an attempt cut off after its UPDATE committed is
// safe to retry.
const revokePersistAttemptTimeout = 4 * time.Second

// revokeReturnTimeout bounds the Azure Return call, which runs detached from
// the request once this request holds the in-flight claim (issue #1527): Azure
// may accept a Return whose caller has gone, so a client disconnect or the app
// server's 30s per-request timeout must not abort it and lose the record.
//
// The budget is sized so the Azure client's own limits, not this deadline,
// end a failing call. The armreservations client runs on azcore's default
// pipeline: up to 4 tries (MaxRetries 3) with at most about 11.4s of backoff
// between them (800ms base, exponential, jitter up to 1.3x), each try bounded
// by the transport's 30s dial and 10s TLS handshake timeouts, and no per-try
// timeout. Four tries that each exhaust dial and handshake take about 171s, so
// 3m lets the client report its own error; only a response that never arrives
// (which nothing in the client bounds) or Azure throttling with long
// Retry-After waits (honored up to 60s each) runs into this deadline. On
// Lambda this budget plus revokePersistTimeout must fit the function timeout,
// enforced by TestLambdaTimeoutCoversRevokeBudget (#913).
const revokeReturnTimeout = 3 * time.Minute

// revokePurchase handles POST /api/purchases/{purchaseId}/revoke.
//
// Authorization: session required + revoke-own:purchases (or revoke-any for
// admins). The handler is fail-closed: if the auth service is nil the request
// is rejected with 403.
//
// Gmail-style pre-fire delay (issue #291 wave-2): when the ID resolves to a
// purchase_execution in status="scheduled" (cloud SDK not yet called), the
// execution is canceled at zero cost and control returns immediately — no
// provider SDK call is made. This path handles AWS, GCP, and Azure uniformly
// since nothing has been committed to any cloud yet.
func (h *Handler) revokePurchase(ctx context.Context, req *events.LambdaFunctionURLRequest, purchaseID string) (any, error) {
	if purchaseID == "" {
		return nil, NewClientError(400, "purchase_id is required")
	}

	// Fail-closed: auth service nil means we cannot verify permissions.
	// Check before requireSession so this always surfaces as a 403 ClientError.
	if h.auth == nil {
		return nil, NewClientError(403, "authentication service not configured")
	}

	session, err := h.requireSession(ctx, req)
	if err != nil {
		return nil, err
	}

	// Gmail-style pre-fire delay: if the ID resolves to a purchase_execution
	// (cloud SDK not yet called), attempt to cancel it for free at the execution
	// layer. The status=="scheduled" check is intentionally omitted here: reading
	// "scheduled" then checking the status is a TOCTOU race. Instead we pass the
	// row straight to revokeScheduledExecution which calls CancelScheduledExecutionAtomic
	// (WHERE status='scheduled' CAS) and returns 410 if the scheduler already fired.
	//
	// A genuine DB error from GetExecutionByID surfaces as 500; ErrNotFound means
	// the ID is not an execution (or is not yet visible) and we fall through to
	// the purchase_history lookup below.
	execution, execErr := h.config.GetExecutionByID(ctx, purchaseID)
	if execErr != nil && !errors.Is(execErr, config.ErrNotFound) {
		return nil, fmt.Errorf("revoke: GetExecutionByID %s: %w", purchaseID, execErr)
	}
	if execErr == nil {
		return h.revokeScheduledExecution(ctx, session, execution)
	}

	return h.loadAndRevokePurchaseHistory(ctx, req, session, purchaseID)
}

// loadAndRevokePurchaseHistory pulls the auth + idempotency-check + provider-dispatch
// logic for the completed-purchase path out of revokePurchase to keep that function
// under the cyclomatic limit.
func (h *Handler) loadAndRevokePurchaseHistory(ctx context.Context, req *events.LambdaFunctionURLRequest, session *Session, purchaseID string) (any, error) {
	record, err := h.config.GetPurchaseHistoryByPurchaseID(ctx, purchaseID)
	if err != nil {
		return nil, fmt.Errorf("revoke: load purchase %s: %w", purchaseID, err)
	}
	if record == nil {
		return nil, NewClientError(404, "purchase not found")
	}

	if err := h.authorizeSessionRevoke(ctx, session, record); err != nil {
		return nil, err
	}

	// Idempotency: already revoked.
	if record.RevokedAt != nil {
		return &revokePurchaseResult{
			Status:     "already_revoked",
			RevokedAt:  record.RevokedAt.Format(time.RFC3339),
			RevokedVia: record.RevokedVia,
		}, nil
	}

	// Partial-success reconciliation (issue #290 Finding #6): if the
	// revocation_in_flight flag is set but revoked_at is still NULL, Azure
	// already issued the refund but our DB write failed, or another request
	// holds the claim and is posting the Return now (issue #1518), or an
	// earlier Return failed without a definite answer and may have been
	// accepted (issue #1529). The answer tells the frontend not to retry the
	// Azure call (which would fail with "already returned"): RECONCILE_PENDING
	// when the return_posted_at stamp proves the Return, else
	// REVOCATION_PENDING_CONFIRMATION (issue #1522).
	// The finalize_revocations sweep will reconcile the row.
	if record.RevocationInFlight {
		return revocationInFlightResult(record), nil
	}

	// Parse the confirmed quote from the request body. Azure revocations
	// require it for TOCTOU-divergence detection (the two-step
	// quote-then-confirm flow, issue #290 Finding #4); callAzureReturn enforces it.
	var body revokeConfirmBody
	if req.Body != "" {
		if jsonErr := json.Unmarshal([]byte(req.Body), &body); jsonErr != nil {
			return nil, NewClientError(400, fmt.Sprintf("invalid request body: %v", jsonErr))
		}
	}

	return h.dispatchProviderRevoke(ctx, record, body)
}

// revokeScheduledExecution cancels a Gmail-style pre-fire delayed execution
// that is still in the "scheduled" state (i.e. the cloud SDK has not been
// called yet). This is a free cancel: no provider SDK call is made.
//
// The method enforces revoke-any/revoke-own RBAC (same permissions as the
// completed-purchase revoke path), then atomically transitions the execution
// to "canceled" and removes its purchase_suppressions.
//
// Returns 410 Gone only when the CAS observes the row already transitioned out
// of "scheduled" (the scheduler fired the SDK call between our SELECT and the
// CAS UPDATE). We do NOT pre-reject on a past ScheduledExecutionAt: a row still
// in "scheduled" is cancellable for free no matter how stale the timestamp,
// which keeps free-cancel working during scheduler lag/backpressure.
func (h *Handler) revokeScheduledExecution(ctx context.Context, session *Session, execution *config.PurchaseExecution) (any, error) {
	// No early window-expiry check on ScheduledExecutionAt: a row that is still
	// status=="scheduled" has NOT been transitioned by the scheduler, so the SDK
	// call has not fired regardless of how far the timestamp is in the past
	// (scheduler lag / backpressure). Returning 410 purely on a past timestamp
	// would break free-cancel during lag even though the CAS below can still
	// cancel it before any cloud call. Let CancelScheduledExecutionAtomic be the
	// sole arbiter: it returns canceled=false (-> 410) only when the row has
	// actually moved out of "scheduled".
	// Account-scope gate (issue #92), ahead of RBAC so an out-of-scope caller
	// gets the enumeration-safe 404 rather than a 403 that confirms the row.
	if err := h.requireExecutionAccess(ctx, session, execution.ExecutionID); err != nil {
		return nil, err
	}
	if err := h.authorizeSessionRevokeExecution(ctx, session, execution); err != nil {
		return nil, err
	}

	// Atomically transition from scheduled -> canceled and remove suppressions.
	var cancelledBy *string
	if session.Email != "" {
		e := session.Email
		cancelledBy = &e
	}
	var canceled bool
	var currentStatus string
	if err := h.config.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		// The scheduled-revoke path uses its own CAS variant that flips ONLY
		// status='scheduled' -> 'canceled'. CancelExecutionAtomic accepts
		// only ('pending','notified') and would always return zero rows on
		// a scheduled row, miscoded as "race lost" -> a misleading 410 even
		// during the happy path. Issue #290 wave-2: keep the two CAS contracts
		// distinct so 410 unambiguously means "scheduler already fired".
		canceled, currentStatus, err = h.config.CancelScheduledExecutionAtomic(ctx, tx, execution.ExecutionID, cancelledBy)
		if err != nil {
			return err
		}
		if !canceled {
			return nil
		}
		return h.config.DeleteSuppressionsByExecutionTx(ctx, tx, execution.ExecutionID)
	}); err != nil {
		return nil, fmt.Errorf("cancel scheduled execution %s: %w", execution.ExecutionID, err)
	}
	if !canceled {
		// A concurrent scheduler tick transitioned the row away from "scheduled"
		// between our SELECT and the CAS UPDATE — the window closed. Return 410
		// so the client knows to switch to the completed-purchase revoke path.
		return nil, NewClientError(410, fmt.Sprintf(
			"revocation window has closed: execution %s was already transitioned to %q", execution.ExecutionID, currentStatus,
		))
	}

	logging.Infof("revokeScheduledExecution: execution_id=%s canceled before SDK call (free cancel)", execution.ExecutionID)

	return map[string]string{
		"status":  "canceled",
		"message": "Purchase canceled. No cloud API call was made; no cost incurred.",
	}, nil
}

// dispatchProviderRevoke routes a revocation request to the correct
// provider-specific implementation. Extracted from revokePurchase to keep
// that function's cyclomatic complexity within the project limit.
func (h *Handler) dispatchProviderRevoke(ctx context.Context, record *config.PurchaseHistoryRecord, confirmed revokeConfirmBody) (any, error) {
	switch record.Provider {
	case "azure":
		return h.revokeAzurePurchase(ctx, record, confirmed)
	case "aws":
		// AWS does not expose a direct RI cancel API. Phase 2 (#291) adds the
		// AWS Support case path. Return 422 so the frontend hides this button.
		return nil, NewClientError(422, "AWS RIs cannot be revoked via direct API; contact AWS Support for a refund within 24h of purchase")
	case "gcp":
		return nil, NewClientError(422, "GCP commitments do not have a free-cancel window")
	default:
		return nil, NewClientError(422, fmt.Sprintf("provider %q does not support in-app revocation", record.Provider))
	}
}

// toPtr returns a pointer to its argument. Generic helper used by the Azure
// revocation call-site to construct ARM struct fields without temp variables.
func toPtr[T any](v T) *T { return &v }
