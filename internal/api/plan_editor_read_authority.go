package api

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

func (h *Handler) requirePlanEditorReadSession(ctx context.Context, req *events.LambdaFunctionURLRequest) (*Session, error) {
	var session *Session
	for _, resource := range []string{auth.ResourcePlans, auth.ResourcePurchases} {
		next, err := h.requirePermission(ctx, req, auth.ActionView, resource)
		if err != nil {
			return nil, err
		}
		if session != nil && (session.UserID != next.UserID || session.UserAPIKeyID != next.UserAPIKeyID) {
			return nil, fmt.Errorf("editor permission identity changed")
		}
		session = next
	}
	return session, nil
}

func (h *Handler) authorizePlanEditorSnapshotRead(ctx context.Context, session *Session, snapshot *config.PlanEditorSnapshot) error {
	scope, err := h.getAccountScope(ctx, session)
	if err != nil {
		return err
	}
	if len(snapshot.Accounts) == 0 && !scope.AllowsAll() {
		return errNotFound
	}
	for _, account := range snapshot.Accounts {
		if !scope.Allows(account.ID, account.Name) {
			return errNotFound
		}
	}
	return h.requirePlanEditorReadConstraints(ctx, session, snapshot)
}

func (h *Handler) requirePlanEditorReadConstraints(ctx context.Context, session *Session, snapshot *config.PlanEditorSnapshot) error {
	sets, err := planEditorReadConstraintSets(snapshot)
	if err != nil {
		return err
	}
	for _, resource := range []string{auth.ResourcePlans, auth.ResourcePurchases} {
		if err := h.requirePermissionConstraints(ctx, session, auth.ActionView, resource, sets); err != nil {
			return err
		}
	}
	return nil
}

func planEditorReadConstraintSets(snapshot *config.PlanEditorSnapshot) ([]auth.PermissionConstraints, error) {
	accounts := make(map[string]config.PlanEditorAccount, len(snapshot.Accounts))
	for _, account := range snapshot.Accounts {
		accounts[account.ID] = account
	}
	for _, id := range snapshot.AccountIDs {
		if _, ok := accounts[id]; !ok {
			return nil, config.ErrPlanEditorAttribution
		}
	}
	sets := planEditorServiceReadContexts(snapshot, accounts)
	for i := range snapshot.Executions {
		rowSets, err := planEditorRowReadContexts(&snapshot.Executions[i], snapshot.AccountIDs, accounts)
		if err != nil {
			return nil, err
		}
		sets = append(sets, rowSets...)
	}
	for _, quantities := range []map[string]config.PlanRampQuantity{snapshot.Baseline, snapshot.Committed} {
		for identity := range quantities {
			parts := strings.Split(identity, "\x1f")
			if len(parts) != 11 {
				return nil, config.ErrPlanEditorAttribution
			}
			sets = append(sets, revokeConstraintSet(parts[1], parts[0], parts[2], parts[3]))
		}
	}
	return planEditorResolvedReadContexts(sets, accounts)
}

func planEditorServiceReadContexts(snapshot *config.PlanEditorSnapshot, accounts map[string]config.PlanEditorAccount) []auth.PermissionConstraints {
	var sets []auth.PermissionConstraints
	for name := range snapshot.Plan.Services {
		service := snapshot.Plan.Services[name]
		var ids []string
		for _, id := range snapshot.AccountIDs {
			if accounts[id].Provider == service.Provider {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			ids = []string{""}
		}
		regions := service.IncludeRegions
		if len(regions) == 0 {
			regions = []string{""}
		}
		for _, id := range ids {
			for _, region := range regions {
				sets = append(sets, revokeConstraintSet(id, service.Provider, service.Service, region))
			}
		}
	}
	return sets
}

func planEditorRowReadContexts(row *config.PurchaseExecution, associations []string, accounts map[string]config.PlanEditorAccount) ([]auth.PermissionConstraints, error) {
	var sets []auth.PermissionConstraints
	seen := make(map[string]bool)
	for i := range row.Recommendations {
		rec := &row.Recommendations[i]
		id := derefString(rec.CloudAccountID)
		if id == "" {
			return nil, config.ErrPlanEditorAttribution
		}
		seen[id] = true
		sets = append(sets, revokeConstraintSet(id, rec.Provider, rec.Service, rec.Region))
	}
	if row.CloudAccountID != nil {
		id := *row.CloudAccountID
		if id == "" {
			return nil, config.ErrPlanEditorAttribution
		}
		if !seen[id] {
			sets = append(sets, revokeConstraintSet(id, accounts[id].Provider, "", ""))
		}
	}
	if row.CloudAccountID == nil && len(row.Recommendations) == 0 {
		for _, id := range associations {
			sets = append(sets, revokeConstraintSet(id, accounts[id].Provider, "", ""))
		}
	}
	return sets, nil
}

func planEditorResolvedReadContexts(sets []auth.PermissionConstraints, accounts map[string]config.PlanEditorAccount) ([]auth.PermissionConstraints, error) {
	covered := make(map[string]bool)
	for _, set := range sets {
		for _, id := range set.AccountIDs {
			account, ok := accounts[id]
			if !ok {
				return nil, config.ErrPlanEditorAttribution
			}
			covered[id] = true
			if !slices.Equal(set.Providers, knownScopeValue(account.Provider)) {
				sets = append(sets, revokeConstraintSet(id, account.Provider, "", ""))
			}
		}
	}
	for id, account := range accounts {
		if !covered[id] {
			sets = append(sets, revokeConstraintSet(id, account.Provider, "", ""))
		}
	}
	if len(sets) == 0 {
		sets = []auth.PermissionConstraints{revokeConstraintSet("", "", "", "")}
	}
	return sets, nil
}
