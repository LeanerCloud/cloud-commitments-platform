package api

import "fmt"

const (
	maxPlanGroupRecommendationIDs      = 1000
	maxPlanRequestRecommendationIDs    = 5000
	planGroupsTooManyIDsCode           = "plan_group_too_many_recommendation_ids"
	planGroupsTooManyTotalIDsCode      = "plan_groups_too_many_recommendation_ids"
	planGroupsMixedWithFlatServiceCode = "plan_groups_with_flat_service_fields"
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
// fields, a group without recommendation ids, and oversized id lists.
func (r *PlanRequest) validateGroups() error {
	if len(r.Groups) == 0 {
		return nil
	}
	if r.hasFlatServiceFields() {
		return NewClientErrorWithDetails(400,
			"groups cannot be combined with provider, service, term, payment or target_coverage",
			map[string]any{"code": planGroupsMixedWithFlatServiceCode})
	}
	total := 0
	for i, g := range r.Groups {
		n := len(g.RecommendationIDs)
		if n == 0 {
			return NewClientError(400, fmt.Sprintf("groups[%d] requires a non-empty recommendation_ids", i))
		}
		if n > maxPlanGroupRecommendationIDs {
			return NewClientErrorWithDetails(400,
				fmt.Sprintf("groups[%d] has %d recommendation_ids, at most %d are allowed", i, n, maxPlanGroupRecommendationIDs),
				map[string]any{"code": planGroupsTooManyIDsCode})
		}
		total += n
	}
	if total > maxPlanRequestRecommendationIDs {
		return NewClientErrorWithDetails(400,
			fmt.Sprintf("groups have %d recommendation_ids in total, at most %d are allowed", total, maxPlanRequestRecommendationIDs),
			map[string]any{"code": planGroupsTooManyTotalIDsCode})
	}
	return nil
}

func (r *PlanRequest) hasFlatServiceFields() bool {
	return r.Provider != "" || r.Service != "" || r.Term != nil || r.Payment != nil || r.TargetCoverage != nil
}
