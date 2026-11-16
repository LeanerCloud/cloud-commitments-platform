package config

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

// CountLadderRunTrancheStatuses returns how many of a run's tranches sit in
// each stored status. The keys are the raw stored statuses, so a legacy
// spelling shows up as its own key; the caller decides what counts as settled.
func (s *PostgresStore) CountLadderRunTrancheStatuses(ctx context.Context, runID string) (map[ladder.TrancheStatus]int, error) {
	rows, err := s.db.Query(ctx, `SELECT status, COUNT(*) FROM ladder_tranches WHERE run_id = $1 GROUP BY status`, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to count ladder_tranches for run_id=%s: %w", runID, err)
	}
	defer rows.Close()
	out := map[ladder.TrancheStatus]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("failed to scan ladder_tranche status count: %w", err)
		}
		out[ladder.TrancheStatus(status)] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read ladder_tranche status counts: %w", err)
	}
	return out, nil
}
