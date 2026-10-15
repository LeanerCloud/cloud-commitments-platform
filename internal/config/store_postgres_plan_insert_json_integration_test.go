//go:build integration

package config

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestInsertPurchasePlanJSONKeepsRawAndCreateParity(t *testing.T) {
	ctx := context.Background()
	store := setupRampStepStore(ctx, t)
	typed := &PurchasePlan{Name: "typed", Enabled: true, Services: map[string]ServiceConfig{"aws/ec2": {
		Provider: "aws", Service: "ec2", Enabled: true, Term: 1, Payment: "no-upfront", Coverage: 80, ExcludeEngines: []string{"x"}}},
		RampSchedule: RampSchedule{Type: "weekly", PercentPerStep: 50, StepIntervalDays: 7, TotalSteps: 2}}
	require.NoError(t, store.CreatePurchasePlan(ctx, typed))
	wantServices, err := json.Marshal(typed.Services)
	require.NoError(t, err)
	var gotServices, ledger string
	require.NoError(t, store.db.QueryRow(ctx, `SELECT services::text, ramp_ledger_state FROM purchase_plans WHERE id=$1`, typed.ID).Scan(&gotServices, &ledger))
	require.JSONEq(t, string(wantServices), gotServices)
	require.Equal(t, "new", ledger)

	raw := &PurchasePlan{ID: uuid.NewString(), Name: "raw", Enabled: true}
	services := `{"aws/ec2":{"provider":"aws","service":"ec2","include_engines":null,"exclude_engines":[],"future":{"big":9007199254740993}}}`
	require.NoError(t, store.insertPurchasePlanJSON(ctx, store.db, raw, []byte(services), []byte(`{"total_steps":3,"future":[]}`)))
	var stored, ramp string
	require.NoError(t, store.db.QueryRow(ctx, `SELECT services::text, ramp_schedule::text FROM purchase_plans WHERE id=$1`, raw.ID).Scan(&stored, &ramp))
	require.JSONEq(t, services, stored)
	require.Contains(t, stored, "9007199254740993")
	require.JSONEq(t, `{"total_steps":3,"future":[]}`, ramp)
}
