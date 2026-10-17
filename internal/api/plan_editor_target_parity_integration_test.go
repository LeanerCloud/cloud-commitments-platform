//go:build integration

package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestPlanEditorTargetMatchesTheStoreMerge feeds planEditorTarget through the real command: the store refuses
// with "authorized target differs from stored patch" when its own merge of the intent disagrees with the target.
func TestPlanEditorTargetMatchesTheStoreMerge(t *testing.T) {
	ctx := context.Background()
	pg := testhelpers.RequirePostgresContainer(ctx, t)
	t.Cleanup(func() { require.NoError(t, pg.Cleanup(context.Background())) })
	require.NoError(t, migrations.RunMigrations(ctx, pg.DB.Pool(), "../database/postgres/migrations", "", ""))
	store := config.NewPostgresStore(pg.DB)
	account := &config.CloudAccount{Name: "parity", Provider: "aws", ExternalID: uuid.NewString()}
	require.NoError(t, store.CreateCloudAccount(ctx, account))
	planID, day := uuid.NewString(), time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 10)

	run := func(t *testing.T, intent *planEditorIntent, newSteps ...int) *config.PlanEditorSnapshot {
		t.Helper()
		before, err := store.ReadPlanEditorSnapshot(ctx, planID)
		identity := config.PlanScheduleCommandIdentity{CommandID: uuid.NewString(), PlanID: planID, Actor: config.PlanScheduleActor{Kind: config.PlanScheduleAdminActor},
			RequestHash: "sha256:" + strings.Repeat("a", 64), ProposedAccountIDs: []string{account.ID}, ProposedProviders: []string{"aws"}}
		if err == nil {
			revision, revErr := before.Revision()
			require.NoError(t, revErr)
			identity.ExpectedRevision = &revision
		} else {
			before = nil
		}
		build := func(_ context.Context, snapshot *config.PlanEditorSnapshot, _ []config.PlanEditorAccount, now time.Time) (*config.PlanScheduleMutation, error) {
			target, targetErr := planEditorTarget(snapshot, planID, intent, now)
			if targetErr != nil {
				return nil, targetErr
			}
			roots := make([]config.PlanScheduleRootChange, len(newSteps))
			for i, step := range newSteps {
				date := day.AddDate(0, 0, 7*(step-1))
				roots[i] = config.PlanScheduleRootChange{StepNumber: step, ScheduledDate: &date}
			}
			target.RampSchedule.DatesManaged = len(roots) > 0 || (snapshot != nil && snapshot.Plan.RampSchedule.DatesManaged)
			mutation := &config.PlanScheduleMutation{Plan: target, Intent: intent.PlanScheduleIntent, AccountIDs: []string{account.ID},
				Roots: roots, Scope: config.PlanScheduleScope{AccountIDs: []string{account.ID}}}
			if snapshot == nil {
				mutation.Intent.AccountIDs = &identity.ProposedAccountIDs
			}
			return mutation, nil
		}
		authorize := func(context.Context, config.PlanScheduleScope, []config.PlanEditorAccount) error { return nil }
		project := func(context.Context, *config.PlanEditorSnapshot, []config.PlanScheduleMapping, json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		}
		_, err = store.ExecutePlanScheduleCommand(ctx, identity, build, authorize, project)
		require.NoError(t, err)
		after, err := store.ReadPlanEditorSnapshot(ctx, planID)
		require.NoError(t, err)
		return after
	}
	percent := func(p float64) *planEditorIntent {
		return targetIntent(func(i *planEditorIntent) { i.Ramp.PercentPerStep = &p })
	}

	include, exclude := []string(nil), []string{}
	create := targetIntent(func(i *planEditorIntent) {
		i.Plan.Name = new("parity plan")
		i.Plan.Enabled = new(true)
		i.Plan.NotificationDaysBefore = new(1)
		i.StepCount = new(3)
		i.Ramp = config.PlanScheduleRampPatch{Type: new("weekly"), StepIntervalDays: new(7), StartDate: &day}
		i.Services = map[string]config.PlanScheduleServicePatch{"aws/ec2": {Provider: new("aws"), Service: new("ec2"), Enabled: new(true), Term: new(1),
			Payment: new("no-upfront"), Coverage: new(80.0), IncludeEngines: &include, ExcludeEngines: &exclude}}
	})
	ramp := run(t, create, 1, 2, 3).Plan.RampSchedule
	require.Equal(t, 3, ramp.TotalSteps)
	require.Equal(t, config.RampAllocationEqualCount, ramp.AllocationMode)
	require.True(t, ramp.DatesManaged)

	patched := run(t, targetIntent(func(i *planEditorIntent) {
		i.Services = map[string]config.PlanScheduleServicePatch{"aws/ec2": {Coverage: new(90.0)}}
	}))
	require.Equal(t, 90.0, patched.Plan.Services["aws/ec2"].Coverage, "service patch")

	ramp = run(t, percent(100.0/3)).Plan.RampSchedule
	require.Empty(t, ramp.AllocationMode, "equal_count converts to cumulative")
	require.Equal(t, 3, ramp.TotalSteps)

	require.Equal(t, 3, run(t, percent(100.0/3)).Plan.RampSchedule.TotalSteps, "an unchanged percent is a no-op")

	ramp = run(t, percent(25), 4).Plan.RampSchedule
	require.Equal(t, 4, ramp.TotalSteps, "cumulative change")
	require.Empty(t, ramp.AllocationMode)

	ramp = run(t, targetIntent(func(i *planEditorIntent) { i.StepCount = new(5) }), 5).Plan.RampSchedule
	require.Equal(t, 5, ramp.TotalSteps, "step_count change")
	require.Equal(t, config.RampAllocationEqualCount, ramp.AllocationMode)
	require.Len(t, run(t, targetIntent(nil)).Executions, 5, "one root per step")
}
