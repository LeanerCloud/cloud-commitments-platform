package purchase

import (
	"errors"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// PlanGoneRefusal is the text recorded on, and returned for, a plan step whose plan is gone.
const PlanGoneRefusal = "refusing to execute: this plan step's plan no longer exists, so nothing was purchased; start a new purchase if it is still wanted"

// IsOrphanedPlanStep reports a plan step whose plan is gone (#1048).
// purchase_executions.plan_id is ON DELETE SET NULL, so a deleted plan leaves
// its rows plan-less with nothing naming the plan. The one stored marker that
// survives is step_number: plan creation and the step sweep always write the
// 1-based step (>= 1, migration 000098), while a direct-execute, retry of one,
// or ladder row has no step and stores 0 (see persistRetryExecution: "for ad-hoc
// executions PlanID is "" and StepNumber is 0"). The DB default of 1 never
// applies because the store writes the field explicitly. Only stored fields are
// read: the approve paths stamp ExecutedByUserID and PreApprovalSkipReason on
// the claimed row in memory before the executor runs, so those prove nothing.
// A retry successor copies PlanID and StepNumber, so it stays refused too.
//
// Legacy risk: an ad-hoc row stored with step_number >= 1 before the store wrote
// the field explicitly would be refused, and a step-0 orphan from before
// migration 000098 is not recognized.
func IsOrphanedPlanStep(exec *config.PurchaseExecution) bool {
	return exec.PlanID == "" && exec.StepNumber >= 1
}

// orphanedPlanStepRefusal is the fire-time refusal. A row with a plan id is not
// its concern: executePurchase refuses one whose plan row is missing.
func orphanedPlanStepRefusal(exec *config.PurchaseExecution) error {
	if IsOrphanedPlanStep(exec) {
		return errors.New(PlanGoneRefusal)
	}
	return nil
}
