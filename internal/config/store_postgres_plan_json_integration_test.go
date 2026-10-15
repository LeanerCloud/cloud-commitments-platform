//go:build integration

package config

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPurchasePlanUpdatePreservesRawJSON(t *testing.T) {
	ctx := context.Background()
	f := newRampFixture(ctx, t, 1, 0, 4)
	rawServices := `{"aws/ec2":{"provider":"aws","service":"ec2","enabled":false,"term":1,"payment":"no-upfront","coverage":80,"ramp_schedule":"","include_engines":null,"exclude_engines":[],"include_regions":[],"exclude_regions":null,"include_types":null,"exclude_types":[],"min_count":0,"future":{"count":9007199254740993}}}`
	_, err := f.store.db.Exec(ctx, `UPDATE purchase_plans SET services=$2,ramp_schedule=ramp_schedule||'{"future":{"exact":9007199254740993}}'::jsonb WHERE id=$1`, f.planID, rawServices)
	require.NoError(t, err)
	before, err := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
	require.NoError(t, err)
	plan := before.Plan
	plan.Name = "metadata edit"
	require.NoError(t, f.store.UpdatePurchasePlan(ctx, plan))
	after, err := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
	require.NoError(t, err)
	require.JSONEq(t, string(before.servicesJSON), string(after.servicesJSON))
	require.JSONEq(t, string(before.rampScheduleJSON), string(after.rampScheduleJSON))
	svc := plan.Services["aws/ec2"]
	svc.Coverage = 50
	svc.IncludeTypes = []string{}
	plan.Services["aws/ec2"] = svc
	require.NoError(t, f.store.UpdatePurchasePlan(ctx, plan))
	after, err = f.store.ReadPlanEditorSnapshot(ctx, f.planID)
	require.NoError(t, err)
	require.JSONEq(t, `{"aws/ec2":{"provider":"aws","service":"ec2","enabled":false,"term":1,"payment":"no-upfront","coverage":50,"ramp_schedule":"","include_engines":null,"exclude_engines":[],"include_regions":[],"exclude_regions":null,"include_types":[],"exclude_types":[],"min_count":0,"future":{"count":9007199254740993}}}`, string(after.servicesJSON))
	require.JSONEq(t, string(before.rampScheduleJSON), string(after.rampScheduleJSON))
	plan.Services = map[string]ServiceConfig{"replacement": svc}
	require.Error(t, f.store.UpdatePurchasePlan(ctx, plan))
	unchanged, err := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
	require.NoError(t, err)
	require.Equal(t, after, unchanged)
}
