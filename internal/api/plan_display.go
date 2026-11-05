package api

import (
	"sort"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// planDisplay is what the dashboard and planned-purchase rows show for a plan.
type planDisplay struct {
	svc config.ServiceConfig
	// providers lists a bound plan's distinct enabled group providers, so a
	// provider filter can match a plan whose groups span providers. Nil for
	// unmarked plans, whose rows keep today's JSON shape.
	providers []string
}

// planDisplayFor summarizes a plan for display. Unmarked plans show their first
// service entry, as before. A bound plan (issue #1746) with several enabled
// groups keeps each field its groups share and shows "multiple" (term 0, which
// the frontend renders as "—") for a field they don't.
func planDisplayFor(plan *config.PurchasePlan) planDisplay {
	if !isBoundPlan(plan) {
		return planDisplay{svc: firstServiceConfig(plan)}
	}
	keys := make([]string, 0, len(plan.Services))
	for k, svc := range plan.Services {
		if svc.Enabled {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return planDisplay{svc: firstServiceConfig(plan)}
	}
	sort.Strings(keys)
	out := plan.Services[keys[0]]
	seen := map[string]struct{}{}
	var providers []string
	for _, k := range keys {
		svc := plan.Services[k]
		if _, dup := seen[svc.Provider]; !dup {
			seen[svc.Provider] = struct{}{}
			providers = append(providers, svc.Provider)
		}
		out.Provider = sharedOrMultiple(out.Provider, svc.Provider)
		out.Service = sharedOrMultiple(out.Service, svc.Service)
		out.Payment = sharedOrMultiple(out.Payment, svc.Payment)
		if out.Term != svc.Term {
			out.Term = 0
		}
	}
	// Keys start with their provider, so sorted keys give sorted providers.
	return planDisplay{svc: out, providers: providers}
}

const multipleValues = "multiple"

func sharedOrMultiple(shared, next string) string {
	if shared == next {
		return shared
	}
	return multipleValues
}
