package purchase

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/ctxutil"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/execution"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
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

// recordHistoryAuditGap stamps exec.Error with a note that a successful
// purchase's history record could not be saved (issue #621). The execution
// keeps a successful status; the marker is what makes the row visible in the
// History view (which synthesizes completed executions that carry an Error).
// Appends rather than overwrites so multiple failed history writes within one
// execution are all recorded.
// historyAuditGapPrefix is the structured prefix stamped on exec.Error by
// recordHistoryAuditGap. Using a fixed prefix makes these entries queryable
// in CloudWatch Insights / log aggregators without grepping free-form text
// (05-L1). The prefix is intentionally stable; changing it invalidates any
// existing dashboards or alerts that pattern-match on it.
const historyAuditGapPrefix = "history_write_failed"

func recordHistoryAuditGap(exec *config.PurchaseExecution, commitmentID string, histErr error) {
	// histErr is a raw persistence error: log it server-side for diagnosis but
	// keep it out of exec.Error, which is surfaced to clients via the history
	// status_description. Only a generic, user-safe note reaches the UI.
	logging.Errorf("history audit gap for commitment %s: %v", commitmentID, histErr)
	// Structured marker: historyAuditGapPrefix + ":" + commitmentID so log
	// queries can filter on "history_write_failed" and operators can correlate
	// the commitment ID to the cloud provider's record without parsing prose.
	note := fmt.Sprintf("%s: commitment %s purchased but its history record failed to save",
		historyAuditGapPrefix, commitmentID)
	exec.Error = appendErrNote(exec.Error, note)
}

// idempotencyLineageKey returns the stable anchor the per-rec provider
// idempotency token is derived from (issue #1012). It prefers the durable
// IdempotencyKey column, which is generated once at first creation and copied
// verbatim onto every Retry successor and combined with the account ID for each
// multi-account fan-out row — so a strand-and-re-drive reproduces the same
// token and the provider dedupes the purchase. It falls back to ExecutionID
// only for legacy rows persisted before migration 000066 (IdempotencyKey == "");
// for a single un-retried execution that fallback is identical to the pre-fix
// behavior, and such legacy rows never gain a retry successor that could
// diverge (the retry handler seeds the successor's key from the predecessor's
// ExecutionID in that case, preserving the match).
func idempotencyLineageKey(exec *config.PurchaseExecution) string {
	if exec.IdempotencyKey != "" {
		return exec.IdempotencyKey
	}
	return exec.ExecutionID
}

// reattachAccountScope is the second #1537 gate: it decides whether a plan
// execution that carries no cloud_account_id is genuinely a root row (safe to
// fan out) or a per-account row that lost its scope in transit, and repairs the
// latter in place.
//
// How per-account-ness is detected. executeForAccount is the ONLY writer that
// puts a colon in a lineage key ("<root-key>:<accountID>", execution.go). Every
// other producer yields a colon-free value: SavePurchaseExecution mints a bare
// uuid.New().String() at first insert, persistRetryExecution copies the
// predecessor's key verbatim or falls back to its ExecutionID (also a UUID),
// and the read path just round-trips the column. (The unrelated
// api.purchaseIdempotencyKey submit-fingerprint is sha256 hex and is never
// written to this field.) So on a scopeless row a colon is by itself proof the
// row is per-account -- which is why the colon, not a match against the plan's
// current account set, is the detector. Matching the current account set would
// fail OPEN exactly when the scoped account has since been removed from the
// plan: no match, no refusal, and the row re-fans-out and double-buys.
//
// What happens then:
//
//   - Colon-free key: a real root row. Returns nil leaving CloudAccountID nil,
//     and the caller fans out as before.
//   - Terminal segment matches a current plan account: repair rather than
//     refuse. The lineage key is durable proof of the intended scope, so
//     stamping CloudAccountID makes this the single-account re-drive it was
//     always meant to be, reproducing the identical per-rec provider token.
//     That is strictly safer than erroring -- same purchase count, correct
//     dedupe -- and it is the only exit that exists: a refused row lands
//     "failed", and retrying it produces another scopeless successor carrying
//     the same key (persistRetryExecution copies both verbatim), so it would
//     be refused forever, while its predecessor is already 409 "was already
//     retried". The repair also persists: executeAndFinalize saves exec, and
//     cloud_account_id is in SavePurchaseExecution's ON CONFLICT SET.
//   - Colon present but no plan account matches: the scoped account has left
//     the plan (removed, or deleted and re-added under a new ID). There is
//     nothing to re-attach to and fanning out would re-suffix the key and
//     re-buy every account that already committed, so refuse. This is the
//     genuinely unrecoverable case and the one that must not fail open.
func reattachAccountScope(exec *config.PurchaseExecution, accounts []config.CloudAccount) error {
	key := idempotencyLineageKey(exec)
	if !strings.Contains(key, ":") {
		return nil // root key: a genuine multi-account fan-out.
	}
	for i := range accounts {
		// An empty ID would degrade the test to HasSuffix(key, ":"), so require
		// a real account ID to match on.
		if accounts[i].ID == "" {
			continue
		}
		if strings.HasSuffix(key, ":"+accounts[i].ID) {
			scoped := accounts[i].ID
			exec.CloudAccountID = &scoped
			logging.Warnf("purchase[%s]: re-attached account scope %s (%s) from the idempotency lineage key; "+
				"the row reached the executor with no cloud_account_id (issue #1537)",
				exec.ExecutionID, accounts[i].ID, accounts[i].Name)
			return nil
		}
	}
	// The key contains a colon (checked above), so the terminal segment is the
	// account it was scoped to. Naming it is what makes the error actionable:
	// that is the account to re-add.
	orphanedAccount := key[strings.LastIndex(key, ":")+1:]
	return fmt.Errorf("refusing to fan out execution %s across %d plan accounts: "+
		"its idempotency lineage key is scoped to account %s, which is no longer on plan %s, "+
		"and the row carries no cloud_account_id, so the scope cannot be recovered; "+
		"re-deriving the key would double-purchase every account that already committed (issue #1537). "+
		"Re-add account %s to the plan to let the retry complete, or cancel this execution",
		exec.ExecutionID, len(accounts), orphanedAccount, exec.PlanID, orphanedAccount)
}

// executeScopeAware dispatches a plan execution that reached the fan-out
// decision with no cloud_account_id. Split out of executePurchase so the
// #1537 scope repair does not push that function over the gocyclo budget.
func (m *Manager) executeScopeAware(ctx context.Context, exec *config.PurchaseExecution, plan *config.PurchasePlan, accounts []config.CloudAccount) error {
	if err := reattachAccountScope(exec, accounts); err != nil {
		return err
	}
	if exec.CloudAccountID != nil {
		// Scope recovered from the lineage key: this is a single-account
		// re-drive, not a fan-out.
		return m.executeSingleAccount(ctx, exec, plan)
	}
	return m.executeMultiAccount(ctx, exec, plan, accounts)
}

// appendErrNote joins note onto an existing error string with "; ",
// returning note alone when existing is empty. Shared by the audit-gap and
// partial-failure paths so a successful-rec audit gap (#621) and a per-rec
// failure list (#642) within the same execution are both preserved on
// exec.Error rather than one clobbering the other.
func appendErrNote(existing, note string) string {
	if existing == "" {
		return note
	}
	if note == "" {
		return existing
	}
	return existing + "; " + note
}

// normalizePurchaseSource canonicalizes exec.Source for downstream tag
// stamping. Defense-in-depth: NormalizeSource rejects anything outside
// the allowed whitelist; an unexpected value (DB tampering, future
// code path) is dropped to "" rather than fed onto a cloud commitment
// where it would be expensive to retract.
//
// On invalid source the purchase PROCEEDS UNTAGGED (05-L3 decision):
// failing the rec over a tag-only field would abort a successful cloud
// purchase, which is a worse outcome than a missing tag. Input
// validation at the API write boundary (exec.Source on save) is the
// correct gate; this fallback is last-resort defense-in-depth.
func (m *Manager) normalizePurchaseSource(exec *config.PurchaseExecution) string {
	source := exec.Source
	if source == "" {
		return ""
	}
	normalized, err := common.NormalizeSource(source)
	if err != nil {
		logging.Warnf("Invalid purchase source %q on execution %s: %v, proceeding untagged", source, exec.ExecutionID, err)
		return ""
	}
	return normalized
}

// selectedIndices returns the positions of recs with Selected=true,
// preserving slice order so the aggregator's pass over the fan-out
// results writes back to exec.Recommendations deterministically.
func selectedIndices(recs []config.RecommendationRecord) []int {
	out := make([]int, 0, len(recs))
	for i := range recs {
		rec := recs[i]
		if rec.Selected {
			out = append(out, i)
		}
	}
	return out
}

// indexKeys formats a slice of indices as string keys for FanOut.
func indexKeys(idx []int) []string {
	out := make([]string, len(idx))
	for i, n := range idx {
		out[i] = strconv.Itoa(n)
	}
	return out
}

// errAmbiguousAccountScope is returned when the selected recommendations of a
// plan-less execution do not resolve to exactly one cloud account. The
// single-account path cannot honor such a batch: buying it under ambient
// credentials (the pre-#1902 behavior) purchases every commitment in the
// CUDly host account and stamps the ambient identity on history (#646).
var errAmbiguousAccountScope = errors.New("selected recommendations do not resolve to a single cloud account")

// SingleCloudAccountIDFromRecs returns the cloud_account_id shared by every
// SELECTED recommendation, nil when no selected recommendation carries one
// (the ambient single-account deployment), and errAmbiguousAccountScope when
// the selected recs span more than one account or mix attributed and
// unattributed entries (#1902). Only selected recs count: they are the recs
// the money moves for (processPurchaseRecommendations buys selectedIndices).
// Exported so the API boundary (validateExecutePurchaseRecommendations)
// applies the identical rule before an execution row exists.
func SingleCloudAccountIDFromRecs(recs []config.RecommendationRecord) (*string, error) {
	var ids []string
	var found *string
	selected, unattributed := 0, 0
	for i := range recs {
		rec := &recs[i]
		if !rec.Selected {
			continue
		}
		selected++
		if rec.CloudAccountID == nil || *rec.CloudAccountID == "" {
			unattributed++
			continue
		}
		if !slices.Contains(ids, *rec.CloudAccountID) {
			ids = append(ids, *rec.CloudAccountID)
			found = rec.CloudAccountID
		}
	}
	switch {
	case len(ids) > 1:
		return nil, fmt.Errorf("%w: %d selected recommendation(s) target %d cloud accounts (%s); submit one purchase per account",
			errAmbiguousAccountScope, selected, len(ids), strings.Join(ids, ", "))
	case len(ids) == 1 && unattributed > 0:
		return nil, fmt.Errorf("%w: %d selected recommendation(s) carry no cloud_account_id while %d target account %s",
			errAmbiguousAccountScope, unattributed, selected-unattributed, ids[0])
	}
	return found, nil
}

// savePurchaseHistory persists one purchase_history row for a successful
// commitment. It returns the store error (rather than only logging it) so the
// caller can record an audit gap on the execution: a swallowed failure here
// used to leave the execution silently "completed" with no purchase_history
// row, making the purchase invisible in the History view (issue #621).
func (m *Manager) savePurchaseHistory(ctx context.Context, exec *config.PurchaseExecution, plan *config.PurchasePlan, rec config.RecommendationRecord, result common.PurchaseResult, accountID string, offeringClass ec2types.OfferingClassType) error {
	purchasedAt := time.Now()
	svc := m.mapServiceType(rec.Service)
	historyRecord := &config.PurchaseHistoryRecord{
		AccountID:        accountID,
		PurchaseID:       result.CommitmentID,
		Timestamp:        purchasedAt,
		Provider:         rec.Provider,
		Service:          rec.Service,
		Region:           rec.Region,
		ResourceType:     rec.ResourceType,
		Count:            rec.Count,
		Term:             rec.Term,
		Payment:          rec.Payment,
		UpfrontCost:      historyUpfrontCost(rec.Provider, svc, result),
		MonthlyCost:      rec.MonthlyCost,
		EstimatedSavings: rec.Savings,
		PlanID:           exec.PlanID,
		PlanName:         plan.Name,
		RampStep:         exec.StepNumber,
		CloudAccountID:   exec.CloudAccountID,
		Source:           exec.Source,
		// Stamp the in-app free-cancel window so the History UI can offer the
		// Revoke button (issue #290). Azure-only in Phase 1; nil for AWS/GCP.
		RevocationWindowClosesAt: config.RevocationWindowClosesAtFor(rec.Provider, purchasedAt),
	}
	// The marketplace-sell guard reads this column instead of asking AWS
	// (issue #292), so it must be the class of the recorded commitment: the
	// provider-reported class on an adopted re-drive, else the class the EC2
	// client was asked to buy.
	if rec.Provider == string(common.ProviderAWS) && (svc == common.ServiceEC2 || svc == common.ServiceCompute) {
		historyRecord.OfferingClass = historyOfferingClass(result, offeringClass)
	}
	if err := m.config.SavePurchaseHistory(ctx, historyRecord); err != nil {
		logging.Errorf("Failed to save history: %v", err)
		return err
	}
	return nil
}

func (m *Manager) sendPurchaseNotification(ctx context.Context, exec *config.PurchaseExecution, plan *config.PurchasePlan, totalSavings, totalUpfront float64) error {
	data := m.buildPurchaseConfirmationData(exec, plan, totalSavings, totalUpfront)
	return m.email.SendPurchaseConfirmation(ctx, data)
}

func (m *Manager) buildPurchaseConfirmationData(exec *config.PurchaseExecution, plan *config.PurchasePlan, totalSavings, totalUpfront float64) email.NotificationData {
	dashboardBase := strings.TrimRight(m.dashboardURL, "/")
	data := email.NotificationData{
		DashboardURL:     dashboardBase,
		TotalSavings:     totalSavings,
		TotalUpfrontCost: totalUpfront,
		PlanName:         plan.Name,
	}
	if dashboardBase != "" {
		data.ArcheraEducationURL = dashboardBase + "/archera-insurance"
	}

	for i := range exec.Recommendations {
		rec := exec.Recommendations[i]
		if rec.Purchased {
			data.Recommendations = append(data.Recommendations, email.RecommendationSummary{
				Service:        rec.Service,
				ResourceType:   rec.ResourceType,
				Engine:         rec.Engine,
				Region:         rec.Region,
				Count:          rec.Count,
				MonthlySavings: rec.Savings,
			})
		}
	}

	return data
}

// logRecCtxErr emits a diagnostic log line when a per-recommendation context has
// been canceled or timed out. It distinguishes DeadlineExceeded (the 30s per-rec
// budget fired) from Canceled (a parent context stopped the execution) so that
// CloudWatch filters can tell the two apart without parsing error strings.
// It is a no-op when recCtxErr is nil.
func logRecCtxErr(executionID, recTuple string, elapsed time.Duration, recCtxErr error) {
	if recCtxErr == nil {
		return
	}
	if errors.Is(recCtxErr, context.DeadlineExceeded) {
		logging.Errorf("purchase[%s]: per-rec deadline exceeded for %s (elapsed=%s)",
			executionID, recTuple, elapsed)
	} else if errors.Is(recCtxErr, context.Canceled) {
		logging.Errorf("purchase[%s]: recommendation context canceled for %s (elapsed=%s)",
			executionID, recTuple, elapsed)
	}
}

// executeSinglePurchase executes a single purchase using the appropriate provider.
// provCfg carries optional per-account credentials; pass nil to use ambient credentials.
// opts carries execution-level metadata (the source surface) that providers stamp
// onto the commitment they create.
func (m *Manager) executeSinglePurchase(ctx context.Context, rec config.RecommendationRecord, provCfg *provider.Config, opts common.PurchaseOptions, ownNames *purchaseOwnNames) (common.PurchaseResult, error) {
	// Per-purchase Info logs tagged with the owning execution ID so a
	// CloudWatch filter on the execution UUID surfaces every step of the
	// purchase attempt -- provider construction, service-client lookup,
	// details decode, the cloud SDK call, and the result. Issue #667
	// surfaced that the prior flow had ZERO observable signal between the
	// approve handler and the final aggregated error toast; a timed-out
	// SDK call left no trace in CloudWatch beyond the wrapped error.
	logging.Infof("purchase[%s]: starting rec %s/%s/%s/%s (count=%d term=%dyr payment=%s)",
		opts.ExecutionID, rec.Provider, rec.Service, rec.Region, rec.ResourceType, rec.Count, rec.Term, rec.Payment)

	// Cap this individual rec at 30s so one hung rec (SDK retry storm, STS
	// hang, pagination hang) cannot exhaust the Lambda budget and starve the
	// other recs running in parallel in the same execution. 30s matches the
	// purchasecfg hard limits (2 retries * 15s HTTP timeout) from issue #683.
	// The parent ctx governs the overall Lambda budget; this per-rec deadline
	// is always the shorter of the two.
	recTuple := fmt.Sprintf("%s/%s/%s/%s", rec.Provider, rec.Service, rec.Region, rec.ResourceType)
	recCtx, recCancel := context.WithTimeout(ctx, m.budgets.perRecTimeout())
	defer recCancel()

	// Create the provider
	t0 := time.Now()
	cloudProvider, err := m.providerFactory.CreateAndValidateProvider(recCtx, rec.Provider, provCfg)
	if err != nil {
		logging.Errorf("purchase[%s]: provider construction failed for %s after %s: %v",
			opts.ExecutionID, rec.Provider, time.Since(t0), err)
		return common.PurchaseResult{}, fmt.Errorf("failed to create %s provider: %w", rec.Provider, err)
	}
	logging.Infof("purchase[%s]: provider %s constructed in %s", opts.ExecutionID, rec.Provider, time.Since(t0))

	// Map service string to ServiceType
	serviceType := m.mapServiceType(rec.Service)

	// Get the service client for this region
	t0 = time.Now()
	serviceClient, err := cloudProvider.GetServiceClient(recCtx, purchaseClientService(rec.Provider, serviceType), rec.Region)
	if err != nil {
		logging.Errorf("purchase[%s]: service client lookup failed for %s/%s in %s after %s: %v",
			opts.ExecutionID, rec.Provider, serviceType, rec.Region, time.Since(t0), err)
		return common.PurchaseResult{}, fmt.Errorf("failed to get service client: %w", err)
	}
	logging.Infof("purchase[%s]: service client %s/%s/%s ready in %s",
		opts.ExecutionID, rec.Provider, serviceType, rec.Region, time.Since(t0))

	// Build the recommendation in common format
	recommendation := common.Recommendation{
		Provider:       common.ProviderType(rec.Provider),
		Service:        serviceType,
		Region:         rec.Region,
		ResourceType:   rec.ResourceType,
		Count:          rec.Count,
		Term:           common.Term(fmt.Sprintf("%dyr", rec.Term)),
		PaymentOption:  common.PaymentOption(rec.Payment),
		CommitmentCost: rec.UpfrontCost,
	}

	// Reconstruct the typed *Details pointer from the persisted JSON
	// payload (issue #453). The scheduler stores the full
	// common.ServiceDetails at collection time; this is the read-back
	// step that yields a *ComputeDetails / *DatabaseDetails /
	// *CacheDetails / *SavingsPlanDetails (etc.) keyed on rec.Service
	// so the cloud service client's findOfferingID type-assertion
	// succeeds with the full per-rec details (Platform, Tenancy,
	// Scope, AZConfig, HourlyCommitment, …), not just an Engine string.
	//
	// Legacy rows persisted before #453 carry an empty rec.Details —
	// DecodeServiceDetailsFor returns a zero-valued typed pointer for
	// those. The AWS EC2 client does not fill the gaps: an empty Platform
	// or an empty or unknown Tenancy or Scope is rejected before the request
	// is sent (providers/aws ec2/client.go buildEC2OfferingQuery), so the
	// purchase fails loudly instead of buying a default configuration. Engine
	// is preserved on the record column too, so we use it as a last-resort
	// fallback for DB/Cache services to avoid silently mis-purchasing a
	// legacy non-default-engine rec as the default engine.
	details, detailsErr := common.DecodeServiceDetailsFor(rec.Service, rec.Details)
	if detailsErr != nil {
		logging.Errorf("purchase[%s]: decode service details failed for %s rec %s: %v",
			opts.ExecutionID, rec.Service, rec.ResourceType, detailsErr)
		return common.PurchaseResult{}, fmt.Errorf("decode service details for %s rec %s: %w", rec.Service, rec.ResourceType, detailsErr)
	}
	if details != nil {
		applyEngineFallback(details, rec.Engine)
		recommendation.Details = details
	}
	// One log line capturing the details shape that's about to drive
	// findOfferingID. detailsAbsent=true is the legacy-row fallback
	// (issue #453) and a strong indicator that filters will fall back
	// to defaults — surface it so a "no offerings found" failure is
	// linkable to a stale rec rather than a transient AWS issue.
	logging.Infof("purchase[%s]: details ready for %s/%s (detailsAbsent=%v engine=%q idempotencyToken=%q)",
		opts.ExecutionID, rec.Provider, rec.Service, len(rec.Details) == 0, rec.Engine, opts.IdempotencyToken)

	// Execute the purchase. This is the call that hits the cloud SDK
	// (and ultimately the AWS / Azure / GCP API). Time it explicitly so
	// CloudWatch shows whether a failure came from the SDK call itself
	// (which the AWS SDK retries up to 3× × 30s by default) versus
	// something earlier in the flow — issue #667.
	opts, err = preSubmitChecks(recCtx, serviceClient, rec, recommendation, opts, ownNames)
	if err != nil {
		return common.PurchaseResult{}, err
	}
	tCall := time.Now()
	result, err := serviceClient.PurchaseCommitment(recCtx, recommendation, opts)
	elapsed := time.Since(tCall)
	if err != nil {
		logRecCtxErr(opts.ExecutionID, recTuple, elapsed, recCtx.Err())
		logging.Errorf("purchase[%s]: %s/%s/%s/%s PurchaseCommitment failed after %s: %v",
			opts.ExecutionID, rec.Provider, rec.Service, rec.Region, rec.ResourceType, elapsed, err)
		return result, purchaseCallError(ctx, err)
	}

	if !result.Success {
		if result.Error != nil {
			logging.Errorf("purchase[%s]: %s/%s/%s/%s PurchaseCommitment returned Success=false after %s: %v",
				opts.ExecutionID, rec.Provider, rec.Service, rec.Region, rec.ResourceType, elapsed, result.Error)
			return result, result.Error
		}
		logging.Errorf("purchase[%s]: %s/%s/%s/%s PurchaseCommitment returned Success=false after %s (no error detail)",
			opts.ExecutionID, rec.Provider, rec.Service, rec.Region, rec.ResourceType, elapsed)
		return result, fmt.Errorf("purchase was not successful")
	}

	cost := "unknown"
	if result.Cost != nil {
		cost = fmt.Sprintf("%.2f", *result.Cost)
	}
	logging.Infof("purchase[%s]: %s/%s/%s/%s PurchaseCommitment succeeded in %s (commitmentID=%s, cost=%s)",
		opts.ExecutionID, rec.Provider, rec.Service, rec.Region, rec.ResourceType, elapsed, result.CommitmentID, cost)
	return result, nil
}

// preSubmitChecks runs what must hold before PurchaseCommitment: the AWS
// ElastiCache recent-reservation refusal (platform#1863), the GCP
// recent-commitment refusal (platform#682, a beforeSubmission error) and go's
// Redshift dispatch time, which bounds its DescribeReservedNodes match window
// by RequestedAt and must be the lineage's first dispatch time, never this
// call's.
func preSubmitChecks(ctx context.Context, client provider.ServiceClient, rec config.RecommendationRecord, recommendation common.Recommendation, opts common.PurchaseOptions, ownNames *purchaseOwnNames) (common.PurchaseOptions, error) {
	if err := elastiCacheRecentRefusal(ctx, client, recommendation, ownNames.elastiCacheNames(), ownElastiCacheID(opts)); err != nil {
		return opts, err
	}
	if err := gcpRecentCommitmentRefusal(ctx, client, recommendation, ownNames.gcpNames(), ownCommitmentName(opts)); err != nil {
		return opts, err
	}
	return withDispatchTime(rec, opts)
}

// ownCommitmentName is the GCP commitment name this rec's purchase call would
// create, or "" when the call carries no idempotency token.
func ownCommitmentName(opts common.PurchaseOptions) string {
	if opts.IdempotencyToken == "" {
		return ""
	}
	return gcpCommitmentName(opts.IdempotencyToken)
}

// purchaseCallError wraps a failed PurchaseCommitment. When the run context
// ended mid-call the request may have landed, so the error also carries
// ErrOutcomeUnknown: the row records an unknown outcome, never a plain failure.
func purchaseCallError(ctx context.Context, err error) error {
	if ctx.Err() != nil && !errors.Is(err, common.ErrOutcomeUnknown) {
		return fmt.Errorf("purchase failed: %w: %w", common.ErrOutcomeUnknown, err)
	}
	// The Azure client wraps a currency refusal with ErrOutcomeUnknown from
	// attempt 2 on and returns attempt 1 bare, so a bare one is a refusal.
	if errors.Is(err, common.ErrCurrencyNotAccepted) && !errors.Is(err, common.ErrOutcomeUnknown) {
		return beforeSubmission(fmt.Errorf("purchase refused: the provider does not accept the billing currency: %w", err))
	}
	return fmt.Errorf("purchase failed: %w", err)
}

// mapServiceType maps a service string to common.ServiceType. Both the
// canonical hyphenated slugs (compute, relational-db, cache, search,
// data-warehouse) and the legacy AWS-only slugs (ec2, rds, elasticache,
// opensearch, redshift, memorydb) are recognized; everything else passes
// through verbatim. Savings Plans slugs are normalized by mapSavingsPlansSlug.
func (m *Manager) mapServiceType(service string) common.ServiceType {
	if svc, ok := mapSavingsPlansSlug(service); ok {
		return svc
	}
	if svc, ok := mapServiceSlug(service); ok {
		return svc
	}
	return common.ServiceType(service)
}

var errUnknownOfferingClass = errors.New("unknown EC2 RI offering class")

// resolveEC2OfferingClass mirrors the pinned EC2 client's resolveOfferingClassType,
// where "" means convertible, so history records the class the client buys.
func resolveEC2OfferingClass(s string) (ec2types.OfferingClassType, error) {
	switch s {
	case "", string(ec2types.OfferingClassTypeConvertible):
		return ec2types.OfferingClassTypeConvertible, nil
	case string(ec2types.OfferingClassTypeStandard):
		return ec2types.OfferingClassTypeStandard, nil
	default:
		return "", fmt.Errorf("%w %q: must be %q or %q", errUnknownOfferingClass, s,
			ec2types.OfferingClassTypeConvertible, ec2types.OfferingClassTypeStandard)
	}
}

// purchaseClientService is the service whose client buys a recommendation of
// service. Azure Redis recommendations are labeled ServiceCache, but only the
// managedredis client (ServiceMemoryDB) buys RedisCache reservations; the
// Azure cache client refuses every purchase (cloud-commitments-go#694, #1801).
// Labels in history and suppressions keep the recommendation's own service.
func purchaseClientService(providerName string, service common.ServiceType) common.ServiceType {
	if providerName == string(common.ProviderAzure) && service == common.ServiceCache {
		return common.ServiceMemoryDB
	}
	return service
}

// mapServiceSlug returns the common.ServiceType for a canonical or legacy
// service slug.
//
// Canonical slugs (compute, relational-db, nosql, cache, search,
// data-warehouse) map to the canonical ServiceType constants. Legacy
// AWS-only slugs (ec2, rds, elasticache, opensearch, redshift, memorydb)
// map to the AWS-legacy constants for backward compat with rec rows
// persisted before the canonical normalisation. nosql is Azure Cosmos DB
// (#613); it used to reach the Azure client only through mapServiceType's
// verbatim fallback.
//
// The split matters because Azure and GCP providers' GetServiceClient
// switches accept canonical-only; passing the AWS-legacy constants for a
// canonical rec falls through to default and returns "unsupported
// service: <legacy>". AWS provider accepts both legacy and canonical
// forms (see providers/aws/provider.go), so the legacy slugs below are
// still safe for AWS rec rows. Bug:
// https://github.com/LeanerCloud/CUDly/issues/626 (tracked in the monorepo).
//
// Pulled out of mapServiceType to keep that function under the gocyclo
// budget (same pattern as mapSavingsPlansSlug).
func mapServiceSlug(service string) (common.ServiceType, bool) {
	slugs := map[string]common.ServiceType{
		// Canonical slugs.
		"compute":        common.ServiceCompute,
		"relational-db":  common.ServiceRelationalDB,
		"nosql":          common.ServiceNoSQL,
		"cache":          common.ServiceCache,
		"search":         common.ServiceSearch,
		"data-warehouse": common.ServiceDataWarehouse,
	}
	if svc, ok := slugs[service]; ok {
		return svc, true
	}
	svc, ok := legacyAWSServiceSlugs[service]
	return svc, ok
}

// legacyAWSServiceSlugs are the AWS-only slugs. A new alias must also be added
// to config.awsLegacyServiceSlugs, else suppressions miss it (#1420); a test
// ranges over this map to enforce that.
var legacyAWSServiceSlugs = map[string]common.ServiceType{
	"ec2":         common.ServiceEC2,
	"rds":         common.ServiceRDS,
	"elasticache": common.ServiceElastiCache,
	"opensearch":  common.ServiceOpenSearch,
	"redshift":    common.ServiceRedshift,
	"memorydb":    common.ServiceMemoryDB,
}

// mapSavingsPlansSlug normalizes both the canonical hyphenated SP slugs and
// the dash-free spellings the frontend has historically sent into the
// matching common.ServiceType. The map covers the legacy umbrella plus the
// four per-plan-type slugs in both spellings — pulled out of mapServiceType
// to keep that switch under the gocyclo budget.
//
// TODO(#95): PR #94 (merged 2026-04-30) renamed "savings-plans" ->
// "savingsplans". purchase_executions rows have a ~6-month retention window,
// so the earliest safe drop date is 2026-10-30. Until then the
// "savings-plans"-spelled aliases below must stay so Lambda-scheduled
// executions persisted before PR #94 still map correctly on retry/approval.
// To drop: verify zero rows with
//
//	SELECT id FROM purchase_executions
//	WHERE recommendations::text LIKE '%"service":"savings-plans"%'
//	LIMIT 1;
//
// then remove every "savings-plans*" key from this map and the matching
// case in pkg/common/service_details_codec.go: newDetailsForService, and
// flip the coverage_extra_test.go case (line ~60) to assert
// ServiceType("savings-plans") (default-arm pass-through). The
// "savingsplans-*" keys are forward-compat and should stay.
//
// A new per-plan-type spelling must also be added to
// config.awsSavingsPlansSpellings, else a suppression stored under it misses
// recs (#1422); a test ranges over savingsPlansSlugs to enforce that.
func mapSavingsPlansSlug(service string) (common.ServiceType, bool) {
	svc, ok := savingsPlansSlugs[service]
	return svc, ok
}

var savingsPlansSlugs = map[string]common.ServiceType{
	"savings-plans":             common.ServiceSavingsPlansAll,
	"savingsplans":              common.ServiceSavingsPlansAll,
	"savings-plans-compute":     common.ServiceSavingsPlansCompute,
	"savingsplans-compute":      common.ServiceSavingsPlansCompute,
	"savings-plans-ec2instance": common.ServiceSavingsPlansEC2Instance,
	"savingsplans-ec2instance":  common.ServiceSavingsPlansEC2Instance,
	"savings-plans-sagemaker":   common.ServiceSavingsPlansSageMaker,
	"savingsplans-sagemaker":    common.ServiceSavingsPlansSageMaker,
	"savings-plans-database":    common.ServiceSavingsPlansDatabase,
	"savingsplans-database":     common.ServiceSavingsPlansDatabase,
}

// updatePlanProgress advances the ramp schedule after a purchase.
// Direct-execute purchases (Opportunities flow) have no plan to advance --
// PlanID is empty and the Postgres UUID column would reject the query
// with SQLSTATE 22P02, so short-circuit cleanly before hitting the store.
//
// The step being completed is read off the execution itself, never off the
// plan's current position: a multi-account step fans out into one execution
// per account, each retryable on its own, so the plan's position is exactly
// what a second completion of one step must not be trusted to imply (issue
// #1669). CompletePlanStep holds a SELECT FOR UPDATE lock for the duration,
// which also closes the lost-update race between overlapping Lambda
// invocations on the same plan (issue #1071).
func (m *Manager) updatePlanProgress(ctx context.Context, exec *config.PurchaseExecution) error {
	if exec.PlanID == "" {
		return nil
	}
	if exec.StepNumber <= 0 {
		// Reachable for a row stamped by pre-#1669 code, which wrote the COUNT
		// of completed steps and so wrote 0 for a plan's first step. Migration
		// 000098 retargets those rows, leaving only ones written during the
		// deploy overlap. Advancing the ramp from an unknown step would be a
		// guess on the money path; report instead.
		return fmt.Errorf("execution %s is attributed to plan %s but carries ramp step %d: refusing to advance the ramp",
			exec.ExecutionID, exec.PlanID, exec.StepNumber)
	}
	return m.config.CompletePlanStep(ctx, exec.PlanID, exec.StepNumber)
}

// getAWSAccountID retrieves the current AWS account ID using STS.
// It returns ("", error) on all failure paths so the caller decides how to
// handle the absent ID (log a sentinel, skip tagging, etc.) rather than
// silently recording "unknown" in the purchase history row (05-L2).
func (m *Manager) getAWSAccountID(ctx context.Context) (string, error) {
	if m.stsClient == nil {
		return "", fmt.Errorf("STS client not configured")
	}

	result, err := m.stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("STS GetCallerIdentity: %w", err)
	}

	if result.Account == nil {
		return "", fmt.Errorf("STS returned nil Account")
	}
	return *result.Account, nil
}

// applyEngineFallback backfills the Engine field on DB / Cache Details
// from the persisted RecommendationRecord.Engine column when the decoded
// Details left Engine empty. Legacy rows (persisted before #453) carry
// an empty Details payload but a populated Engine column — without this
// backfill, a Postgres RDS rec would silently mis-purchase as whatever
// engine buildOfferingFilters defaults to. For non-DB/Cache Details
// types the call is a no-op. Pointer-typed Details get mutated in
// place; callers pass the pointer that's about to be assigned into
// recommendation.Details so the caller's variable sees the update.
func applyEngineFallback(details common.ServiceDetails, engine string) {
	if engine == "" {
		return
	}
	switch d := details.(type) {
	case *common.DatabaseDetails:
		if d.Engine == "" {
			d.Engine = engine
		}
	case *common.CacheDetails:
		if d.Engine == "" {
			d.Engine = engine
		}
	}
}
