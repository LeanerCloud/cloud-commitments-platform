//go:build integration

package config

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCountLadderRunTrancheStatuses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)
	cfgID := seedLadderConfig(ctx, t, store)
	at := time.Now().UTC().Add(time.Hour)
	newRun := func() string {
		run, err := store.SaveLadderRun(ctx, &LadderRunDB{ConfigID: &cfgID, StartedAt: at, Status: ladder.RunStatusPlanned})
		require.NoError(t, err)
		return run.ID
	}
	runA, runB := newRun(), newRun()
	var rows []LadderTrancheDB
	for run, statuses := range map[string][]ladder.TrancheStatus{
		runA: {ladder.TrancheStatusCompleted, ladder.TrancheStatusCompleted, ladder.TrancheStatusFailed, ladder.TrancheStatusScheduled},
		runB: {ladder.TrancheStatusFired},
	} {
		for _, st := range statuses {
			tr := lifecycleTranche(cfgID, 1, st, at)
			tr.RunID = &run
			rows = append(rows, tr)
		}
	}
	require.NoError(t, store.SaveLadderTranches(ctx, rows))

	got, err := store.CountLadderRunTrancheStatuses(ctx, runA)
	require.NoError(t, err)
	assert.Equal(t, map[ladder.TrancheStatus]int{
		ladder.TrancheStatusCompleted: 2, ladder.TrancheStatusFailed: 1, ladder.TrancheStatusScheduled: 1,
	}, got, "only this run's tranches are counted")

	got, err = store.CountLadderRunTrancheStatuses(ctx, newRun())
	require.NoError(t, err)
	assert.Empty(t, got, "a run without tranches has no counts")
}
