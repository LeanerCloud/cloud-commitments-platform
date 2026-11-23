package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/planstep"
	"github.com/aws/aws-lambda-go/events"
	"github.com/jackc/pgx/v5"
)

func (h *Handler) createPlannedPurchases(ctx context.Context, httpReq *events.LambdaFunctionURLRequest, planID string) (*CreatePlannedPurchasesResponse, error) {
	if err := validateUUID(planID); err != nil {
		return nil, err
	}

	// Require update:purchases — creating scheduled purchase rows is a
	// purchase-write operation. Standard Users (DefaultUserPermissions)
	// hold this verb; Plan Authors (create/update/delete:plans only)
	// do not, closing issue #1406 / #1418. The prior gate ("create","plans")
	// incorrectly admitted Plan Authors because they hold create:plans.
	// Scheduled rows are stamped with resolveCreatorUserID(session); a
	// creator-less row could never be approved under 4-eyes, so that case
	// is refused here (issue #677).
	session, err := h.requirePurchaseCreatingPermission(ctx, httpReq, "update", "purchases")
	if err != nil {
		return nil, err
	}

	err = h.requirePlanMutationAccess(ctx, session, planID)
	if err != nil {
		return nil, err
	}

	req, startDate, err := h.parseCreatePurchasesRequest(httpReq.Body)
	if err != nil {
		return nil, err
	}

	if _, err = h.getPlanForPurchaseCreation(ctx, planID); err != nil {
		return nil, err
	}

	created := 0
	err = h.config.WithTx(ctx, func(tx pgx.Tx) error {
		var txErr error
		created, txErr = h.createPlannedPurchasesTx(ctx, tx, planID, req.Count, startDate, resolveCreatorUserID(session), session)
		return txErr
	})
	if err != nil {
		clientErr, isClient := IsClientError(err)
		if isClient {
			return nil, clientErr
		}
		if errors.Is(err, errNotFound) {
			return nil, err
		}
		return nil, mapCreatePlanStorageError(err, "plan not found", "failed to schedule purchases", "createPlannedPurchases: transaction failed: %v", err)
	}
	return &CreatePlannedPurchasesResponse{Created: created}, nil
}

// createPlannedPurchasesTx is the transactional body of createPlannedPurchases:
// take the per-plan ramp lock, decide against the state that lock protects, and
// write, all before anyone else can read it.
//
// The lock is the point. The step range derives from the plan's CurrentStep and
// the "is this step already covered" test reads the executions of those steps,
// so both inputs are exactly the state a concurrent completion or a concurrent
// create mutates. Read either outside the lock and the check becomes advisory:
// two creates both see the step free and each mints a root row for it, and
// approving both re-fans-out over accounts that already bought. CompletePlanStep
// takes this same lock, so a completion cannot interleave either.
func (h *Handler) createPlannedPurchasesTx(ctx context.Context, tx pgx.Tx, planID string, count int, startDate time.Time, creator *string, session *Session) (int, error) {
	plan, err := h.config.LockPurchasePlanTx(ctx, tx, planID)
	if err != nil {
		logging.Errorf("createPlannedPurchases: LockPurchasePlanTx failed (plan=%s): %v", planID, err)
		return 0, NewClientError(503, "could not lock the plan for scheduling; try again")
	}
	if plan == nil {
		return 0, NewClientError(404, "plan not found")
	}
	if err := h.requirePlanMutationAccess(ctx, session, planID); err != nil {
		return 0, err
	}
	if plan.RampSchedule.DatesManaged {
		return 0, editorReconciliationError()
	}
	if err := h.validatePurchaseScheduling(ctx, plan, count, startDate); err != nil {
		return 0, err
	}

	if refuseErr := h.refuseOccupiedRampSteps(ctx, tx, plan, planID, count); refuseErr != nil {
		return 0, refuseErr
	}

	created, createErr := h.createPurchaseExecutionsTx(ctx, tx, plan, planID, count, startDate, creator)
	if createErr != nil {
		return 0, createErr
	}
	if planErr := h.updatePlanNextExecutionDateTx(ctx, tx, plan, startDate); planErr != nil {
		return 0, planErr
	}
	return created, nil
}

func (h *Handler) validatePurchaseScheduling(ctx context.Context, plan *config.PurchasePlan, count int, startDate time.Time) error {
	if !plan.Enabled {
		return NewClientError(409, "plan must be enabled before purchases can be scheduled")
	}
	if err := validateScheduledRamp(plan, count); err != nil {
		return err
	}
	accounts, err := h.config.GetPlanAccounts(ctx, plan.ID)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		return NewClientError(409, "plan must have target accounts before purchases can be scheduled")
	}
	if plan.AutoPurchase {
		if err := h.requireAutomaticScheduling(ctx, plan); err != nil {
			return err
		}
	}
	// start_date is a date-only value parsed as midnight UTC, so today's
	// date is already in the past and is refused along with any earlier
	// day: tomorrow (UTC) is the first accepted date. The check runs here,
	// under the plan lock, because a request can wait on that lock past
	// UTC midnight (#873, #50).
	now := h.clock()
	if !startDate.After(now) {
		return NewClientError(400, "start_date must be in the future")
	}
	return requireStartDateOutsideNoticeMargin(plan, startDate, now)
}

// codeStartDateInsideNoticeMargin is the body code of the 409 for a start date
// too close to now for the plan's notice window.
const codeStartDateInsideNoticeMargin = "start_date_inside_notice_margin"

// requireStartDateOutsideNoticeMargin refuses a start date D < now + (N+1) x 24h
// (D5 of #631). The notification tick sees a plan only when D-now < (N+1) days
// and attaches recommendations only when D >= now + N days, so a root attaches
// from a tick in the 24h slice (D-(N+1)d, D-Nd]. D >= now+(N+1)d keeps that
// whole slice in the future, where a daily sweep reaches it. The message names
// the first UTC midnight at or after the threshold, derived from the plan's own N.
func requireStartDateOutsideNoticeMargin(plan *config.PurchasePlan, startDate, now time.Time) error {
	threshold := plan.VetoWindowEnd(now).Add(24 * time.Hour)
	if !startDate.Before(threshold) {
		return nil
	}
	earliest := time.Date(threshold.Year(), threshold.Month(), threshold.Day(), 0, 0, 0, 0, time.UTC)
	if earliest.Before(threshold) {
		earliest = earliest.AddDate(0, 0, 1)
	}
	return planEditorRefusal(http.StatusConflict, codeStartDateInsideNoticeMargin,
		"start_date leaves too little notice before the first purchase; choose a start date on or after "+earliest.Format("2006-01-02"))
}

// clock returns the handler's current time: the injected now func when a
// test set one, time.Now otherwise.
func (h *Handler) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

func validateScheduledRamp(plan *config.PurchasePlan, count int) error {
	if err := plan.Validate(); err != nil {
		return NewClientError(409, fmt.Sprintf("plan cannot be scheduled: %v", err))
	}
	if err := planstep.Precheck(plan); err != nil {
		return NewClientError(409, err.Error())
	}
	if err := plan.RequireVetoWindow(); err != nil {
		return NewClientError(409, err.Error())
	}
	ramp := plan.RampSchedule
	if count > ramp.TotalSteps-ramp.CurrentStep || (ramp.Type == "immediate" && ramp.TotalSteps != 1) {
		return NewClientError(409, "count exceeds the plan's remaining ramp steps")
	}
	if ramp.Type != "immediate" && ramp.StepIntervalDays <= 0 {
		return NewClientError(409, "step interval must be positive for scheduled ramp purchases")
	}
	return nil
}
