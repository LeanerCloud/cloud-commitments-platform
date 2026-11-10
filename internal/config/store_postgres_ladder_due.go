package config

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

// LadderDueRow is a tranche due by date and whether ClaimDueLadderTranches
// could take it given the layer allowlist, as decided by the claim's own
// predicate.
type LadderDueRow struct {
	Tranche      LadderTrancheDB
	SQLClaimable bool
}

// ListDueLadderTranches returns up to limit scheduled tranches of configID that
// are due by date (scheduled_date in (asOf - LadderFireGrace, asOf]), whatever
// their layer, term or basis, in the claim's date, id order. It is the sweep's
// pre-claim resolve read: a plain read with no lock, so the claim stays the
// authority. A corrupt row (basis and rate disagree) is returned as it is,
// never an error, so one bad row cannot hide the good ones; the caller must
// resolve it as unclaimable. Like the claim it fails on a non-positive limit
// or an empty or unknown layer allowlist.
func (s *PostgresStore) ListDueLadderTranches(ctx context.Context, configID string, asOf time.Time, limit int, layers []ladder.LayerType) ([]LadderDueRow, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("ListDueLadderTranches limit must be positive, got %d", limit)
	}
	if len(layers) == 0 {
		return nil, errors.New("ListDueLadderTranches needs at least one allowed layer")
	}
	for _, l := range layers {
		if err := l.Validate(); err != nil {
			return nil, fmt.Errorf("ListDueLadderTranches layer allowlist: %w", err)
		}
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, config_id, run_id, layer_type, amount_usd_hr, term,
		       payment_option, scheduled_date, status, settled_at, created_at, amount_basis, commitment_rate,
		       COALESCE(`+ladderClaimablePredicateSQL+`, false)
		FROM ladder_tranches
		WHERE config_id = $1
		  AND status = 'scheduled'
		  AND execution_id IS NULL
		  AND scheduled_date <= $2
		  AND scheduled_date > $3
		ORDER BY scheduled_date, id
		LIMIT $9
	`, configID, asOf, ladderGraceCutoff(asOf), layers, string(LadderTerm), string(LadderPayment),
		int64(LadderFireGrace/time.Second), ladderCanceledRunStatuses, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list due ladder_tranches config_id=%s: %w", configID, err)
	}
	defer rows.Close()
	var out []LadderDueRow
	for rows.Next() {
		var d LadderDueRow
		tr := &d.Tranche
		if err := rows.Scan(&tr.ID, &tr.ConfigID, &tr.RunID, &tr.LayerType, &tr.AmountUSDHr, &tr.Term,
			&tr.PaymentOption, &tr.ScheduledDate, &tr.Status, &tr.SettledAt, &tr.CreatedAt, &tr.AmountBasis, &tr.CommitmentRate, &d.SQLClaimable); err != nil {
			return nil, fmt.Errorf("failed to scan due ladder_tranche: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read due ladder_tranches: %w", err)
	}
	return out, nil
}
