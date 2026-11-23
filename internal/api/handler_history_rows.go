package api

import (
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
)

// executionToHistoryRow projects any non-completed PurchaseExecution into
// the flat history-row shape. Status maps 1:1 onto the history Status field.
// For pending/notified rows we attach the approver email so the UI can show
// "awaiting approval from X"; for failed rows we surface the stored Error
// message as the status description so the user sees WHY it failed (e.g.
// "send failed: Missing domain"). createdByEmail is the resolved email for
// the execution's creator (empty when not resolvable).
func executionToHistoryRow(exec config.PurchaseExecution, approver, createdByEmail string) config.PurchaseHistoryRecord {
	var accountID string
	if exec.CloudAccountID != nil {
		accountID = *exec.CloudAccountID
	} else {
		// Web-initiated bulk-purchase executions (handler_purchases_execute_flow.go's
		// newPendingExecution) never populate exec.CloudAccountID — the
		// per-rec CloudAccountID is the canonical source. Fall back to that
		// so the Approval queue's Account cell shows the actual account ID
		// instead of "-". Returns "" when recs disagree (a basket that
		// genuinely spans accounts honestly renders as the dash fallback
		// rather than a misleading single account).
		accountID = collapseRecommendationAccount(exec.Recommendations)
	}
	var createdBy string
	if exec.CreatedByUserID != nil {
		createdBy = *exec.CreatedByUserID
	}
	var retryExecID string
	if exec.RetryExecutionID != nil {
		retryExecID = *exec.RetryExecutionID
	}
	row := config.PurchaseHistoryRecord{
		AccountID:          accountID,
		PurchaseID:         exec.ExecutionID,
		Timestamp:          exec.ScheduledDate,
		Provider:           collapseRecommendationProvider(exec.Recommendations),
		Count:              len(exec.Recommendations),
		PlanID:             exec.PlanID,
		Status:             exec.Status,
		CreatedByUserID:    createdBy,
		CreatedBySystem:    exec.CreatedBySystem,
		CreatedByUserEmail: createdByEmail,
		RetryExecutionID:   retryExecID,
		RetryAttemptN:      exec.RetryAttemptN,
	}
	projectRecommendationFields(&row, exec)
	// Compute ops_hint at read time (issue #47, Q3) so updates to the
	// persistent-failure map land instantly without a re-collect. Only
	// failed rows can carry a hint — the resolver returns "" for empty
	// inputs, so this is safe to call unconditionally.
	if exec.Status == "failed" {
		row.OpsHint = purchase.ResolveOpsHint(exec.Error)
		// A retry the backend will always refuse is permanent, so it wins
		// over the operator-fixable failure-reason hint (which promises that
		// fixing the config makes Retry work). Same call and input as the
		// retry handler's gate so the two cannot disagree (issue #161); a
		// proven-unsent first attempt keeps Retry (issue #1803).
		if reason := purchase.RetryRefusalReason(&exec); reason != "" {
			row.RedriveUnsafe = true
			// Only a proven pre-send AWS refusal on every rec means nothing was
			// bought; for any other outcome the duplicate-purchase warning stays.
			// ops_hint stays set either way: the History UI hides Retry on it.
			if purchase.IsDefiniteAWSRefusal(exec.Recommendations, exec.Error) {
				row.OpsHint = definiteRefusalOpsHint
			} else {
				row.OpsHint = reason
			}
		} else {
			applyRedshiftRetryHint(&row, &exec)
		}
	}
	annotateHistoryRowByStatus(&row, exec, approver)
	return row
}

// applyRedshiftRetryHint hides Retry for a Redshift row the gate refuses for good; the stamp age
// is an app-clock hint, so near 24h Retry may show and then 409 from the database-clock gate.
func applyRedshiftRetryHint(row *config.PurchaseHistoryRecord, exec *config.PurchaseExecution) {
	if !purchase.IsRedshiftExecution(exec) {
		return
	}
	var sinceStamp *time.Duration
	if exec.FirstDispatchAt != nil {
		age := max(0, time.Since(*exec.FirstDispatchAt))
		sinceStamp = &age
	}
	if reason := purchase.RedshiftRetryPermanentRefusal(exec, sinceStamp); reason != "" {
		row.RedriveUnsafe = true
		row.OpsHint = reason
	}
}

// definiteRefusalOpsHint replaces the duplicate-purchase warning when AWS
// refused the purchase before making it.
const definiteRefusalOpsHint = "AWS refused this purchase before it was made; start a new purchase after fixing the cause"

// annotateHistoryRowByStatus fills in Approver + StatusDescription on the
// row based on exec.Status. Split from executionToHistoryRow to keep the
// switch below under the gocyclo threshold.
func annotateHistoryRowByStatus(row *config.PurchaseHistoryRecord, exec config.PurchaseExecution, approver string) {
	switch exec.Status {
	case "pending", "notified":
		row.Approver = approver
	case "failed":
		row.StatusDescription = purchase.SanitizeStoredError(exec.Error)
	case "expired":
		row.StatusDescription = "approval link expired (not approved within 7 days)"
	case config.StatusCanceled:
		annotateCancelled(row, exec, approver)
	default:
		// In-flight (approved/running/scheduled/paused) and audit-gap
		// (partially_completed/completed) cases. Split out to keep this switch
		// under the cyclomatic-complexity limit.
		annotateInFlightOrAuditGapRow(row, exec, approver)
	}
}

// annotateInFlightOrAuditGapRow handles the non-terminal and audit-gap statuses
// for annotateHistoryRowByStatus: approved/running, scheduled, paused,
// partially_completed, and completed (audit-gap). Extracted to keep the parent
// switch under the cyclomatic-complexity limit.
func annotateInFlightOrAuditGapRow(row *config.PurchaseHistoryRecord, exec config.PurchaseExecution, approver string) {
	switch exec.Status {
	case "approved", "running":
		// In-flight (issue #621): approved/running rows are NOT terminal —
		// the synchronous AWS purchase is mid-execution or got interrupted
		// (Lambda timeout / crash). Resolve who approved (so the user knows
		// who to ask) and overlay an "in progress" note so the UI never
		// renders this as a finished purchase.
		annotateApproved(row, exec, approver)
		row.StatusDescription = "approved — purchase in progress"
	case "scheduled":
		// Gmail-style pre-fire delay (issue #291 wave-2): the cloud SDK has not
		// been called yet. The "revocation window" for the frontend Revoke button
		// is the time until scheduled_execution_at (after which the scheduler
		// fires the SDK call and the row transitions to approved/running). Populate
		// RevocationWindowClosesAt with the fire time so canRevokeCompletedRow can
		// use its standard window check (issue #290, second-wave CR Finding E).
		if exec.ScheduledExecutionAt != nil {
			t := *exec.ScheduledExecutionAt
			row.RevocationWindowClosesAt = &t
		}
		row.StatusDescription = "scheduled — revoke before execution window closes to cancel for free"
	case "paused":
		row.Approver = approver
		row.StatusDescription = "purchase paused — resume or cancel from the plan"
	case "partially_completed":
		// #642: some recs committed, some failed. The committed recs are
		// surfaced via their own purchase_history rows; this synthesized row
		// is the audit flag for the failures. Flag IsAuditGap so the dashboard
		// excludes its execution-level dollars (the committed dollars are
		// counted on the per-rec purchase_history rows, not here) — same
		// double-count guard as the audit-gap completed case below.
		row.IsAuditGap = true
		annotateApproved(row, exec, approver)
		row.StatusDescription = "partially completed — some commitments succeeded, others failed: " + purchase.SanitizeStoredError(exec.Error)
	case "completed":
		// Only audit-gap completed executions reach here (fetchExecutionsAsHistory
		// skips clean completed rows). exec.Error carries why the history write
		// failed after a successful purchase — surface it so the purchase is
		// visible despite the missing purchase_history row (issue #621). Flag the
		// row so the dollar accounting excludes its execution-level total without
		// relying on StatusDescription being set (which annotateApproved may also
		// populate).
		row.IsAuditGap = true
		annotateApproved(row, exec, approver)
		if exec.Error != "" {
			row.StatusDescription = "purchase completed but its history record could not be saved: " + purchase.SanitizeStoredError(exec.Error)
		}
	}
}
