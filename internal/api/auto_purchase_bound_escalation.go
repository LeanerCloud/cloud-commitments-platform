package api

import (
	"slices"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// boundGroupsEscalate reports whether a write widens what an armed bound plan
// (issue #1746) buys in a way the provider and service sets cannot show, so
// the arming grant's constraints must be checked again. It applies when the
// stored or the new plan carries the bound_groups marker; unmarked plans keep
// today's rules.
func boundGroupsEscalate(before, after *config.PurchasePlan) bool {
	beforeBound := isBoundPlan(before)
	afterBound := isBoundPlan(after)
	if !beforeBound && !afterBound {
		return false
	}
	if beforeBound && !afterBound {
		return true // the marker was removed
	}
	old := make(map[string]config.ServiceConfig, len(before.Services))
	for k := range before.Services {
		svc := before.Services[k]
		old[svc.EffectiveGroupID(k)] = svc
	}
	for k := range after.Services {
		svc := after.Services[k]
		// A disabled group buys nothing; enabling it later is checked then.
		if !svc.Enabled {
			continue
		}
		prev, ok := old[svc.EffectiveGroupID(k)]
		if !ok || groupWidens(&prev, &svc) {
			return true
		}
	}
	return false
}

func isBoundPlan(p *config.PurchasePlan) bool {
	return p != nil && p.RampSchedule.AllocationMode == config.RampAllocationBoundGroups
}

// groupWidens compares one group before and after a write.
func groupWidens(prev, next *config.ServiceConfig) bool {
	if next.Enabled && !prev.Enabled {
		return true
	}
	// An empty set means filter re-match, the widest binding there is.
	if len(prev.BoundIdentities) > 0 && len(next.BoundIdentities) == 0 {
		return true
	}
	for _, id := range next.BoundIdentities {
		if !slices.Contains(prev.BoundIdentities, id) {
			return true
		}
	}
	if groupCapRaised(prev.MaxStepCommitment, next.MaxStepCommitment) {
		return true
	}
	return next.Term != prev.Term || canonicalPayment(next) != canonicalPayment(prev)
}

// groupCapRaised treats a nil group cap as unbounded, unlike capCents(nil).
func groupCapRaised(prev, next *float64) bool {
	if next == nil {
		return prev != nil
	}
	if prev == nil {
		return false
	}
	return capCents(next) > capCents(prev)
}

func canonicalPayment(svc *config.ServiceConfig) string {
	if canon, ok := config.NormalizePaymentOption(svc.Provider, svc.Payment); ok {
		return canon
	}
	return svc.Payment
}
