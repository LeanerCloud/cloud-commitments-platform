package api

import "fmt"

const (
	maxPlanGroupRecommendationIDs      = 1000
	maxPlanRequestRecommendationIDs    = 5000
	planGroupsTooManyIDsCode           = "plan_group_too_many_recommendation_ids"
	planGroupsTooManyTotalIDsCode      = "plan_groups_too_many_recommendation_ids"
	planGroupsMixedWithFlatServiceCode = "plan_groups_with_flat_service_fields"
	planGroupsNotSupportedCode         = "plan_groups_not_supported"
)

// PlanGroupRequest is one service group of a plan built from selected
// recommendations. It carries the per-group service settings that a flat plan
// request sets once at the top level.
type PlanGroupRequest struct {
	Payment           *string  `json:"payment,omitempty"`
	Term              *int     `json:"term,omitempty"`
	Coverage          *int     `json:"coverage,omitempty"`
	MaxStepCommitment *float64 `json:"max_step_commitment,omitempty"`
	Provider          string   `json:"provider"`
	Service           string   `json:"service"`
	RecommendationIDs []string `json:"recommendation_ids"`
}

// validateGroups refuses a groups request that also sets the flat service
// fields, a group without provider, service or usable recommendation ids,
// out-of-range coverage or cap, duplicate ids and oversized id lists.
func (r *PlanRequest) validateGroups() error {
	if len(r.Groups) == 0 {
		return nil
	}
	if r.hasFlatServiceFields() {
		return NewClientErrorWithDetails(400,
			"groups cannot be combined with provider, service, term, payment or target_coverage",
			map[string]any{"code": planGroupsMixedWithFlatServiceCode})
	}
	seen := map[string]struct{}{}
	total := 0
	for i, g := range r.Groups {
		if err := g.validate(i, seen); err != nil {
			return err
		}
		total += len(g.RecommendationIDs)
	}
	if total > maxPlanRequestRecommendationIDs {
		return NewClientErrorWithDetails(400,
			fmt.Sprintf("groups have %d recommendation_ids in total, at most %d are allowed", total, maxPlanRequestRecommendationIDs),
			map[string]any{"code": planGroupsTooManyTotalIDsCode})
	}
	return nil
}

// validate checks one group; seen carries the ids of the earlier groups.
func (g PlanGroupRequest) validate(i int, seen map[string]struct{}) error {
	if err := g.validateSettings(i); err != nil {
		return err
	}
	return g.validateIDs(i, seen)
}

func (g PlanGroupRequest) validateSettings(i int) error {
	if g.Provider == "" || g.Service == "" {
		return NewClientError(400, fmt.Sprintf("groups[%d] requires provider and service", i))
	}
	if g.Coverage != nil && (*g.Coverage < 0 || *g.Coverage > 100) {
		return NewClientError(400, fmt.Sprintf("groups[%d].coverage must be between 0 and 100", i))
	}
	if g.MaxStepCommitment != nil && *g.MaxStepCommitment <= 0 {
		return NewClientError(400, fmt.Sprintf("groups[%d].max_step_commitment must be greater than 0", i))
	}
	return nil
}

func (g PlanGroupRequest) validateIDs(i int, seen map[string]struct{}) error {
	n := len(g.RecommendationIDs)
	if n == 0 {
		return NewClientError(400, fmt.Sprintf("groups[%d] requires a non-empty recommendation_ids", i))
	}
	if n > maxPlanGroupRecommendationIDs {
		return NewClientErrorWithDetails(400,
			fmt.Sprintf("groups[%d] has %d recommendation_ids, at most %d are allowed", i, n, maxPlanGroupRecommendationIDs),
			map[string]any{"code": planGroupsTooManyIDsCode})
	}
	for _, id := range g.RecommendationIDs {
		if id == "" {
			return NewClientError(400, fmt.Sprintf("groups[%d] has an empty recommendation id", i))
		}
		if _, dup := seen[id]; dup {
			return NewClientError(400, fmt.Sprintf("groups[%d] repeats recommendation id %q", i, id))
		}
		seen[id] = struct{}{}
	}
	return nil
}

func (r *PlanRequest) hasFlatServiceFields() bool {
	return r.Provider != "" || r.Service != "" || r.Term != nil || r.Payment != nil || r.TargetCoverage != nil
}
