package api

import (
	"context"
	"fmt"
	"sort"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

// getUpcomingPurchases enumerates pending purchase_executions joined to
// their parent PurchasePlan for display. Mirrors the data flow in
// getPlannedPurchases (handler_purchases_planned.go) so the dashboard widget and
// the Plans page walk the same canonical "what's about to happen" set.
//
// The widget previously enumerated plans and synthesized one row per plan
// from plan.NextExecutionDate. That was wrong because action endpoints
// (DELETE /api/purchases/planned/{id}, pause, resume, run) all target
// purchase_executions.execution_id, not purchase_plans.id; the Cancel
// button could only fire correctly when the row in front of the operator
// IS a real execution. PR #207 worked around it by routing Cancel to
// api.deletePlan(planID), but that deleted the whole plan — too
// aggressive for "skip this scheduled run". This version emits actual
// execution rows so Cancel can target just the planned purchase via
// DELETE /api/purchases/planned/{id}.
//
// The optional provider, account_ids and legacy account_id query parameters
// narrow the rows like the summary route does (issue #713, see
// parseUpcomingFilter); they never widen the session's allowed_accounts.
func (h *Handler) getUpcomingPurchases(ctx context.Context, req *events.LambdaFunctionURLRequest) (*UpcomingPurchaseResponse, error) {
	session, err := h.requirePermission(ctx, req, "view", "purchases")
	if err != nil {
		return nil, err
	}
	// Issue #713: the topbar provider/account chips, validated before any read.
	filter, err := h.parseUpcomingFilter(ctx, session, req.QueryStringParameters)
	if err != nil {
		return nil, err
	}

	executions, err := h.config.GetPendingExecutions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get pending executions: %w", err)
	}

	plans, err := h.config.ListPurchasePlans(ctx, config.PurchasePlanFilter{})
	if err != nil {
		return nil, fmt.Errorf("failed to get purchase plans: %w", err)
	}
	planMap := make(map[string]*config.PurchasePlan, len(plans))
	for i := range plans {
		planMap[plans[i].ID] = &plans[i]
	}

	scope, err := h.getAccountScope(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	upcoming, err := h.upcomingInScope(ctx, scope, filter, executions, planMap)
	if err != nil {
		return nil, err
	}
	return &UpcomingPurchaseResponse{Purchases: upcoming}, nil
}

// upcomingInScope builds the upcoming rows for the executions whose plan still
// exists, that the session scope can see, and that pass the caller's filter,
// in input order.
func (h *Handler) upcomingInScope(ctx context.Context, scope auth.AccountScope, filter upcomingFilter, executions []config.PurchaseExecution, planMap map[string]*config.PurchasePlan) ([]UpcomingPurchase, error) {
	// Memoises GetPlanAccounts, mirroring getPlannedPurchases.
	planAccounts := make(map[string][]config.CloudAccount)

	var upcoming []UpcomingPurchase
	for i := range executions {
		exec := executions[i]
		plan := planMap[exec.PlanID]
		if plan == nil {
			// Orphaned execution (plan was deleted but cleanup missed it).
			// Hide rather than crash; cleanup is a separate concern.
			continue
		}
		ok, err := h.isExecutionInScope(ctx, scope, &exec, planAccounts)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		row := upcomingFromExecution(plan, &exec)
		ok, err = filter.matches(ctx, h, &row, &exec, planAccounts)
		if err != nil {
			return nil, err
		}
		if ok {
			upcoming = append(upcoming, row)
		}
	}
	return upcoming, nil
}

// firstServiceConfig returns the ServiceConfig for the lexicographically first
// key in plan.Services, giving a deterministic result regardless of map
// iteration order. Returns the zero value when the map is empty (both callers
// tolerate empty provider/service strings — they fall back to blank display
// fields rather than panicking).
func firstServiceConfig(plan *config.PurchasePlan) config.ServiceConfig {
	if len(plan.Services) == 0 {
		return config.ServiceConfig{}
	}
	keys := make([]string, 0, len(plan.Services))
	for k := range plan.Services {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return plan.Services[keys[0]]
}

// upcomingFromExecution projects a (plan, execution) pair onto the
// UpcomingPurchase response. Uses the first service entry from the plan as
// representative — the response shape doesn't support multi-service plans.
// Step number comes from the execution row directly (already stamped by the
// scheduler at instance-create time).
func upcomingFromExecution(plan *config.PurchasePlan, exec *config.PurchaseExecution) UpcomingPurchase {
	display := planDisplayFor(plan)
	svc := display.svc
	return UpcomingPurchase{
		Providers:        display.providers,
		ExecutionID:      exec.ExecutionID,
		PlanID:           plan.ID,
		PlanName:         plan.Name,
		ScheduledDate:    exec.ScheduledDate.Format("2006-01-02"),
		Provider:         svc.Provider,
		Service:          svc.Service,
		StepNumber:       exec.StepNumber,
		TotalSteps:       plan.RampSchedule.TotalSteps,
		EstimatedSavings: exec.EstimatedSavings,
		CreatedByUserID:  exec.CreatedByUserID,

		HasRecommendations: len(exec.Recommendations) > 0,
	}
}
