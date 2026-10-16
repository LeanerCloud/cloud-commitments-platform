package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

type planEditorMutationAuthority struct {
	CanUpdatePlan, CanArmOrRaiseCap bool
	Roots                           PlanEditorAuthority
}

func (h *Handler) buildPlanEditorMutationAuthority(ctx context.Context, req *events.LambdaFunctionURLRequest, session *Session, snapshot *config.PlanEditorSnapshot) (*planEditorMutationAuthority, error) {
	sets, err := planEditorReadConstraintSets(snapshot)
	if err != nil {
		return nil, err
	}
	update, err := h.planEditorOptionalGrant(ctx, req, session, auth.ActionUpdate, auth.ResourcePlans, sets)
	if err != nil {
		return nil, err
	}
	out := &planEditorMutationAuthority{CanUpdatePlan: update, Roots: PlanEditorAuthority{Rows: make(map[string]PlanEditorRowAuthority)}}
	if !update {
		return out, nil
	}
	out.Roots.AddMissingRoot, err = h.planEditorOptionalGrant(ctx, req, session, auth.ActionUpdate, auth.ResourcePurchases, sets)
	if err != nil {
		return nil, err
	}
	out.Roots.Rows, err = h.planEditorRowMutationAuthorities(ctx, req, session, snapshot)
	if err != nil {
		return nil, err
	}
	if session.UserID != apiKeyAdminUserID {
		out.CanArmOrRaiseCap, err = h.planEditorOptionalGrant(ctx, req, session, auth.ActionExecute, auth.ResourcePurchases, sets)
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (h *Handler) planEditorOptionalGrant(ctx context.Context, req *events.LambdaFunctionURLRequest, session *Session, action, resource string, sets []auth.PermissionConstraints) (bool, error) {
	next, err := h.requirePermission(ctx, req, action, resource)
	if isForbidden(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if next.UserID != session.UserID || next.UserAPIKeyID != session.UserAPIKeyID {
		return false, fmt.Errorf("editor permission identity changed")
	}
	err = h.requirePermissionConstraints(ctx, session, action, resource, sets)
	if isForbidden(err) {
		return false, nil
	}
	return err == nil, err
}

func (h *Handler) planEditorRowMutationAuthorities(ctx context.Context, req *events.LambdaFunctionURLRequest, session *Session, snapshot *config.PlanEditorSnapshot) (map[string]PlanEditorRowAuthority, error) {
	accounts := make(map[string]config.PlanEditorAccount, len(snapshot.Accounts))
	for _, account := range snapshot.Accounts {
		accounts[account.ID] = account
	}
	grants := make(map[string]PlanEditorRowAuthority)
	for i := range snapshot.Executions {
		row := &snapshot.Executions[i]
		switch row.Status {
		case "pending", "notified", "scheduled":
		default:
			continue
		}
		sets, err := planEditorRowActionContexts(row, snapshot.AccountIDs, accounts)
		if err != nil {
			return nil, err
		}
		grants[row.ExecutionID], err = h.planEditorRowMutationAuthority(ctx, req, session, row, sets)
		if err != nil {
			return nil, err
		}
	}
	return grants, nil
}

func planEditorRowActionContexts(row *config.PurchaseExecution, associations []string, accounts map[string]config.PlanEditorAccount) ([]auth.PermissionConstraints, error) {
	sets, err := planEditorRowReadContexts(row, associations, accounts)
	if err != nil {
		return nil, err
	}
	subset := make(map[string]config.PlanEditorAccount)
	for _, set := range sets {
		for _, id := range set.AccountIDs {
			if account, ok := accounts[id]; ok {
				subset[id] = account
			}
		}
	}
	return planEditorResolvedReadContexts(sets, subset)
}

func (h *Handler) planEditorRowMutationAuthority(ctx context.Context, req *events.LambdaFunctionURLRequest, session *Session, row *config.PurchaseExecution, sets []auth.PermissionConstraints) (PlanEditorRowAuthority, error) {
	allowed, err := h.planEditorOptionalGrant(ctx, req, session, auth.ActionUpdate, auth.ResourcePurchases, sets)
	if err != nil || !allowed {
		return PlanEditorRowAuthority{}, err
	}
	own := row.CreatedByUserID != nil && *row.CreatedByUserID == session.UserID
	if !own {
		allowed, err = h.planEditorOptionalGrant(ctx, req, session, auth.ActionUpdateAny, auth.ResourcePurchases, sets)
		if err != nil || !allowed {
			return PlanEditorRowAuthority{}, err
		}
	}
	grant := PlanEditorRowAuthority{ReplacePending: true}
	if row.Status == "scheduled" {
		grant.ReplaceScheduled, err = h.planEditorScheduledRevocationGrant(ctx, req, session, own, sets)
	}
	return grant, err
}

func (h *Handler) planEditorScheduledRevocationGrant(ctx context.Context, req *events.LambdaFunctionURLRequest, session *Session, own bool, sets []auth.PermissionConstraints) (bool, error) {
	allowed, err := h.planEditorOptionalGrant(ctx, req, session, auth.ActionRevokeAny, auth.ResourcePurchases, sets)
	if err == nil && !allowed && own {
		return h.planEditorOptionalGrant(ctx, req, session, auth.ActionRevokeOwn, auth.ResourcePurchases, sets)
	}
	return allowed, err
}

const (
	PlanEditorUnsupportedField    PlanEditorReason = "unsupported_field"
	PlanEditorUnsupportedSchedule PlanEditorReason = "unsupported_schedule"
	PlanEditorAffectedRootFixed   PlanEditorReason = "affected_root_fixed"
)

type PlanEditorFieldRule struct {
	Editable                          bool              `json:"editable"`
	Reason                            *PlanEditorReason `json:"reason"`
	RequiresApprovedEdits             bool              `json:"requires_approved_edits"`
	RequiresProposedValueRevalidation bool              `json:"requires_proposed_value_revalidation"`
}

type PlanEditorFieldPolicy struct {
	Controls         map[string]PlanEditorFieldRule            `json:"controls"`
	Services         map[string]map[string]PlanEditorFieldRule `json:"services"`
	CanChangeShape   bool                                      `json:"can_change_shape"`
	ShapeLockReason  *PlanEditorReason                         `json:"shape_lock_reason"`
	CanArmOrRaiseCap bool                                      `json:"can_arm_or_raise_cap"`
}

type planEditorFieldPolicyResult struct {
	Fields                 PlanEditorFieldPolicy
	Graph                  *PlanEditorProjection
	GraphUnavailableReason *PlanEditorReason
}

func (h *Handler) buildPlanEditorFieldPolicy(ctx context.Context, req *events.LambdaFunctionURLRequest, session *Session, snapshot *config.PlanEditorSnapshot, evaluatedAt time.Time) (*planEditorFieldPolicyResult, error) {
	authority, err := h.buildPlanEditorMutationAuthority(ctx, req, session, snapshot)
	if err != nil {
		return nil, err
	}
	graph, err := buildPlanEditorProjection(snapshot, authority.Roots, evaluatedAt)
	if err != nil && !errors.Is(err, ErrPlanEditorUnsupportedSchedule) {
		return nil, err
	}
	out := &planEditorFieldPolicyResult{Graph: graph}
	if graph == nil {
		out.GraphUnavailableReason = new(PlanEditorUnsupportedSchedule)
	}
	effective := planEditorEffectiveFieldRule(snapshot, graph, authority.CanUpdatePlan)
	identity := planEditorIdentityFieldRule(snapshot, effective)
	shape := identity
	if graph == nil {
		shape = planEditorLockedField(PlanEditorUnsupportedSchedule)
	} else if !graph.StructuralShapeChangeAllowed {
		shape = planEditorLockedField(*graph.ShapeLockReason)
	}
	ordinary := PlanEditorFieldRule{Editable: authority.CanUpdatePlan}
	if !ordinary.Editable {
		ordinary.Reason = new(PlanEditorPermissionDenied)
	}
	revalidated := ordinary
	revalidated.RequiresProposedValueRevalidation = true
	unsupported := planEditorLockedField(PlanEditorUnsupportedField)
	filter := planEditorFilterFieldRule(snapshot, effective)
	out.Fields = PlanEditorFieldPolicy{
		Controls: map[string]PlanEditorFieldRule{
			"name": ordinary, "description": unsupported, "enabled": revalidated, "auto_purchase": revalidated,
			"max_step_commitment": revalidated, "notification_days_before": effective,
			"step_count": shape, "ramp_type": shape, "percent_per_step": shape, "step_interval_days": shape,
			"start_date": shape, "target_accounts": identity, "allocation_mode": unsupported, "dates_managed": unsupported,
		},
		Services:       make(map[string]map[string]PlanEditorFieldRule, len(snapshot.Plan.Services)),
		CanChangeShape: shape.Editable, ShapeLockReason: shape.Reason, CanArmOrRaiseCap: authority.CanArmOrRaiseCap,
	}
	for key := range snapshot.Plan.Services {
		out.Fields.Services[key] = map[string]PlanEditorFieldRule{
			"provider": identity, "service": identity, "enabled": effective, "term": identity, "payment": identity,
			"coverage": effective, "ramp_schedule": shape, "include_engines": filter, "exclude_engines": filter,
			"include_regions": filter, "exclude_regions": filter, "include_types": filter, "exclude_types": filter, "min_count": filter,
		}
	}
	return out, nil
}

func planEditorLockedField(reason PlanEditorReason) PlanEditorFieldRule {
	return PlanEditorFieldRule{Reason: &reason}
}

func planEditorEffectiveFieldRule(snapshot *config.PlanEditorSnapshot, graph *PlanEditorProjection, canUpdate bool) PlanEditorFieldRule {
	if !canUpdate {
		return planEditorLockedField(PlanEditorPermissionDenied)
	}
	if planEditorUnmappedAction(snapshot) {
		return planEditorLockedField(PlanEditorAffectedRootFixed)
	}
	rule := PlanEditorFieldRule{Editable: true, RequiresProposedValueRevalidation: true}
	if graph == nil {
		if snapshot.Baseline != nil {
			return planEditorLockedField(PlanEditorProgressOrBaseline)
		}
		return rule
	}
	for i := range graph.Steps {
		slot := &graph.Steps[i]
		if slot.LogicalCompleted {
			continue
		}
		switch slot.Capability {
		case PlanEditorReplacePending, PlanEditorNeedsCreation:
		case PlanEditorApprovedToggleRequired:
			rule.Editable, rule.RequiresApprovedEdits, rule.Reason = false, true, new(PlanEditorApprovedEditsDisabled)
		default:
			return planEditorLockedField(PlanEditorAffectedRootFixed)
		}
	}
	return rule
}

// planEditorFilterFieldRule locks the filter lists and min_count once a step is
// scheduled: planstep.Precheck refuses any scheduled plan that carries one, so
// offering the edit would only fail at Save.
func planEditorFilterFieldRule(snapshot *config.PlanEditorSnapshot, effective PlanEditorFieldRule) PlanEditorFieldRule {
	if (effective.Editable || effective.RequiresApprovedEdits) && planEditorHasScheduledStep(snapshot) {
		return planEditorLockedField(PlanEditorAffectedRootFixed)
	}
	return effective
}

func planEditorHasScheduledStep(snapshot *config.PlanEditorSnapshot) bool {
	ramp := &snapshot.Plan.RampSchedule
	for i := range snapshot.Executions {
		row := &snapshot.Executions[i]
		if row.StepNumber > ramp.CurrentStep && row.StepNumber <= ramp.TotalSteps && !planEditorHarmlessTerminal(row) {
			return true
		}
	}
	return false
}

func planEditorUnmappedAction(snapshot *config.PlanEditorSnapshot) bool {
	ramp := &snapshot.Plan.RampSchedule
	for i := range snapshot.Executions {
		row := &snapshot.Executions[i]
		if row.StepNumber > ramp.CurrentStep && row.StepNumber <= ramp.TotalSteps {
			continue
		}
		if planEditorHarmlessTerminal(row) {
			continue
		}
		if planEditorTerminalPrefix(row, ramp.CurrentStep) {
			continue
		}
		return true
	}
	return false
}

func planEditorHarmlessTerminal(row *config.PurchaseExecution) bool {
	switch row.Status {
	case config.StatusCanceled, config.LegacyStatusCanceled, config.StatusExpired:
		return !planEditorPriorAction(row)
	default:
		return false
	}
}

func planEditorIdentityFieldRule(snapshot *config.PlanEditorSnapshot, effective PlanEditorFieldRule) PlanEditorFieldRule {
	if snapshot.Plan.RampSchedule.CurrentStep > 0 || snapshot.Baseline != nil {
		return planEditorLockedField(PlanEditorProgressOrBaseline)
	}
	return effective
}

func planEditorTerminalPrefix(row *config.PurchaseExecution, current int) bool {
	return row.StepNumber > 0 && row.StepNumber <= current &&
		(row.Status == config.StatusCompleted || row.Status == config.StatusPartiallyCompleted) &&
		row.CompletedAt != nil && !row.CompletedAt.IsZero()
}
