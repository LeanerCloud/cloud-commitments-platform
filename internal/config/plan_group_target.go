package config

import (
	"errors"
	"fmt"
	"maps"
	"math"
)

// ErrGroupTargetRefused marks a group target edit that is not allowed: an
// unbound plan, an unknown group, or a cap that would rise (issue #1057).
var ErrGroupTargetRefused = errors.New("group target edit refused")

// PlanGroupTarget edits one group of a bound plan by group id. Only caps may
// change, and only downward: an edit never lets a managed plan buy more
// (cap raises go through the grant gate, not this path). A nil field keeps the
// stored value.
type PlanGroupTarget struct {
	GroupID               string
	MaxStepCommitment     *float64
	PlanMaxStepCommitment *float64
}

// MergePlanGroupTarget returns a copy of plan with edit applied to the one
// group entry whose GroupID matches (keyed "provider/service#<group_id>").
// Every other entry and the allocation_mode marker are kept as stored. The
// effective cap of a step is min(group cap, plan cap), a missing cap counting
// as unbounded; a set cap may stay equal or drop, never rise.
func MergePlanGroupTarget(plan *PurchasePlan, edit PlanGroupTarget) (*PurchasePlan, error) {
	if plan == nil || plan.RampSchedule.AllocationMode != RampAllocationBoundGroups {
		return nil, fmt.Errorf("%w: plan is not a bound-groups plan", ErrGroupTargetRefused)
	}
	key, ok := groupEntryKey(plan, edit.GroupID)
	if !ok {
		return nil, fmt.Errorf("%w: plan has no group %q", ErrGroupTargetRefused, edit.GroupID)
	}
	next := *plan
	next.Services = maps.Clone(plan.Services)
	group := next.Services[key]
	oldEffective := EffectiveStepCap(plan.MaxStepCommitment, group.MaxStepCommitment)
	if edit.MaxStepCommitment != nil {
		if err := lowerCap("group "+edit.GroupID, group.MaxStepCommitment, *edit.MaxStepCommitment); err != nil {
			return nil, err
		}
		c := *edit.MaxStepCommitment
		group.MaxStepCommitment = &c
		next.Services[key] = group
	}
	if edit.PlanMaxStepCommitment != nil {
		if err := lowerCap("plan", plan.MaxStepCommitment, *edit.PlanMaxStepCommitment); err != nil {
			return nil, err
		}
		c := *edit.PlanMaxStepCommitment
		next.MaxStepCommitment = &c
	}
	if EffectiveStepCap(next.MaxStepCommitment, group.MaxStepCommitment) > oldEffective {
		return nil, fmt.Errorf("%w: effective cap for group %q would rise", ErrGroupTargetRefused, edit.GroupID)
	}
	return &next, nil
}

// EffectiveStepCap is min(group cap, plan cap) with a nil cap unbounded.
func EffectiveStepCap(plan, group *float64) float64 {
	eff := math.Inf(1)
	if plan != nil {
		eff = *plan
	}
	if group != nil && *group < eff {
		eff = *group
	}
	return eff
}

func groupEntryKey(plan *PurchasePlan, groupID string) (string, bool) {
	if groupID == "" {
		return "", false
	}
	for key := range plan.Services {
		svc := plan.Services[key]
		if svc.GroupID == groupID && checkGroupKey(key, &svc) == nil {
			return key, true
		}
	}
	return "", false
}

func lowerCap(what string, stored *float64, requested float64) error {
	if !(requested > 0) || math.IsInf(requested, 0) {
		return fmt.Errorf("%w: %s cap %v must be positive and finite", ErrGroupTargetRefused, what, requested)
	}
	if stored != nil && requested > *stored {
		return fmt.Errorf("%w: %s cap may not rise from %v to %v", ErrGroupTargetRefused, what, *stored, requested)
	}
	return nil
}
