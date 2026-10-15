package api

import (
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

type PlanEditorApprovalState string

const (
	PlanEditorApprovalNotApproved     PlanEditorApprovalState = "not_approved"
	PlanEditorApprovalFuture          PlanEditorApprovalState = "approved_future"
	PlanEditorApprovalDue             PlanEditorApprovalState = "approved_due"
	PlanEditorApprovalLegacyUncertain PlanEditorApprovalState = "legacy_uncertain"
	PlanEditorApprovalNotApplicable   PlanEditorApprovalState = "not_applicable"
	PlanEditorApprovalUnknown         PlanEditorApprovalState = "unknown"
)

func planEditorApprovalState(row *config.PurchaseExecution, evaluatedAt time.Time) PlanEditorApprovalState {
	switch row.Status {
	case "pending", "notified", "paused":
		if planEditorPriorAction(row) {
			return PlanEditorApprovalUnknown
		}
		return PlanEditorApprovalNotApproved
	case "scheduled":
		if planEditorPriorAction(row) {
			return PlanEditorApprovalUnknown
		}
		if row.ScheduledExecutionAt == nil || row.ScheduledExecutionAt.IsZero() {
			return PlanEditorApprovalLegacyUncertain
		}
		if row.ScheduledExecutionAt.After(evaluatedAt) {
			return PlanEditorApprovalFuture
		}
		return PlanEditorApprovalDue
	case "approved":
		return PlanEditorApprovalLegacyUncertain
	case config.StatusCompleted, config.StatusPartiallyCompleted, config.StatusCanceled, config.LegacyStatusCanceled, config.StatusExpired:
		return PlanEditorApprovalNotApplicable
	default:
		return PlanEditorApprovalUnknown
	}
}

func planEditorPriorAction(row *config.PurchaseExecution) bool {
	if row.ExecutedAt != nil || row.CompletedAt != nil || row.PreApprovalSkipReason != nil && *row.PreApprovalSkipReason != "" {
		return true
	}
	for i := range row.Recommendations {
		if row.Recommendations[i].Purchased {
			return true
		}
	}
	switch row.Status {
	case "pending", "notified", "paused":
		return row.ScheduledExecutionAt != nil
	}
	return false
}

type PlanEditorRootState string
type PlanEditorCapability string
type PlanEditorReason string

const (
	PlanEditorRootMissing            PlanEditorRootState  = "missing"
	PlanEditorRootUnique             PlanEditorRootState  = "unique"
	PlanEditorRootAmbiguous          PlanEditorRootState  = "ambiguous"
	PlanEditorFixed                  PlanEditorCapability = "fixed"
	PlanEditorReplacePending         PlanEditorCapability = "replace_pending"
	PlanEditorApprovedToggleRequired PlanEditorCapability = "approved_toggle_required"
	PlanEditorNeedsCreation          PlanEditorCapability = "needs_creation"
	PlanEditorDenied                 PlanEditorCapability = "denied"
	PlanEditorAmbiguousRoot          PlanEditorReason     = "ambiguous_root"
	PlanEditorOccupiedCapacity       PlanEditorReason     = "occupied_capacity"
	PlanEditorInvalidDate            PlanEditorReason     = "invalid_date"
	PlanEditorNotFuture              PlanEditorReason     = "not_future"
	PlanEditorLegacyUncertain        PlanEditorReason     = "legacy_uncertain"
	PlanEditorStatusFixed            PlanEditorReason     = "status_fixed"
	PlanEditorPermissionDenied       PlanEditorReason     = "permission_denied"
	PlanEditorApprovedEditsDisabled  PlanEditorReason     = "approved_edits_disabled"
)

type PlanEditorAuthority struct {
	AddMissingRoot bool
	Rows           map[string]PlanEditorRowAuthority
}

type PlanEditorRowAuthority struct {
	ReplacePending, ReplaceScheduled bool
}

type planEditorStepPolicy struct {
	Root                            *config.PurchaseExecution
	StructuralChangeAllowed         bool
	RootState                       PlanEditorRootState
	ApprovalState                   PlanEditorApprovalState
	Capability                      PlanEditorCapability
	Reason                          *PlanEditorReason
	Editable, RequiresApprovedEdits bool
}

func buildPlanEditorStepPolicy(rows []config.PurchaseExecution, step int, authority PlanEditorAuthority, evaluatedAt time.Time) planEditorStepPolicy {
	p := planEditorStepPolicy{RootState: PlanEditorRootMissing, ApprovalState: PlanEditorApprovalUnknown, Capability: PlanEditorFixed}
	var blocked bool
	p.Root, p.RootState, blocked = planEditorSelectRoot(rows, step)
	if p.RootState == PlanEditorRootAmbiguous {
		p.Root = nil
		return planEditorFixedPolicy(p, PlanEditorAmbiguousRoot)
	}
	if p.Root != nil {
		p.ApprovalState = planEditorApprovalState(p.Root, evaluatedAt)
		blocked = blocked || planEditorPriorAction(p.Root)
	}
	if blocked {
		return planEditorFixedPolicy(p, PlanEditorOccupiedCapacity)
	}
	if p.Root != nil {
		if reason := planEditorStructuralReason(p.Root, evaluatedAt); reason != nil {
			return planEditorFixedPolicy(p, *reason)
		}
	}
	p.StructuralChangeAllowed = true
	return planEditorOperationPolicy(p, authority)
}

func planEditorRootCandidate(row *config.PurchaseExecution, incoming map[string]bool) bool {
	lineage := row.IdempotencyKey
	if lineage == "" {
		lineage = row.ExecutionID
	}
	return row.CloudAccountID == nil && row.RetryAttemptN == 0 && row.RetryExecutionID == nil &&
		!incoming[row.ExecutionID] && !strings.Contains(lineage, ":")
}

func planEditorStructuralReason(row *config.PurchaseExecution, evaluatedAt time.Time) *PlanEditorReason {
	if row.ScheduledDate.IsZero() {
		return new(PlanEditorInvalidDate)
	}
	if !row.ScheduledDate.After(evaluatedAt) {
		return new(PlanEditorNotFuture)
	}
	switch row.Status {
	case "pending", "notified":
		return nil
	case "scheduled":
		if row.ScheduledExecutionAt == nil || row.ScheduledExecutionAt.IsZero() {
			return new(PlanEditorLegacyUncertain)
		}
		if !row.ScheduledExecutionAt.After(evaluatedAt) {
			return new(PlanEditorNotFuture)
		}
		return nil
	case "approved":
		return new(PlanEditorLegacyUncertain)
	default:
		return new(PlanEditorStatusFixed)
	}
}

func planEditorFixedPolicy(p planEditorStepPolicy, reason PlanEditorReason) planEditorStepPolicy {
	p.Reason = &reason
	return p
}

func planEditorOperationPolicy(p planEditorStepPolicy, authority PlanEditorAuthority) planEditorStepPolicy {
	p.Capability, p.Reason = PlanEditorDenied, new(PlanEditorPermissionDenied)
	if p.Root == nil {
		if authority.AddMissingRoot {
			p.Capability, p.Reason = PlanEditorNeedsCreation, nil
		}
		return p
	}
	grant := authority.Rows[p.Root.ExecutionID]
	if p.Root.Status == "scheduled" {
		if grant.ReplaceScheduled {
			p.Capability, p.Reason = PlanEditorApprovedToggleRequired, new(PlanEditorApprovedEditsDisabled)
			p.RequiresApprovedEdits = true
		}
	} else if grant.ReplacePending {
		p.Capability, p.Reason, p.Editable = PlanEditorReplacePending, nil, true
	}
	return p
}

func planEditorIncomingRetries(rows []config.PurchaseExecution) map[string]bool {
	incoming := make(map[string]bool)
	for i := range rows {
		if rows[i].RetryExecutionID != nil {
			incoming[*rows[i].RetryExecutionID] = true
		}
	}
	return incoming
}

func planEditorSelectRoot(rows []config.PurchaseExecution, step int) (*config.PurchaseExecution, PlanEditorRootState, bool) {
	incoming := planEditorIncomingRetries(rows)
	var root *config.PurchaseExecution
	state := PlanEditorRootMissing
	blocked := false
	for i := range rows {
		row := &rows[i]
		if row.StepNumber != step {
			continue
		}
		if row.Status == config.StatusCanceled || row.Status == config.LegacyStatusCanceled {
			blocked = blocked || planEditorPriorAction(row)
		} else if planEditorRootCandidate(row, incoming) {
			switch state {
			case PlanEditorRootUnique:
				state = PlanEditorRootAmbiguous
			case PlanEditorRootMissing:
				root, state = row, PlanEditorRootUnique
			}
		} else {
			blocked = true
		}
	}
	return root, state, blocked
}
