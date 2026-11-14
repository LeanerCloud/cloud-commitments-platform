package purchase

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// ErrStepCommitmentCap marks a plan step refused by the per-plan spend cap.
var ErrStepCommitmentCap = errors.New("plan step refused by commitment cap")

// ErrRecOutsideBoundGroups marks a bound plan step (issue #1746) holding a
// recommendation that no group bound. It wraps ErrStepCommitmentCap, so every
// cap site refuses it the same way.
var ErrRecOutsideBoundGroups = fmt.Errorf("%w: recommendation is outside every bound group", ErrStepCommitmentCap)

// StepCapApplies reports whether CheckStepCommitmentCap must run for plan:
// auto-purchase plans, plans with a cap, and bound plans, whose recommendations
// must map to a group whether or not any cap is set. The attach and email
// approval sites share it so they cannot drift.
func StepCapApplies(plan *config.PurchasePlan) bool {
	return plan != nil && (plan.AutoPurchase || plan.MaxStepCommitment != nil || isBoundPlan(plan))
}

func isBoundPlan(plan *config.PurchasePlan) bool {
	return plan.RampSchedule.AllocationMode == config.RampAllocationBoundGroups
}

// CheckStepCommitmentCap refuses a plan step's recommendations unless the plan
// carries a cap and the step's TOTAL commitment (upfront + monthly * term * 12,
// see RecommendationRecord.TotalCommitment) fits under it. It fails closed:
//   - a nil plan, a plan without MaxStepCommitment (nil or <= 0), or a
//     non-finite cap refuses everything;
//   - a recommendation with a zero, negative or non-finite total commitment is
//     refused (Azure monthly commitments have upfront 0, so upfront alone
//     would pass anything);
//   - a recommendation whose CommitmentCurrency is not provably USD, other or
//     empty (unknown), is refused: the cap is USD and nothing converts;
//   - no recommendations is not a step that can buy, so it is not this
//     check's concern and returns nil (the no-recommendations refusal owns it).
//
// It only ever refuses; nothing here buys.
func CheckStepCommitmentCap(plan *config.PurchasePlan, recs []config.RecommendationRecord) error {
	if len(recs) == 0 {
		return nil
	}
	if plan != nil && isBoundPlan(plan) {
		return checkBoundStepCap(plan, recs)
	}
	if !planCapUsable(plan) {
		return errNoPlanCap()
	}
	var total float64
	for i := range recs {
		c, err := capCommitment(&recs[i])
		if err != nil {
			return err
		}
		total += c
	}
	if total > *plan.MaxStepCommitment {
		return fmt.Errorf("%w: step total commitment %.2f exceeds max_step_commitment %.2f", ErrStepCommitmentCap, total, *plan.MaxStepCommitment)
	}
	return nil
}

func planCapUsable(plan *config.PurchasePlan) bool {
	return plan != nil && plan.MaxStepCommitment != nil && capUsable(*plan.MaxStepCommitment)
}

// capUsable reports whether a cap is a positive finite number. NaN is not: it
// fails every comparison, so as a cap it would silently let any step through.
func capUsable(c float64) bool {
	return c > 0 && !math.IsInf(c, 0)
}

func errNoPlanCap() error {
	return fmt.Errorf("%w: plan has no max_step_commitment set (auto-purchase of steps with recommendations is refused until one is set)", ErrStepCommitmentCap)
}

// checkBoundStepCap is CheckStepCommitmentCap for a bound plan (issue #1746).
// Every rec must belong to a group by its identity. An auto-purchase plan
// still needs a plan cap; a manual one (a human approves each row) may have
// none. A rec's commitment is checked only where a cap applies to it: the
// plan cap, or its group's cap. Each group stays under its own cap, and the
// total under the plan cap, so the effective cap is min(group, plan).
func checkBoundStepCap(plan *config.PurchasePlan, recs []config.RecommendationRecord) error {
	planCap := planCapUsable(plan)
	if !planCap && (plan.AutoPurchase || plan.MaxStepCommitment != nil) {
		return errNoPlanCap()
	}
	groupOf, err := boundGroupOf(plan)
	if err != nil {
		return err
	}
	totals, total, err := boundStepTotals(plan, recs, groupOf, planCap)
	if err != nil {
		return err
	}
	if planCap && total > *plan.MaxStepCommitment {
		return fmt.Errorf("%w: step total commitment %.2f exceeds max_step_commitment %.2f", ErrStepCommitmentCap, total, *plan.MaxStepCommitment)
	}
	return checkBoundGroupCaps(plan, totals)
}

// boundGroupOf maps each bound identity key to the service key of its group.
// A disabled group is skipped, as the resolver skips it, so its identities are
// outside every group. An identity bound by two groups is refused: validation
// rejects it, but a stored plan may hold one. Keys are visited in order so the
// pair named in the refusal is the same on every check.
func boundGroupOf(plan *config.PurchasePlan) (map[string]string, error) {
	keys := make([]string, 0, len(plan.Services))
	for k := range plan.Services {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	groupOf := make(map[string]string)
	for _, k := range keys {
		if !plan.Services[k].Enabled {
			continue
		}
		for _, id := range plan.Services[k].BoundIdentities {
			if prev, dup := groupOf[id]; dup && prev != k {
				return nil, fmt.Errorf("%w: a recommendation is bound by two groups (%q and %q)", ErrStepCommitmentCap, prev, k)
			}
			groupOf[id] = k
		}
	}
	return groupOf, nil
}

// boundStepTotals maps every rec to its bound group and sums the commitments a
// cap applies to: all of them under a plan cap, otherwise only those whose
// group has its own cap. A rec outside every group is refused.
func boundStepTotals(plan *config.PurchasePlan, recs []config.RecommendationRecord, groupOf map[string]string, planCap bool) (totals map[string]float64, total float64, err error) {
	totals = map[string]float64{}
	for i := range recs {
		key, keyErr := config.RecIdentityKey(&recs[i])
		if keyErr != nil {
			return nil, 0, fmt.Errorf("%w: recommendation %q: %w", ErrStepCommitmentCap, recs[i].ID, keyErr)
		}
		g, ok := groupOf[key]
		if !ok {
			return nil, 0, fmt.Errorf("%w (recommendation %q)", ErrRecOutsideBoundGroups, recs[i].ID)
		}
		if !planCap && plan.Services[g].MaxStepCommitment == nil {
			continue
		}
		c, capErr := capCommitment(&recs[i])
		if capErr != nil {
			return nil, 0, capErr
		}
		totals[g] += c
		total += c
	}
	return totals, total, nil
}

// checkBoundGroupCaps refuses the first group, in key order, whose cap is
// unusable or that is over its own cap.
func checkBoundGroupCaps(plan *config.PurchasePlan, totals map[string]float64) error {
	keys := make([]string, 0, len(totals))
	for g := range totals {
		keys = append(keys, g)
	}
	sort.Strings(keys)
	for _, g := range keys {
		gc := plan.Services[g].MaxStepCommitment
		if gc == nil {
			continue
		}
		if !capUsable(*gc) {
			return fmt.Errorf("%w: group %q has no usable max_step_commitment (%v)", ErrStepCommitmentCap, g, *gc)
		}
		if totals[g] > *gc {
			return fmt.Errorf("%w: group %q step commitment %.2f exceeds its max_step_commitment %.2f", ErrStepCommitmentCap, g, totals[g], *gc)
		}
	}
	return nil
}

// capCommitment returns a rec's total commitment for the cap comparison, or
// refuses it. The cap is USD with no FX conversion, so a currency that is not
// provably USD, other or empty (unknown), is refused (issue #1427).
func capCommitment(rec *config.RecommendationRecord) (float64, error) {
	if !config.IsUSDCommitmentCurrency(rec.CommitmentCurrency) {
		return 0, fmt.Errorf("%w: recommendation %q is not provably priced in USD, so it cannot be compared against the USD cap; refresh the recommendations so they carry a USD price", ErrStepCommitmentCap, rec.ID)
	}
	c := rec.TotalCommitment()
	if !(c > 0) || math.IsInf(c, 0) {
		return 0, fmt.Errorf("%w: recommendation %q has non-positive or non-finite total commitment %v", ErrStepCommitmentCap, rec.ID, c)
	}
	return c, nil
}

// recheckStepCap re-reads the plan and re-applies CheckStepCommitmentCap to a
// row this worker has just claimed. A plan that cannot be read refuses too.
func (m *Manager) recheckStepCap(ctx context.Context, exec *config.PurchaseExecution) error {
	plan, err := m.config.GetPurchasePlan(ctx, exec.PlanID)
	if err != nil {
		return fmt.Errorf("failed to re-read plan %s for the post-claim cap check: %w", exec.PlanID, err)
	}
	return CheckStepCommitmentCap(plan, exec.Recommendations)
}

// failClaimedBeforeSubmission fails a claimed (running) row that was refused
// before any provider call, with the beforeSubmission class so its suppressions
// release. It returns the refusal, joined with ErrAuditLoss when the terminal
// save fails (the row would then be stranded in "running" for the reaper).
// Operators are notified either way, as executeAndFinalize does (issue #1195).
func (m *Manager) failClaimedBeforeSubmission(ctx context.Context, exec *config.PurchaseExecution, cause error) error {
	execErr := beforeSubmission(cause)
	m.finalizeExecution(exec, execErr)
	defer m.notifyExecutionFailure(ctx, exec)
	logging.Errorf("Refused claimed purchase %s before submission: %v", exec.ExecutionID, cause)
	if err := m.saveTerminalExecution(ctx, exec, execErr); err != nil {
		return fmt.Errorf("%w: terminal save failed (%w); original refusal: %w", config.ErrAuditLoss, err, execErr)
	}
	return execErr
}
