// Package purchase handles the purchase workflow including approvals and execution.
package purchase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/ctxutil"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/oidc"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/jackc/pgx/v5"
)

// STSClient interface for AWS STS operations.
type STSClient interface {
	GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// ManagerConfig holds configuration for the purchase manager.
type ManagerConfig struct {
	ProviderFactory provider.FactoryInterface
	AmbientAWSCreds aws.CredentialsProvider
	STSClient       STSClient
	AssumeRoleSTS   credentials.STSClient
	CredentialStore credentials.CredentialStore
	OIDCSigner      oidc.Signer
	// AttemptJournal records each purchase attempt durably (go#107). Nil keeps
	// the provider's own window dedupe.
	AttemptJournal AttemptJournal
	EmailSender    email.SenderInterface
	ConfigStore    config.StoreInterface
	// BastionSTSFactory builds the STS client a bastion-mode account's target
	// role is assumed with, signed with the bastion account's credentials.
	// Without it bastion accounts fail to resolve (issue #263).
	BastionSTSFactory      credentials.STSClientFactory
	DashboardURL           string
	OIDCIssuerURL          string
	NotificationDaysBefore int
	// MaxDueRowAge caps how long after its due time the fire and process
	// sweeps may still buy a row (issue #712); an older row is failed
	// unbought instead. Zero means DefaultMaxDueRowAge.
	MaxDueRowAge time.Duration
}

// AttemptJournal is the journal the manager hands to providers. It must also
// read claims (go#700) and take the durable purchase claim (go#745), so a wrapper
// that hides ClaimedByOthers or the claim methods fails to compile instead of
// silently disabling the cross-lineage exclusion or the cross-process claim the
// Azure reservation flow finds by type assertion.
type AttemptJournal interface {
	common.AttemptJournal
	common.ClaimReader
	common.PurchaseClaimer
}

// Manager handles purchase workflow.
type Manager struct {
	resolver        stepResolver
	email           email.SenderInterface
	stsClient       STSClient
	assumeRoleSTS   credentials.STSClient
	config          config.StoreInterface
	ambientAWSCreds aws.CredentialsProvider
	credStore       credentials.CredentialStore
	attemptJournal  AttemptJournal
	providerFactory provider.FactoryInterface
	oidcSigner      oidc.Signer
	bastionSTS      credentials.STSClientFactory
	oidcIssuerURL   string
	dashboardURL    string
	// budgets lets tests shorten purchase deadlines. Production leaves it
	// zero, which means the default durations below.
	budgets    purchaseBudgets
	notifyDays int
	// maxDueRowAge is ManagerConfig.MaxDueRowAge; read it via dueRowMaxAge.
	maxDueRowAge time.Duration
}

// ProcessResult holds the result of processing scheduled purchases.
type ProcessResult struct {
	Errors    []string `json:"errors,omitempty"`
	Processed int      `json:"processed"`
	Executed  int      `json:"executed"`
	Failed    int      `json:"failed"`
	Recovered int      `json:"recovered,omitempty"`
	// Stale counts the Failed rows refused because they were due more than
	// the due-row age cap ago (ErrDueRowTooOld, issue #712). Each one is
	// also in Failed and Errors.
	Stale int `json:"stale,omitempty"`
}

// staleApprovedThreshold is how long an execution may sit in the "approved"
// status before the recovery sweep in ProcessScheduledPurchases treats it as
// stranded (issue #632). It must be comfortably larger than the longest
// possible synchronous purchase run so a legitimately in-flight execution is
// never failed out from under itself. The purchase Lambda timeout is 60s;
// 15min (matching the RI-exchange stale-sweep threshold in pkg/exchange)
// leaves a wide safety margin.
//
// Note: the stuck-purchase reaper (reaper.go) fails "running" rows after
// DefaultReapAfter (10m) but leaves "approved" rows alone until
// minApprovedReapAfter, which is derived from this constant plus one process
// sweep interval, so this recovery always gets to re-drive a stranded
// approval before the reaper fails it (issue #894).
const staleApprovedThreshold = 15 * time.Minute

// NotificationResult holds the result of sending notifications.
type NotificationResult struct {
	Notified int `json:"notified"`
}

// NewManager creates a new purchase manager.
func NewManager(cfg ManagerConfig) *Manager {
	factory := cfg.ProviderFactory
	if factory == nil {
		factory = &provider.DefaultFactory{}
	}

	return &Manager{
		config:          cfg.ConfigStore,
		email:           cfg.EmailSender,
		stsClient:       cfg.STSClient,
		assumeRoleSTS:   cfg.AssumeRoleSTS,
		bastionSTS:      cfg.BastionSTSFactory,
		ambientAWSCreds: cfg.AmbientAWSCreds,
		credStore:       cfg.CredentialStore,
		attemptJournal:  cfg.AttemptJournal,
		providerFactory: factory,
		notifyDays:      cfg.NotificationDaysBefore,
		dashboardURL:    cfg.DashboardURL,
		oidcSigner:      cfg.OIDCSigner,
		oidcIssuerURL:   cfg.OIDCIssuerURL,
		maxDueRowAge:    cfg.MaxDueRowAge,
	}
}

// finalizeExecution sets the status and completion time on an execution based on the error.
//
// Both partial sentinels (single-account *partialPurchaseError and
// multi-account *multiAccountPartialError) map the root row to
// "partially_completed", never "failed": real commitments exist and a re-approve
// would double-buy them (issues #642 / #1014). errAllAccountsFailed falls
// through to the "failed" default — nothing committed, so a Retry is safe.
func (m *Manager) finalizeExecution(exec *config.PurchaseExecution, execErr error) {
	var partial *partialPurchaseError
	var multiPartial *multiAccountPartialError
	switch {
	case execErr == nil:
		completedAt := time.Now()
		exec.Status = "completed"
		exec.CompletedAt = &completedAt
	case errors.As(execErr, &partial), errors.As(execErr, &multiPartial):
		// At least one rec / account committed a real purchase while others
		// failed. Never mark such a row "failed" — the commitments are real
		// and a re-approve would double-buy them. Record the partial outcome
		// and stamp CompletedAt so the row reads as terminal (the successful
		// recs are done), with the per-rec failures preserved in Error.
		// Append rather than overwrite so any audit-gap note already stamped
		// by aggregatePurchaseOutcomes (a successful rec whose history write
		// failed, issue #621) is not lost.
		completedAt := time.Now()
		exec.Status = "partially_completed"
		exec.Error = appendErrNote(exec.Error, execErr.Error())
		exec.CompletedAt = &completedAt
	default:
		exec.Status = "failed"
		exec.Error = execErr.Error()
	}
}

// claimAndExecuteWithin is the single atomic claim-then-execute funnel for the
// cron ProcessScheduledPurchases sweep, mirroring the guard the synchronous
// approve path already gets from ApproveAndExecute's CAS (issue #1013).
//
// It atomically transitions the row from an executable state
// (pending/notified) to "running" via TransitionExecutionStatus and
// only proceeds when it wins that CAS. A lost CAS (row already claimed by a
// concurrent worker, an overlapping cron tick, or the row
// vanishing mid-flight) is benign: the function returns (claimed=false, nil) and
// the caller should skip without re-running the purchase. A real DB error
// during the claim returns (false, err).
//
// The claim is pinned to the recommendations snapshot the caller gated on: a row
// that was empty when gated must still be empty, and a row with recommendations
// must still have them. A recommendation attach landing between the gate and the
// claim therefore loses the pin, which is a benign skip (the next tick gates the
// row again), instead of running recommendations nobody checked against the cap
// or the notified rule.
//
// An approved row is not claimable here: stranded approvals go through the re-drive
// (claimAndRedrive), and the approve paths claim the row themselves (issue #1814).
//
// For a row with recommendations the step cap is re-checked against the claimed row and the
// current plan; a refusal
// fails the row with the beforeSubmission class, so its suppressions release.
//
// When maxAge is positive, a claimed row that was due more than maxAge ago is
// refused before submission (ErrDueRowTooOld) instead of bought (issue #712).
// The check runs on the claimed row, so a concurrent sweep that lost the claim
// never reaches it and the row is failed and reported exactly once. Only the
// process sweep passes a cap.
//
// On a won claim it runs executeAndFinalize and returns (true, execErr).
func (m *Manager) claimAndExecuteWithin(ctx context.Context, exec *config.PurchaseExecution, maxAge time.Duration) (claimed bool, err error) {
	gateSawEmpty := len(exec.Recommendations) == 0
	// System-initiated: cron executor passes nil so transitioned_by = NULL.
	updated, claimErr := m.config.TransitionExecutionStatusPinned(ctx, exec.ExecutionID, []string{"pending", "notified"}, "running", nil, gateSawEmpty)
	if claimErr != nil {
		if errors.Is(claimErr, config.ErrNotFound) || errors.Is(claimErr, config.ErrExecutionNotInExpectedStatus) {
			// Benign CAS race-loss: an overlapping sweep or approve already owns
			// this row (or it was deleted), or its recommendations changed since
			// the gate. Skip without executing.
			logging.Warnf("Skipping execution %s (CAS claim lost to concurrent worker or changed recommendations): %v", exec.ExecutionID, claimErr)
			return false, nil
		}
		return false, fmt.Errorf("failed to claim execution %s for execution: %w", exec.ExecutionID, claimErr)
	}
	// Carry the committed DB state (status=running, plus any fields refreshed by
	// the RETURNING clause) onto the caller's struct so the rest of the run
	// starts from the claimed row rather than the pre-claim snapshot.
	*exec = *updated
	if refusal := m.claimedRowRefusal(ctx, exec, maxAge); refusal != nil {
		return true, m.failClaimedBeforeSubmission(ctx, exec, refusal)
	}
	return true, m.executeAndFinalize(ctx, exec)
}

// claimedRowRefusal runs claimAndExecuteWithin's checks on the row its claim
// returned: the due-row age cap when maxAge is positive, then the step cap for
// a row with recommendations. Every claimed row came from pending or notified, the states the
// AutoPurchase gate applies to.
func (m *Manager) claimedRowRefusal(ctx context.Context, exec *config.PurchaseExecution, maxAge time.Duration) error {
	if maxAge > 0 {
		if refusal := dueRowAgeRefusal(exec, maxAge, time.Now()); refusal != nil {
			return refusal
		}
	}
	if len(exec.Recommendations) > 0 {
		return m.recheckStepCap(ctx, exec)
	}
	return nil
}

// isMultiAccountAckable reports whether a multi-account execErr should be
// treated as handled (not retried or counted as a flat failure) because at least one account
// committed a real purchase (issue #1014). A nil error (full success) is also
// ackable. errAllAccountsFailed is NOT ackable — nothing committed, so a
// retry is safe and useful.
func isMultiAccountAckable(execErr error) bool {
	if execErr == nil {
		return true
	}
	var partial *multiAccountPartialError
	return errors.As(execErr, &partial)
}

const (
	// The budgets are anchored on the execution deadline E (3m30s), not on
	// when the previous phase ended: account rows save by E+T/2, the root and
	// the skip rows by E+T, and one batched email for the skipped and late
	// accounts goes out by E+T+N, so the worst case is 3m30s + 30s + 30s =
	// 270s however many accounts are in flight at E (#1672). A late account
	// sends no email of its own (executeForAccountLate). On AWS Lambda the function timeout
	// ends the invocation whatever these deadlines say, so the Lambda timeout
	// must cover that sum plus a 30s margin (lambda_timeout >= 300, enforced by
	// Terraform validation and TestLambdaTimeoutCoversPurchaseBudget, #913,
	// #1434).
	purchaseExecutionTimeout = 3*time.Minute + 30*time.Second
	terminalSaveTimeout      = 30 * time.Second
	// purchaseRunBudget is the longest one purchase can run end to end; the
	// fire sweep starts a row only while this much time remains (#1582).
	purchaseRunBudget = purchaseExecutionTimeout + terminalSaveTimeout + failureNotifyTimeout
	// perRecPurchaseTimeout caps one recommendation's provider calls; see
	// executeSinglePurchase.
	perRecPurchaseTimeout = 30 * time.Second
)

// purchaseBudgets overrides deadlines for tests. A zero field means the
// production default, so a Manager built by NewManager uses the constants.
type purchaseBudgets struct {
	execTimeout  time.Duration
	terminalSave time.Duration
	perRec       time.Duration
}

func (b purchaseBudgets) executionTimeout() time.Duration {
	if b.execTimeout > 0 {
		return b.execTimeout
	}
	return purchaseExecutionTimeout
}

func (b purchaseBudgets) terminalSaveTimeout() time.Duration {
	if b.terminalSave > 0 {
		return b.terminalSave
	}
	return terminalSaveTimeout
}

func (b purchaseBudgets) perRecTimeout() time.Duration {
	if b.perRec > 0 {
		return b.perRec
	}
	return perRecPurchaseTimeout
}

// executeAndFinalize runs a purchase and handles status updates, record saving, and progress.
//
// The root execution row is ALWAYS saved with its finalized status — including
// the multi-account fan-out case (issue #1014 / H2). The per-account fan-out
// rows are distinct rows that executeForAccount already saved with their own
// authoritative status; the root row reflects the AGGREGATE outcome
// (completed / partially_completed / failed, classified by finalizeExecution
// from the typed sentinel). The previous `!wasMultiAccount` save-skip left the
// root in whatever pre-execution status it carried, which, now that
// claimAndExecuteWithin claims the root to "running" first (issue #1013), would strand
// the root row in "running" until the reaper failed it.
func (m *Manager) executeAndFinalize(ctx context.Context, exec *config.PurchaseExecution) error {
	// A disconnected caller must not cancel a purchase after the claim.
	// Its terminal outcome must survive an exhausted execution budget too.
	execCtx, cancelExec := ctxutil.DetachedWithTimeout(ctx, m.budgets.executionTimeout())
	defer cancelExec()
	execDeadline, _ := execCtx.Deadline() // always set: DetachedWithTimeout
	// Last line of defense before money moves (issue #1718). Every executor
	// entry point funnels through here, so one check covers all of them.
	execErr := m.preExecutionRefusal(execCtx, exec)
	if execErr == nil {
		execErr = m.stampRedshiftDispatch(execCtx, exec)
	}
	if execErr == nil {
		execErr = m.executePurchase(execCtx, exec)
	}
	m.finalizeExecution(exec, execErr)
	if execErr != nil {
		logging.Errorf("Failed to execute purchase %s: %v", exec.ExecutionID, execErr)
	}
	// Anchored on the execution deadline, not "now": the late accounts' row
	// saves end at execDeadline+T/2 (saveAccountRow), so the root always keeps
	// the rest of its slice and the whole run ends by execDeadline+T+N (#1672).
	saveCtx, cancelSave := ctxutil.DetachedWithDeadline(ctx, execDeadline.Add(m.budgets.terminalSaveTimeout()))
	defer cancelSave()
	// Operators hear about every failed or partial row (issue #714), even
	// when the terminal save below fails. A fan-out root logs the alarm
	// marker without an email (issue #1195; see notifyRootFailure).
	defer m.notifyRootFailure(saveCtx, exec, execErr)
	if err := m.saveTerminalAndSkipped(saveCtx, exec, execErr); err != nil {
		logging.Errorf("AUDIT LOSS: failed to save execution status: %v", err)
		// Wrap with ErrAuditLoss regardless of whether executePurchase itself
		// failed. When execErr != nil (provider/partial error), finalizeExecution
		// stamped a terminal status on the in-memory exec struct, but if
		// SavePurchaseExecution then failed the DB row is still in "running" --
		// exactly the stranded-row scenario ErrAuditLoss signals. Preserve the
		// original execErr as the innermost %w so errors.As/errors.Is can still
		// reach it from callers (e.g. claimAndRedrive checking ErrAuditLoss).
		if execErr != nil {
			execErr = fmt.Errorf("%w: terminal save failed (%w); original execution error: %w",
				config.ErrAuditLoss, err, execErr)
		} else {
			execErr = fmt.Errorf("%w: %w", config.ErrAuditLoss, err)
		}
	}
	if execErr == nil {
		if err := m.updatePlanProgress(saveCtx, exec); err != nil {
			m.recordRampAdvanceRefusal(saveCtx, exec, err)
		}
	}
	return execErr
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

// preExecutionRefusal runs every check that must pass before any provider call.
// All refusals except the armed re-drive and missing-journal ones are
// beforeSubmission errors, so the row ends failed with no_submission stamped.
func (m *Manager) preExecutionRefusal(ctx context.Context, exec *config.PurchaseExecution) error {
	if refusal := orphanedPlanStepRefusal(exec); refusal != nil {
		return beforeSubmission(refusal)
	}
	if err := m.armedRedriveRefusal(ctx, exec); err != nil {
		return err
	}
	if refusal := m.staleAzurePricingRefusal(ctx, exec); refusal != nil {
		return beforeSubmission(refusal)
	}
	if refusal := m.ladderExecutionRefusal(ctx, exec); refusal != nil {
		return beforeSubmission(refusal)
	}
	if refusal := duplicateSelectionRefusal(exec); refusal != nil {
		return beforeSubmission(refusal)
	}
	// A Redshift retry reuses the first attempt's request time; only the journal lets go's dedupe tell its node from another's.
	if hasRedshiftRec(exec) && exec.RetryAttemptN > 0 && m.attemptJournal == nil {
		return errors.New(redshiftNoJournalRefusal)
	}
	return nil
}

// duplicateSelectionRefusal refuses a selection two of whose recs would buy the same reservation.
func duplicateSelectionRefusal(exec *config.PurchaseExecution) error {
	switch {
	case hasDuplicateRedshiftTuple(exec):
		return errors.New(redshiftDuplicateTupleRefusal)
	case hasDuplicateElastiCachePool(exec):
		return errors.New(elastiCacheDuplicatePoolRefusal)
	}
	return nil
}

// beforeSubmissionError marks a failure that happened before any cloud
// purchase call was made for the execution: the code path itself proves no
// commitment can exist, independent of the error text or of provider
// sentinels (the go outcome-unknown sentinel is not pinned yet). It keeps the
// wrapped error's message unchanged.
type beforeSubmissionError struct{ err error }

func (e *beforeSubmissionError) Error() string { return e.err.Error() }
func (e *beforeSubmissionError) Unwrap() error { return e.err }

func beforeSubmission(err error) error { return &beforeSubmissionError{err: err} }

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

// processOneExecution runs the full gate+claim+execute pipeline for a single
// due pending/notified execution. It updates the Processed/Executed/Failed
// counters and Errors slice on the supplied ProcessResult in place.
// Extracted from ProcessScheduledPurchases to keep that function under the
// gocyclo:10 threshold.
func (m *Manager) processOneExecution(ctx context.Context, exec config.PurchaseExecution, result *ProcessResult) {
	// AutoPurchase gate: pending/notified rows are only eligible for
	// automatic execution when the owning plan has AutoPurchase=true AND
	// the row was not web-submitted (those must go through the token-link
	// path). Fail closed: a plan-fetch error counts as a failure rather
	// than defaulting to "execute" (no silent money action on error).
	eligible, gateErr := m.executableByScheduler(ctx, &exec)
	if gateErr != nil {
		result.Failed++
		result.Errors = append(result.Errors, fmt.Sprintf("%s: AutoPurchase gate check failed: %v", exec.ExecutionID, gateErr))
		return
	}
	if !eligible {
		logging.Infof("Skipping execution %s (AutoPurchase=false, kill switch off, source=web, 4-eyes on so it needs a human approval in the approval queue, or recommendation-bearing row not yet notified; requires explicit approval)", exec.ExecutionID)
		return
	}

	result.Processed++
	logging.Infof("Executing scheduled purchase: %s", exec.ExecutionID)

	// Atomically claim the row before executing (issue #1013). Overlapping
	// cron ticks (a tick that runs longer than the interval, EventBridge
	// duplicate/overlapping deliveries, or cron racing a manual run) would
	// otherwise both execute the same due row. claimAndExecuteWithin CASes the row
	// to "running" and only the winner runs; a lost claim is skipped without
	// re-executing.
	claimed, execErr := m.claimAndExecuteWithin(ctx, &exec, m.dueRowMaxAge())
	if !claimed {
		// execErr != nil here is a real DB error during the claim (count as
		// failed); execErr == nil is a benign CAS race-loss (skip silently).
		if execErr != nil {
			result.Failed++
			result.Errors = append(result.Errors, fmt.Sprintf("%s: claim failed: %v", exec.ExecutionID, execErr))
		}
		return
	}

	// A multi-account run where at least one account committed is a success
	// for ack purposes (issue #1014): the per-account rows own the truth and
	// re-running would double-buy. Only a genuine failure (nothing
	// committed) is counted/surfaced.
	if isMultiAccountAckable(execErr) {
		result.Executed++
		return
	}
	if errors.Is(execErr, ErrDueRowTooOld) {
		result.Stale++
	}
	result.Failed++
	result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", exec.ExecutionID, execErr))
}

// ProcessScheduledPurchases checks for and executes scheduled purchases.
func (m *Manager) ProcessScheduledPurchases(ctx context.Context) (*ProcessResult, error) {
	logging.Info("Processing scheduled purchases...")

	// Recover any executions stranded in "approved" by an interrupted
	// synchronous run before processing fresh pending work (issue #632).
	recovered, err := m.RecoverStrandedApprovals(ctx)
	if err != nil {
		// A recovery failure must not block scheduled purchases — log and continue
		// with the pending-execution pass; the next tick retries the sweep.
		logging.Errorf("Failed to recover stranded approved executions: %v", err)
	}

	// Get all pending executions
	executions, err := m.config.GetPendingExecutions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get pending executions: %w", err)
	}

	now := time.Now()
	result := &ProcessResult{Recovered: recovered}

	for i := range executions {
		exec := executions[i]
		// Check if it's time to execute
		if exec.ScheduledDate.After(now) {
			logging.Debugf("Execution %s not yet due (scheduled for %s)", exec.ExecutionID, exec.ScheduledDate)
			continue
		}

		// Positive allowlist (issue #1013 / M2): only the two executable
		// pre-purchase states proceed. The query already filters to
		// pending/notified, but a row another worker transitioned in the gap
		// between SELECT and here (approved/running/failed/...) must be skipped.
		// The atomic claim below is the real guard; this is defense-in-depth.
		if exec.Status != "pending" && exec.Status != "notified" {
			continue
		}

		m.processOneExecution(ctx, exec, result)
	}

	return result, nil
}

// enoughTimeFor reports whether ctx has at least budget left. A context
// without a deadline always has enough.
func enoughTimeFor(ctx context.Context, budget time.Duration) bool {
	deadline, ok := ctx.Deadline()
	return !ok || time.Until(deadline) >= budget
}
