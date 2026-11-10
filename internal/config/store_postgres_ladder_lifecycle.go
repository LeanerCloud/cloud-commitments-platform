package config

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/jackc/pgx/v5"
)

// LadderFireGrace is how late a scheduled ladder tranche may still be fired.
// It is the single definition behind both sides of the boundary: in-flight
// netting counts a scheduled tranche while it is still fireable, and the
// overdue sweep cancels it once it is not. Keeping both on ladderGraceCutoff
// guarantees every scheduled row is counted or canceled, never neither
// (neither would let the next run re-plan an amount the executor can still
// buy, and both would be bought) and never both.
const LadderFireGrace = 24 * time.Hour

// LadderCeilingWindow is the rolling window over which the claim sums the
// commitment already fired for a config against the sweep ceiling. It is not
// LadderFireGrace: the two are equal today but mean different things.
const LadderCeilingWindow = 24 * time.Hour

// The only term and payment option automated ladder buys use (owner decision
// D6=A). Planning and the claim share them so a tranche is sized, stored and
// bought as one offering.
const (
	LadderTerm    = ladder.Term1Year
	LadderPayment = ladder.PaymentNoUpfront
)

// ladderCanceledRunStatuses are both stored spellings of a canceled run: rows
// written before #554 carry the legacy one, which is read and never written.
var ladderCanceledRunStatuses = []string{string(ladder.RunStatusCanceled), "cancelled"} //nolint:misspell // legacy stored ladder status value

// ladderClaimablePredicateSQL is the one definition of which due tranches a
// claim may take beyond the config, status and date window, shared with
// ListDueLadderTranches so the sweep's resolve step cannot drift from the
// claim. Placeholders: $4 layers, $5 term, $6 payment, $7 run window seconds,
// $8 canceled run statuses. Only an auto_approve run that is not canceled
// counts: every run persists tranches whatever its mode, and the config's mode
// can flip after a run was planned for approval by email.
const ladderClaimablePredicateSQL = `amount_basis = 'commitment'
		  AND layer_type = ANY($4::text[])
		  AND term = $5
		  AND payment_option = $6
		  AND EXISTS (
		        SELECT 1 FROM ladder_runs r
		        WHERE r.id = ladder_tranches.run_id
		          AND ladder_tranches.scheduled_date <= r.started_at + $7::bigint * interval '1 second'
		          AND r.status <> ALL($8::text[])
		          AND r.mode = '` + string(ladder.ModeAutoApprove) + `')`

// ladderGraceCutoff returns the instant at or before which a scheduled
// tranche is expired: scheduled_date <= cutoff is canceled, and
// scheduled_date > cutoff is still netted and claimable.
func ladderGraceCutoff(asOf time.Time) time.Time {
	return asOf.Add(-LadderFireGrace)
}

// LadderClaimFilter narrows a claim. Every field is required and fails closed.
type LadderClaimFilter struct {
	// Layers are the layers that have an instrument builder; empty is an error.
	Layers []ladder.LayerType
	// TrancheIDs are the ids the pre-claim resolve step accepted; empty claims nothing.
	TrancheIDs []string
	// CeilingMicros is the config's sweep ceiling in micro-dollars of hourly
	// commitment. The claim keeps the longest prefix of due rows that fits under
	// it minus what the last LadderCeilingWindow already fired; <= 0 is an error.
	CeilingMicros int64
}

// validate rejects an allowlist that would silently match nothing or, if a
// caller later treated it as "no filter", everything.
func (f LadderClaimFilter) validate() error {
	if len(f.Layers) == 0 {
		return errors.New("ClaimDueLadderTranches needs at least one allowed layer")
	}
	for _, l := range f.Layers {
		if err := l.Validate(); err != nil {
			return fmt.Errorf("ClaimDueLadderTranches layer allowlist: %w", err)
		}
	}
	if f.CeilingMicros <= 0 {
		return fmt.Errorf("ClaimDueLadderTranches needs a positive ceiling, got %d micro-dollars", f.CeilingMicros)
	}
	return nil
}

// ladderTrancheEdges lists the legal tranche status transitions. completed,
// failed and canceled are terminal. A fired tranche whose purchase outcome is
// unknown stays fired (and keeps netting) until it is resolved to completed or
// failed.
var ladderTrancheEdges = map[ladder.TrancheStatus][]ladder.TrancheStatus{
	ladder.TrancheStatusScheduled: {ladder.TrancheStatusFired, ladder.TrancheStatusCanceled},
	ladder.TrancheStatusFired:     {ladder.TrancheStatusCompleted, ladder.TrancheStatusFailed},
}

func validateLadderTrancheEdge(from, to ladder.TrancheStatus) error {
	for _, allowed := range ladderTrancheEdges[from] {
		if allowed == to {
			return nil
		}
	}
	return fmt.Errorf("illegal ladder tranche transition %q -> %q", from, to)
}

// TransitionLadderTranche moves one tranche from `from` to `to` with a
// compare-and-set UPDATE. It returns false (and no error) when the row is
// missing or no longer in `from`, so a lost race is distinguishable from a DB
// failure. An illegal edge is an error. scheduled -> fired is not allowed here
// because a claim must create the execution in the same transaction; use
// ClaimDueLadderTranches. A scheduled tranche that already has an execution
// cannot be canceled. reason is stored in status_reason and is required for
// the failed and canceled outcomes, so a terminal tranche always says why.
func (s *PostgresStore) TransitionLadderTranche(ctx context.Context, id string, from, to ladder.TrancheStatus, reason string) (bool, error) {
	var moved bool
	err := s.WithTx(ctx, func(tx pgx.Tx) error {
		var txErr error
		moved, txErr = s.TransitionLadderTrancheTx(ctx, tx, id, from, to, reason)
		return txErr
	})
	return moved, err
}

// TransitionLadderTrancheTx is TransitionLadderTranche inside the caller's
// transaction, for callers that must re-check other rows under lock first.
func (s *PostgresStore) TransitionLadderTrancheTx(ctx context.Context, tx pgx.Tx, id string, from, to ladder.TrancheStatus, reason string) (bool, error) {
	if err := validateLadderTrancheEdge(from, to); err != nil {
		return false, err
	}
	if from == ladder.TrancheStatusScheduled && to == ladder.TrancheStatusFired {
		return false, fmt.Errorf("ladder tranche %s: scheduled -> fired must go through ClaimDueLadderTranches", id)
	}
	if reason == "" && (to == ladder.TrancheStatusFailed || to == ladder.TrancheStatusCanceled) {
		return false, fmt.Errorf("ladder tranche %s: a %s transition needs a status reason", id, to)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE ladder_tranches
		SET status = $3, status_reason = NULLIF($4, ''),
		    settled_at = CASE WHEN $3 = 'completed' THEN clock_timestamp() ELSE settled_at END
		WHERE id = $1
		  AND status = $2
		  AND ($2 <> 'scheduled' OR execution_id IS NULL)
	`, id, string(from), string(to), reason)
	if err != nil {
		return false, fmt.Errorf("failed to transition ladder_tranche id=%s %s -> %s: %w", id, from, to, err)
	}
	return tag.RowsAffected() == 1, nil
}

// ClaimDueLadderTranches atomically claims up to limit tranches of configID
// that are due (scheduled_date <= asOf) but not expired (scheduled_date >
// asOf - LadderFireGrace). For each claimed row, in the same transaction,
// create runs and must return the id of the purchase_executions row it
// created; the tranche is then set to fired with that execution_id. If create
// returns an error the whole transaction rolls back and nothing is claimed, so
// a claim and its execution row can never disagree after a crash. Rows locked
// by a concurrent claim are skipped (FOR UPDATE SKIP LOCKED), so exactly one
// caller wins each tranche. The whole claim runs under the config's ladder
// lock (taken first), including the create callback: with a caller asOf earlier
// than the planner's, a claim could otherwise fire a row the planner's netting
// read had already excluded as expired, and the row would be bought AND
// re-planned. A callback slower than ladderLockTimeout makes planners fail loudly.
//
// Only rows stored in commitment dollars (amount_basis = 'commitment', which
// the table constraint pairs with a commitment_rate) are claimable: a buyer
// spends amount_usd_hr as commitment dollars, and a legacy NULL-basis or
// on_demand_equivalent row would over-buy by 1/rate (#946). Those rows stay
// netted until the overdue sweep cancels them, and the next run re-plans
// their gap in commitment dollars.
//
// The filter fails closed. Empty or unknown Layers is an error and nothing is
// locked. Empty TrancheIDs claims nothing, and IDs only intersect with the
// other predicates (config, status, basis and the due window at this asOf), so
// a stale or foreign id never widens the claim. A layer without an instrument
// builder must not be listed in Layers.
//
// Only tranches at the owner-decided offering (LadderTerm, LadderPayment) whose
// run exists and started no earlier than LadderFireGrace before their date are
// claimable: a ramp step planned days ahead on stale usage, a tranche without a
// run and any other offering are refused in SQL. A refused row is left
// scheduled, keeps netting and is canceled as expired by the overdue sweep.
// Run rows are only read, never locked, so a concurrent amend is not blocked.
//
// CeilingMicros caps the commitment fired in any rolling LadderCeilingWindow:
// after the filters, the claim keeps the longest prefix (in date, id order)
// whose exact micro-dollar sum fits under CeilingMicros minus every tranche of
// this config whose execution was created in the window, in any status. It
// stops at the first row that does not fit instead of skipping to a smaller
// later one, and an exhausted budget claims nothing without an error. Trimmed
// rows stay scheduled.
func (s *PostgresStore) ClaimDueLadderTranches(
	ctx context.Context,
	configID string,
	asOf time.Time,
	limit int,
	filter LadderClaimFilter,
	create func(ctx context.Context, tx pgx.Tx, tranche LadderTrancheDB) (executionID string, err error),
) ([]LadderTrancheDB, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("ClaimDueLadderTranches limit must be positive, got %d", limit)
	}
	if err := filter.validate(); err != nil {
		return nil, err
	}
	var claimed []LadderTrancheDB
	err := s.withLadderConfigLock(ctx, configID, func(tx pgx.Tx) error {
		due, err := claimableLadderTranchesTx(ctx, tx, configID, asOf, limit, filter)
		if err != nil {
			return err
		}
		for i := range due {
			execID, err := create(ctx, tx, due[i])
			if err != nil {
				return fmt.Errorf("create execution for ladder_tranche id=%s: %w", due[i].ID, err)
			}
			if execID == "" {
				return fmt.Errorf("create execution for ladder_tranche id=%s returned an empty execution id", due[i].ID)
			}
			if _, err := tx.Exec(ctx, `
				UPDATE ladder_tranches SET status = $2, execution_id = $3
				WHERE id = $1 AND status = 'scheduled' AND execution_id IS NULL
			`, due[i].ID, string(ladder.TrancheStatusFired), execID); err != nil {
				return fmt.Errorf("failed to claim ladder_tranche id=%s: %w", due[i].ID, err)
			}
			due[i].Status = ladder.TrancheStatusFired
			due[i].ExecutionID = &execID
		}
		claimed = due
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// selectDueLadderTranchesTx returns the locked due rows and, in the same order,
// each row's amount in exact micro-dollars.
func selectDueLadderTranchesTx(ctx context.Context, tx pgx.Tx, configID string, asOf time.Time, limit int, filter LadderClaimFilter) ([]LadderTrancheDB, []int64, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, config_id, run_id, layer_type, amount_usd_hr, term,
		       payment_option, scheduled_date, status, settled_at, created_at, amount_basis, commitment_rate,
		       (amount_usd_hr * 1000000)::bigint
		FROM ladder_tranches
		WHERE config_id = $1
		  AND status = 'scheduled'
		  AND execution_id IS NULL
		  AND scheduled_date <= $2
		  AND scheduled_date > $3
		  AND `+ladderClaimablePredicateSQL+`
		  AND id = ANY($10::uuid[])
		ORDER BY scheduled_date, id
		LIMIT $9
		FOR UPDATE SKIP LOCKED
	`, configID, asOf, ladderGraceCutoff(asOf), filter.Layers, string(LadderTerm), string(LadderPayment),
		int64(LadderFireGrace/time.Second), ladderCanceledRunStatuses, limit, filter.TrancheIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to select due ladder_tranches config_id=%s: %w", configID, err)
	}
	defer rows.Close()
	var out []LadderTrancheDB
	var micros []int64
	for rows.Next() {
		var tr LadderTrancheDB
		var amountMicros int64
		if err := rows.Scan(&tr.ID, &tr.ConfigID, &tr.RunID, &tr.LayerType, &tr.AmountUSDHr, &tr.Term,
			&tr.PaymentOption, &tr.ScheduledDate, &tr.Status, &tr.SettledAt, &tr.CreatedAt, &tr.AmountBasis, &tr.CommitmentRate, &amountMicros); err != nil {
			return nil, nil, fmt.Errorf("failed to scan due ladder_tranche: %w", err)
		}
		if _, err := trancheCommitmentRate(&tr); err != nil {
			return nil, nil, fmt.Errorf("invalid due ladder_tranche %s: %w", tr.ID, err)
		}
		out = append(out, tr)
		micros = append(micros, amountMicros)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("failed to read due ladder_tranches: %w", err)
	}
	return out, micros, nil
}
