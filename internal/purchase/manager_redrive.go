package purchase

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// allRecsSafeToRedrive reports whether this sweep's automatic in-place re-drive
// may run for exec. Two conditions: every recommendation must be safe to
// re-drive, which RedriveRefusalReason below owns and documents, AND the
// execution must carry at least one recommendation.
//
// The empty-recommendations condition is this sweep's own, deliberately not part
// of the shared safety policy. It is not a duplicate-risk statement: a re-drive
// that purchases nothing cannot double-buy. It means "nothing here worth
// re-driving, hand it to a human", and the safe-fail path it selects is benign
// (the row is marked failed and surfaces in History, where a human can retry
// it). Folding it into the shared predicate would export it to the user-facing
// retry endpoint, where the consequence is the opposite of benign: a permanent
// refusal of a row that cannot double-buy, with no recovery path (issue #1668).
//
// An in-place re-drive keeps the same row, so the lineage key is unchanged and
// DeriveIdempotencyToken(idempotencyLineageKey(exec), i) reproduces the original
// token exactly. That is what lets the provider-side dedupe engage at all.
func allRecsSafeToRedrive(exec *config.PurchaseExecution) bool {
	return len(exec.Recommendations) > 0 && RedriveRefusalReason(exec) == ""
}

// armedRedriveRefusal returns a non-nil error when exec must not be executed
// because it is a RETRY of a purchase that may already have landed, on a
// provider that offers no way to collapse the second attempt onto the first.
//
// Issue #1668 closed this at creation time: the user-facing Retry endpoint
// refuses to build such a successor. A creation-time gate cannot reach
// successors that a PRE-#1668 retry already created, which sit in
// pending/notified/approved/scheduled and buy a second, non-cancelable
// commitment the moment they are approved (issue #1718). This is the
// executor-side backstop for exactly those rows.
//
// It is placed in executeAndFinalize rather than at the individual executors
// because that function is the single funnel every executor reaches money
// through: claimAndExecuteWithin (cron), ApproveAndExecute (token + session
// approve), fireOneDue (the scheduled pre-fire delay sweep) and claimAndRedrive
// (the reaper). One gate at the funnel cannot be missed by a future executor
// the way three copies at three call sites can. executePurchase has exactly one
// production caller, immediately below this check.
//
// The checks run in this order, so a lookup happens only when it can matter:
//
//   - (a) RetryAttemptN <= 0 returns nil. A fresh execution is a FIRST purchase
//     with nothing to duplicate, and Azure savings plans must remain buyable.
//   - (b) An empty RedriveRefusalReason returns nil: the provider has a
//     duplicate guard (#1668 predicate, not a second notion of re-drive safety).
//   - (c) Otherwise, for a first retry only, the predecessor is read. When it is
//     a failed no_submission attempt 0, the lineage provably sent nothing and
//     the no-guard refusal is lifted (retryRefusalReason). A lookup error
//     refuses with fixed text, never a beforeSubmission error.
//
// Returning an error rather than skipping silently means finalizeExecution
// stamps the row "failed" with this reason and executeAndFinalize persists it,
// so a refused row is visible in History instead of remaining armed for the
// next sweep.
func (m *Manager) armedRedriveRefusal(ctx context.Context, exec *config.PurchaseExecution) error {
	if exec.RetryAttemptN <= 0 {
		return nil
	}
	reason := RedriveRefusalReason(exec)
	if reason == "" {
		return nil
	}
	// Only a first retry can follow a proven-unsent first attempt (D1: no chain
	// walk), so deeper attempts skip the lookup and keep the refusal.
	proven := false
	if exec.RetryAttemptN == 1 {
		var err error
		proven, err = m.config.PredecessorProvenUnsent(ctx, exec.ExecutionID)
		if err != nil {
			logging.Errorf("predecessor proof lookup failed for execution %s: %v", exec.ExecutionID, err)
			return errors.New(armedRedriveLookupRefusal)
		}
	}
	if reason = retryRefusalReason(exec, proven); reason == "" {
		return nil
	}
	return fmt.Errorf("refusing to execute retry attempt %d: %s", exec.RetryAttemptN, reason)
}

// armedRedriveLookupRefusal is fixed text (no wrapped DB error) so the stored
// error stays on the errnotes allowlist.
const armedRedriveLookupRefusal = "refusing to execute retry: could not confirm that the earlier attempt sent nothing; start a new purchase instead of retrying"

// RedriveRefusalReason returns a short operator-facing reason why exec must not
// be re-driven, or "" when every recommendation on it carries a provider-side
// guarantee that a second attempt collapses onto the first.
//
// This is the single source of truth for re-drive safety. Both the reaper's
// automatic in-place re-drive (via allRecsSafeToRedrive) and the user-facing
// Retry endpoint (Handler.checkRetryEligibilityGates in internal/api) gate on
// it, so a provider/service that is unsafe for one is unsafe for the other.
// Before issue #1668 only the reaper consulted it, and clicking Retry on a
// landed Azure savings-plans row bought a second savings plan, which cannot be
// canceled.
//
// Safe providers / services (issue #639):
//   - AWS RDS, ElastiCache, MemoryDB, OpenSearch and Savings Plans: a
//     deterministic reservation ID or ClientToken collapses a repeat onto the
//     first attempt (#636/#638).
//   - AWS Redshift: the Retry gate (checkRedshiftRetryGate) and the dispatch gate
//     (stampRedshiftDispatch) reuse the first attempt's request time, so go's
//     dedupe adopts a node already bought; both refuse when that cannot be proven.
//   - Azure reservations (compute, relational-db, cache, nosql, memorydb,
//     search, data-warehouse): DoIdempotentPurchaseTwoStep performs a
//     tag-based lookup before purchasing (#729 / #721).
//   - GCP compute (CUDs): server-side RequestId + deterministic name from
//     the token (#654).
//
// NOT safe:
//   - AWS EC2 (slugs ec2, compute): the purchase API takes no client token, so a
//     lost response may have bought the commitment and a second attempt would buy
//     another (MON-02). The provider sends the call once and reports "purchase
//     outcome unknown".
//   - Azure savings-plans: the pinned client names the order alias after the
//     idempotency token when one is set (a timestamp name only on the
//     empty-token CLI path), but no test or provider doc here confirms that a
//     repeat PUT of the same alias collapses onto the first order, and there
//     is no tag-based lookup. Until that is verified, a re-drive is treated as
//     able to buy a duplicate savings plan. The refusal covers exactly the
//     slugs that dispatch to the Azure savings-plans client (pinned by
//     TestRedriveGuardReachMatchesDispatchReach).
//   - Any provider this function does not recognize, rather than assuming a
//     guard exists.
//
// Empty provider ("") is treated as AWS (pre-multi-cloud legacy rows).
//
// It answers exactly one question: could re-driving these recommendations buy
// something twice. An execution with no recommendations buys nothing, so it has
// no duplicate risk and gets no refusal here. Callers that need "there is
// nothing worth re-driving" must say so themselves, as allRecsSafeToRedrive
// does above.
//
// The reason is rendered verbatim to the operator, so it explains the refusal
// in product terms rather than naming internals.
func RedriveRefusalReason(exec *config.PurchaseExecution) string {
	for i := range exec.Recommendations {
		if reason := recRedriveRefusalReason(exec.Recommendations[i]); reason != "" {
			return reason
		}
	}
	return ""
}

// Fixed refusal texts, shared with the stored-error allowlist (errnotes.go).
const (
	redriveReasonAWSNoGuard = "AWS EC2 purchases have no provider-side duplicate guard, and a lost response means the earlier attempt may already have bought the commitment. Check EC2 Reserved Instances in the target account and region; if it is missing, start a new purchase instead of retrying"
	// Stored by armed-refusal rows written before Redshift re-drive was lifted; errnotes only.
	redriveReasonAWSNoGuardLegacy = "AWS EC2 and Redshift purchases have no provider-side duplicate guard, and a lost response means the earlier attempt may already have bought the commitment. Check EC2 Reserved Instances or Redshift Reserved Nodes in the target account and region; if it is missing, start a new purchase instead of retrying"
	redriveReasonAzureSavingsPlan = "Azure savings plans have no provider-side duplicate guard, so re-driving this purchase would buy a second savings plan that cannot be canceled"
)

// azureRedriveRefusedSavingsPlansSlugs is the single list of Azure service
// spellings whose re-drive is refused. "savings-plans" no longer dispatches
// (#7), but a legacy row may still carry it and a possibly-landed Azure
// savings plan is non-cancellable (#1668), so refusing is the safe direction.
var azureRedriveRefusedSavingsPlansSlugs = map[string]bool{
	"savingsplans":  true,
	"savings-plans": true,
}

// recRedriveRefusalReason returns why one recommendation cannot be safely re-driven, or ""
// when it can; split out of RedriveRefusalReason for the gocyclo budget.
func recRedriveRefusalReason(rec config.RecommendationRecord) string {
	switch rec.Provider {
	case "", "aws":
		// Empty provider is legacy AWS. RDS, ElastiCache, MemoryDB, OpenSearch
		// and Savings Plans collapse a repeat onto the first attempt. Redshift
		// is guarded by the journaled lineage gate (redshift_lineage.go). EC2
		// takes no client token or caller ID, so a lost response may hide a
		// bought commitment and a second attempt waits for someone to check.
		if awsPurchaseIsEC2(rec.Service) {
			return redriveReasonAWSNoGuard
		}
		return ""
	case "azure":
		// Azure savings-plans: a repeat order-alias PUT is not confirmed to
		// collapse onto the first order (see RedriveRefusalReason), so a
		// re-drive may create a duplicate. All other Azure services use
		// DoIdempotentPurchaseTwoStep (#729).
		if azureRedriveRefusedSavingsPlansSlugs[rec.Service] {
			return redriveReasonAzureSavingsPlan
		}
		return ""
	case "gcp":
		// GCP compute CUDs use RequestId + deterministic name from the token (#654).
		return ""
	default:
		// Unknown provider: refuse to re-drive rather than risk a double-buy.
		return fmt.Sprintf("provider %q is not known to reject a duplicate purchase, so re-driving this could buy a second commitment", rec.Provider)
	}
}

// awsPurchaseIsEC2 reports whether a service slug (ec2 or compute) dispatches to the EC2 path,
// matching through mapServiceSlug, the executor's own table, so spellings cannot drift.
func awsPurchaseIsEC2(service string) bool {
	svc, ok := mapServiceSlug(service)
	if !ok {
		return false
	}
	switch svc {
	case common.ServiceEC2, common.ServiceCompute:
		return true
	default:
		return false
	}
}

// claimAndRedrive atomically claims a stranded execution (by transitioning its
// status from "approved" to "running") and then re-drives it via
// executeAndFinalize. It is extracted from RecoverStrandedApprovals to keep that
// function's cyclomatic complexity within the gocyclo:10 limit.
//
// It re-drives the row the claim returned, never the caller's pre-claim
// snapshot (issue #1037). The sweep chose the re-drive path from a SELECT, and
// an edit or a concurrent writer can change the row's recommendations, amount
// or account before the claim lands. The re-drive gate (redrivable) is therefore
// re-run on the claimed row; when the claimed row no longer qualifies it is
// failed exactly as safeFail would have failed it had the sweep read it in
// that state (failClaimedRedrive), and nothing is bought.
//
// Returns (true, nil) when the claim was won and the re-drive completed (row is
// now in a terminal state), or when the claimed row was failed because it no
// longer qualifies for a re-drive. A re-drive error is not fatal --
// executeAndFinalize already stamped a terminal status -- so it returns
// (false, nil) on drive failure too (the failed row is still visible in
// History).
// Returns (false, nil) when the CAS claim is lost to a concurrent sweep or the
// row vanishes mid-flight (both are benign races).
// Returns (false, err) on a genuine DB error during the claim step, or when a
// terminal status could not be persisted (ErrAuditLoss).
func (m *Manager) claimAndRedrive(ctx context.Context, exec *config.PurchaseExecution) (bool, error) {
	// Atomically claim ownership before re-driving to prevent two concurrent
	// sweeps (or a late original completion) from both calling executeAndFinalize
	// on the same approved row. The CAS transitions "approved" -> "running";
	// only the winner proceeds.
	// System-initiated: recovery sweep passes nil so transitioned_by = NULL.
	claimed, claimErr := m.claimRunning(ctx, exec.ExecutionID, []string{"approved"}, nil)
	if claimErr != nil {
		// ErrNotFound: row vanished between SELECT and CAS - benign race.
		// ErrExecutionNotInExpectedStatus: another sweep or the original run
		// already claimed/completed this row - also benign.
		if errors.Is(claimErr, config.ErrNotFound) || errors.Is(claimErr, config.ErrExecutionNotInExpectedStatus) {
			logging.Warnf("Skipping re-drive of %s (CAS claim lost to concurrent worker): %v", exec.ExecutionID, claimErr)
			return false, nil
		}
		return false, fmt.Errorf("failed to claim execution %s for re-drive: %w", exec.ExecutionID, claimErr)
	}
	// Carry the whole committed row, not just its status: the purchase below
	// must buy exactly the row this claim owns. The snapshot the sweep listed
	// may predate an edit that landed before the CAS (issue #1037).
	*exec = *claimed
	if !redrivable(exec) {
		return m.failClaimedRedrive(ctx, exec)
	}
	logging.Infof("Recovering stranded execution %s via idempotent re-drive (issue #639)", exec.ExecutionID)
	if driveErr := m.executeAndFinalize(ctx, exec); driveErr != nil {
		logging.Errorf("Re-drive of stranded execution %s failed: %v", exec.ExecutionID, driveErr)
		// Persistence failures (ErrAuditLoss) are non-benign: the row was CAS-ed
		// to "running" but SavePurchaseExecution failed, so no terminal status was
		// persisted. Propagate so the sweep surfaces the error rather than silently
		// dropping a row that is now stranded in "running".
		if errors.Is(driveErr, config.ErrAuditLoss) {
			return false, fmt.Errorf("persistence failure re-driving execution %s (row stranded in running): %w", exec.ExecutionID, driveErr)
		}
		// Benign provider/rec errors: finalizeExecution already stamped a terminal
		// status (failed/partially_completed) and SavePurchaseExecution succeeded.
		// The row is in a terminal state; log and continue without counting as
		// recovered.
		return false, nil
	}
	return true, nil
}

// safeFail atomically transitions a stranded execution to "failed" and stamps a
// recovery error on it. It is extracted from RecoverStrandedApprovals to keep
// that function's cyclomatic complexity within the gocyclo:10 limit.
//
// Returns (true, nil) when the row was successfully transitioned to "failed".
// Returns (false, nil) when TransitionExecutionStatus fails but the row has
// already left "approved" (benign race - the original run completed late;
// not counted as a recovery since no action was taken here).
// Returns (false, err) when a real store failure occurs.
func (m *Manager) safeFail(ctx context.Context, exec *config.PurchaseExecution) (bool, error) {
	logging.Errorf("Recovering stranded approved execution %s (approved but never finalized; failing it for visibility)", exec.ExecutionID)

	// System-initiated: stranded-execution recovery passes nil so transitioned_by = NULL.
	updated, txErr := m.config.TransitionExecutionStatus(ctx, exec.ExecutionID, []string{"approved"}, "failed", nil)
	if txErr != nil {
		// ErrNotFound means the row vanished between the stale SELECT and
		// this CAS attempt (e.g. deleted by an operator or a concurrent
		// sweep already claimed and deleted it). That is a benign race-loss:
		// there is nothing left to fail, and the caller should not be
		// charged with an error.
		if errors.Is(txErr, config.ErrNotFound) {
			logging.Warnf("Skipping recovery of %s (row no longer exists, benign race-loss): %v", exec.ExecutionID, txErr)
			return false, nil
		}
		// ErrExecutionNotInExpectedStatus means the row exists but its
		// status has already moved out of "approved" (a concurrent sweep or
		// the original run won the CAS race). Treat identically to the
		// ErrNotFound case: nothing left to do here, no re-read needed.
		// This mirrors claimAndRedrive and reaper.go, which both treat this
		// sentinel as terminally benign.
		if errors.Is(txErr, config.ErrExecutionNotInExpectedStatus) {
			logging.Warnf("Skipping recovery of %s (row already left approved state, benign CAS race-loss): %v", exec.ExecutionID, txErr)
			return false, nil
		}
		// Distinguish benign races (row already left the "approved"
		// state - concurrent sweep handled it, or the original run
		// finished after the LIST snapshot) from real store
		// failures (DB unreachable, query syntax error). A real
		// store failure must fail the sweep so a transient DB
		// outage does not silently under-recover. We probe the
		// current row state via GetExecutionByID: a clean read
		// with Status != "approved" confirms the race; any other
		// outcome (read error, still-approved row) is a real
		// failure worth propagating.
		current, getErr := m.config.GetExecutionByID(ctx, exec.ExecutionID)
		if getErr == nil && current != nil && current.Status != "approved" {
			logging.Warnf("Skipping recovery of %s (already transitioned out of approved): %v", exec.ExecutionID, txErr)
			return false, nil
		}
		return false, fmt.Errorf("failed to transition stranded execution %s to failed: %w", exec.ExecutionID, txErr)
	}
	// This sweep won the CAS, so it is the one writer that notifies (#1195),
	// unless the guarded stamp below finds the row already recovered: then the
	// in-memory failed row is stale and a failure email would be false.
	// The interrupted run may already have bought, so the email reports an
	// unknown outcome rather than a failure to retry (#1415).
	notify := true
	defer func() {
		if notify {
			m.notifyOutcomeUnknown(ctx, updated)
		}
	}()

	updated.Error = strandedRecoveryError(exec)
	if saveErr := m.config.SaveExecutionIfStatus(ctx, updated, []string{config.StatusFailed}); saveErr != nil {
		notify = !errors.Is(saveErr, config.ErrExecutionNotInExpectedStatus)
		// Guarded on "failed" (issue #835): the stamp lands only while the row
		// is still in the status this CAS set, never over a later writer's.
		// The atomic flip to "failed" already landed via TransitionExecutionStatus;
		// only the explanatory error string failed to persist. Log loudly but
		// still count the recovery - the row is no longer stranded in "approved".
		logging.Errorf("AUDIT GAP: failed to stamp recovery error on %s: %v", exec.ExecutionID, saveErr)
	}
	return true, nil
}
