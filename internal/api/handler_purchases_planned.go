package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
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
	for i := range executions {
		exec := executions[i]
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

func (h *Handler) pausePlannedPurchase(ctx context.Context, req *events.LambdaFunctionURLRequest, executionID string) (*StatusResponse, error) {
	if err := validateUUID(executionID); err != nil {
		return nil, err
	}

	session, err := h.requirePermission(ctx, req, "update", "purchases")
	if err != nil {
		return nil, err
	}
	if err := h.requireExecutionAccess(ctx, session, executionID); err != nil {
		return nil, err
	}
	if err := h.authorizeBoundedExecutionManagement(ctx, session, executionID); err != nil {
		return nil, err
	}

	// Atomically transition to paused
	if _, err := h.config.TransitionExecutionStatus(ctx, executionID, []string{"pending", "notified"}, "paused", resolveCreatorUserID(session)); err != nil {
		return nil, NewClientError(409, fmt.Sprintf("execution %s cannot be paused: %v", executionID, err))
	}

	return &StatusResponse{Status: "paused"}, nil
}

func (h *Handler) resumePlannedPurchase(ctx context.Context, req *events.LambdaFunctionURLRequest, executionID string) (*StatusResponse, error) {
	if err := validateUUID(executionID); err != nil {
		return nil, err
	}

	session, err := h.requirePermission(ctx, req, "update", "purchases")
	if err != nil {
		return nil, err
	}
	if accessErr := h.requireExecutionAccess(ctx, session, executionID); accessErr != nil {
		return nil, accessErr
	}
	row, err := h.loadPlannedExecution(ctx, executionID)
	if err != nil {
		return nil, err
	}
	verb, err := h.authorizeRowManagement(ctx, session, row)
	if err != nil {
		return nil, err
	}

	// A recommendation-bearing row that is past its date is moved to
	// now + NotificationDaysBefore on resume; a plan without a veto window
	// would move it to "now", so refuse before changing anything.
	if checkErr := h.requireResumeVetoWindow(ctx, row, time.Now()); checkErr != nil {
		return nil, checkErr
	}

	// Resuming puts the row back on schedule, so it counts against
	// MaxPurchaseAmount like a spend (issue #1130).
	if capErr := h.enforceResumeAmountCap(ctx, session, verb, row.Recommendations); capErr != nil {
		return nil, capErr
	}

	// Atomically transition from paused back to pending. Pinned so a
	// recommendation-set change (empty to non-empty) between the cap check
	// and this write loses the CAS; it does not catch in-set content edits,
	// which no writer performs.
	resumed, transitionErr := h.config.TransitionExecutionStatusPinned(ctx, executionID, []string{"paused"}, "pending", resolveCreatorUserID(session), len(row.Recommendations) == 0)
	if transitionErr != nil {
		return nil, NewClientError(409, fmt.Sprintf("execution %s cannot be resumed: %v", executionID, transitionErr))
	}
	if pushErr := h.pushPastScheduledDate(ctx, resumed, time.Now()); pushErr != nil {
		return nil, pushErr
	}

	return &StatusResponse{Status: "resumed"}, nil
}

// enforceResumeAmountCap bounds the row's total commitment by the cap of the
// verb that authorized the resume (issue #1130), checked on the row's full
// scope and amount as approve is: a grant scoped elsewhere must not lift the
// cap of the one that covers the row. The admin API key (empty verb) and a row
// without recommendations skip the check (recommendations attach later only
// to pending/notified rows and the scheduler re-checks the plan cap).
func (h *Handler) enforceResumeAmountCap(ctx context.Context, session *Session, verb string, recs []config.RecommendationRecord) error {
	if verb == "" || len(recs) == 0 {
		return nil
	}
	return h.enforcePurchaseConstraints(ctx, session, verb, recs)
}

// requireResumeVetoWindow refuses to resume a recommendation-bearing row whose
// scheduled_date has passed when its plan has no veto window (S13).
func (h *Handler) requireResumeVetoWindow(ctx context.Context, row *config.PurchaseExecution, now time.Time) error {
	if len(row.Recommendations) == 0 || !row.ScheduledDate.Before(now) {
		return nil
	}
	plan, err := h.config.GetPurchasePlan(ctx, row.PlanID)
	if err != nil {
		return fmt.Errorf("failed to read plan %s before resuming execution %s: %w", row.PlanID, row.ExecutionID, err)
	}
	if plan.RampSchedule.DatesManaged {
		return editorReconciliationError()
	}
	if vetoErr := plan.RequireVetoWindow(); vetoErr != nil {
		return NewClientError(409, fmt.Sprintf("execution %s cannot be resumed: %v", row.ExecutionID, vetoErr))
	}
	return nil
}

// pushPastScheduledDate moves a resumed recommendation-bearing row whose
// scheduled_date has already passed to now + the plan's NotificationDaysBefore
// days, so that once it is re-notified the scheduler cannot buy it before the
// veto window has run. The row is pending, which the scheduler does not
// execute while it carries recommendations, so the gap between the resume
// and this write cannot buy. Rows without recommendations keep their date
// (the scheduler fails them).
func (h *Handler) pushPastScheduledDate(ctx context.Context, row *config.PurchaseExecution, now time.Time) error {
	if len(row.Recommendations) == 0 || !row.ScheduledDate.Before(now) {
		return nil
	}
	plan, err := h.config.GetPurchasePlan(ctx, row.PlanID)
	if err != nil {
		return fmt.Errorf("failed to read plan %s to reschedule resumed execution %s: %w", row.PlanID, row.ExecutionID, err)
	}
	if vetoErr := plan.RequireVetoWindow(); vetoErr != nil {
		return NewClientError(409, fmt.Sprintf("execution %s was resumed but cannot be rescheduled: %v", row.ExecutionID, vetoErr))
	}
	// Conditional single-column update, not a full-row save: a cancel or
	// approve landing after the resume must not be overwritten. A lost
	// condition is a benign no-op (the row is no longer awaiting approval).
	if _, err := h.config.RescheduleAwaitingExecution(ctx, row.ExecutionID, now.AddDate(0, 0, plan.NotificationDaysBefore)); err != nil {
		if errors.Is(err, config.ErrEditorReconciliationRequired) {
			return editorReconciliationError()
		}
		return fmt.Errorf("failed to reschedule resumed execution %s: %w", row.ExecutionID, err)
	}
	return nil
}

// runPlannedPurchase forces a pending or paused scheduled purchase to
// execute immediately (the "Run now" button) instead of waiting for its
// scheduled date. It delegates to purchase.Manager.RunPlannedPurchaseNow,
// which shares ApproveAndExecute's 4-eyes-gated, CAS-guarded
// claim-then-execute funnel (issue #218): the previous implementation only
// CASed the row's status to "running" and reported success from that bare
// flip, but no executor consumes "running" rows -- the scheduler only picks
// up pending/notified, so nothing ever bought the purchase and the reaper
// later failed the row out from under the operator who was told it ran.
func (h *Handler) runPlannedPurchase(ctx context.Context, req *events.LambdaFunctionURLRequest, executionID string) (any, error) {
	if err := validateUUID(executionID); err != nil {
		return nil, err
	}

	session, err := h.requirePermission(ctx, req, "execute", "purchases")
	if err != nil {
		return nil, err
	}
	if err := h.requireExecutionAccess(ctx, session, executionID); err != nil {
		return nil, err
	}
	// update-any lets a non-creator run the row only within its Constraints
	// (issue #1129).
	if err := h.authorizeBoundedExecutionManagement(ctx, session, executionID); err != nil {
		return nil, err
	}

	// Re-hydrate the persisted recommendations and enforce the session's
	// execute:purchases Constraints against them before RunPlannedPurchaseNow
	// fires the purchase (issue #60). requirePermission above only checked
	// the bare execute:purchases verb: a session running a plan via
	// update-any:purchases (which lets a non-creator past the creator check
	// within its own Constraints) would otherwise bypass any MaxPurchaseAmount/
	// Providers/Services/Regions/AccountIDs cap configured on their own
	// execute:purchases permission, exactly the fail-open class the web
	// execute path (validateExecutePurchaseRequest) already closed for
	// SEC-01 (#1141). Routed through the shared enforcePurchaseConstraints
	// helper so a planned execution whose recs sum to $0 is refused by
	// requireNonZeroCommitment rather than reading as an uncapped
	// MaxPurchaseAmount (issue #60 review follow-up).
	execution, fetchErr := h.config.GetExecutionByID(ctx, executionID)
	if errors.Is(fetchErr, config.ErrNotFound) {
		return nil, errNotFound
	}
	if fetchErr != nil {
		return nil, fmt.Errorf("failed to get execution: %w", fetchErr)
	}
	// An empty step has no constraint sets to evaluate (the auth check fails
	// closed on them, which surfaced as a 500), and the executor refuses it
	// anyway so it cannot complete and advance the ramp (issue #1695).
	if len(execution.Recommendations) == 0 {
		return nil, NewClientError(409, fmt.Sprintf("execution %s cannot be started: %v", executionID, purchase.ErrPlanStepNoRecommendations))
	}
	if constraintErr := h.enforcePurchaseConstraints(ctx, session, "execute", execution.Recommendations); constraintErr != nil {
		return nil, constraintErr
	}

	// The check above read a snapshot; the funnel re-runs it on the row its
	// claim returns, the row it buys (issue #415).
	final, revocationToken, runErr := h.purchase.RunPlannedPurchaseNow(ctx, executionID, fourEyesActorIdentity(session), resolveCreatorUserID(session), h.runNowClaimCheck(session))
	if runErr != nil {
		return nil, h.executeFailure(ctx, req, executionID, "started", final, revocationToken, session.Email, runErr)
	}

	// The shared execute funnel rotated the row's token into a revocation
	// token whose raw value exists only here (issue #103); email it like the
	// session-approve path does. Best-effort: the purchase already committed.
	h.sendPurchaseExecutedEmail(ctx, req, final, revocationToken, session.Email)

	return map[string]any{
		"execution_id": executionID,
		"status":       final.Status,
		"message":      "Purchase executed",
	}, nil
}
