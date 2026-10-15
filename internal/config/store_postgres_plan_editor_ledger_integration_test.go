//go:build integration

package config

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestPlanEditorLedger_LegacyHistoryRemainsInspectable(t *testing.T) {
	ctx := context.Background()
	f := newRampFixture(ctx, t, 1, 0, 4)
	for _, current := range []int{4, 0} {
		_, err := f.store.db.Exec(ctx, `UPDATE purchase_plans SET ramp_schedule=jsonb_set(ramp_schedule,'{current_step}',to_jsonb($2::int)) WHERE id=$1`, f.planID, current)
		require.NoError(t, err)
	}
	var state string
	require.NoError(t, f.store.db.QueryRow(ctx, `SELECT ramp_ledger_state FROM purchase_plans WHERE id=$1`, f.planID).Scan(&state))
	require.Equal(t, "legacy", state, "completed-then-rewound lineage cannot regain trust")
	s, err := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
	require.NoError(t, err, "legacy lineage must remain inspectable without authorizing sizing")
	require.Empty(t, s.Executions)
	require.Equal(t, PlanEditorLedgerLegacy, s.LedgerState)
	require.False(t, s.HasTrustedRampLedger())
	_, err = f.store.db.Exec(ctx, `UPDATE purchase_plans SET ramp_baseline='{}' WHERE id=$1`, f.planID)
	require.NoError(t, err)
	s, err = f.store.ReadPlanEditorSnapshot(ctx, f.planID)
	require.NoError(t, err)
	require.NotNil(t, s.Baseline)
	require.Empty(t, s.Baseline)
	require.False(t, s.HasTrustedRampLedger())
	rec := baselineRec(f.accounts[0], "m5.large", 20)
	key := baselineKey(t, rec)
	f.rowWithRecs(ctx, t, 1, &f.accounts[0], StatusPartiallyCompleted, false, rec)
	raw, err := json.Marshal(PlanRampBaseline{key: {Count: 100}})
	require.NoError(t, err)
	_, err = f.store.db.Exec(ctx, `UPDATE purchase_plans SET ramp_baseline=$2,ramp_schedule=jsonb_set(ramp_schedule,'{current_step}','1') WHERE id=$1`, f.planID, raw)
	require.NoError(t, err)
	s, err = f.store.ReadPlanEditorSnapshot(ctx, f.planID)
	require.NoError(t, err)
	require.Equal(t, PlanEditorLedgerLegacy, s.LedgerState)
	require.Equal(t, 100, s.Baseline[key].Count)
	require.Equal(t, 20, s.Committed[key].Count, "observed retained sum is not authoritative purchased capacity")
	require.Len(t, s.Executions, 1)
	require.False(t, s.HasTrustedRampLedger())
}

func TestPlanEditorLedger_FreezeAndRepeatableRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newRampFixture(ctx, t, 1, 0, 4)
	before, err := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
	require.NoError(t, err)
	require.Equal(t, PlanEditorLedgerNew, before.LedgerState)
	require.Nil(t, before.Baseline)
	require.False(t, before.HasTrustedRampLedger())
	db := f.store.db
	paused := editorPausedDB{dbConn: db, read: make(chan struct{}), resume: make(chan struct{})}
	reader := &PostgresStore{db: paused}
	done := make(chan struct{})
	var got *PlanEditorSnapshot
	var readErr error
	go func() { defer close(done); got, readErr = reader.ReadPlanEditorSnapshot(ctx, f.planID) }()
	select {
	case <-paused.read:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	key := baselineKey(t, baselineRec(f.accounts[0], "m5.large", 100))
	require.NoError(t, f.store.WithTx(ctx, func(tx pgx.Tx) error {
		_, lockErr := f.store.LockPurchasePlanTx(ctx, tx, f.planID)
		if lockErr != nil {
			return lockErr
		}
		wrote, setErr := f.store.SetPlanRampBaselineTx(ctx, tx, f.planID, PlanRampBaseline{key: {Count: 100}})
		require.True(t, wrote)
		return setErr
	}))
	close(paused.resume)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.NoError(t, readErr)
	require.Equal(t, before, got, "marker and baseline must share the first plan read's snapshot")
	after, err := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
	require.NoError(t, err)
	require.Equal(t, PlanEditorLedgerTrusted, after.LedgerState)
	require.Equal(t, 100, after.Baseline[key].Count)
	require.True(t, after.HasTrustedRampLedger())
	oldRevision, err := before.Revision()
	require.NoError(t, err)
	newRevision, err := after.Revision()
	require.NoError(t, err)
	require.NotEqual(t, oldRevision, newRevision)
	row := f.rowWithRecs(ctx, t, 1, &f.accounts[0], StatusCompleted, false, baselineRec(f.accounts[0], "m5.large", 20))
	require.NoError(t, f.store.CompletePlanStep(ctx, f.planID, row.StepNumber))
	progressed, err := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
	require.NoError(t, err)
	require.True(t, progressed.HasTrustedRampLedger())
	require.Equal(t, 20, progressed.Committed[key].Count)
	_, err = db.Exec(ctx, `UPDATE purchase_plans SET ramp_schedule=jsonb_set(ramp_schedule,'{current_step}','4') WHERE id=$1`, f.planID)
	require.NoError(t, err)
	finished, err := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
	require.NoError(t, err)
	require.Equal(t, PlanEditorLedgerLegacy, finished.LedgerState)
	require.False(t, finished.HasTrustedRampLedger())
	require.Eventually(t, func() bool { return db.(*database.Connection).Pool().Stat().AcquiredConns() == 0 }, time.Second, time.Millisecond)
}

func TestPlanEditorLedger_QuantityShapeAndProvenance(t *testing.T) {
	ctx := context.Background()
	f := newRampFixture(ctx, t, 1, 0, 4)
	key := baselineKey(t, baselineRec(f.accounts[0], "m5.large", 100))
	write := func(raw []byte) {
		t.Helper()
		p := &PurchasePlan{Name: "quantity fixture", RampSchedule: RampSchedule{Type: "weekly", PercentPerStep: 25, TotalSteps: 4, StepIntervalDays: 7}}
		require.NoError(t, f.store.CreatePurchasePlan(ctx, p))
		f.planID = p.ID
		require.NoError(t, f.store.WithTx(ctx, func(tx pgx.Tx) error {
			_, err := f.store.LockPurchasePlanTx(ctx, tx, p.ID)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE purchase_plans SET ramp_baseline=$2,ramp_ledger_state='trusted' WHERE id=$1`, p.ID, raw)
			return err
		}))
		var state string
		require.NoError(t, f.store.db.QueryRow(ctx, `SELECT ramp_ledger_state FROM purchase_plans WHERE id=$1`, p.ID).Scan(&state))
		require.Equal(t, "trusted", state, "each case starts from a fresh NULL baseline and actual trusted lineage")
	}
	for _, quantity := range []string{`{"count":0}`, `{"count":0,"hourly":0}`} {
		raw, err := json.Marshal(map[string]json.RawMessage{key: json.RawMessage(quantity)})
		require.NoError(t, err)
		write(raw)
		s, readErr := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
		require.NoError(t, readErr)
		require.Equal(t, PlanRampQuantity{}, s.Baseline[key])
		require.True(t, s.HasTrustedRampLedger(), "genuine zero remains a valid trusted baseline quantity")
	}
	for _, quantity := range []string{`null`, `{}`, `{"count":null}`, `{"count":0,"hourly":null}`, `{"count":0,"extra":1}`, `{"count":[]}`, `{"count":1.5}`, `{"count":0,"hourly":"1"}`, `[]`, `1`} {
		t.Run(quantity, func(t *testing.T) {
			raw, err := json.Marshal(map[string]json.RawMessage{key: json.RawMessage(quantity)})
			require.NoError(t, err)
			write(raw)
			s, readErr := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
			require.Error(t, readErr)
			require.Nil(t, s)
		})
	}
	for _, raw := range []string{"null", "[]", "1", `{"broken":{"count":1}}`} {
		write([]byte(raw))
		s, err := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
		require.Error(t, err)
		require.Nil(t, s)
	}
	require.Zero(t, f.store.db.(*database.Connection).Pool().Stat().AcquiredConns())
}
