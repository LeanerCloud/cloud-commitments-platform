package api

import "github.com/LeanerCloud/cloud-commitments-platform/internal/config"

// planForResponse is the plan as plan responses show it (issue #1746): each
// bound group reports bound_count instead of its bound_identities, which can
// hold up to 1000 identity keys and would otherwise inflate every plan read
// past the Lambda response limit for clients without gzip. Plans with no bound
// identities are returned as they are, so their JSON is unchanged. The stored
// plan is never modified: the services map is copied first.
func planForResponse(plan *config.PurchasePlan) *config.PurchasePlan {
	if plan == nil || !hasBoundIdentities(plan) {
		return plan
	}
	out := *plan
	out.Services = make(map[string]config.ServiceConfig, len(plan.Services))
	for k := range plan.Services {
		svc := plan.Services[k]
		svc.BoundCount = len(svc.BoundIdentities)
		svc.BoundIdentities = nil
		out.Services[k] = svc
	}
	return &out
}

func hasBoundIdentities(plan *config.PurchasePlan) bool {
	for k := range plan.Services {
		if len(plan.Services[k].BoundIdentities) > 0 {
			return true
		}
	}
	return false
}
