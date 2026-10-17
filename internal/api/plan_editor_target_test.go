package api

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/require"
)

var targetNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func targetBefore() *config.PlanEditorSnapshot {
	return &config.PlanEditorSnapshot{Plan: &config.PurchasePlan{
		ID: "plan", Name: "old", Enabled: true, AutoPurchase: true, NotificationDaysBefore: 3, MaxStepCommitment: new(500.0),
		Services: map[string]config.ServiceConfig{
			"aws/ec2": {Provider: "aws", Service: "ec2", Enabled: true, Term: 1, Payment: "no-upfront", Coverage: 80, RampSchedule: "weekly",
				IncludeEngines: []string{"e1", "e2"}, ExcludeEngines: []string{"x1", "x2"}, IncludeRegions: []string{"r1", "r2"},
				ExcludeRegions: []string{"q1", "q2"}, IncludeTypes: []string{"t1", "t2"}, ExcludeTypes: []string{"u1", "u2"}, MinCount: 4},
			"azure/vm": {Provider: "azure", Service: "vm", Enabled: false, Term: 3, Payment: "monthly"},
		},
		RampSchedule: config.RampSchedule{Type: "weekly", PercentPerStep: 25, StepIntervalDays: 7, TotalSteps: 4, CurrentStep: 1,
			StartDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
	}}
}

func targetIntent(mutate func(*planEditorIntent)) *planEditorIntent {
	intent := &planEditorIntent{}
	if mutate != nil {
		mutate(intent)
	}
	return intent
}

func requireInvalid(t *testing.T, err error) {
	t.Helper()
	var client *clientError
	require.ErrorAs(t, err, &client)
	require.Equal(t, 400, client.code)
	require.Equal(t, codeInvalidRequest, client.Details()["code"])
}

func TestPlanEditorTargetPlanFields(t *testing.T) {
	start := time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC)
	before := targetBefore()
	before.Plan.RampSchedule.CurrentStep = 0
	target, err := planEditorTarget(before, "plan", targetIntent(func(i *planEditorIntent) {
		i.Plan = config.PlanSchedulePlanPatch{Name: new("new"), Enabled: new(false), AutoPurchase: new(false), NotificationDaysBefore: new(9),
			CapSupplied: true, MaxStepCommitment: new(750.0)}
		i.Ramp = config.PlanScheduleRampPatch{Type: new("custom"), PercentPerStep: new(30.0), StepIntervalDays: new(5), StartDate: &start}
	}), targetNow)
	require.NoError(t, err)
	require.Equal(t, "new", target.Name)
	require.False(t, target.Enabled)
	require.False(t, target.AutoPurchase)
	require.Equal(t, 9, target.NotificationDaysBefore)
	require.Equal(t, 750.0, *target.MaxStepCommitment)
	require.Equal(t, "custom", target.RampSchedule.Type)
	require.Equal(t, 30.0, target.RampSchedule.PercentPerStep)
	require.Equal(t, 5, target.RampSchedule.StepIntervalDays)
	require.Equal(t, start, target.RampSchedule.StartDate)
	require.Equal(t, 4, target.RampSchedule.TotalSteps)
	require.Zero(t, target.RampSchedule.CurrentStep)
	require.Equal(t, 500.0, *before.Plan.MaxStepCommitment, "the source cap pointer is not aliased")
	require.Equal(t, "old", before.Plan.Name)
}

func TestPlanEditorTargetIntentionalZeroValues(t *testing.T) {
	target, err := planEditorTarget(targetBefore(), "plan", targetIntent(func(i *planEditorIntent) {
		i.Plan = config.PlanSchedulePlanPatch{Name: new(""), NotificationDaysBefore: new(0), Enabled: new(false)}
		i.Services = map[string]config.PlanScheduleServicePatch{"aws/ec2": {Enabled: new(false), Term: new(0), Coverage: new(0.0), Payment: new(""), MinCount: new(0), RampSchedule: new("")}}
	}), targetNow)
	require.NoError(t, err)
	require.Empty(t, target.Name)
	require.Zero(t, target.NotificationDaysBefore)
	svc := target.Services["aws/ec2"]
	require.False(t, svc.Enabled)
	require.Zero(t, svc.Term)
	require.Zero(t, svc.Coverage)
	require.Empty(t, svc.Payment)
	require.Zero(t, svc.MinCount)
	require.Empty(t, svc.RampSchedule)
}

func TestPlanEditorTargetCap(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch config.PlanSchedulePlanPatch
		want  *float64
	}{
		{"omitted preserves", config.PlanSchedulePlanPatch{Name: new("n")}, new(500.0)},
		{"null clears", config.PlanSchedulePlanPatch{CapSupplied: true}, nil},
		{"value sets", config.PlanSchedulePlanPatch{CapSupplied: true, MaxStepCommitment: new(10.0)}, new(10.0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, err := planEditorTarget(targetBefore(), "plan", targetIntent(func(i *planEditorIntent) { i.Plan = tc.patch }), targetNow)
			require.NoError(t, err)
			require.Equal(t, tc.want, target.MaxStepCommitment)
		})
	}
}

func TestPlanEditorTargetServiceFields(t *testing.T) {
	before := targetBefore()
	target, err := planEditorTarget(before, "plan", targetIntent(func(i *planEditorIntent) {
		i.Services = map[string]config.PlanScheduleServicePatch{"aws/ec2": {
			Enabled: new(false), Term: new(3), Payment: new("all-upfront"), Coverage: new(55.0), RampSchedule: new("immediate"),
			IncludeEngines: &[]string{"E"}, ExcludeEngines: &[]string{"X"}, IncludeRegions: &[]string{"R"},
			ExcludeRegions: &[]string{"Q"}, IncludeTypes: &[]string{"T"}, ExcludeTypes: &[]string{"U"}, MinCount: new(9)}}
	}), targetNow)
	require.NoError(t, err)
	svc := target.Services["aws/ec2"]
	require.False(t, svc.Enabled)
	require.Equal(t, 3, svc.Term)
	require.Equal(t, "all-upfront", svc.Payment)
	require.Equal(t, 55.0, svc.Coverage)
	require.Equal(t, "immediate", svc.RampSchedule)
	require.Equal(t, []string{"E"}, svc.IncludeEngines)
	require.Equal(t, []string{"X"}, svc.ExcludeEngines)
	require.Equal(t, []string{"R"}, svc.IncludeRegions)
	require.Equal(t, []string{"Q"}, svc.ExcludeRegions)
	require.Equal(t, []string{"T"}, svc.IncludeTypes)
	require.Equal(t, []string{"U"}, svc.ExcludeTypes)
	require.Equal(t, 9, svc.MinCount)
	require.Equal(t, targetBefore().Plan.Services, before.Plan.Services, "the source services are not aliased")
}

func TestPlanEditorTargetFilterLists(t *testing.T) {
	type pick struct {
		name  string
		patch func(*config.PlanScheduleServicePatch, *[]string)
		get   func(config.ServiceConfig) []string
	}
	picks := []pick{
		{"include_engines", func(p *config.PlanScheduleServicePatch, v *[]string) { p.IncludeEngines = v }, func(s config.ServiceConfig) []string { return s.IncludeEngines }},
		{"exclude_engines", func(p *config.PlanScheduleServicePatch, v *[]string) { p.ExcludeEngines = v }, func(s config.ServiceConfig) []string { return s.ExcludeEngines }},
		{"include_regions", func(p *config.PlanScheduleServicePatch, v *[]string) { p.IncludeRegions = v }, func(s config.ServiceConfig) []string { return s.IncludeRegions }},
		{"exclude_regions", func(p *config.PlanScheduleServicePatch, v *[]string) { p.ExcludeRegions = v }, func(s config.ServiceConfig) []string { return s.ExcludeRegions }},
		{"include_types", func(p *config.PlanScheduleServicePatch, v *[]string) { p.IncludeTypes = v }, func(s config.ServiceConfig) []string { return s.IncludeTypes }},
		{"exclude_types", func(p *config.PlanScheduleServicePatch, v *[]string) { p.ExcludeTypes = v }, func(s config.ServiceConfig) []string { return s.ExcludeTypes }},
	}
	for _, p := range picks {
		for _, tc := range []struct {
			name  string
			value *[]string
			check func(*testing.T, []string)
		}{
			{"omitted preserves", nil, func(t *testing.T, got []string) { require.Len(t, got, 2) }},
			{"null is nil", new([]string(nil)), func(t *testing.T, got []string) { require.Nil(t, got) }},
			{"empty is empty", &[]string{}, func(t *testing.T, got []string) { require.NotNil(t, got); require.Empty(t, got) }},
		} {
			t.Run(p.name+" "+tc.name, func(t *testing.T) {
				target, err := planEditorTarget(targetBefore(), "plan", targetIntent(func(i *planEditorIntent) {
					var patch config.PlanScheduleServicePatch
					p.patch(&patch, tc.value)
					i.Services = map[string]config.PlanScheduleServicePatch{"aws/ec2": patch}
				}), targetNow)
				require.NoError(t, err)
				tc.check(t, p.get(target.Services["aws/ec2"]))
			})
		}
	}
}

func TestPlanEditorTargetServiceIdentity(t *testing.T) {
	t.Run("untouched legacy service is unchanged by a plan-only patch", func(t *testing.T) {
		before := targetBefore()
		target, err := planEditorTarget(before, "plan", targetIntent(func(i *planEditorIntent) { i.Plan.Name = new("n") }), targetNow)
		require.NoError(t, err)
		require.Equal(t, before.Plan.Services, target.Services)
	})
	t.Run("repeating the stored identity keeps an opaque key", func(t *testing.T) {
		before := targetBefore()
		before.Plan.Services["legacy-key"] = config.ServiceConfig{Provider: "gcp", Service: "cloudsql", Term: 1}
		target, err := planEditorTarget(before, "plan", targetIntent(func(i *planEditorIntent) {
			i.Services = map[string]config.PlanScheduleServicePatch{"legacy-key": {Provider: new("gcp"), Service: new("cloudsql"), Coverage: new(10.0)}}
		}), targetNow)
		require.NoError(t, err)
		require.Contains(t, target.Services, "legacy-key")
		require.Len(t, target.Services, 3)
		require.Equal(t, 10.0, target.Services["legacy-key"].Coverage)
	})
	t.Run("a real identity change moves exactly one service", func(t *testing.T) {
		target, err := planEditorTarget(targetBefore(), "plan", targetIntent(func(i *planEditorIntent) {
			i.Services = map[string]config.PlanScheduleServicePatch{"azure/vm": {Service: new("compute")}}
		}), targetNow)
		require.NoError(t, err)
		require.NotContains(t, target.Services, "azure/vm")
		moved := target.Services["azure/compute"]
		require.Equal(t, "compute", moved.Service)
		require.Equal(t, "azure", moved.Provider)
		require.Equal(t, 3, moved.Term)
		require.Equal(t, targetBefore().Plan.Services["aws/ec2"], target.Services["aws/ec2"])
		require.Len(t, target.Services, 2)
	})
	t.Run("collision with a stored key fails", func(t *testing.T) {
		before := targetBefore()
		before.Plan.Services["azure/compute"] = config.ServiceConfig{Provider: "azure", Service: "compute"}
		_, err := planEditorTarget(before, "plan", targetIntent(func(i *planEditorIntent) {
			i.Services = map[string]config.PlanScheduleServicePatch{"azure/vm": {Service: new("compute")}}
		}), targetNow)
		requireInvalid(t, err)
	})
	t.Run("taking a key another patch vacates fails", func(t *testing.T) {
		before := targetBefore()
		before.Plan.Services["azure/compute"] = config.ServiceConfig{Provider: "azure", Service: "compute"}
		_, err := planEditorTarget(before, "plan", targetIntent(func(i *planEditorIntent) {
			i.Services = map[string]config.PlanScheduleServicePatch{
				"azure/compute": {Service: new("cache")}, "azure/vm": {Service: new("compute")}}
		}), targetNow)
		requireInvalid(t, err)
	})
	t.Run("collision between two patches fails", func(t *testing.T) {
		_, err := planEditorTarget(nil, "plan", targetIntent(func(i *planEditorIntent) {
			i.Services = map[string]config.PlanScheduleServicePatch{
				"a": {Provider: new("aws"), Service: new("ec2")}, "b": {Provider: new("aws"), Service: new("ec2")}}
		}), targetNow)
		requireInvalid(t, err)
	})
	t.Run("an unknown service patch on Edit fails", func(t *testing.T) {
		_, err := planEditorTarget(targetBefore(), "plan", targetIntent(func(i *planEditorIntent) {
			i.Services = map[string]config.PlanScheduleServicePatch{"missing": {Coverage: new(1.0)}}
		}), targetNow)
		requireInvalid(t, err)
	})
}

func TestPlanEditorTargetCreate(t *testing.T) {
	target, err := planEditorTarget(nil, "new-id", targetIntent(func(i *planEditorIntent) {
		i.Plan.Name = new("fresh")
		i.StepCount = new(5)
		i.Services = map[string]config.PlanScheduleServicePatch{"client-key": {Provider: new("aws"), Service: new("ec2"), Enabled: new(true), Term: new(1)}}
	}), targetNow)
	require.NoError(t, err)
	require.Equal(t, "new-id", target.ID)
	require.Equal(t, targetNow, target.CreatedAt)
	require.Equal(t, targetNow, target.UpdatedAt)
	require.Len(t, target.Services, 1)
	require.Equal(t, "ec2", target.Services["aws/ec2"].Service)
	require.Zero(t, target.RampSchedule.CurrentStep)
	require.Equal(t, 5, target.RampSchedule.TotalSteps)
}

func TestPlanEditorTargetStepCount(t *testing.T) {
	target, err := planEditorTarget(targetBefore(), "plan", targetIntent(func(i *planEditorIntent) { i.StepCount = new(8) }), targetNow)
	require.NoError(t, err)
	require.Equal(t, 8, target.RampSchedule.TotalSteps)
	require.Equal(t, 12.5, target.RampSchedule.PercentPerStep)
	require.Equal(t, config.RampAllocationEqualCount, target.RampSchedule.AllocationMode)

	equal := targetBefore()
	equal.Plan.RampSchedule.AllocationMode = config.RampAllocationEqualCount
	for name, tc := range map[string]struct {
		before *config.PlanEditorSnapshot
		mutate func(*planEditorIntent)
	}{
		"below current step":          {targetBefore(), func(i *planEditorIntent) { i.StepCount = new(0) }},
		"above the maximum":           {targetBefore(), func(i *planEditorIntent) { i.StepCount = new(config.MaxTotalSteps + 1) }},
		"with percent_per_step":       {targetBefore(), func(i *planEditorIntent) { i.StepCount = new(5); i.Ramp.PercentPerStep = new(20.0) }},
		"percent on equal-count plan": {equal, func(i *planEditorIntent) { i.Ramp.PercentPerStep = new(20.0) }},
	} {
		t.Run(name, func(t *testing.T) {
			_, targetErr := planEditorTarget(tc.before, "plan", targetIntent(tc.mutate), targetNow)
			requireInvalid(t, targetErr)
		})
	}
	current := targetBefore()
	current.Plan.RampSchedule.CurrentStep = 3
	_, err = planEditorTarget(current, "plan", targetIntent(func(i *planEditorIntent) { i.StepCount = new(2) }), targetNow)
	requireInvalid(t, err)
	target, err = planEditorTarget(current, "plan", targetIntent(func(i *planEditorIntent) { i.StepCount = new(3) }), targetNow)
	require.NoError(t, err)
	require.Equal(t, 3, target.RampSchedule.TotalSteps)
}

func TestPlanEditorTargetPatchedListsAreDetached(t *testing.T) {
	before := targetBefore()
	_, err := planEditorTarget(before, "plan", targetIntent(func(i *planEditorIntent) {
		i.Services = map[string]config.PlanScheduleServicePatch{"aws/ec2": {
			IncludeEngines: &[]string{"z"}, ExcludeEngines: &[]string{"z"}, IncludeRegions: &[]string{"z"},
			ExcludeRegions: &[]string{"z"}, IncludeTypes: &[]string{"z"}, ExcludeTypes: &[]string{"z"}}}
	}), targetNow)
	require.NoError(t, err)
	require.Equal(t, targetBefore().Plan.Services, before.Plan.Services, "patching must not write through the source backing arrays")
}

func TestPlanEditorTargetCumulativeSharedVectors(t *testing.T) {
	raw, err := os.ReadFile("../../frontend/src/__tests__/fixtures/cumulative-step-count.json")
	require.NoError(t, err)
	var rows []struct {
		Percent float64 `json:"percent"`
		Steps   int     `json:"steps"`
	}
	require.NoError(t, json.Unmarshal(raw, &rows))
	require.NotEmpty(t, rows)
	for _, row := range rows {
		create := func(i *planEditorIntent) { i.Ramp.PercentPerStep = &row.Percent }
		target, targetErr := planEditorTarget(nil, "new", targetIntent(create), targetNow)
		if row.Steps > config.MaxTotalSteps {
			requireInvalid(t, targetErr)
			continue
		}
		require.NoError(t, targetErr, "percent %v", row.Percent)
		require.Equal(t, row.Steps, target.RampSchedule.TotalSteps, "percent %v", row.Percent)
		require.Empty(t, target.RampSchedule.AllocationMode)
		require.Equal(t, row.Percent, target.RampSchedule.PercentPerStep)
	}
}

func TestPlanEditorTargetCumulativeModes(t *testing.T) {
	unstarted := func(mode config.RampAllocationMode, percent float64, total int) *config.PlanEditorSnapshot {
		before := targetBefore()
		before.Plan.RampSchedule = config.RampSchedule{Type: "weekly", PercentPerStep: percent, StepIntervalDays: 7, TotalSteps: total, AllocationMode: mode}
		return before
	}
	percent := func(p float64) func(*planEditorIntent) {
		return func(i *planEditorIntent) { i.Ramp.PercentPerStep = &p }
	}
	for name, tc := range map[string]struct {
		before  *config.PlanEditorSnapshot
		mutate  func(*planEditorIntent)
		total   int
		mode    config.RampAllocationMode
		percent float64
	}{
		"changed percent derives steps":       {unstarted("", 25, 4), percent(20), 5, "", 20},
		"changed percent drops no-op count":   {unstarted("", 33, 3), percent(25), 4, "", 25},
		"unchanged percent is a no-op":        {unstarted("", 33, 3), percent(33), 3, "", 33},
		"unchanged within epsilon is a no-op": {unstarted("", 33, 3), percent(33 + 1e-12), 3, "", 33 + 1e-12},
		"changed beyond epsilon rederives":    {unstarted("", 33, 3), percent(33 + 1e-7), 4, "", 33 + 1e-7},
		"equal_count converts":                {unstarted(config.RampAllocationEqualCount, 25, 4), percent(25), 4, "", 25},
		"equal_count converts and reshapes":   {unstarted(config.RampAllocationEqualCount, 100.0/3, 3), percent(25), 4, "", 25},
		"step_count selects equal_count":      {unstarted("", 25, 4), func(i *planEditorIntent) { i.StepCount = new(3) }, 3, config.RampAllocationEqualCount, 100.0 / 3},
	} {
		t.Run(name, func(t *testing.T) {
			target, err := planEditorTarget(tc.before, "plan", targetIntent(tc.mutate), targetNow)
			require.NoError(t, err)
			require.Equal(t, tc.total, target.RampSchedule.TotalSteps)
			require.Equal(t, tc.mode, target.RampSchedule.AllocationMode)
			require.Equal(t, tc.percent, target.RampSchedule.PercentPerStep)
		})
	}

	started := unstarted("", 25, 4)
	started.Plan.RampSchedule.CurrentStep = 1
	baseline := unstarted("", 25, 4)
	baseline.Baseline = config.PlanRampBaseline{}
	equalStarted := unstarted(config.RampAllocationEqualCount, 25, 4)
	equalStarted.Plan.RampSchedule.CurrentStep = 1
	for name, tc := range map[string]struct {
		before *config.PlanEditorSnapshot
		mutate func(*planEditorIntent)
	}{
		"changed percent after progress":        {started, percent(20)},
		"changed percent with a baseline":       {baseline, percent(20)},
		"equal_count conversion after progress": {equalStarted, percent(25)},
		"percent with step_count":               {unstarted("", 25, 4), func(i *planEditorIntent) { i.StepCount = new(5); i.Ramp.PercentPerStep = new(20.0) }},
		"percent over the step cap":             {unstarted("", 25, 4), percent(0.5)},
		"create with neither":                   {nil, func(i *planEditorIntent) { i.Plan.Name = new("n") }},
		"create with step_count 0":              {nil, func(i *planEditorIntent) { i.StepCount = new(0) }},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := planEditorTarget(tc.before, "plan", targetIntent(tc.mutate), targetNow)
			requireInvalid(t, err)
		})
	}
	target, err := planEditorTarget(started, "plan", targetIntent(percent(25)), targetNow)
	require.NoError(t, err, "an unchanged percent never reshapes a started plan")
	require.Equal(t, 4, target.RampSchedule.TotalSteps)
}

func TestPlanEditorTargetCreatedKeysCannotCollide(t *testing.T) {
	service := func() config.PlanScheduleServicePatch {
		return config.PlanScheduleServicePatch{Provider: new("aws"), Service: new("ec2"), Enabled: new(true)}
	}
	_, err := planEditorTarget(nil, "new", targetIntent(func(i *planEditorIntent) {
		i.StepCount = new(2)
		i.Services = map[string]config.PlanScheduleServicePatch{"a": service(), "aws/ec2": service()}
	}), targetNow)
	requireInvalid(t, err)
}
