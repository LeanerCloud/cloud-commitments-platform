package purchase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// ErrFourEyesDenied wraps every refusal by the 4-eyes approval policy, as
// opposed to a failure to evaluate it (config or store errors).
var ErrFourEyesDenied = errors.New("approval declined")

// ErrInvalidApprovalToken and ErrApprovalTokenExpired let the HTTP layer map a
// bad email-link token to a 4xx instead of a 500.
var (
	ErrInvalidApprovalToken = errors.New("invalid approval token")
	ErrApprovalTokenExpired = errors.New("approval token has expired")
)

// runNowSkipReason is stamped onto PreApprovalSkipReason when an operator's
// "Run now" stands in for the approval and the scheduled wait.
const runNowSkipReason = "run-now"

// directExecuteSkipReason is stamped onto PreApprovalSkipReason when a session
// holding the direct-execute permission skips the approval round-trip.
const directExecuteSkipReason = "direct-execute permission"

// ApproveExecution is the token-authenticated approve entry point used by
// the legacy email-link flow. After validating
// the approval token it hands off to the ApproveAndExecute funnel
// (transitionApproveAndExecute), which performs the atomic status transition
// with the token pinned inside it, runs the AWS purchase synchronously, and
// mints the fresh revocation token returned here.
//
// actor carries the email of the operator who triggered the approval
// (the session-authed click on the HTTP path) so it can be stamped onto
// ApprovedBy. Empty actor is recorded as NULL — the column is nullable TEXT and
// we don't want to claim "approved by nobody".
//
// check is the caller's ClaimedRowCheck, run against the claimed row before
// any provider call (issue #415); the token path passes nil.
//
// Returns the same row and token as ApproveAndExecute (issue #532): the row
// carries the run's final status, also on an execution error, and is nil when
// the run never started (bad token, orphan, 4-eyes denial, lost claim), so the
// caller can tell a refusal from a provider failure after the claim. The raw
// revocation token minted on success is a return value rather than something
// the caller re-fetches from the DB (see transitionApproveAndExecute). It is
// empty on failure, and best-effort empty if the mint itself failed after an
// otherwise-successful approve. A partially_completed run returns its token
// together with its error, so a non-empty token with an error means part of
// the purchase committed and is revocable.
func (m *Manager) ApproveExecution(ctx context.Context, executionID, token, actor string, check ClaimedRowCheck) (*config.PurchaseExecution, string, error) {
	t0 := time.Now()
	logging.Infof("purchase[%s]: ApproveExecution entry (auth=token actor=%q)", executionID, maskActor(actor))

	execution, err := m.config.GetExecutionByID(ctx, executionID)
	if errors.Is(err, config.ErrNotFound) {
		return nil, "", fmt.Errorf("execution not found: %s", executionID)
	}
	if err != nil {
		return nil, "", fmt.Errorf("failed to get execution: %w", err)
	}

	// Validate token and TTL (Finding #4 + issue #397).
	if tokErr := ValidateApprovalToken(execution, token); tokErr != nil {
		return nil, "", tokErr
	}

	// Preflight guard (issue #609): reject non-AWS orphan executions before
	// the cloud SDK is reached. See OrphanExecutionError for the full rationale.
	if checkErr := OrphanExecutionError(execution); checkErr != nil {
		logging.Errorf("purchase[%s]: ApproveExecution preflight rejected after %s: %v",
			executionID, time.Since(t0), checkErr)
		return nil, "", checkErr
	}

	// Token path: no authenticated session UUID is available, so the
	// transition is recorded as system-initiated (transitioned_by = NULL).
	// The token check above read a snapshot; the claim pins the same token
	// inside its status CAS so a re-notification that rotated it since cannot
	// win the claim with the old one (issue #1190).
	final, revocationToken, err := m.transitionApproveAndExecute(ctx, executionID, actor, nil, []string{"pending", "notified"}, "", token, check)
	if err != nil {
		logging.Errorf("purchase[%s]: ApproveExecution (token path) failed after %s: %v",
			executionID, time.Since(t0), err)
	} else {
		logging.Infof("purchase[%s]: ApproveExecution (token path) completed in %s",
			executionID, time.Since(t0))
	}
	return final, revocationToken, err
}

// maskActor masks an actor email/username for safe log emission.
// Full email addresses are PII; log only the domain part.
// Empty actor is logged as "<anon>" so the distinction between
// "no actor provided" and "actor was empty string" is visible.
func maskActor(actor string) string {
	if actor == "" {
		return "<anon>"
	}
	if at := strings.Index(actor, "@"); at >= 0 {
		return "***" + actor[at:]
	}
	// Not an email address -- log only the last 4 chars (e.g. a username).
	if len(actor) > 4 {
		return "..." + actor[len(actor)-4:]
	}
	return "****"
}

// ValidateApprovalToken checks that the execution carries a non-empty token,
// that the supplied token matches the stored one using constant-time comparison
// (Finding #4 -- prevents timing attacks), and that the token has not expired
// (issue #397). Legacy rows with a nil ApprovalTokenExpiresAt pass the TTL
// check for backward compatibility. Extracted from ApproveExecution to keep
// that function under the gocyclo threshold. Exported so the HTTP layer's
// delayed-approval branch (which never calls ApproveExecution) validates the
// token the same way (issue #586).
//
// execution.ApprovalToken is the SHA-256 hex digest stored at rest
// (issue #103), never the raw secret; config.ApprovalTokenMatches hashes the
// supplied token and compares digests in constant time.
func ValidateApprovalToken(execution *config.PurchaseExecution, token string) error {
	if execution.ApprovalToken == "" || token == "" {
		return ErrInvalidApprovalToken
	}
	if !config.ApprovalTokenMatches(execution.ApprovalToken, token) {
		return ErrInvalidApprovalToken
	}
	if execution.ApprovalTokenExpiresAt != nil && time.Now().After(*execution.ApprovalTokenExpiresAt) {
		return ErrApprovalTokenExpired
	}
	return nil
}

// OrphanExecutionError returns a descriptive error when the execution has no
// resolvable CloudAccountID and the provider is explicitly non-AWS (issue #609).
// AWS executions with a nil CloudAccountID are passed through because the
// ambient-host-account fallback from PR #607/#604 handles them. Empty
// provider is treated as AWS (legacy rows pre-dating multi-cloud support).
//
// An execution is NOT considered orphan if any individual recommendation
// carries its own non-nil CloudAccountID — multi-rec fan-out executions can
// have a nil execution-level field while individual recs still name the
// target account.
//
// Exported so the HTTP layer (internal/api) can call the same guard without
// duplicating the predicate. Extracted from ApproveExecution to keep that
// function below the gocyclo threshold — same pattern as loadCancelableExecution.
func OrphanExecutionError(execution *config.PurchaseExecution) error {
	if execution.CloudAccountID != nil {
		return nil
	}
	if len(execution.Recommendations) == 0 {
		return nil
	}
	provider := execution.Recommendations[0].Provider
	if provider == "" || provider == "aws" {
		return nil
	}
	// Any rec-level CloudAccountID means at least one recommendation has a
	// concrete target account — the execution is not fully orphaned.
	for i := range execution.Recommendations {
		if execution.Recommendations[i].CloudAccountID != nil {
			return nil
		}
	}
	return fmt.Errorf("execution %s references an account that no longer exists (provider: %s): cancel this purchase — it cannot execute",
		execution.ExecutionID, provider)
}

// ApproveAndExecute atomically flips a pending/notified execution to
// "approved" (stamping ApprovedBy) and then runs the purchase
// synchronously. Callers MUST have already authorized the actor — this
// method does no RBAC or token validation; it is shared between:
//
//   - ApproveExecution (token path: token validated by the caller and
//     pinned inside the CAS; it calls transitionApproveAndExecute directly)
//   - approvePurchaseViaSession (session path: RBAC validated by the caller)
//   - directExecutePurchase (execute_mode="direct": RBAC validated by the caller)
//
// Concurrency: TransitionExecutionStatus uses an atomic UPDATE WHERE status
// IN ('pending','notified'). Two callers racing to approve the same row
// will see exactly one win — the loser receives a clear "cannot
// transition" error. Cross-execution concurrency is unaffected: each
// approval drives its own executeAndFinalize, which already fans out
// per-account in parallel via executeMultiAccount.
//
// The returned row carries the approval's final status, also on an execution
// error; it is nil when the run never started. check runs against the claimed
// row before any provider call; see ClaimedRowCheck.
func (m *Manager) ApproveAndExecute(ctx context.Context, executionID, actor string, transitionedBy *string, check ClaimedRowCheck) (*config.PurchaseExecution, string, error) {
	return m.transitionApproveAndExecute(ctx, executionID, actor, transitionedBy, []string{"pending", "notified"}, "", "", check)
}

// DirectExecute is ApproveAndExecute for a session with the direct-execute
// permission: the same 4-eyes-gated, CAS-guarded funnel, but it stamps the
// executed_* audit fields once the CAS has won, so a refused or lost direct
// execute leaves no executed_at on a row that never ran.
func (m *Manager) DirectExecute(ctx context.Context, executionID, actor string, transitionedBy *string, check ClaimedRowCheck) (*config.PurchaseExecution, string, error) {
	return m.transitionApproveAndExecute(ctx, executionID, actor, transitionedBy, []string{"pending", "notified"}, directExecuteSkipReason, "", check)
}

// RunPlannedPurchaseNow lets an operator force a scheduled purchase (pending
// or paused) to execute immediately instead of waiting for its scheduled
// date, going through the exact same 4-eyes gate and synchronous
// executeAndFinalize funnel ApproveAndExecute uses (issue #218). The
// previous runPlannedPurchase implementation only flipped the row's status
// to "running" and returned success: no executor consumes "running" rows
// (the scheduler picks up pending/notified, the reaper only cleans up a
// stuck "running" row after 10 minutes), so nothing ever bought the
// purchase and the row later reads "failed" out from under the operator who
// was told it ran.
//
// Sharing transitionApproveAndExecute's CAS with ApproveAndExecute and
// claimAndExecuteWithin is also what makes this idempotent: a second "Run now"
// click, or the scheduler racing the same pending row, loses the atomic
// UPDATE ... WHERE status IN (...) and gets a clean "cannot transition"
// error rather than executing the purchase a second time.
//
// The returned row carries the run-now audit stamp and its final status, also
// on an execution error; it is nil when the run never started. check runs
// against the claimed row before any provider call; see ClaimedRowCheck.
func (m *Manager) RunPlannedPurchaseNow(ctx context.Context, executionID, actor string, transitionedBy *string, check ClaimedRowCheck) (*config.PurchaseExecution, string, error) {
	return m.transitionApproveAndExecute(ctx, executionID, actor, transitionedBy, []string{"pending", "notified", "paused"}, runNowSkipReason, "", check)
}

// transitionApproveAndExecute is the shared body behind ApproveAndExecute
// and RunPlannedPurchaseNow: enforce the 4-eyes gate, atomically CAS the row
// from one of fromStatuses straight to "running" (the claim), stamp ApprovedBy, then run the
// purchase synchronously. Parameterizing on fromStatuses lets both entry
// points share one implementation of the gate and the CAS instead of the
// gate being reimplemented (or skipped) at a second call site.
//
// Returns the raw revocation token minted on a successful execute, or ""
// if the (best-effort) mint failed. A partially_completed run also gets one,
// returned alongside its error (see IsCommittedPartialRun, issue #532).
// Every caller emails a "purchase executed, revoke here" link, and since
// only the token's hash is stored (issue #103) a DB re-read can never yield
// a raw, emailable value, so the rotation lives here, in the one funnel all
// approve paths share.
//
// A non-empty skipReason stamps the executed_* audit fields once the CAS wins.
// The returned row is nil when the CAS was never won, or when the audit stamp
// found the claim already lost (nothing is bought then; issue #835).
//
// A non-empty approvalToken (ApproveExecution only) is pinned inside the CAS:
// a token rotated or expired since the caller validated it refuses the claim
// with config.ErrApprovalTokenChanged and leaves the row untouched (issue
// #1190). Every other caller passes "".
//
// check runs against the row the CAS returned, which is the row
// executeAndFinalize buys, before any audit stamp or provider call (issue
// #415). A refusal fails that row before submission (status "failed",
// no_submission set, a plan row's suppressions released) and returns it with
// an error satisfying errors.Is(err, ErrClaimedRowRefused).
func (m *Manager) transitionApproveAndExecute(ctx context.Context, executionID, actor string, transitionedBy *string, fromStatuses []string, skipReason, approvalToken string, check ClaimedRowCheck) (*config.PurchaseExecution, string, error) {
	t0 := time.Now()
	logging.Infof("purchase[%s]: transitionApproveAndExecute starting (actor=%q, from=%v)", executionID, maskActor(actor), fromStatuses)

	// Universal 4-eyes gate (issue #1005 / PR #1500 adversarial review): runs
	// before any state mutation so every caller -- session approve, direct
	// execute, run-now, token approve -- is covered by one
	// policy check. transitionedBy doubles as the actor's own UUID for this
	// check when the caller has one (session-based callers); see
	// enforceFourEyesPolicy's doc comment for the full rationale.
	if err := m.enforceFourEyesPolicy(ctx, executionID, actor, transitionedBy); err != nil {
		logging.Warnf("purchase[%s]: transitionApproveAndExecute denied by 4-eyes policy: %v", executionID, err)
		return nil, "", err
	}

	// transitionedBy carries the session user's UUID for human-initiated
	// approvals (stamped onto transitioned_by); it is nil for token/system
	// flows so transitioned_by = NULL on those hops. The human-readable actor
	// email is recorded separately onto approved_by (below).
	// One CAS straight to "running": winning it is the claim, so no other
	// executor (cron, re-drive, scheduler) can run this row, and there is
	// no window where the row sits "approved" with a stale copy in flight
	// (issue #829). A lost CAS surfaces ErrExecutionNotInExpectedStatus.
	updated, err := m.claimAndCheck(ctx, executionID, fromStatuses, transitionedBy, approvalToken, check, t0)
	if err != nil {
		return updated, "", err
	}
	logging.Infof("purchase[%s]: approved and claimed running in %s", executionID, time.Since(t0))

	if skipReason != "" {
		now := time.Now()
		reason := skipReason
		updated.ExecutedAt = &now
		updated.ExecutedByUserID = transitionedBy
		updated.PreApprovalSkipReason = &reason
	}
	if actor != "" {
		a := actor
		updated.ApprovedBy = &a
	}
	if actor != "" || skipReason != "" {
		// Guarded on "running". A store error stays best-effort, but a lost
		// guard means the claim is gone, so nothing is bought (issue #835).
		if stampErr := m.stampClaimedAudit(ctx, updated, "approval audit fields"); stampErr != nil {
			return nil, "", stampErr
		}
	}

	logging.Infof("purchase[%s]: executing purchase synchronously", executionID)
	revocationToken, execErr := m.runClaimedAndMint(ctx, updated, t0)
	return updated, revocationToken, execErr
}

// IsCommittedPartialRun reports whether an execute call that returned row and
// err ended partially_completed with that status saved: some commitments were
// bought, so the run is revocable and its executed email must go out like a
// completed run's (issue #532). An audit loss is excluded: the stored row is
// still "running", whatever the returned row says.
func IsCommittedPartialRun(row *config.PurchaseExecution, err error) bool {
	return err != nil && row != nil && row.Status == config.StatusPartiallyCompleted && !errors.Is(err, config.ErrAuditLoss)
}

// runClaimedAndMint runs the claimed row and, when the run committed anything
// (completed, or a saved partially_completed), mints its revocation token. It
// returns the raw token ("" when nothing committed or the best-effort mint
// failed) and the run's error, which a partial run keeps.
func (m *Manager) runClaimedAndMint(ctx context.Context, updated *config.PurchaseExecution, t0 time.Time) (string, error) {
	executionID := updated.ExecutionID
	execErr := m.executeAndFinalize(ctx, updated)
	if execErr != nil {
		logging.Errorf("purchase[%s]: transitionApproveAndExecute failed after %s: %v", executionID, time.Since(t0), execErr)
		if !IsCommittedPartialRun(updated, execErr) {
			return "", execErr
		}
	} else {
		logging.Infof("purchase[%s]: transitionApproveAndExecute completed in %s", executionID, time.Since(t0))
	}

	// Mint a fresh revocation token now that the purchase has committed, in
	// full or in part (a partially_completed row is revocable, issue #532).
	// The pre-approve approval token must not double as the revocation token:
	// on the token path it was just consumed to authorize this very approve
	// action, and on every path its raw value is no longer recoverable once
	// hashed at rest (issue #103). See rotateApprovalToken's doc comment.
	revocationToken, mintErr := m.rotateApprovalToken(ctx, updated, config.RevocationWindow)
	if mintErr != nil {
		logging.Warnf("purchase[%s]: transitionApproveAndExecute: revocation token mint failed (best-effort): %v", executionID, mintErr)
	}
	return revocationToken, execErr
}
