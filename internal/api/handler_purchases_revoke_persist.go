package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
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
