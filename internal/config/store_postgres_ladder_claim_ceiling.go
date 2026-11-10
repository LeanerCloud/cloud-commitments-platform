package config

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// claimableLadderTranchesTx selects the due rows under lock and trims them to
// the sweep ceiling. The caller holds the config's ladder lock, so no other
// claim of this config can add to the in-flight sum between the read and the
// claim.
func claimableLadderTranchesTx(ctx context.Context, tx pgx.Tx, configID string, asOf time.Time, limit int, filter LadderClaimFilter) ([]LadderTrancheDB, error) {
	due, micros, err := selectDueLadderTranchesTx(ctx, tx, configID, asOf, limit, filter)
	if err != nil || len(due) == 0 {
		return nil, err
	}
	inFlight, err := inFlightLadderMicros(ctx, tx, configID, asOf)
	if err != nil {
		return nil, err
	}
	n := ladderCeilingPrefix(micros, filter.CeilingMicros-inFlight)
	if n == 0 {
		return nil, nil
	}
	return due[:n], nil
}

// inFlightLadderMicros sums, in exact micro-dollars, every tranche of the
// config (any status) whose execution was created within LadderCeilingWindow
// before asOf. A row created exactly one window ago has aged out.
func inFlightLadderMicros(ctx context.Context, tx pgx.Tx, configID string, asOf time.Time) (int64, error) {
	var sum int64
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM((t.amount_usd_hr * 1000000)::bigint), 0)::bigint
		FROM ladder_tranches t
		JOIN purchase_executions e ON e.execution_id = t.execution_id
		WHERE t.config_id = $1
		  AND e.created_at > $2
	`, configID, asOf.Add(-LadderCeilingWindow)).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("failed to sum in-flight ladder commitment config_id=%s: %w", configID, err)
	}
	return sum, nil
}

// ladderCeilingPrefix returns how many leading amounts fit within budget. It
// stops at the first amount that does not fit, so an oversized head row blocks
// later smaller ones, and compares against the remaining budget so the running
// sum can never overflow.
func ladderCeilingPrefix(amounts []int64, budget int64) int {
	remaining := budget
	for i, a := range amounts {
		if remaining <= 0 || a > remaining {
			return i
		}
		remaining -= a
	}
	return len(amounts)
}
