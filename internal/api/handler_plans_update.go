package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

func (h *Handler) updatePlan(ctx context.Context, httpReq *events.LambdaFunctionURLRequest, planID string) (any, error) {
	// Validate UUID format to prevent injection attacks
	if err := validateUUID(planID); err != nil {
		return nil, err
	}

	session, err := h.requirePermission(ctx, httpReq, "update", "plans")
	if err != nil {
		return nil, err
	}

	err = h.requirePlanMutationAccess(ctx, session, planID)
	if err != nil {
		return nil, err
	}

	req, err := decodePlanRequest(httpReq.Body)
	if err != nil {
		return nil, err
	}
	if groupsErr := refuseGroupsOnUpdate(req); groupsErr != nil {
		return nil, groupsErr
	}
	var capField struct {
		ExpectedUpdatedAt *string         `json:"expected_updated_at"`
		MaxStepCommitment json.RawMessage `json:"max_step_commitment"`
	}
	if err = json.Unmarshal([]byte(httpReq.Body), &capField); err != nil {
		return nil, NewClientError(400, "invalid request body")
	}

	existingPlan, err := h.loadPlanForUpdate(ctx, planID, capField.ExpectedUpdatedAt)
	if err != nil {
		return nil, err
	}

	plan, err := mergeUpdatedPlan(existingPlan, req, planID, capField.MaxStepCommitment != nil)
	if err != nil {
		return nil, err
	}

	if err := h.updatePlanGated(ctx, httpReq, session, existingPlan, plan, "updatePlan"); err != nil {
		return nil, err
	}

	return planForResponse(plan), nil
}

// updatePlanGated writes plan with the arming gate decided against the plan's
// accounts as the plan row lock shows them. The grant lookups (auth service,
// store) run first, outside any transaction, so a locked transaction never waits
// on a second pool connection; the gate under the lock only confirms the
// accounts it was decided for are still the plan's (setPlanAccounts takes the
// same lock). before is the snapshot plan was built from; if the row moved on,
// the write fails its optimistic check instead.
func (h *Handler) updatePlanGated(ctx context.Context, httpReq *events.LambdaFunctionURLRequest, session *Session, before, plan *config.PurchasePlan, op string) error {
	if planArmed(plan) && enablesEntry(before, plan) {
		if err := refuseUnboundBinding(plan); err != nil {
			return err
		}
	}
	if !planArmEscalates(before, plan) {
		return h.mapPlanUpdateError(h.config.UpdatePurchasePlan(ctx, plan), op)
	}
	for attempt := 0; attempt < planGateAttempts; attempt++ {
		accountIDs, err := h.planArmAccountIDs(ctx, plan, nil)
		if err != nil {
			return err
		}
		if grantErr := h.requirePlanArmGrantFor(ctx, httpReq, session, before, plan, accountIDs); grantErr != nil {
			return grantErr
		}
		err = h.config.UpdatePurchasePlanGated(ctx, plan, func(_ *config.PurchasePlan, lockedIDs []string) error {
			if !sameIDSet(lockedIDs, accountIDs) {
				return errPlanMovedUnderGate
			}
			return nil
		})
		if !errors.Is(err, errPlanMovedUnderGate) {
			return h.mapPlanUpdateError(err, op)
		}
	}
	return errPlanGateContended
}

func (h *Handler) mapPlanUpdateError(err error, op string) error {
	if err == nil {
		return nil
	}
	if ce, ok := IsClientError(err); ok {
		return ce
	}
	return mapCreatePlanStorageError(err, "plan not found", "failed to update plan", "%s: UpdatePurchasePlan failed", op)
}

// sameIDSet reports whether a and b hold the same ids, ignoring order, case and
// duplicates.
func sameIDSet(a, b []string) bool {
	norm := func(ids []string) []string {
		out := make([]string, len(ids))
		for i, id := range ids {
			out[i] = strings.ToLower(id)
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	return slices.Equal(norm(a), norm(b))
}

func (h *Handler) loadPlanForUpdate(ctx context.Context, planID string, expectedUpdatedAt *string) (*config.PurchasePlan, error) {
	plan, err := h.config.GetPurchasePlan(ctx, planID)
	if err != nil {
		return nil, mapCreatePlanStorageError(err,
			"plan not found", "failed to load plan",
			"updatePlan: GetPurchasePlan failed")
	}
	if expectedUpdatedAt != nil && *expectedUpdatedAt != "" {
		expected, parseErr := time.Parse(time.RFC3339Nano, *expectedUpdatedAt)
		if parseErr != nil {
			return nil, NewClientError(400, "expected_updated_at must be an RFC3339 timestamp")
		}
		if !plan.UpdatedAt.Equal(expected) {
			return nil, NewClientError(409, "plan changed; refresh the plan and try again")
		}
	}
	return plan, nil
}

// mergeUpdatedPlan builds the plan an update request describes, carrying over
// the state the request cannot express (timestamps, ramp progress, services)
// from the stored plan, and validates it.
func mergeUpdatedPlan(existingPlan *config.PurchasePlan, req *PlanRequest, planID string, capPresent bool) (*config.PurchasePlan, error) {
	// Create new plan from request
	plan := req.toPurchasePlan()
	plan.ID = planID

	// Preserve timestamps from existing plan
	plan.CreatedAt = existingPlan.CreatedAt
	plan.UpdatedAt = existingPlan.UpdatedAt

	// toPurchasePlan always rebuilds the ramp schedule from scratch
	// (CurrentStep=0, StartDate=now) and recomputes NextExecutionDate,
	// since it has no way to see the existing plan. Left alone, every PUT
	// -- including a bare rename -- silently restarts an in-progress ramp,
	// so the scheduler re-buys steps that already executed (issue #219).
	if err := refuseOmittedRampChange(existingPlan, req, plan); err != nil {
		return nil, err
	}
	if err := preserveRampProgress(existingPlan, plan); err != nil {
		return nil, err
	}

	if req.MaxStepCommitment == nil && !capPresent {
		plan.MaxStepCommitment = existingPlan.MaxStepCommitment
	}
	if capPresent && req.MaxStepCommitment == nil && plan.AutoPurchase {
		return nil, NewClientError(400, "max_step_commitment cannot be cleared while auto_purchase is enabled")
	}

	// Fold the request's single service into the stored map rather than
	// replacing it, so other services survive the edit (issue #934).
	services, err := mergeUpdatedServices(existingPlan, plan.Services, req.omittedFields())
	if err != nil {
		return nil, err
	}
	plan.Services = services

	// Validate the plan
	if err := plan.Validate(); err != nil {
		return nil, NewClientError(400, fmt.Sprintf("validation error: %s", err))
	}
	return plan, nil
}

// refuseOmittedRampChange refuses an update that omits ramp_schedule when the
// immediate preset it would fall back to is a different schedule from the
// stored one (#1935). PUT replaces the ramp, so omitting it means immediate: on
// a plan with no completed step that used to turn a weekly or monthly ramp into
// buying the whole base in one step, silently. A plan with a completed step is
// refused by preserveRampProgress with its own message, so it is left to that.
func refuseOmittedRampChange(existingPlan *config.PurchasePlan, req *PlanRequest, plan *config.PurchasePlan) error {
	// Only the legacy zero value (a plan stored with no schedule) is exempt. A
	// one-step custom ramp of 51 to 99% is not immediate: it buys that share, and
	// the fallback would make it 100%, which rampScheduleShapeUnchanged catches.
	if req.RampSchedule != "" || existingPlan.RampSchedule.CurrentStep > 0 || existingPlan.RampSchedule.TotalSteps == 0 ||
		rampScheduleShapeUnchanged(existingPlan.RampSchedule, plan.RampSchedule) {
		return nil
	}
	return NewClientError(http.StatusConflict,
		"ramp_schedule was omitted, which means immediate and would replace this plan's "+existingPlan.RampSchedule.Type+
			" schedule; resend the current ramp_schedule to keep it, or send immediate to change it")
}

// preserveRampProgress carries ramp-in-progress state from the existing
// plan onto a freshly rebuilt one (issue #219). LastExecutionDate and
// LastNotificationSent record real purchase history, so they are always
// carried over; a rebuild has no way to know either and would otherwise
// wipe them. CurrentStep, StartDate, and NextExecutionDate are carried
// over when the requested ramp schedule has the same shape as the existing
// one (see rampScheduleShapeUnchanged): an unrelated field edit (rename,
// target coverage, ...) resubmits the same schedule and must not restart it.
// The stored type label is kept too, so resubmitting a non-preset weekly or
// monthly ramp as the equal custom one leaves the plan as it was (issue
// #1184). A different shape is refused once a step has completed,
// because the rebuilt schedule would restart at step zero and forget the
// percentage already bought (issue #386); before the first step it is a
// plain schedule change.
func preserveRampProgress(existingPlan, plan *config.PurchasePlan) error {
	plan.LastExecutionDate = existingPlan.LastExecutionDate
	plan.LastNotificationSent = existingPlan.LastNotificationSent
	// A bound plan keeps its marker across any ramp edit: without it the plan
	// would buy by filter instead of its selected recommendations (#1746).
	if existingPlan.RampSchedule.AllocationMode == config.RampAllocationBoundGroups {
		plan.RampSchedule.AllocationMode = config.RampAllocationBoundGroups
	}

	if rampScheduleShapeUnchanged(existingPlan.RampSchedule, plan.RampSchedule) {
		plan.RampSchedule.Type = existingPlan.RampSchedule.Type
		plan.RampSchedule.CurrentStep = existingPlan.RampSchedule.CurrentStep
		plan.RampSchedule.StartDate = existingPlan.RampSchedule.StartDate
		// Editor-owned markers the request cannot express; dropping them hands
		// the plan back to the interval formula.
		plan.RampSchedule.DatesManaged = existingPlan.RampSchedule.DatesManaged
		plan.RampSchedule.AllocationMode = existingPlan.RampSchedule.AllocationMode
		plan.NextExecutionDate = existingPlan.NextExecutionDate
		return nil
	}
	if existingPlan.RampSchedule.CurrentStep > 0 {
		return NewClientError(http.StatusConflict, fmt.Sprintf(
			"cannot change the ramp schedule of a plan with %d completed step(s); "+
				"resend the current ramp_schedule to keep it, or create a new plan",
			existingPlan.RampSchedule.CurrentStep))
	}
	return nil
}

// rampScheduleShapeUnchanged compares the schedule's shape, deliberately
// excluding the progress fields CurrentStep and StartDate, to tell apart
// "the update resubmitted the same schedule" from "the update picked a
// genuinely different one".
//
// The type must match, except that weekly, monthly and custom are only labels
// for the same step-based behavior (nothing downstream branches on them; only
// "immediate" is special). The UI can send just the presets or "custom", so a
// stored weekly 20%/7d ramp comes back as custom 20%/7d, and treating that as
// a change refused every edit of such a plan once a step had run (issue #1184).
func rampScheduleShapeUnchanged(a, b config.RampSchedule) bool {
	return rampTypesEquivalent(a.Type, b.Type) &&
		a.PercentPerStep == b.PercentPerStep &&
		a.StepIntervalDays == b.StepIntervalDays &&
		a.TotalSteps == b.TotalSteps
}

// rampTypesEquivalent reports whether two ramp type labels describe the same
// behavior: identical labels, or two of the step-based weekly/monthly/custom.
// "immediate" and any unrecognized label must match exactly.
func rampTypesEquivalent(a, b string) bool {
	if a == b {
		return true
	}
	return isStepBasedRampType(a) && isStepBasedRampType(b)
}

func isStepBasedRampType(t string) bool {
	return t == "weekly" || t == "monthly" || t == "custom"
}
