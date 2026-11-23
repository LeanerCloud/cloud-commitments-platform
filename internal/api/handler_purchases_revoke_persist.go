package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/ctxutil"
)

// revocationInFlightResult is the answer for a row whose revocation is in
// flight. RECONCILE_PENDING with azure_returned:true only when the row
// carries the return_posted_at stamp, which proves Azure accepted the Return.
// Without it the request holding the claim may still be posting the Return,
// and it may yet fail, so the answer is REVOCATION_PENDING_CONFIRMATION with
// no azure_returned (issue #1522). A nil record is "no stamp".
func revocationInFlightResult(record *config.PurchaseHistoryRecord) any {
	if record != nil && record.ReturnPostedAt != nil {
		return &revokeReconcilePendingResult{
			Code:          revokeCodeReconcilePending,
			AzureReturned: true,
			Message:       "Refund already issued. We will reconcile your audit record shortly. Do not retry.",
		}
	}
	return newRevokePendingConfirmation()
}

// claimRevocationInFlight sets the in-flight flag BEFORE the Azure Return
// (partial-success guard, issue #290 Finding #6) so that if the subsequent
// MarkPurchaseRevoked write fails, the row is visible to the
// finalize_revocations sweep rather than silently stuck.
//
// The flip is also the claim that lets exactly one request post the Return
// (issue #1518). Both results are nil only when this request won it; every
// other outcome stops before the Return:
//   - the row was revoked by a concurrent request after this one loaded it
//     (issue #316): already_revoked, as the up-front idempotency check would
//     have answered;
//   - another request holds the claim: the row is re-read (lostClaimResult);
//     already_revoked if it is now revoked, RECONCILE_PENDING if the
//     return_posted_at stamp is set, else REVOCATION_PENDING_CONFIRMATION;
//   - the flip failed: an error (500). No Return has been posted, so a retry
//     is safe, whereas posting one without the claim could race the holder's
//     and, if this request's MarkPurchaseRevoked then failed too, leave a
//     refund that no in-flight flag points the sweep at.
//
// Because only the claim holder reaches the Return, the ClearRevocationInFlight
// in handleAzureReturnError only ever clears this request's own flag.
//
// On Lambda the claim is not taken at all when too little invocation time is
// left to post the Return and record its result (checkRevokeTimeLeft, issue
// #1529).
func (h *Handler) claimRevocationInFlight(ctx context.Context, record *config.PurchaseHistoryRecord) (any, error) {
	if err := h.checkRevokeTimeLeft(ctx, record.PurchaseID); err != nil {
		return nil, err
	}
	flipErr := h.config.FlipPurchaseRevocationInFlight(ctx, record.PurchaseID)
	switch {
	case flipErr == nil:
		return nil, nil
	case errors.Is(flipErr, config.ErrRevocationInFlight):
		logging.Infof("revoke azure: purchase_id=%s is already being revoked by a concurrent request; not posting a second Return", record.PurchaseID)
		return h.lostClaimResult(ctx, record.PurchaseID), nil
	case errors.Is(flipErr, config.ErrPurchaseAlreadyRevoked):
		logging.Infof("revoke azure: purchase_id=%s was revoked by a concurrent request; not posting a second Return", record.PurchaseID)
		return h.alreadyRevokedResult(ctx, record.PurchaseID), nil
	default:
		return nil, fmt.Errorf("revoke azure: claim revocation of %s (no Return posted): %w", record.PurchaseID, flipErr)
	}
}

// lostClaimResult answers a request that lost the claim. The row is re-read
// because the claim holder may have recorded the Return since this request
// loaded it. A row now revoked answers already_revoked; a failed or empty
// re-read proves nothing, so it answers REVOCATION_PENDING_CONFIRMATION
// (issue #1522).
func (h *Handler) lostClaimResult(ctx context.Context, purchaseID string) any {
	current, err := h.config.GetPurchaseHistoryByPurchaseID(ctx, purchaseID)
	if err != nil {
		logging.Warnf("revoke azure: purchase_id=%s re-read after a lost claim failed, answering pending confirmation: %v", purchaseID, err)
		return newRevokePendingConfirmation()
	}
	if current != nil && current.RevokedAt != nil {
		return &revokePurchaseResult{
			Status:     "already_revoked",
			RevokedAt:  current.RevokedAt.Format(time.RFC3339),
			RevokedVia: current.RevokedVia,
		}
	}
	return revocationInFlightResult(current)
}

// alreadyRevokedResult answers already_revoked for a row a concurrent request
// revoked. Best-effort: the re-read stamp only fills in the response.
func (h *Handler) alreadyRevokedResult(ctx context.Context, purchaseID string) *revokePurchaseResult {
	result := &revokePurchaseResult{Status: "already_revoked"}
	if current, err := h.config.GetPurchaseHistoryByPurchaseID(ctx, purchaseID); err == nil && current != nil && current.RevokedAt != nil {
		result.RevokedAt = current.RevokedAt.Format(time.RFC3339)
		result.RevokedVia = current.RevokedVia
	}
	return result
}

// handleAzureReturnError handles a failed Return. Only a definite refusal
// (isAzureDefiniteRefusal) proves no refund was issued; any other failure
// keeps the in-flight flag and answers that the outcome is uncertain
// (azureReturnUncertain, issue #1529).
//
// After a definite refusal it clears the in-flight flag and maps the error to
// the right status: 422 on the 7-day window edge, 400 on other client errors,
// 500 otherwise. The caller holds the claim from claimRevocationInFlight, so
// the flag it clears is its own (issue #1518).
//
// The Return ran detached, so by the time it fails the request may be gone;
// the clear runs on its own detached revokePersistTimeout budget so it can
// still land (issue #1527).
func (h *Handler) handleAzureReturnError(reqCtx context.Context, record *config.PurchaseHistoryRecord, attempts int32, err error) (any, error) {
	if !isAzureDefiniteRefusal(err, attempts) {
		return azureReturnUncertain(record, attempts, err), nil
	}
	if isAzureAlreadyReturned(err, attempts) {
		// The reservation is already returned (an earlier Return whose flag the
		// sweep released, issue #1584): record it as revoked rather than
		// clearing the flag and leaving a refunded purchase showing active. The
		// refund amount of that earlier Return is unknown, so none is stored.
		logging.Warnf("revoke azure: purchase_id=%s Azure says the reservation is already returned; marking it revoked", record.PurchaseID)
		return h.persistAzureRevocation(reqCtx, record, nil, "")
	}
	ctx, cancel := ctxutil.DetachedWithTimeout(reqCtx, revokePersistTimeout)
	defer cancel()
	// Azure refused the Return. Clear the in-flight flag so the row is not left in
	// a permanently sticky state that would mislead the finalize_revocations
	// sweep into thinking Azure already issued a refund (Finding D, second-wave
	// CR). Best-effort: log and continue even if the clear fails.
	if clearErr := h.config.ClearRevocationInFlight(ctx, record.PurchaseID); clearErr != nil {
		logging.Warnf("revoke azure: ClearRevocationInFlight for %s failed after Return error (continuing): %v", record.PurchaseID, clearErr)
	}
	// Window-edge: if Azure rejects the Return with RefundPolicyViolated it
	// means our safety-margin check passed but Azure's clock disagreed (the
	// reservation crossed the 7-day boundary between our check and the API
	// call). Surface a clean 422 so the frontend can show a user-friendly
	// "window just closed" message (issue #290 Finding #3).
	if isAzureWindowEdgeError(err) {
		return nil, NewClientError(422, "Azure reservation return window has closed; the 7-day refund period has expired")
	}
	if isAzureClientError(err) {
		return nil, azureRefundRejected("Azure refund rejected", record.PurchaseID, err)
	}
	return nil, fmt.Errorf("revoke azure: Return failed: %w", err)
}

// persistAzureRevocation records the successful revocation with exponential-
// backoff retries. If every attempt fails, Azure has already refunded but the
// DB write could not land, so it returns a RECONCILE_PENDING result (no
// retry) for the finalize_revocations sweep to reconcile, rather than a 500.
//
// Each attempt first records that Azure accepted the Return
// (MarkRevocationReturnPosted, until one lands): the finalize_revocations
// sweep only finalizes rows with that record (issue #1523).
//
// Azure has already refunded by the time this runs, so the writes ignore the
// request's cancellation and run under their own revokePersistTimeout (issue
// #1525): a request canceled right after the Return still records it. Each
// attempt is capped at revokePersistAttemptTimeout, and a wait that would
// outlast the budget is not taken (issue #1527). Nothing before the claim is
// detached.
func (h *Handler) persistAzureRevocation(reqCtx context.Context, record *config.PurchaseHistoryRecord, calcRefundAmount *float64, calcRefundCurrency string) (any, error) {
	ctx, cancel := ctxutil.DetachedWithTimeout(reqCtx, revokePersistTimeout)
	defer cancel()
	now := time.Now().UTC()
	returnRecorded := false
	attempts := 1
	markErr := h.recordAzureReturnAndMark(ctx, record.PurchaseID, now, calcRefundAmount, calcRefundCurrency, &returnRecorded)
	for _, backoff := range revokeMarkRetryBackoffs {
		if markErr == nil {
			break
		}
		logging.Warnf("revoke azure: MarkPurchaseRevoked attempt %d failed for %s: %v (retrying in %s)",
			attempts, record.PurchaseID, markErr, backoff)
		if !waitRevokeRetry(ctx, backoff) {
			break
		}
		attempts++
		markErr = h.recordAzureReturnAndMark(ctx, record.PurchaseID, now, calcRefundAmount, calcRefundCurrency, &returnRecorded)
	}
	if markErr != nil {
		// All retries failed. Azure has already refunded but we cannot persist
		// the revocation state. Return RECONCILE_PENDING so the frontend knows
		// the refund issued but not to retry — the finalize_revocations sweep
		// will reconcile the DB write on its next tick, provided the Return
		// was recorded.
		logging.Errorf("revoke azure: MarkPurchaseRevoked failed for %s after %d attempts (Azure already returned, return recorded=%t): %v",
			record.PurchaseID, attempts, returnRecorded, markErr)
		return &revokeReconcilePendingResult{
			Code:          revokeCodeReconcilePending,
			AzureReturned: true,
			Message:       "Refund issued. We will reconcile your audit record shortly. Do not retry.",
		}, nil
	}

	// PII policy: log execution and account IDs only, not user identifiers.
	logging.Infof("revoke azure: purchase_id=%s account_id=%s revoked_via=direct-api", record.PurchaseID, record.AccountID)

	return &revokePurchaseResult{
		Status:     "revoked",
		RevokedAt:  now.Format(time.RFC3339),
		RevokedVia: "direct-api",
	}, nil
}

// waitRevokeRetry waits d before the next persist attempt and reports whether
// to make it. It returns false at once when ctx's deadline is less than d
// away, and as soon as ctx is done, so the retries never run past
// revokePersistTimeout (issue #1527).
func waitRevokeRetry(ctx context.Context, d time.Duration) bool {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < d {
		return false
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// recordAzureReturnAndMark is one persist attempt after Azure accepted the
// Return, capped at revokePersistAttemptTimeout: stamp return_posted_at unless
// an earlier attempt already did (a failure is logged; the mark may still
// land), then MarkPurchaseRevoked.
func (h *Handler) recordAzureReturnAndMark(budgetCtx context.Context, purchaseID string, now time.Time, calcRefundAmount *float64, calcRefundCurrency string, returnRecorded *bool) error {
	ctx, cancel := context.WithTimeout(budgetCtx, revokePersistAttemptTimeout)
	defer cancel()
	if !*returnRecorded {
		if err := h.config.MarkRevocationReturnPosted(ctx, purchaseID); err != nil {
			logging.Warnf("revoke azure: MarkRevocationReturnPosted for %s failed (continuing to the mark): %v", purchaseID, err)
		} else {
			*returnRecorded = true
		}
	}
	return h.config.MarkPurchaseRevoked(ctx, purchaseID, now, "direct-api", "", calcRefundAmount, calcRefundCurrency)
}
