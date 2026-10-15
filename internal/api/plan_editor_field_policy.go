package api

import (
	"context"
	"fmt"

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
