package api

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

type planEditorIntent struct {
	config.PlanScheduleIntent
	CommandID                   string
	ExpectedRevision            *string
	ReviewedDiff                string
	AcknowledgedCancellationIDs []string
	AcknowledgedReapprovalIDs   []string
}

type planEditorIntentWire struct {
	CommandID                   string            `json:"command_id"`
	ExpectedRevision            *string           `json:"expected_revision"`
	Plan                        json.RawMessage   `json:"plan"`
	StepCount                   *int              `json:"step_count"`
	Steps                       []json.RawMessage `json:"steps"`
	AllowApprovedEdits          bool              `json:"allow_approved_edits"`
	ReviewedDiff                string            `json:"reviewed_diff"`
	AcknowledgedCancellationIDs []string          `json:"acknowledged_cancellation_ids"`
	AcknowledgedReapprovalIDs   []string          `json:"acknowledged_reapproval_ids"`
}

func decodePlanEditorIntent(body string) (*planEditorIntent, error) {
	wire, err := decodePlanEditorEnvelope(body)
	if err != nil {
		return nil, NewClientError(400, "Invalid graph editor intent")
	}
	change, err := decodePlanEditorPlan(wire.Plan)
	if err != nil {
		return nil, NewClientError(400, "Invalid graph editor plan changes")
	}
	change.Steps, err = decodePlanEditorSteps(wire.Steps)
	if err != nil {
		return nil, NewClientError(400, "Invalid graph editor steps")
	}
	change.StepCount, change.AllowApprovedEdits = wire.StepCount, wire.AllowApprovedEdits
	if wire.ExpectedRevision == nil && (change.StepCount == nil || change.AccountIDs == nil || len(*change.AccountIDs) == 0) {
		return nil, NewClientError(400, "Create requires step_count and target_accounts")
	}
	return &planEditorIntent{PlanScheduleIntent: *change, CommandID: wire.CommandID, ExpectedRevision: wire.ExpectedRevision,
		ReviewedDiff: wire.ReviewedDiff, AcknowledgedCancellationIDs: wire.AcknowledgedCancellationIDs, AcknowledgedReapprovalIDs: wire.AcknowledgedReapprovalIDs}, nil
}

func decodePlanEditorEnvelope(body string) (*planEditorIntentWire, error) {
	fields, err := planEditorObject([]byte(body), "command_id expected_revision plan step_count steps allow_approved_edits reviewed_diff acknowledged_cancellation_ids acknowledged_reapproval_ids", "expected_revision")
	if err != nil {
		return nil, err
	}
	var wire planEditorIntentWire
	if decodeErr := json.Unmarshal([]byte(body), &wire); decodeErr != nil {
		return nil, decodeErr
	}
	if identityErr := validatePlanEditorEnvelopeIdentity(fields, &wire); identityErr != nil {
		return nil, identityErr
	}
	wire.AcknowledgedCancellationIDs, err = planEditorUUIDs(wire.AcknowledgedCancellationIDs, config.MaxTotalSteps)
	if err != nil {
		return nil, err
	}
	wire.AcknowledgedReapprovalIDs, err = planEditorUUIDs(wire.AcknowledgedReapprovalIDs, config.MaxTotalSteps)
	if err != nil {
		return nil, err
	}
	return &wire, nil
}

func decodePlanEditorSteps(raw []json.RawMessage) ([]config.PlanScheduleStep, error) {
	out := make([]config.PlanScheduleStep, 0, len(raw))
	previous := 0
	for _, value := range raw {
		fields, err := planEditorObject(value, "step_number scheduled_date", "scheduled_date")
		if err != nil {
			return nil, err
		}
		if len(fields) != 2 {
			return nil, errors.New("step fields required")
		}
		var step config.PlanScheduleStep
		if decodeErr := json.Unmarshal(fields["step_number"], &step.StepNumber); decodeErr != nil {
			return nil, decodeErr
		}
		if step.StepNumber <= previous || step.StepNumber > config.MaxTotalSteps {
			return nil, errors.New("unordered step numbers")
		}
		step.ScheduledDate, err = planEditorDate(fields["scheduled_date"])
		if err != nil {
			return nil, err
		}
		out, previous = append(out, step), step.StepNumber
	}
	return out, nil
}

func planEditorFingerprint(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil && strings.ToLower(value) == value
}

func validatePlanEditorSaveProofs(intent *planEditorIntent) error {
	if !planEditorFingerprint(intent.ReviewedDiff) || intent.AcknowledgedCancellationIDs == nil || intent.AcknowledgedReapprovalIDs == nil {
		return NewClientError(400, "Save requires reviewed_diff and acknowledgement arrays")
	}
	return nil
}

func validatePlanEditorEnvelopeIdentity(fields map[string]json.RawMessage, wire *planEditorIntentWire) error {
	if fields["expected_revision"] == nil || fields["steps"] == nil || !planEditorCanonicalUUID(wire.CommandID) {
		return errors.New("required editor envelope fields")
	}
	if wire.ExpectedRevision != nil && !planEditorFingerprint(*wire.ExpectedRevision) {
		return errors.New("invalid revision")
	}
	if wire.StepCount != nil && (*wire.StepCount < 1 || *wire.StepCount > config.MaxTotalSteps) {
		return errors.New("invalid step count")
	}
	return nil
}
