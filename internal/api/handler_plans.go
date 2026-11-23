// Package api provides the HTTP API handlers for the CUDly dashboard.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

// Plans handlers.
func (h *Handler) listPlans(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (*PlansResponse, error) {
	session, err := h.requirePermission(ctx, req, "view", "plans")
	if err != nil {
		return nil, err
	}

	// parseAccountIDs validates and splits the comma-separated account_ids
	// query param. Returns nil (no filter) when absent or empty.
	accountIDs, err := parseAccountIDs(params["account_ids"])
	if err != nil {
		return nil, NewClientError(400, err.Error())
	}

	filter, anyAccount, err := h.scopedPlanFilter(ctx, session, accountIDs)
	if err != nil {
		return nil, err
	}
	if !anyAccount {
		return &PlansResponse{Plans: []PlanWithHealth{}}, nil
	}
	plans, err := h.config.ListPurchasePlans(ctx, filter)
	if err != nil {
		return nil, err
	}

	// Ensure all enabled plans have NextExecutionDate calculated
	now := h.clock()
	for i := range plans {
		if plans[i].Enabled && plans[i].NextExecutionDate == nil {
			plans[i].NextExecutionDate = calculateNextExecutionDate(&plans[i], now)
		}
	}

	return &PlansResponse{Plans: h.attachPlanHealth(ctx, plans, now)}, nil
}

// scopedPlanFilter narrows account_ids to the session's allowed_accounts. Only
// unrestricted callers get unassigned plans, matching requirePlanAccess.
func (h *Handler) scopedPlanFilter(ctx context.Context, session *Session, accountIDs []string) (filter config.PurchasePlanFilter, anyAccount bool, err error) {
	scope, err := h.getAccountScope(ctx, session)
	if err != nil {
		return filter, false, fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if scope.AllowsAll() {
		return config.PurchasePlanFilter{AccountIDs: accountIDs, IncludeUnassigned: true}, true, nil
	}
	allowed, _, err := h.intersectAccountFilterScope(ctx, session, accountIDs, nil)
	if err != nil {
		return filter, false, err
	}
	return config.PurchasePlanFilter{AccountIDs: allowed}, len(allowed) > 0, nil
}

// attachPlanHealth computes the per-plan health-score badge (issue #340
// follow-up) for every plan in the list, from exact per-plan execution
// counts over the trailing planHealthLookbackDays
// (config.StoreInterface.CountExecutionsByPlanAndStatus, aggregated in SQL)
// rather than from a capped page of execution rows, which would understate
// plans whose executions fall outside the newest page.
//
// If the counts fetch fails, every plan's HealthScore is left nil, which
// serializes as `"health_score": null` and renders as an explicit "unknown"
// badge. A score is a number operators make purchasing decisions from, so
// an uncomputable one must stay absent: substituting a default (100 reads
// as "healthy", 0 as "broken") would be a fabricated number indistinguishable
// from a real one. The request itself still succeeds -- the Plans page is
// not primarily about execution history, so a 500 here would be a worse
// trade than a visibly-unknown badge.
func (h *Handler) attachPlanHealth(ctx context.Context, plans []config.PurchasePlan, now time.Time) []PlanWithHealth {
	result := make([]PlanWithHealth, len(plans))
	for i := range plans {
		result[i] = PlanWithHealth{PurchasePlan: *planForResponse(&plans[i])}
	}
	if len(plans) == 0 {
		return result
	}

	since := now.AddDate(0, 0, -planHealthLookbackDays)
	countsByPlan, err := h.config.CountExecutionsByPlanAndStatus(ctx, planHealthExecutionStatuses, since)
	if err != nil {
		logging.Warnf("listPlans: CountExecutionsByPlanAndStatus failed, plan health reported as unknown: %v", err)
		return result
	}

	// A blocked ramp is worth up to 25 points, so a score computed without it
	// is not a partially-informed score, it is a wrong one -- and it errs
	// healthy, on plans that are stopped. Withhold every score rather than
	// publish that, exactly as the counts fetch above does.
	stuckByPlan, err := h.config.GetStuckRampSteps(ctx)
	if err != nil {
		logging.Warnf("listPlans: GetStuckRampSteps failed, plan health reported as unknown: %v", err)
		return result
	}

	// Same reasoning: a doomed root is worth 25 points and only subtracts, so
	// an unreadable report withholds every score instead of erring healthy.
	doomedByPlan, err := h.config.GetDoomedNextRoots(ctx, now)
	if err != nil {
		logging.Warnf("listPlans: GetDoomedNextRoots failed, plan health reported as unknown: %v", err)
		return result
	}

	for i := range plans {
		score, factors := computePlanHealth(plans[i], now, countsByPlan[plans[i].ID], stuckByPlan[plans[i].ID], doomedByPlan[plans[i].ID])
		result[i].HealthScore = &score
		result[i].HealthFactors = factors
	}
	return result
}

// calculateNextExecutionDate calculates the next execution date for a plan.
func calculateNextExecutionDate(plan *config.PurchasePlan, now time.Time) *time.Time {
	var nextDate time.Time
	if plan.RampSchedule.Type == "immediate" {
		// For immediate, schedule for tomorrow
		nextDate = now.AddDate(0, 0, 1)
	} else if plan.RampSchedule.StepIntervalDays > 0 {
		// For other schedules, use the step interval from now
		nextDate = now.AddDate(0, 0, plan.RampSchedule.StepIntervalDays)
	} else {
		// Default to tomorrow
		nextDate = now.AddDate(0, 0, 1)
	}
	return &nextDate
}

// mapCreatePlanStorageError keeps storage details out of API responses.
func mapCreatePlanStorageError(err error, notFoundMsg, genericMsg, logFmt string, logArgs ...any) error {
	var taken *config.PlanNameTakenError
	if errors.As(err, &taken) {
		return planNameTakenError(taken)
	}
	if errors.Is(err, config.ErrPlanProviderMismatch) {
		// The text is built by the store from account names the caller has
		// already been authorized to attach, and is the same text the
		// PUT /plans/:id/accounts path returns, so it is forwarded as is.
		return NewClientError(http.StatusBadRequest, err.Error())
	}
	if errors.Is(err, config.ErrNotFound) {
		return NewClientError(http.StatusNotFound, notFoundMsg)
	}
	if errors.Is(err, config.ErrPurchasePlanConflict) {
		return NewClientError(http.StatusConflict, "plan changed; refresh the plan and try again")
	}
	if errors.Is(err, config.ErrEditorReconciliationRequired) {
		return editorReconciliationError()
	}
	logging.Errorf(logFmt, logArgs...)
	return NewClientError(http.StatusInternalServerError, genericMsg)
}

// decodePlanRequest parses a create or update body and refuses an out-of-range
// target_coverage or an unknown ramp_schedule before any store access.
func decodePlanRequest(body string) (*PlanRequest, error) {
	var req PlanRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return nil, NewClientError(400, "invalid request body")
	}
	if err := req.validateTargetCoverage(); err != nil {
		return nil, err
	}
	if err := req.validateRampSchedule(); err != nil {
		return nil, err
	}
	if err := req.validateGroups(); err != nil {
		return nil, err
	}
	return &req, nil
}

// refuseGroupsOnUpdate fails loud: groups bind a plan at create only, so a PUT
// carrying them would otherwise be dropped silently (#1746).
func refuseGroupsOnUpdate(req *PlanRequest) error {
	if len(req.Groups) > 0 {
		return NewClientErrorWithDetails(400, "groups can only be set when creating a plan",
			map[string]any{"code": planGroupsNotSupportedCode})
	}
	return nil
}

func (h *Handler) createPlan(ctx context.Context, httpReq *events.LambdaFunctionURLRequest) (any, error) {
	// Require create:plans permission
	session, err := h.requirePermission(ctx, httpReq, "create", "plans")
	if err != nil {
		return nil, err
	}

	req, err := decodePlanRequest(httpReq.Body)
	if err != nil {
		return nil, err
	}

	// target_accounts is required: a plan must be tied to at least one
	// cloud_account row. The historical "leave blank to mean all accounts of
	// this provider" behavior created "universal plans" (rows in
	// purchase_plans with no matching plan_accounts row) that were hard to
	// scope, hard to filter, and hard to govern. Reject early with a clear
	// 400 so the frontend can surface the error before any DB write.
	if err := validateTargetAccounts(req.TargetAccounts); err != nil {
		return nil, err
	}

	// Refuse before the plan row exists: a scoped caller must not create a
	// plan that buys for an account outside their allowed_accounts.
	if err := h.requireAccountsAccess(ctx, session, req.TargetAccounts); err != nil {
		return nil, err
	}

	plan := req.toPurchasePlan()
	if err := h.bindPlanGroups(ctx, req, plan); err != nil {
		return nil, err
	}

	// Validate the plan
	if err := h.validateAndGatePlan(ctx, httpReq, session, nil, plan, req.TargetAccounts); err != nil {
		return nil, err
	}

	// Resolved before the store call and never defaulted: a scope error must
	// fail the request, not read as "no plan is visible" and let a duplicate in.
	scope, scopeErr := h.getAccountScope(ctx, session)
	if scopeErr != nil {
		return nil, fmt.Errorf("failed to get allowed accounts: %w", scopeErr)
	}

	// One transaction writes the plan and its accounts, so no partial plan can
	// exist and a concurrent create of the same name sees this one.
	if err := h.config.CreatePurchasePlanWithAccounts(ctx, plan, req.TargetAccounts, planVisibleTo(scope)); err != nil {
		return nil, mapCreatePlanStorageError(err,
			"account not found", "failed to create plan",
			"createPlan: CreatePurchasePlanWithAccounts failed (provider=%s service=%s accounts=%d): %v",
			req.Provider, req.Service, len(req.TargetAccounts), err)
	}

	return planForResponse(plan), nil
}

// planVisibleTo reports whether a caller with scope can see a plan attached to
// accounts. It mirrors requirePlanAccess: one in-scope account is enough, and a
// plan with no accounts is invisible to a scoped caller. It is pure so the
// store can run it inside its transaction.
func planVisibleTo(scope auth.AccountScope) func(accounts []config.CloudAccount) bool {
	return func(accounts []config.CloudAccount) bool {
		if scope.AllowsAll() {
			return true
		}
		for i := range accounts {
			if scope.Allows(accounts[i].ID, accounts[i].Name) {
				return true
			}
		}
		return false
	}
}

// validateTargetAccounts rejects a missing/empty target_accounts payload and
// rejects entries that are not valid UUIDs. Mirrors the validation the
// dedicated PUT /plans/:id/accounts endpoint already performs (see
// setPlanAccounts in handler_accounts_plans.go) so a request that gets past
// createPlan would also get past that endpoint.
func validateTargetAccounts(ids []string) error {
	if len(ids) == 0 {
		return NewClientError(400, "target_accounts is required: a plan must be tied to at least one account")
	}
	for _, aid := range ids {
		if err := validateUUID(aid); err != nil {
			return NewClientError(400, fmt.Sprintf("invalid target_account %q: must be a valid UUID", aid))
		}
	}
	return nil
}

func (h *Handler) getPlan(ctx context.Context, req *events.LambdaFunctionURLRequest, planID string) (any, error) {
	// Validate UUID format to prevent injection attacks
	if err := validateUUID(planID); err != nil {
		return nil, err
	}

	session, err := h.requirePermission(ctx, req, "view", "plans")
	if err != nil {
		return nil, err
	}

	err = h.requirePlanAccess(ctx, session, planID)
	if err != nil {
		return nil, err
	}

	// Map a missing plan to 404 (mirrors updatePlan / patchPlan /
	// getPlanForPurchaseCreation). GetPurchasePlan wraps config.ErrNotFound,
	// which the router's IsNotFoundError check does NOT recognize (it matches
	// only the api-package sentinel), so a raw return here surfaced a deleted
	// plan as a 500. A scheduled-purchase row can outlive its plan (issue
	// #1403: ON DELETE SET NULL detaches the execution, or the user's account
	// scope changes), and the Edit button then loads GET /plans/{id}; the
	// frontend needs a 404 to distinguish "plan is gone" from a server error
	// and reconcile the stale row instead of dead-ending on a generic message.
	plan, err := h.config.GetPurchasePlan(ctx, planID)
	if err != nil {
		return nil, mapCreatePlanStorageError(err,
			"plan not found", "failed to load plan",
			"getPlan: GetPurchasePlan failed")
	}

	// Ensure plan has NextExecutionDate calculated
	if plan.Enabled && plan.NextExecutionDate == nil {
		plan.NextExecutionDate = calculateNextExecutionDate(plan, time.Now())
	}

	return planForResponse(plan), nil
}

func (h *Handler) deletePlan(ctx context.Context, req *events.LambdaFunctionURLRequest, planID string) (any, error) {
	// Validate UUID format to prevent injection attacks
	if err := validateUUID(planID); err != nil {
		return nil, err
	}

	session, err := h.requirePermission(ctx, req, "delete", "plans")
	if err != nil {
		return nil, err
	}

	if err := h.requirePlanMutationAccess(ctx, session, planID); err != nil {
		return nil, err
	}

	if err := h.config.DeletePurchasePlan(ctx, planID); err != nil {
		if errors.Is(err, config.ErrManagedPlanLiveExecutions) {
			return nil, planEditorRefusal(http.StatusConflict, codeManagedPlanLiveExecutions,
				"this plan still has active or unsettled purchases; cancel or revoke them first")
		}
		return nil, err
	}

	return map[string]string{"status": "deleted"}, nil
}
