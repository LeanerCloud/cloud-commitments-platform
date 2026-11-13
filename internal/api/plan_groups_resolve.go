package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/planstep"
)

// resolveGroupRecs reads each group's selected recommendations from the stored
// table, one read per group scoped to its provider, service and the plan's
// target accounts. Any id that is missing, ambiguous or not the group's
// term, payment and a target account is a 400 naming the id and group: a bound
// plan never silently drops or substitutes a selection (issue #1746, D4).
// out[i] holds groups[i]'s recommendations in request order.
func (h *Handler) resolveGroupRecs(ctx context.Context, groups []PlanGroupRequest, targets []string) ([][]config.RecommendationRecord, error) {
	allowed := make(map[string]struct{}, len(targets))
	for _, id := range targets {
		allowed[id] = struct{}{}
	}
	out := make([][]config.RecommendationRecord, len(groups))
	for i := range groups {
		recs, err := h.resolveGroup(ctx, i, &groups[i], targets, allowed)
		if err != nil {
			return nil, err
		}
		out[i] = recs
	}
	return out, nil
}

func (h *Handler) resolveGroup(ctx context.Context, i int, g *PlanGroupRequest, targets []string, allowed map[string]struct{}) ([]config.RecommendationRecord, error) {
	term, payment, err := groupTermPayment(i, g)
	if err != nil {
		return nil, err
	}
	rows, err := h.config.ListStoredRecommendations(ctx, config.RecommendationFilter{Provider: g.Provider, Service: g.Service, AccountIDs: targets})
	if err != nil {
		logging.Errorf("createPlan: ListStoredRecommendations failed (group=%d provider=%s service=%s): %v", i, g.Provider, g.Service, err)
		return nil, NewClientError(500, "failed to load recommendations")
	}
	byID, err := indexWanted(i, rows, g.RecommendationIDs)
	if err != nil {
		return nil, err
	}
	recs := make([]config.RecommendationRecord, 0, len(g.RecommendationIDs))
	for _, id := range g.RecommendationIDs {
		rec, ok := byID[id]
		if !ok {
			return nil, NewClientError(400, fmt.Sprintf("groups[%d]: recommendation %q was not found for %s/%s in the plan's accounts", i, id, g.Provider, g.Service))
		}
		if !matchesGroup(rec, g, term, payment, allowed) {
			return nil, NewClientError(400, fmt.Sprintf("groups[%d]: recommendation %q does not match the group's term and payment or the plan's accounts", i, id))
		}
		recs = append(recs, *rec)
	}
	return recs, nil
}

// indexWanted indexes the rows whose id was requested; an id on more than one
// row is ambiguous and refused.
func indexWanted(i int, rows []config.RecommendationRecord, ids []string) (map[string]*config.RecommendationRecord, error) {
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	byID := make(map[string]*config.RecommendationRecord, len(wanted))
	for j := range rows {
		id := rows[j].ID
		if _, ok := wanted[id]; !ok {
			continue
		}
		if _, dup := byID[id]; dup {
			return nil, NewClientError(400, fmt.Sprintf("groups[%d]: recommendation %q is ambiguous", i, id))
		}
		byID[id] = &rows[j]
	}
	return byID, nil
}

// matchesGroup reports whether rec is the group's provider and service, has its
// term and payment, and sits in a target account (the same predicate Resolve
// applies when it sizes the group).
func matchesGroup(rec *config.RecommendationRecord, g *PlanGroupRequest, term int, payment string, allowed map[string]struct{}) bool {
	if !strings.EqualFold(rec.Provider, g.Provider) || rec.Service != g.Service {
		return false
	}
	match, err := planstep.MatchesPlan(rec, term, payment, allowed)
	return err == nil && match
}

// bindPlanGroups turns a groups request into a bound plan: the selected
// recommendations are resolved and their identities become the purchase set. A
// request without groups is left as the flat plan toPurchasePlan built.
func (h *Handler) bindPlanGroups(ctx context.Context, req *PlanRequest, plan *config.PurchasePlan) error {
	if len(req.Groups) == 0 {
		return nil
	}
	recs, err := h.resolveGroupRecs(ctx, req.Groups, req.TargetAccounts)
	if err != nil {
		return err
	}
	services, err := buildBoundServices(req.Groups, recs)
	if err != nil {
		return err
	}
	plan.Services = services
	plan.RampSchedule.AllocationMode = config.RampAllocationBoundGroups
	return nil
}
