package purchase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/ctxutil"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/execution"
	"github.com/google/uuid"
)

// ErrPlanStepNoRecommendations marks a plan-step execution that carries no
// recommendations. Nothing attaches recommendations to a plan step yet
// (platform#609), so such a step can only "succeed" by buying nothing, which
// would complete it and advance the ramp. It is refused instead. The text is
// user-facing, so it names no internal issue (tracked in platform#631); rows
// stored before that carry a "(platform#631: ...)" suffix, which errnotes.go
// still recognizes.
var ErrPlanStepNoRecommendations = errors.New("plan step has no recommendations attached, so it cannot buy anything")

// executePurchase performs the actual purchase.
// When the plan has associated cloud accounts and a credential store is configured,
// it fans out execution in parallel — one goroutine per account, each with its own
// PurchaseExecution record tagged with cloud_account_id.
// If no accounts are configured or no credential store is available, it takes the
// single-account path, which resolves credentials from the recommendations' own
// cloud account and refuses a batch spanning more than one (see #1902). Only a
// batch whose selected recommendations carry no account at all falls back to
// ambient credentials.
// executePurchase runs the purchase for a single execution. When the plan has
// associated cloud accounts it fans out via executeMultiAccount (which saves its
// own per-account records); otherwise it runs the single-account path. The root
// row is always finalized+saved by the caller regardless of which path ran.
func (m *Manager) executePurchase(ctx context.Context, exec *config.PurchaseExecution) (err error) {
	logging.Infof("Executing purchase for plan %q, step %d", exec.PlanID, exec.StepNumber)

	// Direct-execute purchases (Opportunities "Purchase" button) arrive
	// with no associated plan. PlanID is empty and the Postgres UUID
	// column rejects "" with SQLSTATE 22P02, so skip the plan/accounts
	// fetch entirely and synthesize a placeholder plan whose Name is the
	// only field downstream history/notification code reads. Direct-execute
	// purchases target exactly one account: resolveSingleAccountProvider
	// refuses a batch whose selected recs span accounts (#1902), so fall
	// straight through to the single-account path.
	var plan *config.PurchasePlan
	if exec.PlanID == "" {
		plan = &config.PurchasePlan{Name: "Direct purchase"}
	} else {
		plan, err = m.config.GetPurchasePlan(ctx, exec.PlanID)
		if err != nil {
			return beforeSubmission(fmt.Errorf("failed to get plan: %w", err))
		}
		if plan == nil {
			return beforeSubmission(fmt.Errorf("plan not found: %s", exec.PlanID))
		}

		// Checked once here, before the per-account fan-out: past this point
		// every account would write its own failed row and strand its own ramp
		// unit, and credential resolution would mask the real cause.
		if len(exec.Recommendations) == 0 {
			return beforeSubmission(fmt.Errorf("execution %s: %w", exec.ExecutionID, ErrPlanStepNoRecommendations))
		}

		// Fan out across plan accounts when accounts are configured, unless
		// this lineage's first attempt pinned the other shape (issue #97).
		if exec.CloudAccountID == nil {
			accounts, err := m.config.GetPlanAccounts(ctx, exec.PlanID)
			if err != nil {
				return fmt.Errorf("failed to load plan accounts for plan %s: %w", exec.PlanID, err)
			}
			fanOut, err := m.lineageFansOut(ctx, exec, accounts)
			if err != nil {
				return err
			}
			if fanOut {
				return m.executeScopeAware(ctx, exec, plan, accounts)
			}
		}
	}

	// Single-account (legacy) path.
	return m.executeSingleAccount(ctx, exec, plan)
}

// executeSingleAccount runs the legacy single-account purchase path: resolve
// per-account credentials + the target account id, execute the selected recs,
// then notify and classify the outcome. Returns nil on full success, a
// *partialPurchaseError when some recs committed and others failed (#642), or a
// plain error when nothing committed. Split out of executePurchase to keep that
// function under the gocyclo budget.
func (m *Manager) executeSingleAccount(ctx context.Context, exec *config.PurchaseExecution, plan *config.PurchasePlan) error {
	provCfg, target, err := m.prepareSingleAccountSubmission(ctx, exec)
	if err != nil {
		return err
	}
	// #646: stamp the resolved target account on history, not the ambient
	// AWS host account. resolveSingleAccountProvider returns the target's
	// ExternalID (the provider-appropriate account identifier for AWS /
	// Azure / GCP). Only fall back to the ambient AWS STS identity when no
	// target account could be identified (truly-ambient AWS single-account
	// execution where credentials are inherited from the host).
	accountID := target.externalID
	if accountID == "" {
		// Fall back to the ambient AWS STS identity for logging/tagging.
		// getAWSAccountID returns ("", error) on failure so the caller
		// can substitute a sentinel rather than baking "unknown" into the
		// purchase history record (05-L2).
		if id, err := m.getAWSAccountID(ctx); err != nil {
			logging.Warnf("purchase[%s]: could not resolve ambient AWS account ID: %v; proceeding with empty account ID",
				exec.ExecutionID, err)
		} else {
			accountID = id
		}
	}

	totalSavings, totalUpfront, purchaseErrors, allRefused, procErr := m.processPurchaseRecommendations(ctx, exec, plan, accountID, provCfg, target.awsPartition)
	if procErr != nil {
		return procErr
	}

	if len(purchaseErrors) > 0 && !anyRecPurchased(exec.Recommendations) {
		// Nothing committed — a clean total failure.
		return failedPurchasesError(purchaseErrors, allRefused)
	}

	// Confirm only when at least one rec committed (full or partial success).
	// buildPurchaseConfirmationData only lists Purchased recs, so a partial run
	// notifies on exactly those, and a run with nothing selected (it never
	// reached a cloud API) sends no "purchase confirmed" email for $0 (#339).
	if anyRecPurchased(exec.Recommendations) {
		if notifyErr := m.sendPurchaseNotification(ctx, exec, plan, totalSavings, totalUpfront); notifyErr != nil {
			logging.Errorf("Failed to send confirmation: %v", notifyErr)
		}
	}

	if len(purchaseErrors) > 0 {
		// #642: partial success. Real commitments were written to
		// purchase_history with Purchased=true; never mark the row "failed"
		// (it would invite a re-approve that double-buys the purchased recs).
		// Return a sentinel so finalizeExecution records "partially_completed".
		return &partialPurchaseError{errors: purchaseErrors}
	}
	return nil
}

// prepareSingleAccountSubmission runs the single-account path's pre-submission
// steps: check the recs match the row's account, resolve the provider and
// target account, then pin the lineage's shape now that the attempt can submit
// (issue #97; see fan_out_shape.go). Every error it returns is before
// submission. Split out of executeSingleAccount to keep that function under
// the gocyclo budget.
func (m *Manager) prepareSingleAccountSubmission(ctx context.Context, exec *config.PurchaseExecution) (*provider.Config, purchaseTarget, error) {
	if err := requireRecsMatchRowAccount(exec); err != nil {
		return nil, purchaseTarget{}, beforeSubmission(err)
	}
	provCfg, target, err := m.resolveSingleAccountProvider(ctx, exec)
	if err != nil {
		return nil, purchaseTarget{}, beforeSubmission(err)
	}
	if err := m.pinLineageShape(ctx, exec, config.FanOutShapeSingle); err != nil {
		return nil, purchaseTarget{}, err // already beforeSubmission
	}
	return provCfg, target, nil
}

// purchaseTarget identifies the account a single-account execution buys for.
// The zero value means no target account was identified (ambient credentials).
type purchaseTarget struct {
	// externalID is the provider-appropriate account identifier stamped on
	// purchase_history (#646).
	externalID string
	// awsPartition is the account's AWS partition from its role ARN, used to
	// refuse aws-cn Savings Plans before any AWS call (issue #1194).
	awsPartition string
}

// anyRecPurchased reports whether at least one recommendation in the slice
// was marked Purchased=true by the aggregator (a real commitment was created
// on the cloud provider). It is the gate that distinguishes a partial success
// (#642 — some recs committed, some failed) from a total failure (nothing
// committed, e.g. credential resolution failed before any rec ran).
func anyRecPurchased(recs []config.RecommendationRecord) bool {
	for i := range recs {
		if recs[i].Purchased {
			return true
		}
	}
	return false
}

// multiAccountPartialError is the sentinel returned by executeMultiAccount when
// at least one account committed a real purchase while one or more others
// failed (issue #1014). It is the multi-account analog of partialPurchaseError:
// the executor entry points must NOT treat this as a flat failure — the
// per-account rows already own their authoritative status (partially_completed /
// completed / failed) and real commitments exist, so the cron sweep must count it as
// executed (not retry it) to avoid re-running the fan-out and double-buying
// the accounts that already succeeded (which #1012's stable key would otherwise
// dedupe, but the contract should not depend on that second line of defense).
type multiAccountPartialError struct {
	errors []string
	// skipped are the failed rows for accounts that never started. They are
	// saved and announced by executeAndFinalize, inside the root's terminal
	// windows, not here (issue #1672).
	skipped   []*config.PurchaseExecution
	committed int
}

func (e *multiAccountPartialError) Error() string {
	return fmt.Sprintf("multi-account execution: %d account(s) committed, %d failed: %s",
		e.committed, len(e.errors), strings.Join(e.errors, "; "))
}

// errAllAccountsFailed is returned by executeMultiAccount when no account
// committed any purchase — a flat failure that callers may retry.
var errAllAccountsFailed = errors.New("multi-account execution: all accounts failed")

// executeMultiAccount fans out executePurchase across all plan accounts in parallel.
// Each account gets its own PurchaseExecution record tagged with cloud_account_id.
//
// Returns nil when every account fully succeeded; a *multiAccountPartialError
// when at least one account committed a purchase and at least one failed (so
// callers treat it as handled rather than retry, issue #1014); and errAllAccountsFailed
// (wrapping the per-account errors) when no account committed anything.
func (m *Manager) executeMultiAccount(ctx context.Context, baseExec *config.PurchaseExecution, plan *config.PurchasePlan, accounts []config.CloudAccount) error {
	// Checked before any account runs: every account would otherwise buy the
	// whole step, and a step with nothing to buy would return nil below and
	// be recorded as completed, advancing the ramp (platform#631).
	scoped, scopeErr := scopeExecutionsByAccount(baseExec, accounts)
	if scopeErr != nil {
		return beforeSubmission(scopeErr)
	}
	buying := make([]config.CloudAccount, 0, len(scoped))
	for i := range accounts {
		if _, ok := scoped[accounts[i].ID]; ok {
			buying = append(buying, accounts[i])
		}
	}
	var started sync.Map
	late := &lateRows{}
	results := execution.RunForAccountsWithConcurrency(ctx, buying, func(ctx context.Context, account config.CloudAccount) (bool, error) {
		started.Store(account.ID, true)
		return m.executeForAccountLate(ctx, scoped[account.ID], plan, account, late)
	}, getMaxAccountParallelism())
	return withLateRows(summarizeFanOut(scoped, results, &started), late.take())
}

// summarizeFanOut turns the per-account results into the fan-out's error (see
// executeMultiAccount for the contract). Split out to keep both under the
// gocyclo budget.
func summarizeFanOut(scoped map[string]*config.PurchaseExecution, results []execution.Result[bool], started *sync.Map) error {
	committed := 0
	var errs []string
	for _, r := range results {
		if r.Value {
			// At least one rec committed for this account (full or partial).
			committed++
		}
		if r.Err != nil {
			errs = append(errs, fmt.Sprintf("account %s: %v", r.AccountID, r.Err))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	if committed > 0 {
		// Some accounts committed real purchases; the per-account rows already
		// recorded the truth. Surface a partial sentinel so the entry points
		// ack and never mislabel the run as a flat failure (issue #1014).
		return &multiAccountPartialError{committed: committed, errors: errs, skipped: skippedAccountRows(scoped, results, started)}
	}
	err := fmt.Errorf("%w: %s", errAllAccountsFailed, strings.Join(errs, "; "))
	if fanOutNotSubmitted(results, started) {
		// Nothing was submitted by any account (#1844), so the root carries the
		// proof saveTerminalExecution reads; the %w chain is unchanged.
		return beforeSubmission(err)
	}
	return err
}

// saveExecutionStatusBestEffort saves the execution record and logs any error.
// Used in error paths where we need to persist the failure status but cannot
// propagate the save error (the original error is already being returned).
func (m *Manager) saveExecutionStatusBestEffort(ctx context.Context, exec *config.PurchaseExecution) {
	if err := m.saveAccountRow(ctx, exec); err != nil {
		logging.Warnf("execution: failed to persist error status for account %v: %v", exec.CloudAccountID, err)
	}
}

// saveAccountRow saves a started account's terminal row on a context detached
// from the run's cancellation, so a run context that ended mid-account still
// leaves the History row and its Retry (issue #1672). The deadline is half the
// terminal-save budget past the run's own deadline (execDeadline+T/2), so
// every account in flight when the run ends shares one slice and the root keeps
// the other half. A row that misses it is logged as AUDIT LOSS.
func (m *Manager) saveAccountRow(ctx context.Context, exec *config.PurchaseExecution) error {
	deadline := time.Now().Add(m.budgets.terminalSaveTimeout())
	if runDeadline, ok := ctx.Deadline(); ok {
		// ctx is the run context. Without a deadline (tests, callers outside
		// executeAndFinalize) the fallback is a fresh full budget.
		deadline = runDeadline.Add(m.budgets.terminalSaveTimeout() / 2)
	}
	saveCtx, cancel := ctxutil.DetachedWithDeadline(ctx, deadline)
	defer cancel()
	err := m.config.SavePurchaseExecution(saveCtx, exec)
	if err != nil {
		logging.Errorf("AUDIT LOSS: failed to save the record of execution %s for account %s: %v", exec.ExecutionID, accountIDOf(exec), err)
	}
	return err
}

// accountIDOf is the row's cloud account ID for log lines ("" for a root row).
func accountIDOf(row *config.PurchaseExecution) string {
	if row.CloudAccountID == nil {
		return ""
	}
	return *row.CloudAccountID
}

// executeForAccount runs a single plan execution for one cloud account.
// It creates a new PurchaseExecution record tagged with cloud_account_id, resolves
// per-account credentials, executes purchases, and saves the result.
//
// Returns committed=true when at least one rec committed a real purchase for
// this account (full or partial success), so executeMultiAccount can tell a
// partial run (some accounts bought) apart from a flat failure and pick the
// right sentinel (issue #1014). The returned error is non-nil whenever any rec
// failed, but the authoritative per-account row has already been saved with the
// correct status (partially_completed / failed) before it surfaces.
func (m *Manager) executeForAccount(ctx context.Context, baseExec *config.PurchaseExecution, plan *config.PurchasePlan, account config.CloudAccount) (committed bool, err error) {
	return m.executeForAccountLate(ctx, baseExec, plan, account, nil)
}

// executeForAccountLate is executeForAccount for the fan-out: when the account
// finishes after ctx ended it hands its failed row to late instead of sending
// its own email, and executeAndFinalize sends one batched email after the root
// save (issue #1672). A nil late keeps the per-account email.
func (m *Manager) executeForAccountLate(ctx context.Context, baseExec *config.PurchaseExecution, plan *config.PurchasePlan, account config.CloudAccount, late *lateRows) (committed bool, err error) {
	// Create a per-account copy of the execution record with an independent
	// Recommendations slice so concurrent goroutines don't race on writes.
	acctExec := newAccountExecution(baseExec, account.ID)
	// Runs on every return path below, once the row's terminal status is
	// stamped; a no-op unless it ended failed or partially_completed (#714).
	defer m.notifyAccountOutcome(ctx, &acctExec, late)

	provCfg, err := m.resolveAccountProvider(ctx, account)
	if err != nil {
		acctExec.Status = "failed"
		acctExec.Error = err.Error()
		credErr := beforeSubmission(fmt.Errorf("credential resolution failed for account %s: %w", account.ID, err))
		// Audit-trail save: the failed row is as much a part of the audit
		// trail as a success row, so a save failure must surface (AUDIT LOSS)
		// instead of being silently dropped. Extracted to a helper to keep
		// executeForAccount under the gocyclo:10 budget.
		return false, m.persistFailedAccountExecution(ctx, &acctExec, account, credErr)
	}
	// Pinned on the ROOT lineage, and only once this account can submit (issue
	// #97); see fan_out_shape.go.
	if pinErr := m.pinLineageShape(ctx, baseExec, config.FanOutShapeMulti); pinErr != nil {
		acctExec.Status = "failed"
		acctExec.Error = pinErr.Error()
		return false, m.persistFailedAccountExecution(ctx, &acctExec, account, pinErr)
	}

	accountID := account.ExternalID
	totalSavings, totalUpfront, purchaseErrors, allRefused, procErr := m.processPurchaseRecommendations(ctx, &acctExec, plan, accountID, provCfg, account.AWSPartition())
	if procErr != nil {
		acctExec.Status = "failed"
		acctExec.Error = procErr.Error()
		m.saveExecutionStatusBestEffort(ctx, &acctExec)
		return false, procErr
	}

	// Resolve the final status/error/completion stamp from the per-rec outcome
	// (#642), and learn whether any rec committed (#1014).
	_, committed = applyAccountOutcome(&acctExec, purchaseErrors)
	markRefusedAccountRow(&acctExec, allRefused)

	if saveErr := m.saveAccountRow(ctx, &acctExec); saveErr != nil {
		return committed, fmt.Errorf("AUDIT LOSS: failed to save execution record for account %s: %w", account.ID, saveErr)
	}

	// Send the confirmation whenever at least one rec committed (full or
	// partial success). buildPurchaseConfirmationData only lists Purchased
	// recs, so a partial run notifies on exactly the recs that fired. Gating on
	// committed rather than "no errors" keeps an account with nothing selected
	// from getting a "purchase confirmed" email for $0 (#339).
	if committed {
		if err := m.sendPurchaseNotification(ctx, &acctExec, plan, totalSavings, totalUpfront); err != nil {
			logging.Errorf("Failed to send confirmation for account %s: %v", account.ID, err)
		}
	}

	// Surface the per-rec failures to the multi-account aggregator so the
	// overall run reflects that not everything succeeded, even though the
	// authoritative per-account row was already saved as partially_completed
	// (not failed) and the confirmation already went out.
	if len(purchaseErrors) > 0 {
		return committed, failedPurchasesError(purchaseErrors, allRefused)
	}
	return committed, nil
}

// notifyAccountOutcome is executeForAccountLate's failure alert: the account's
// own email, unless the run context already ended, in which case a failed row
// goes to late for the batched email.
func (m *Manager) notifyAccountOutcome(ctx context.Context, row *config.PurchaseExecution, late *lateRows) {
	if ctx.Err() != nil && late.add(row) {
		return
	}
	m.notifyFanOutFailure(ctx, row)
}

// newAccountExecution is the per-account copy of baseExec, with an independent
// Recommendations slice so concurrent goroutines don't race on writes.
//
// ExecutionID is the per-account row identity (still a fresh UUID), but the
// per-rec provider idempotency token must NOT derive from it: a re-drive of
// the root execution would mint new UUIDs and lose the provider dedupe,
// double-buying every account (issue #1012 / H1). Seed a STABLE per-account
// idempotency key from the root's lineage key + the account ID so a re-drive
// reproduces the identical token per account. The account ID is the cloud
// account's stable config ID, not the (also stable) ExternalID — either is
// durable, but account.ID is the value that uniquely keys the fan-out unit.
func newAccountExecution(baseExec *config.PurchaseExecution, accountID string) config.PurchaseExecution {
	acctExec := *baseExec
	acctExec.ExecutionID = uuid.New().String()
	acctExec.IdempotencyKey = idempotencyLineageKey(baseExec) + ":" + accountID
	acctExec.CloudAccountID = &accountID
	recs := make([]config.RecommendationRecord, len(baseExec.Recommendations))
	copy(recs, baseExec.Recommendations)
	acctExec.Recommendations = recs
	return acctExec
}
