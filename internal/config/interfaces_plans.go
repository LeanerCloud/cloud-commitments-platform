package config

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// PlanStore holds the purchase-plan methods of StoreInterface; StoreInterface embeds it.
type PlanStore interface {
	// Purchase plans
	CreatePurchasePlan(ctx context.Context, plan *PurchasePlan) error
	// CreatePurchasePlanWithAccounts creates plan and attaches accountIDs in
	// one transaction. It returns *PlanNameTakenError (errors.Is
	// ErrPlanNameTaken) when a plan whose accounts satisfy visible already has
	// the same name, compared with lower(btrim(name, E' \t\r\n')). visible must be pure.
	CreatePurchasePlanWithAccounts(ctx context.Context, plan *PurchasePlan, accountIDs []string, visible func(accounts []CloudAccount) bool) error
	GetPurchasePlan(ctx context.Context, planID string) (*PurchasePlan, error)
	// UpdatedAt must be the read version; a stale version returns ErrPurchasePlanConflict.
	UpdatePurchasePlan(ctx context.Context, plan *PurchasePlan) error
	StampPlanNotificationSent(ctx context.Context, planID string, at time.Time) error
	// CompletePlanStep records that ramp step stepNumber finished and advances
	// the plan's schedule to it, inside a SELECT FOR UPDATE transaction that
	// prevents the concurrent-write lost-update race of issue #1071.
	//
	// It is idempotent in stepNumber: completing a step the plan has already
	// counted writes nothing. Which sentinel says so depends on where the ramp
	// sits relative to the completing step, and callers branch on the
	// difference (purchase.recordRampAdvanceRefusal does):
	//
	//   - CurrentStep == stepNumber reports ErrRampStepCountedBySibling. This
	//     is the issue #1669 scenario: an operator retries two separately-failed
	//     accounts of one ramp step, and the second completion finds the ramp
	//     already sitting on that step because the first advanced it. The step
	//     WAS counted, so this is routine and is not recorded against the
	//     execution.
	//   - CurrentStep > stepNumber reports ErrRampStepAlreadyCounted. The ramp
	//     moved past the step earlier, so this purchase bought commitment the
	//     plan will never count. That is an anomaly and IS recorded.
	//
	// A step counts as complete only when EVERY cloud account the step fanned
	// out to has bought, not when any one of them has (issue #1861). Until
	// then it reports ErrRampStepIncomplete, which is a "not yet" rather than a
	// failure of the purchase that just completed -- the purchase is real and
	// already recorded; only the plan's position is withheld.
	CompletePlanStep(ctx context.Context, planID string, stepNumber int) error
	// GetStuckRampSteps returns, keyed by plan ID, the plan's next ramp step
	// and how many of that step's executions have a terminal, unsuccessful
	// latest attempt with no retry in flight -- a ramp CompletePlanStep will
	// keep refusing to advance until an operator intervenes. Plans with
	// nothing stuck are absent from the map. Feeds the plan-health
	// ramp_blocked factor (issue #1861).
	GetStuckRampSteps(ctx context.Context) (map[string]RampStepBlock, error)
	// GetDoomedNextRoots returns, keyed by plan ID, the plans whose next
	// empty root row is dated where the notification tick can never attach
	// recommendations (inside the notice window, or any future date when the
	// plan has no window), evaluated at now. Plans with nothing doomed are
	// absent. Feeds the plan-health step_will_not_attach factor.
	GetDoomedNextRoots(ctx context.Context, now time.Time) (map[string]DoomedRoot, error)
	// LockPurchasePlanTx reads a plan under a row lock held for the rest of tx.
	// It is the per-plan ramp lock CompletePlanStep takes, exposed so a caller
	// that reads a plan's ramp position and then writes against it does so
	// atomically with respect to concurrent completions and creations.
	// Returns (nil, nil) when the plan does not exist.
	LockPurchasePlanTx(ctx context.Context, tx pgx.Tx, planID string) (*PurchasePlan, error)
	// LockPlanProvidersTx takes the transaction-scoped advisory lock of every
	// named provider, in sorted order, so two plan steps that resolve the same
	// provider's recommendations cannot both attach the same capacity. Lock
	// order is advisory lock, then LockPurchasePlanTx. The ladder config lock
	// (PlanAndSaveLadderRun, ClaimDueLadderTranches, AmendLadderTranche) is always taken BEFORE both:
	// ClaimDueLadderTranches's create callback may take these locks, so they
	// come after the ladder lock; code holding an execution or tranche row
	// (retry linker, reconciler) must never take the ladder lock. No cycle.
	// Duplicates are taken once; an empty list and an empty provider name are
	// errors.
	LockPlanProvidersTx(ctx context.Context, tx pgx.Tx, providers []string) error
	// OccupiedRampStepsInRangeTx returns the steps in [from, to] of planID that
	// already have a fan-out unit which bought or is still working, ascending.
	// Callers about to mint executions for a step range use it to refuse a step
	// that is already covered, which would otherwise re-fan-out across accounts
	// that already committed under a fresh idempotency lineage (issue #1861).
	// Requires the caller to hold LockPurchasePlanTx on planID in the same
	// transaction; without it the answer is advisory and two concurrent callers
	// both see the step free.
	OccupiedRampStepsInRangeTx(ctx context.Context, tx pgx.Tx, planID string, from, to int) ([]int, error)
	// ListExecutionsForPlanStepTx returns EVERY execution row (any status,
	// account or retry attempt) recorded for stepNumber of planID. The
	// get-or-create of a plan step reads it under LockPurchasePlanTx so any
	// existing row, settled or not, blocks minting a second root (platform#631).
	ListExecutionsForPlanStepTx(ctx context.Context, tx pgx.Tx, planID string, stepNumber int) ([]PurchaseExecution, error)
	// SetPlanRampBaselineTx freezes the plan's ramp baseline if none is set and
	// reports whether it wrote. Immutable once set; not part of
	// UpdatePurchasePlanTx, so a stale plan save cannot touch it. The caller
	// holds LockPurchasePlanTx.
	SetPlanRampBaselineTx(ctx context.Context, tx pgx.Tx, planID string, baseline PlanRampBaseline) (bool, error)
	// GetPlanRampBaselineTx returns the frozen baseline, nil when none is set.
	GetPlanRampBaselineTx(ctx context.Context, tx pgx.Tx, planID string) (PlanRampBaseline, error)
	// ListPlanCommittedTx returns what the plan's executions already hold per
	// recommendation identity (see its implementation for the status rules).
	// The caller holds LockPurchasePlanTx.
	ListPlanCommittedTx(ctx context.Context, tx pgx.Tx, planID string) (map[string]PlanRampQuantity, error)
	// CompleteTargetMetStepTx records a plan step whose cumulative target is
	// already held as a completed no-op row (new, or rootID's empty root row
	// completed in place) and advances the ramp over it in the same
	// transaction. The caller holds LockPurchasePlanTx.
	CompleteTargetMetStepTx(ctx context.Context, tx pgx.Tx, planID string, stepNumber int, rootID, note string) error
	// SetExecutionRecommendationsIfEmptyTx attaches resolved recommendations
	// and their totals to a pending/notified execution that has none yet.
	// Returns false when another resolver already attached them (or the row
	// left pending/notified); the caller re-reads instead of overwriting.
	SetExecutionRecommendationsIfEmptyTx(ctx context.Context, tx pgx.Tx, executionID string, recs []RecommendationRecord, upfront, savings float64) (bool, error)
	// ExpireExecutionAtomic flips a pending/notified execution to expired in
	// tx so its suppressions can be deleted in the same transaction. Returns
	// false when the row had already left pending/notified or its
	// scheduled_date no longer equals observedScheduledDate (a re-armed root
	// carries a new date, so a stale sweep list cannot expire it again).
	ExpireExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID string, observedScheduledDate time.Time) (bool, error)
	// UpdatePurchasePlanTx checks UpdatedAt and replaces it with the returned version.
	// The caller must discard the transaction-local plan if the transaction rolls back.
	UpdatePurchasePlanTx(ctx context.Context, tx pgx.Tx, plan *PurchasePlan) error
	// UpdatePurchasePlanGated is UpdatePurchasePlan with gate run under the
	// plan's row lock, before the write (nil gate: plain update).
	UpdatePurchasePlanGated(ctx context.Context, plan *PurchasePlan, gate PlanWriteGate) error
	DeletePurchasePlan(ctx context.Context, planID string) error
	ListPurchasePlans(ctx context.Context, filter PurchasePlanFilter) ([]PurchasePlan, error)
}
