package api

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

// plannedListStatuses are the execution statuses surfaced in the Scheduled
// (Planned) Purchases list. It deliberately includes "paused" so a paused
// execution stays VISIBLE with its badge instead of silently dropping out of
// the list. It does NOT use GetPendingExecutions, which the
// scheduler relies on to decide what to FIRE: paused rows must be listed but
// never fired, so the two concerns use different status sets.
var plannedListStatuses = []string{"pending", "notified", "paused"}

func (h *Handler) getPlannedPurchases(ctx context.Context, req *events.LambdaFunctionURLRequest) (*PlannedPurchasesResponse, error) {
	// This endpoint backs the Plans page's "Scheduled (Planned) Purchases"
	// list, which is plan-scheduled data, not purchase-execution data. Gate on
	// view:plans (mirroring listPlans) so Read-Only users, who hold view:plans
	// but not view:purchases (see auth.DefaultReadOnlyPermissions), can see it.
	// Per-plan account scoping is still enforced below via isPlanAllowedCached,
	// and the pause/resume/run/delete mutations keep their stronger
	// update/execute/delete:purchases gates, so this does not let Read-Only
	// users manage planned purchases.
	session, err := h.requirePermission(ctx, req, "view", "plans")
	if err != nil {
		return nil, err
	}

	// GetPlannedExecutions orders scheduled_date ASC at the DB level so the
	// soonest-first list isn't truncated when total rows exceed MaxListLimit.
	// (GetExecutionsByStatuses uses DESC + LIMIT for History; mixing them here
	// drops the genuinely-soonest rows, exactly the rows this list must show.
	// An in-memory re-sort cannot recover what LIMIT already discarded.)
	executions, err := h.config.GetPlannedExecutions(ctx, plannedListStatuses, config.MaxListLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to get planned executions: %w", err)
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
	purchases, err := h.plannedPurchasesInScope(ctx, scope, executions, planMap)
	if err != nil {
		return nil, err
	}
	return &PlannedPurchasesResponse{
		Purchases: purchases,
	}, nil
}

// plannedPurchasesInScope builds the list rows for the executions whose plan
// still exists and that the caller's account scope can see, in input order.
func (h *Handler) plannedPurchasesInScope(ctx context.Context, scope auth.AccountScope, executions []config.PurchaseExecution, planMap map[string]*config.PurchasePlan) ([]PlannedPurchase, error) {
	// Memoises GetPlanAccounts: plan-level parents of the same plan share it.
	planAccounts := make(map[string][]config.CloudAccount)
	var purchases []PlannedPurchase
	for _rvc := range executions {
		exec := executions[_rvc]
		plan := planMap[exec.PlanID]
		if plan == nil {
			continue
		}
		ok, err := h.isExecutionInScope(ctx, scope, &exec, planAccounts)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		purchases = append(purchases, buildPlannedPurchase(plan, &exec))
	}
	return purchases, nil
}

// isExecutionInScope is requireExecutionInScope for list filtering: an
// out-of-scope execution is hidden (false) rather than an error, while store
// failures still propagate.
func (h *Handler) isExecutionInScope(ctx context.Context, scope auth.AccountScope, exec *config.PurchaseExecution, planAccounts map[string][]config.CloudAccount) (bool, error) {
	err := h.requireExecutionInScope(ctx, scope, exec, planAccounts)
	if IsNotFoundError(err) {
		return false, nil
	}
	return err == nil, err
}

// buildPlannedPurchase converts a (plan, execution) pair into the API-facing PlannedPurchase.
// Provider/service/term/payment are taken from firstServiceConfig (lexicographic key order)
// so the result is deterministic regardless of map iteration order.
func buildPlannedPurchase(plan *config.PurchasePlan, exec *config.PurchaseExecution) PlannedPurchase {
	display := planDisplayFor(plan)
	svc := display.svc
	row := PlannedPurchase{
		Providers:        display.providers,
		ID:               exec.ExecutionID,
		PlanID:           exec.PlanID,
		PlanName:         plan.Name,
		ScheduledDate:    exec.ScheduledDate.Format("2006-01-02"),
		Provider:         svc.Provider,
		Service:          svc.Service,
		ResourceType:     "Various",
		Region:           "Multiple",
		Term:             svc.Term,
		Payment:          svc.Payment,
		EstimatedSavings: exec.EstimatedSavings,
		UpfrontCost:      exec.TotalUpfrontCost,
		Status:           exec.Status,
		StepNumber:       exec.StepNumber,
		TotalSteps:       plan.RampSchedule.TotalSteps,
		CreatedByUserID:  exec.CreatedByUserID,

		HasRecommendations: len(exec.Recommendations) > 0,
	}
	if len(exec.Recommendations) > 0 {
		first := exec.Recommendations[0]
		row.ResourceType, row.Region = first.ResourceType, first.Region
		for i := range exec.Recommendations {
			rec := &exec.Recommendations[i]
			row.Count += rec.Count
			if rec.ResourceType == "" || rec.ResourceType != first.ResourceType {
				row.ResourceType = "Various"
			}
			if rec.Region == "" || rec.Region != first.Region {
				row.Region = "Multiple"
			}
		}
	}
	return row
}
