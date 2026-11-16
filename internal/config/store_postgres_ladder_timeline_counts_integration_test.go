//go:build integration

package config

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/stretchr/testify/require"
)

// The pre-cloud-commitments-go#554 stored spelling, split so the spell checker accepts it.
const legacyCancelled = "cancel" + "led"

func TestLadderTimelineRunsTrancheCountsAndDelayedFirstStep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	store := setupLadderStore(ctx, t)
	cfgID := seedLadderConfig(ctx, t, store)
	var accountID string
	require.NoError(t, store.db.QueryRow(ctx, "SELECT cloud_account_id FROM ladder_configs WHERE id=$1", cfgID).Scan(&accountID))
	start := time.Now().UTC()
	newRun := func(cfg string) string {
		run, err := store.SaveLadderRun(ctx, &LadderRunDB{ConfigID: &cfg, Status: ladder.RunStatusPlanned, StartedAt: start, Plan: []byte(`{}`)})
		require.NoError(t, err)
		return run.ID
	}
	mixed, empty, delayed, onTime := newRun(cfgID), newRun(cfgID), newRun(cfgID), newRun(cfgID)
	insert := func(cfg, run, status string, offset time.Duration) {
		_, err := store.db.Exec(ctx, `INSERT INTO ladder_tranches(config_id,run_id,layer_type,amount_usd_hr,term,payment_option,scheduled_date,status,settled_at)
 VALUES ($1,$2,'compute-sp',1,'1yr','no-upfront',$3,$4::text,CASE WHEN $4::text = 'completed' THEN NOW() END)`, cfg, run, start.Add(offset), status)
		require.NoError(t, err)
	}
	for _, status := range []string{"scheduled", "scheduled", "fired", "completed", "completed", "completed", "failed", "canceled", legacyCancelled, legacyCancelled} {
		insert(cfgID, mixed, status, 0)
	}
	insert(cfgID, delayed, "scheduled", LadderFireGrace+time.Hour)
	insert(cfgID, delayed, "scheduled", LadderFireGrace+48*time.Hour)
	insert(cfgID, onTime, "scheduled", LadderFireGrace)
	insert(cfgID, onTime, "scheduled", 30*24*time.Hour)

	// Another account: its run and counts must never reach this account's read,
	// and a tranche of its config that points at one of our runs is not ours.
	otherCfg := seedLadderConfigWithExtID(ctx, t, store, "222222222222")
	otherRun := newRun(otherCfg)
	for i := 0; i < 5; i++ {
		insert(otherCfg, otherRun, "completed", 0)
	}
	insert(otherCfg, mixed, "failed", 0)
	insert(otherCfg, delayed, "scheduled", 0)

	page, err := store.ListLadderTimelineRuns(ctx, accountID, "aws", nil)
	require.NoError(t, err)
	byID := map[string]LadderTimelineRun{}
	for _, run := range page.Runs {
		byID[run.ID] = run
	}
	require.Len(t, byID, 4, "the other account's run is out of scope")
	require.NotContains(t, byID, otherRun)
	require.Equal(t, LadderTrancheCounts{Scheduled: 2, Fired: 1, Completed: 3, Failed: 1, Canceled: 3}, byID[mixed].TrancheCounts)
	require.False(t, byID[mixed].DelayedFirstStep)
	require.Equal(t, LadderTrancheCounts{}, byID[empty].TrancheCounts)
	require.False(t, byID[empty].DelayedFirstStep, "no tranches, no delayed first step")
	require.Equal(t, LadderTrancheCounts{Scheduled: 2}, byID[delayed].TrancheCounts)
	require.True(t, byID[delayed].DelayedFirstStep)
	require.False(t, byID[onTime].DelayedFirstStep, "a first step exactly at the grace boundary is still claimable")

	otherPage, err := store.ListLadderTimelineRuns(ctx, func() string {
		var id string
		require.NoError(t, store.db.QueryRow(ctx, "SELECT cloud_account_id FROM ladder_configs WHERE id=$1", otherCfg).Scan(&id))
		return id
	}(), "aws", nil)
	require.NoError(t, err)
	require.Len(t, otherPage.Runs, 1)
	require.Equal(t, LadderTrancheCounts{Completed: 5}, otherPage.Runs[0].TrancheCounts)
}
