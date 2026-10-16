package api

import (
	"net/http"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// planEditorExistingSlot returns the stored slot for a 1-based step ordinal, or
// nil when the policy has no graph or no slot at that ordinal.
func planEditorExistingSlot(policy *planEditorFieldPolicyResult, ordinal int) *PlanEditorSlot {
	if policy == nil || policy.Graph == nil || ordinal < 1 || ordinal > len(policy.Graph.Steps) {
		return nil
	}
	return &policy.Graph.Steps[ordinal-1]
}

func planEditorSequenceInvalid(msg string) error {
	return planEditorRefusal(http.StatusConflict, codeEditorSequenceInvalid, msg)
}

// validatePlanEditorDates enforces the date rules for the complete remaining
// sequence. changed holds the ordinals whose root is created or replaced. Only
// a changed step, or the step after one, is order-checked against its
// predecessor. An unknown stored date therefore blocks a changed step next to
// it (409), and is tolerated only when no changed step is adjacent. The editor's validateDraftDates
// (frontend/src/lib/plan-draft-dates.ts) checks order and "after today" on every
// draft date; the server is stricter for changed steps (veto window) and
// deliberately silent about untouched ones.
func validatePlanEditorDates(before *config.PlanEditorSnapshot, intent *planEditorIntent, changed map[int]bool, policy *planEditorFieldPolicyResult, notice int, now time.Time) error {
	if err := planEditorDateWindow(intent.Steps, changed, notice, now); err != nil {
		return err
	}
	previous, havePrevious, err := planEditorLastFixedDate(before, policy)
	if err != nil {
		return err
	}
	previousChanged := false
	for _, step := range intent.Steps {
		var date *string
		if step.ScheduledDate != nil {
			date = new(step.ScheduledDate.UTC().Format(time.DateOnly))
		}
		if havePrevious && (previousChanged || changed[step.StepNumber]) {
			if err := planEditorDatePair(previous, date); err != nil {
				return err
			}
		}
		previous, havePrevious, previousChanged = date, true, changed[step.StepNumber]
	}
	return nil
}

// planEditorLastFixedDate returns the stored date of the step before the
// sequence (CurrentStep), which the first remaining date must follow.
func planEditorLastFixedDate(before *config.PlanEditorSnapshot, policy *planEditorFieldPolicyResult) (date *string, found bool, err error) {
	if before == nil || before.Plan.RampSchedule.CurrentStep < 1 {
		return nil, false, nil
	}
	slot := planEditorExistingSlot(policy, before.Plan.RampSchedule.CurrentStep)
	if slot == nil {
		return nil, false, planEditorRefusal(http.StatusConflict, codeEditorStateUnsupported, "the stored schedule has no step to order the new dates against; reload the editor")
	}
	return slot.ScheduledDate, true, nil
}

func planEditorDateWindow(steps []config.PlanScheduleStep, changed map[int]bool, notice int, now time.Time) error {
	if len(changed) > 0 && (notice < 1 || notice > config.MaxNotificationDaysBefore) {
		return planEditorSequenceInvalid("new purchases require a valid veto window")
	}
	for _, step := range steps {
		if !changed[step.StepNumber] {
			continue
		}
		if step.ScheduledDate == nil || !step.ScheduledDate.After(now.Add(time.Duration(notice)*24*time.Hour)) {
			return planEditorSequenceInvalid("new purchase dates must allow a fresh veto window")
		}
	}
	return nil
}

func planEditorDatePair(previous, date *string) error {
	if previous == nil || date == nil {
		return planEditorSequenceInvalid("an unavailable fixed neighbor prevents retiming")
	}
	if *date <= *previous {
		return planEditorSequenceInvalid("purchase dates must be strictly ordered on separate UTC days")
	}
	return nil
}
