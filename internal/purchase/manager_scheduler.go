package purchase

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// RecoverStrandedApprovals finds executions stuck in the "approved" status past
// staleApprovedThreshold and either re-drives them idempotently (executions
// where every rec is safe to re-drive and the row has a durable ExecutionID)
// or drives them into a terminal "failed" state (rows with unsafe recs or
// without a stable ExecutionID).
//
// Idempotent re-drive path (issue #639): all AWS, Azure reservations, and GCP
// compute service clients derive or look up a deterministic idempotency key
// from DeriveIdempotencyToken(idempotencyLineageKey(exec), i). This is an
// IN-PLACE re-drive — the same row, so the lineage key is unchanged — which
// reproduces the same token, so the cloud provider dedupes the second call and
// no double-purchase occurs. (The retry/fan-out double-buy hole #1012 closes is
// about NEW rows minting a fresh ExecutionID; that does not apply here.) The row
// transitions directly to "completed"
// (or "failed"/"partially_completed" on a genuine error), bypassing the manual
// Retry step required by the old safe-fail path. See allRecsSafeToRedrive for
// which provider/service combinations are eligible.
//
// Safe-fail path: Azure savings-plans recs are excluded because a repeat
// order-alias PUT is not confirmed to be deduplicated (see
// RedriveRefusalReason). Executions without a stable ExecutionID (legacy
// rows) also fall through because
// idempotencyLineageKey(exec) falls back to "" for them and
// DeriveIdempotencyToken("", i) would produce the same token set for every
// such row. These fall through to the original behavior: the row is atomically
// transitioned to "failed" so it surfaces in History and can be Retry-ed by
// an operator after confirming the cloud-side state.
//
// The transition in the safe-fail path is atomic: TransitionExecutionStatus only
// flips rows still in "approved", so if the original run finally completes between
// the stale SELECT and this UPDATE, the transition is a no-op and the genuine
// "completed" status is preserved.
func (m *Manager) RecoverStrandedApprovals(ctx context.Context) (int, error) {
	stranded, err := m.config.GetStaleApprovedExecutions(ctx, staleApprovedThreshold)
	if err != nil {
		return 0, fmt.Errorf("failed to list stranded approved executions: %w", err)
	}

	recovered := 0
	for i := range stranded {
		exec := &stranded[i]

		// Idempotent re-drive path (issue #639): all recs honor
		// opts.IdempotencyToken via DeriveIdempotencyToken(idempotencyLineageKey(exec), i),
		// so a second in-place call on the same row is a safe no-op on the
		// provider side. The ExecutionID must be non-empty so the lineage key
		// (or its ExecutionID fallback for legacy rows) is unique; an empty ID
		// would map every legacy row to the same token set.
		if redrivable(exec) {
			counted, driveErr := m.claimAndRedrive(ctx, exec)
			if driveErr != nil {
				return recovered, driveErr
			}
			if counted {
				recovered++
			}
			continue
		}

		// Safe-fail path for mixed/Azure/GCP/legacy executions.
		counted, failErr := m.safeFail(ctx, exec)
		if failErr != nil {
			return recovered, failErr
		}
		if counted {
			recovered++
		}
	}

	return recovered, nil
}

// executableByScheduler reports whether a pending or notified execution may be
// auto-executed by the cron sweep, without
// an explicit human approval action (fail closed on money paths).
//
// Rules:
//   - web-submitted rows (Source == common.PurchaseSourceWeb, "cudly-web") must
//     wait for the token-link approval path; the scheduler must never
//     bypass that gate.
//   - All other pending/notified rows require the owning plan to have
//     AutoPurchase=true. A plan-fetch error, or a missing plan, is propagated
//     so the caller can fail closed rather than defaulting to "execute". A
//     direct-execute row (no PlanID) is never eligible and is not an error.
//   - The global kill switch (GlobalConfig.AutoPurchaseEnabled) must be on. A
//     read error is propagated. Only AutoPurchase plans reach this check, so
//     manual purchases are unaffected.
//   - With 4-eyes approval mode on (GlobalConfig.RequireDifferentApprover), a
//     recommendation-bearing row is never auto-executed: it waits for a human
//     approval in the approval queue (#1771).
//   - A recommendation-bearing row must be "notified" (the notification email is
//     the human veto window; a "pending" row may never have been emailed) and
//     must fit the plan's max_step_commitment (CheckStepCommitmentCap, whose
//     refusal is returned as an error). Rows without recommendations are left
//     to the no-recommendations refusal in executePurchase.
//
// "approved" rows are handled by the session/token approval paths and
// RecoverStrandedApprovals; this helper is only called for pending/notified.
func (m *Manager) executableByScheduler(ctx context.Context, exec *config.PurchaseExecution) (bool, error) {
	// Compare against the typed source constant, not a bare "web" literal: the
	// persisted value is "cudly-web" (common.PurchaseSourceWeb), so the old
	// literal never matched and web rows could be auto-executed without the
	// token-link approval when AutoPurchase=true (fail-open on a money path).
	if exec.Source == common.PurchaseSourceWeb {
		return false, nil
	}
	plan, err := m.autoPurchasePlan(ctx, exec)
	if err != nil {
		return false, err
	}
	if plan == nil {
		return false, nil
	}
	cfg, err := m.autoPurchaseConfig(ctx)
	if err != nil {
		return false, err
	}
	if !cfg.AutoPurchaseEnabled {
		return false, nil
	}
	if len(exec.Recommendations) == 0 {
		return true, nil
	}
	// With 4-eyes on, no step buys without a second person: an auto plan's
	// step waits in the approval queue for a human approval (session approve),
	// like a manual plan's (#1771).
	if cfg.RequireDifferentApprover {
		return false, nil
	}
	if exec.Status != "notified" {
		return false, nil
	}
	if err := CheckStepCommitmentCap(plan, exec.Recommendations); err != nil {
		return false, err
	}
	return true, nil
}

// autoPurchasePlan returns the plan that owns exec when that plan has
// AutoPurchase=true, nil when the row cannot be auto-purchased, and an error
// when the plan cannot be read (the caller fails closed).
//
// A direct-execute row (PlanID == "") has no plan to arm it, so it is never
// auto-purchased. Short-circuit it before the store: the Postgres UUID column
// rejects "" with SQLSTATE 22P02, which every scheduler tick would otherwise
// count as a failure for as long as the row stays pending (issue #243). A nil
// plan with no error is refused as not found, as executePurchase does, rather
// than dereferenced.
func (m *Manager) autoPurchasePlan(ctx context.Context, exec *config.PurchaseExecution) (*config.PurchasePlan, error) {
	if exec.PlanID == "" {
		return nil, nil
	}
	plan, err := m.config.GetPurchasePlan(ctx, exec.PlanID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch plan %s for AutoPurchase gate: %w", exec.PlanID, err)
	}
	if plan == nil {
		return nil, fmt.Errorf("plan %s not found for AutoPurchase gate", exec.PlanID)
	}
	if !plan.AutoPurchase {
		return nil, nil
	}
	return plan, nil
}

// autoPurchaseEnabled reads the global auto-purchase kill switch. It fails
// closed: a read error is returned, never read as "enabled".
func (m *Manager) autoPurchaseEnabled(ctx context.Context) (bool, error) {
	cfg, err := m.autoPurchaseConfig(ctx)
	if err != nil {
		return false, err
	}
	return cfg.AutoPurchaseEnabled, nil
}

// autoPurchaseConfig reads the global config the auto-purchase gates need. A
// read error or a missing config is returned, so no gate reads it as "on".
func (m *Manager) autoPurchaseConfig(ctx context.Context) (*config.GlobalConfig, error) {
	cfg, err := m.config.GetGlobalConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to read global config for the auto-purchase kill switch: %w", err)
	}
	if cfg == nil {
		return nil, errors.New("global config unavailable for the auto-purchase kill switch")
	}
	return cfg, nil
}
