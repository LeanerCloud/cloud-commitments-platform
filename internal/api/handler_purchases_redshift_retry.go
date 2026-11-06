package api

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
)

// checkRedshiftRetryGate refuses a Retry that could still double-buy: permanently
// (unknown or past-horizon dispatch) or until AWS settles. Force never applies.
func (h *Handler) checkRedshiftRetryGate(ctx context.Context, failedExec *config.PurchaseExecution) error {
	if !purchase.IsRedshiftExecution(failedExec) {
		return nil
	}
	clock, err := h.config.GetRedshiftRetryClock(ctx, failedExec.ExecutionID)
	if err != nil {
		return fmt.Errorf("read redshift retry clock: %w", err)
	}
	if reason := purchase.RedshiftRetryPermanentRefusal(failedExec, clock.SinceStamp); reason != "" {
		return NewClientErrorWithDetails(409,
			"this purchase cannot be retried safely: "+reason,
			map[string]any{"ops_hint": reason, "redrive_unsafe": true})
	}
	if wait := purchase.RedshiftRetryWait(failedExec, clock.SinceUpdate); wait > 0 {
		secs := int((wait + time.Second - 1) / time.Second)
		return NewClientErrorWithHeaders(409,
			"this Redshift purchase failed too recently to retry safely; retry after the wait",
			map[string]any{"retry_after_seconds": secs},
			map[string]string{"Retry-After": strconv.Itoa(secs)})
	}
	return nil
}

// retrySuccessorFirstDispatchAt carries the lineage stamp to a retry successor;
// a never-submitted attempt 0 has none and the successor mints its own.
func retrySuccessorFirstDispatchAt(failedExec *config.PurchaseExecution) *time.Time {
	if purchase.NeverSubmitted(failedExec) {
		return nil
	}
	return clonePtr(failedExec.FirstDispatchAt)
}
