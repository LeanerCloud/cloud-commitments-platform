package api

import (
	"fmt"
	"net/http"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// codeEditorReconciliationRequired is the machine-readable body code for a
// legacy write refused because the plan is owned by the schedule editor.
const codeEditorReconciliationRequired = "editor_reconciliation_required"

// codeManagedPlanLiveExecutions is the body code for deleting a managed plan
// that still has active or unsettled purchases.
const codeManagedPlanLiveExecutions = "managed_plan_has_live_executions"

// codePlanNameTaken is the body code for creating a plan whose name a plan the
// caller can see already has (issue #1734).
const codePlanNameTaken = "plan_name_taken"

// planNameTakenError is the 409 for a duplicate plan name. The message shows
// the stored name of the existing plan, which the details id opens.
func planNameTakenError(taken *config.PlanNameTakenError) error {
	return NewClientErrorWithDetails(http.StatusConflict,
		fmt.Sprintf("a plan named %q already exists; open it or choose another name", taken.ExistingName),
		map[string]any{"code": codePlanNameTaken, "existing_plan_id": taken.ExistingID})
}

// planEditorRefusal builds a client error carrying a machine-readable code.
func planEditorRefusal(status int, code, msg string) error {
	return NewClientErrorWithDetails(status, msg, map[string]any{"code": code})
}

func editorReconciliationError() error {
	return planEditorRefusal(http.StatusConflict, codeEditorReconciliationRequired,
		"this plan is managed by the schedule editor; make this change in the editor")
}
