package config

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

// LadderStore holds the ladder methods of StoreInterface; StoreInterface embeds it.
type LadderStore interface {
	// Ladder configuration (per-account, per-provider).
	// GetLadderConfigs returns the rows newest first, capped at MaxListLimit.
	// GetLadderConfig returns the single row for (cloudAccountID, provider),
	// or (nil, nil) when no row exists.
	// UpsertLadderConfig inserts or updates via the UNIQUE(cloud_account_id, provider)
	// constraint and returns the persisted row with all DB-stamped fields populated.
	GetLadderConfigs(ctx context.Context) ([]LadderConfigDB, error)
	GetLadderConfig(ctx context.Context, cloudAccountID, provider string) (*LadderConfigDB, error)
	UpsertLadderConfig(ctx context.Context, cfg *LadderConfigDB) (*LadderConfigDB, error)
	// GetLadderSweepCeiling and SetLadderSweepCeiling read and write the
	// per-sweep ceiling (store_postgres_ladder_ceiling.go); both return
	// ErrLadderCeilingUnavailable until the migration is applied.
	GetLadderSweepCeiling(ctx context.Context, configID string) (*int64, error)
	SetLadderSweepCeiling(ctx context.Context, configID string, usdPerHr *float64) error

	// Ladder run/tranche persistence (migration 000080/000081, PR-2).
	//
	// SaveLadderRun inserts a new ladder_runs row, returning the persisted row
	// with all DB-stamped fields (id, created_at, updated_at) populated.
	// If run.ID is empty, a new UUID is generated before the insert.
	//
	// GetLadderRun returns the row for the given id, or (nil, nil) when no
	// row exists (mirrors GetLadderConfig semantics).
	//
	// SaveLadderTranches inserts a batch of ladder_tranches rows inside a
	// single transaction. Each tranche must carry a non-empty ID; duplicate
	// IDs within the batch are rejected at the DB UNIQUE constraint level.
	//
	// LatestLadderRunStartedAt returns the maximum started_at for the given
	// config_id, or nil when no run has been recorded yet. Powers the per-cadence
	// self-gate in the scheduler (Q6).
	//
	// TransitionLadderRunStatus atomically updates the status of a ladder_runs
	// row from one of the fromStatuses to toStatus, returning the updated row.
	// Returns (nil, nil) when zero rows are affected (CAS race lost or wrong
	// current status), so callers can distinguish a race from a hard error.
	// Statuses are typed ladder.RunStatus so callers cannot pass an arbitrary
	// string that would never match a stored status. A transition to
	// RunStatusCanceled is refused (ErrLadderRunCancelViaTransition).
	//
	// CancelLadderRun is the only way to cancel a ladder run: a CAS UPDATE
	// from one of fromStatuses to cancelled that stamps cancelled_by with the
	// actor's email in the same statement. An empty actor is refused
	// (ErrLadderRunCancelActorRequired); (nil, nil) means the CAS lost.
	// PlanAndSaveLadderRun is the ladder planning persist: in ONE READ COMMITTED
	// transaction under the config's advisory lock it reads the in-flight
	// commitment (scheduled rows within LadderFireGrace plus fired rows), calls
	// build with it, then inserts the run, sweeps overdue tranches and inserts
	// the new ones (append-only, L5). Overlapping runs for one config serialize:
	// the second nets the first's committed rows. build must be pure compute
	// (no I/O); run.StartedAt must equal asOf. asOf MUST be captured before the
	// caller reads the existing commitment (E): tranches completed after asOf
	// are added to the in-flight figure, so one that completes between the E
	// read and the locked read is netted, never missed (a double count is a
	// safe under-buy). This holds only if a tranche is marked completed after
	// its commitment is visible through E's source (executor PR5a). The lock is
	// also taken first by ClaimDueLadderTranches.
	PlanAndSaveLadderRun(ctx context.Context, configID string, asOf time.Time, build LadderPlanBuilder) (*LadderRunDB, error)
	SaveLadderRun(ctx context.Context, run *LadderRunDB) (*LadderRunDB, error)
	GetLadderRun(ctx context.Context, id string) (*LadderRunDB, error)
	SaveLadderTranches(ctx context.Context, tranches []LadderTrancheDB) error
	LatestLadderRunStartedAt(ctx context.Context, configID string) (*time.Time, error)
	// LatestLadderRunID returns the id of the config's run with the latest
	// started_at, or an error wrapping ErrNotFound when it has none.
	LatestLadderRunID(ctx context.Context, configID string) (string, error)
	TransitionLadderRunStatus(ctx context.Context, id string, fromStatuses []ladder.RunStatus, toStatus ladder.RunStatus) (*LadderRunDB, error)
	CancelLadderRun(ctx context.Context, id string, fromStatuses []ladder.RunStatus, cancelledBy string) (*LadderRunDB, error)
	// CountLadderRunTrancheStatuses returns the number of the run's tranches
	// per stored status (raw keys, so a legacy spelling is its own key).
	CountLadderRunTrancheStatuses(ctx context.Context, runID string) (map[ladder.TrancheStatus]int, error)
}
