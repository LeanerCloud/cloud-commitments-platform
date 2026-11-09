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
	for k := range plan.Services {
		if plan.Services[k].Enabled {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return planDisplay{svc: firstServiceConfig(plan)}
	}
	sort.Strings(keys)
	out := plan.Services[keys[0]]
	out.Provider = config.ServiceEntryProvider(keys[0], out.Provider)
	seen := map[string]struct{}{}
	var providers []string
	for _, k := range keys {
		svc := plan.Services[k]
		provider := config.ServiceEntryProvider(k, svc.Provider)
		if _, dup := seen[provider]; !dup {
			seen[provider] = struct{}{}
			providers = append(providers, provider)
		}
		out.Provider = sharedOrMultiple(out.Provider, provider)
		out.Service = sharedOrMultiple(out.Service, svc.Service)
		out.Payment = sharedOrMultiple(out.Payment, svc.Payment)
		if out.Term != svc.Term {
			out.Term = 0
		}
	}
	// The key prefix is the provider, so sorted keys give sorted providers.
	return planDisplay{svc: out, providers: providers}
}

const multipleValues = "multiple"

func sharedOrMultiple(shared, next string) string {
	if shared == next {
		return shared
	}
	return multipleValues
}
