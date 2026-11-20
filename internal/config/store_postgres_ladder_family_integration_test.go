//go:build integration

package config

// Family round-trips through every read that scans a LadderTrancheDB (#1964).

import (
	"sort"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/stretchr/testify/require"
)

func familyOf(rows []LadderDueRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Tranche.Family)
	}
	sort.Strings(out)
	return out
}

func TestLadderTrancheFamily_TwoFamiliesSameLayerAndStepCoexistThroughDueAndClaim(t *testing.T) {
	f := newDueFixture(t)
	at := f.asOf.Add(-time.Minute)
	runID := f.run(t, at, string(ladder.RunStatusPlanned), autoApproveMode())
	a := f.add(t, 1, at, runID, func(tr *LadderTrancheDB) { tr.Family = "m5" })
	b := f.add(t, 2, at, runID, func(tr *LadderTrancheDB) { tr.Family = "c6g" })
	none := f.add(t, 3, at, runID, nil)

	require.Equal(t, []string{"", "c6g", "m5"}, familyOf(f.list(t)), "due read keeps all three rows with their family")

	claimed := f.claimAll(t, a, b, none)
	got := map[string]string{}
	for _, c := range claimed {
		got[c.ID] = c.Family
	}
	require.Equal(t, map[string]string{a.ID: "m5", b.ID: "c6g", none.ID: ""}, got, "claim read round-trips family")
}

func TestLadderTrancheFamily_FiredListRoundTrips(t *testing.T) {
	f := newDueFixture(t)
	tr := f.linked(t, f.asOf.Add(-time.Minute))
	_, err := f.store.db.Exec(f.ctx, `UPDATE ladder_tranches SET status = 'fired', family = 'r6i' WHERE id = $1`, tr.ID)
	require.NoError(t, err)
	fired, err := f.store.ListFiredLadderTranches(f.ctx, nil, 10)
	require.NoError(t, err)
	var got []string
	for _, x := range fired {
		if x.ID == tr.ID {
			got = append(got, x.Family)
		}
	}
	require.Equal(t, []string{"r6i"}, got)
}
