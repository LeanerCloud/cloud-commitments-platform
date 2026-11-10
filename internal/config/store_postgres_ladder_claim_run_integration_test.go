//go:build integration

package config

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/stretchr/testify/require"
)

// saveClaimable saves rows that a claim test expects to find. Each config gets
// one planned run started at its latest row date, so every row sits inside the
// run-start window and rows[i].RunID is set in place. Rows with a run keep it.
func saveClaimable(ctx context.Context, t *testing.T, store *PostgresStore, rows []LadderTrancheDB) {
	t.Helper()
	latest := make(map[string]time.Time)
	for i := range rows {
		require.NotNil(t, rows[i].ConfigID)
		if rows[i].RunID == nil && rows[i].ScheduledDate.After(latest[*rows[i].ConfigID]) {
			latest[*rows[i].ConfigID] = rows[i].ScheduledDate
		}
	}
	runIDs := make(map[string]string, len(latest))
	for cfg, start := range latest {
		cfgID := cfg
		run, err := store.SaveLadderRun(ctx, &LadderRunDB{ConfigID: &cfgID, StartedAt: start, Status: ladder.RunStatusPlanned})
		require.NoError(t, err)
		runIDs[cfg] = run.ID
	}
	for i := range rows {
		if rows[i].RunID == nil {
			id := runIDs[*rows[i].ConfigID]
			rows[i].RunID = &id
		}
	}
	require.NoError(t, store.SaveLadderTranches(ctx, rows))
}
