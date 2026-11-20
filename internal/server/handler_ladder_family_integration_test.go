//go:build integration

package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

func familyRowAmounts(ctx context.Context, t *testing.T, e *ladderB1Env, runID string) map[string]float64 {
	t.Helper()
	rs, err := e.conn.Query(ctx, `SELECT family, amount_usd_hr::float8 FROM ladder_tranches WHERE run_id=$1`, runID)
	require.NoError(t, err)
	defer rs.Close()
	out := map[string]float64{}
	for rs.Next() {
		var f string
		var a float64
		require.NoError(t, rs.Scan(&f, &a))
		out[f] += a
	}
	require.NoError(t, rs.Err())
	return out
}

// #1964: PlanAndSaveLadderRun against Postgres with two families. The first run
// persists one family-bound row each; the second run reads them back as netting
// rows WITH their family, so a canceled m5 row reopens only m5 headroom.
func TestLadderFamily_PlanAndSaveNetsPerFamilyAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn := ladderB1Conn(ctx, t)
	env := newLadderB1Env(ctx, t, conn, []pkgladder.RampStep{{AfterDays: 0, Fraction: 1.0}})
	capUSD := 1000.0
	env.cfg.MaxHourlyCommitPerRun = &capUSD
	env.cap2 = familyLadderCapability(t, map[string]float64{"m5": 6, "c5": 2})

	run1 := env.run(ctx, t, env.start)
	first := familyRowAmounts(ctx, t, env, run1)
	require.Len(t, first, 2)
	assert.InDelta(t, 4.8, first["m5"], 1e-6)
	assert.InDelta(t, 1.6, first["c5"], 1e-6)

	plan1, _ := env.planAndTotal(ctx, t, run1)
	seen := map[string]bool{}
	for _, a := range plan1.Actions {
		if a.Action == string(pkgladder.ActionPurchase) {
			seen[a.Family] = true
		}
	}
	assert.Equal(t, map[string]bool{"m5": true, "c5": true}, seen, "plan JSON carries both families")

	run2 := env.run(ctx, t, env.start.Add(time.Hour))
	assert.Empty(t, familyRowAmounts(ctx, t, env, run2), "both families netted by their stored family rows")

	_, err := conn.Exec(ctx, `UPDATE ladder_tranches SET status='canceled' WHERE run_id=$1 AND family='m5'`, run1)
	require.NoError(t, err)
	run3 := env.run(ctx, t, env.start.Add(2*time.Hour))
	third := familyRowAmounts(ctx, t, env, run3)
	assert.InDelta(t, 4.5, third["m5"], 1e-5, "m5 headroom reopened (gap 4.8 split 6:0.4)")
	assert.InDelta(t, 0.3, third["c5"], 1e-5, "c5 stays netted by its own row")
}
