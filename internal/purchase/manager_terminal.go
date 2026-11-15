package purchase

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
)

// saveTerminalExecution persists the finalized row. A plan row releases its
// suppressions in the same transaction ONLY when it ended failed and execErr
// proves no purchase call was submitted for any rec (beforeSubmissionError).
// Every other failure keeps them: a lost response ("outcome unknown"), a
// refused armed re-drive or a partial run may still have created a
// commitment, and releasing would let the next step buy it again. Kept
// suppressions expire with the grace period or are released on explicit
// cancel or revoke (platform#631).
func (m *Manager) saveTerminalExecution(ctx context.Context, exec *config.PurchaseExecution, execErr error) error {
	var notSubmitted *beforeSubmissionError
	provenNotSubmitted := exec.Status == "failed" && !anyRecPurchased(exec.Recommendations) && errors.As(execErr, &notSubmitted)
	if provenNotSubmitted {
		// Persist the proof for every row, plan or direct, so a later reader
		// (the ladder reconciler) can tell this failure from a reaped or
		// outcome-unknown one.
		exec.NoSubmission = true
	}
	// Guarded on "running": the executor owns the row only while it holds the
	// claim, so a row the reaper failed or an operator canceled in the meantime
	// is never silently overwritten (issue #829).
	if exec.PlanID == "" || !provenNotSubmitted {
		return m.config.SaveExecutionIfStatus(ctx, exec, executorOwnedStatuses)
	}
	return m.config.WithTx(ctx, func(tx pgx.Tx) error {
		if err := m.config.SaveExecutionIfStatusTx(ctx, tx, exec, executorOwnedStatuses); err != nil {
			return err
		}
		return m.config.DeleteSuppressionsByExecutionTx(ctx, tx, exec.ExecutionID)
	})
}

// recordRampAdvanceRefusal reports a ramp advance that did not happen, stamping
// it onto the execution row that just completed when the outcome is permanent
// so the decision outlives the log retention window.
//
// The refusal paths are new in issue #1669: the previous blind CurrentStep++
// could not decline, so a purchase always moved the ramp. Now money can be spent
// on a step the plan then declines to count, and CompletePlanStep returning
// before its write leaves next_execution_date stale, which shouldNotifyPlan
// reads as daysUntil < 0 and stops notifying that plan. Such a stall is not
// self-correcting, and a logging.Errorf was its only trace.
//
// Two outcomes are deliberately NOT stamped, because both are ordinary and
// neither stays true (issue #1861). A step still waiting on its other accounts
// is the shape of a partially-failed multi-account step being repaired one
// account at a time, and it stops being true the moment the last account buys.
// A step a sibling execution counted moments earlier is the shape of that
// step's last two accounts finishing together, and it means the step WAS
// counted. A note for either would outlive the fact and, worse, would flip a
// cleanly-completed row into History's audit-gap rendering, which keys on a
// non-empty Error and would then tell the operator the purchase's history
// record could not be saved. The waiting state is derived live instead, by
// config.GetStuckRampSteps for the plan-health ramp_blocked factor.
//
// The status stays "completed": the purchase did complete, and only the plan's
// progress accounting did not. Recovery is deliberately not scheduled here; this
// records the fact so History shows it and an operator can act on it.
func (m *Manager) recordRampAdvanceRefusal(ctx context.Context, exec *config.PurchaseExecution, cause error) {
	if errors.Is(cause, config.ErrRampStepIncomplete) || errors.Is(cause, config.ErrRampStepCountedBySibling) {
		logging.Warnf("Plan %s ramp step %d not advanced by execution %s: %v",
			exec.PlanID, exec.StepNumber, exec.ExecutionID, cause)
		return
	}
	logging.Errorf("Failed to update plan progress: %v", cause)
	exec.Error = appendErrNote(exec.Error, fmt.Sprintf("ramp not advanced: %v", cause))
	// Guarded on the terminal status this run just wrote, so the full-row
	// write cannot put back a status another writer set since (issue #835).
	if saveErr := m.config.SaveExecutionIfStatus(ctx, exec, []string{exec.Status}); saveErr != nil {
		logging.Errorf("AUDIT LOSS: failed to persist ramp-advance refusal for execution %s: %v",
			exec.ExecutionID, saveErr)
	}
}

// saveTerminalAndSkipped saves the root row, then the rows of accounts a
// fan-out skipped, all on ctx's single terminal-save deadline, then sends one
// email for the skipped rows and the late accounts' rows (issue #1672). The
// root goes first and its result is the return value: a skip row that does
// not fit in the deadline is logged as AUDIT LOSS and never fails the root.
func (m *Manager) saveTerminalAndSkipped(ctx context.Context, exec *config.PurchaseExecution, execErr error) error {
	err := m.saveOrRecoverTerminal(ctx, exec, execErr)
	var partial *multiAccountPartialError
	var saved []*config.PurchaseExecution
	if errors.As(execErr, &partial) {
		saved = m.saveSkippedRows(ctx, exec, partial.skipped)
	}
	m.notifyBatchedAccounts(ctx, exec, saved, lateRowsOf(execErr))
	return err
}

// saveSkippedRows saves each skip row until ctx ends and returns the saved ones.
func (m *Manager) saveSkippedRows(ctx context.Context, root *config.PurchaseExecution, rows []*config.PurchaseExecution) []*config.PurchaseExecution {
	var saved []*config.PurchaseExecution
	for _, row := range rows {
		err := ctx.Err()
		if err == nil {
			err = m.config.SavePurchaseExecution(ctx, row)
		}
		if err != nil {
			logging.Errorf("AUDIT LOSS: failed to save the skipped-account record for account %s of execution %s: %v", accountIDOf(row), root.ExecutionID, err)
			continue
		}
		saved = append(saved, row)
	}
	return saved
}
