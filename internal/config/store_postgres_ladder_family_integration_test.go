//go:build integration

package config

// Family round-trips through every read that scans a LadderTrancheDB (#1964).

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
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

func TestLadderTrancheFamily_NettingRowsAndTimelineCarryFamily(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	store := setupLadderStore(ctx, t)
	cfgID := seedLadderConfig(ctx, t, store)
	var accountID string
	require.NoError(t, store.db.QueryRow(ctx, "SELECT cloud_account_id FROM ladder_configs WHERE id=$1", cfgID).Scan(&accountID))
	run, err := store.SaveLadderRun(ctx, &LadderRunDB{ConfigID: &cfgID, Status: ladder.RunStatusPlanned, StartedAt: time.Now().UTC(), Plan: []byte(`{}`)})
	require.NoError(t, err)
	_, err = store.db.Exec(ctx, `INSERT INTO ladder_tranches(config_id,run_id,layer_type,amount_usd_hr,amount_basis,term,payment_option,scheduled_date,status,family,settled_at,created_at)
 VALUES ($1,$2,'ec2-sp',1,'on_demand_equivalent','1yr','no-upfront',NOW(),'fired','m5',NULL,NOW()+interval '1 second'),
 ($1,$2,'ec2-sp',2,'on_demand_equivalent','1yr','no-upfront',NOW(),'completed','c6g',NOW(),NOW()+interval '2 second'),
 ($1,$2,'ec2-sp',3,'on_demand_equivalent','1yr','no-upfront',NOW(),'fired','',NULL,NOW()+interval '3 second')`, cfgID, run.ID)
	require.NoError(t, err)

	families := func(rows []LadderInFlightRow, err error) []string {
		require.NoError(t, err)
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.Family)
		}
		sort.Strings(out)
		return out
	}
	asOf := time.Now().UTC()
	require.Equal(t, []string{"", "m5"}, families(inFlightLadderRows(ctx, store.db, false, cfgID, asOf)), "in-flight netting rows carry family")
	require.Equal(t, []string{"c6g"}, families(settledLadderRows(ctx, store.db, false, cfgID, asOf.Add(-time.Hour))), "settled netting rows carry family")

	page, err := store.ListLadderTimeline(ctx, accountID, "aws", nil)
	require.NoError(t, err)
	require.Len(t, page.Events, 3)
	got := make([]string, 0, len(page.Events))
	for _, e := range page.Events {
		got = append(got, e.Family)
	}
	require.Equal(t, []string{"m5", "c6g", ""}, got, "timeline events are ordered by created_at")
	raw, err := json.Marshal(page.Events)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"family":"m5"`)
	require.Equal(t, 2, strings.Count(string(raw), `"family"`), "empty family is omitted from the JSON")
}
