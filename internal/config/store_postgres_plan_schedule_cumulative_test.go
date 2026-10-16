package config

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCumulativeStepCountMatchesSharedVectors(t *testing.T) {
	raw, err := os.ReadFile("../../frontend/src/__tests__/fixtures/cumulative-step-count.json")
	require.NoError(t, err)
	var rows []struct {
		Percent float64 `json:"percent"`
		Steps   int     `json:"steps"`
	}
	require.NoError(t, json.Unmarshal(raw, &rows))
	require.NotEmpty(t, rows)
	for _, row := range rows {
		require.Equal(t, row.Steps, CumulativeStepCount(row.Percent), "percent %v", row.Percent)
		_, err = mergeRamp(`{}`, PlanScheduleIntent{Ramp: PlanScheduleRampPatch{PercentPerStep: &row.Percent}}, true, false)
		if row.Steps > MaxTotalSteps {
			require.ErrorIs(t, err, ErrPurchasePlanConflict, "percent %v is over the step cap", row.Percent)
		} else {
			require.NoError(t, err, "percent %v", row.Percent)
		}
	}
	for n := 1; n <= 334; n++ {
		require.Equal(t, n, CumulativeStepCount(100/float64(n)), "100/%d", n)
	}
	for _, invalid := range []float64{0, -1, 100.0001, 1e-300} {
		if invalid > 0 && invalid < 1 {
			require.Greater(t, CumulativeStepCount(invalid), MaxTotalSteps)
		} else {
			require.Zero(t, CumulativeStepCount(invalid), "percent %v", invalid)
		}
	}
}

func TestMergePlanScheduleRampCumulativeMode(t *testing.T) {
	percent := func(p float64) PlanScheduleIntent {
		return PlanScheduleIntent{Ramp: PlanScheduleRampPatch{PercentPerStep: &p}}
	}
	out, err := mergeRamp(`{}`, percent(25), true, false)
	require.NoError(t, err)
	require.JSONEq(t, `{"current_step":0,"percent_per_step":25,"total_steps":4}`, out)

	equal := `{"type":"weekly","percent_per_step":33.333333333333336,"current_step":1,"total_steps":3,"allocation_mode":"equal_count","future":{"k":1}}`
	out, err = mergeRamp(equal, percent(25), false, true)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"weekly","percent_per_step":25,"current_step":1,"total_steps":4,"dates_managed":true,"future":{"k":1}}`, out,
		"percent without a count converts an equal-count plan and drops allocation_mode")

	cumulative := `{"type":"weekly","percent_per_step":20,"current_step":0,"total_steps":5}`
	out, err = mergeRamp(cumulative, percent(100.0/31), false, false)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"weekly","percent_per_step":3.225806451612903,"current_step":0,"total_steps":31}`, out)

	for name, tc := range map[string]struct {
		raw    string
		intent PlanScheduleIntent
		create bool
	}{
		"percent below the current step": {`{"current_step":3,"total_steps":5}`, percent(100), false},
		"percent over the step cap":      {`{}`, percent(0.5), true},
		"zero percent":                   {`{"total_steps":2}`, percent(0), false},
		"percent above 100":              {`{"total_steps":2}`, percent(101), false},
		"percent with a count":           {equal, PlanScheduleIntent{StepCount: new(3), Ramp: PlanScheduleRampPatch{PercentPerStep: new(25.0)}}, false},
		"create with neither":            {`{}`, PlanScheduleIntent{Ramp: PlanScheduleRampPatch{Type: new("weekly")}}, true},
	} {
		_, err = mergeRamp(tc.raw, tc.intent, tc.create, false)
		require.ErrorIs(t, err, ErrPurchasePlanConflict, name)
	}
}

func TestPreparePlanScheduleTargetsCumulative(t *testing.T) {
	start := time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC)
	service := ServiceConfig{Provider: "aws", Service: "ec2", Enabled: true, Term: 1, Payment: "no-upfront", Coverage: 80}
	patch := map[string]PlanScheduleServicePatch{"aws/ec2": {Provider: new("aws"), Service: new("ec2"), Enabled: new(true), Term: new(1),
		Payment: new("no-upfront"), Coverage: new(80.0)}}
	plan := func(mode RampAllocationMode, percent float64, total int) *PurchasePlan {
		return &PurchasePlan{ID: "p", Name: "n", Enabled: true, Services: map[string]ServiceConfig{"aws/ec2": service},
			RampSchedule: RampSchedule{Type: "weekly", PercentPerStep: percent, StepIntervalDays: 7, TotalSteps: total, StartDate: start,
				DatesManaged: true, AllocationMode: mode}}
	}
	create := PlanScheduleIntent{Services: patch, Ramp: PlanScheduleRampPatch{Type: new("weekly"), PercentPerStep: new(30.0), StepIntervalDays: new(7), StartDate: &start}}
	services, ramp, err := preparePlanScheduleTargets(nil, create, plan("", 30, 4), true)
	require.NoError(t, err, "Create in cumulative mode")
	require.NotContains(t, string(ramp), "allocation_mode")

	_, _, err = preparePlanScheduleTargets(nil, create, plan("", 30, 3), true)
	require.ErrorIs(t, err, ErrPurchasePlanConflict, "a target that disagrees with the derived count is refused")

	before := &PlanEditorSnapshot{servicesJSON: services, rampScheduleJSON: ramp}
	edit := PlanScheduleIntent{Ramp: PlanScheduleRampPatch{PercentPerStep: new(25.0)}}
	_, ramp2, err := preparePlanScheduleTargets(before, edit, plan("", 25, 4), true)
	require.NoError(t, err, "Edit of a cumulative plan")
	require.JSONEq(t, `{"type":"weekly","percent_per_step":25,"step_interval_days":7,"current_step":0,"total_steps":4,"start_date":"2030-01-02T00:00:00Z","dates_managed":true}`, string(ramp2))

	equal := &PlanEditorSnapshot{servicesJSON: services, rampScheduleJSON: []byte(`{"type":"weekly","percent_per_step":33.333333333333336,"step_interval_days":7,"current_step":0,"total_steps":3,"start_date":"2030-01-02T00:00:00Z","allocation_mode":"equal_count"}`)}
	_, ramp3, err := preparePlanScheduleTargets(equal, edit, plan("", 25, 4), true)
	require.NoError(t, err, "equal_count to cumulative")
	require.NotContains(t, string(ramp3), "allocation_mode")
	_, _, err = preparePlanScheduleTargets(equal, edit, plan(RampAllocationEqualCount, 25, 4), true)
	require.ErrorIs(t, err, ErrPurchasePlanConflict, "a target that keeps allocation_mode after conversion is refused")
}

func TestMergePlanScheduleRampUnchangedPercentIsANoOp(t *testing.T) {
	same := func(p float64) PlanScheduleIntent {
		return PlanScheduleIntent{Ramp: PlanScheduleRampPatch{PercentPerStep: &p}}
	}
	for name, tc := range map[string]struct {
		raw    string
		intent PlanScheduleIntent
		total  int
	}{
		"legacy 33x3 keeps 3":         {`{"percent_per_step":33,"current_step":0,"total_steps":3}`, same(33), 3},
		"legacy 25x3 keeps 3":         {`{"percent_per_step":25,"current_step":0,"total_steps":3}`, same(25), 3},
		"within epsilon keeps 3":      {`{"percent_per_step":33,"current_step":0,"total_steps":3}`, same(33 + 1e-12), 3},
		"changed percent rederives":   {`{"percent_per_step":33,"current_step":0,"total_steps":3}`, same(25), 4},
		"equal_count converts":        {`{"percent_per_step":25,"current_step":0,"total_steps":3,"allocation_mode":"equal_count"}`, same(25), 4},
		"progressed no-op is allowed": {`{"percent_per_step":33,"current_step":2,"total_steps":3}`, same(33), 3},
	} {
		out, err := mergeRamp(tc.raw, tc.intent, false, false)
		require.NoError(t, err, name)
		var ramp RampSchedule
		require.NoError(t, json.Unmarshal([]byte(out), &ramp), name)
		require.Equal(t, tc.total, ramp.TotalSteps, name)
	}
}

func TestPreparePlanScheduleTargetsRefusesStartedPercentReshape(t *testing.T) {
	edit := PlanScheduleIntent{Ramp: PlanScheduleRampPatch{PercentPerStep: new(25.0)}}
	target := &PurchasePlan{Services: map[string]ServiceConfig{}, RampSchedule: RampSchedule{PercentPerStep: 25, TotalSteps: 4}}
	for name, before := range map[string]*PlanEditorSnapshot{
		"progress": {servicesJSON: []byte(`{}`), rampScheduleJSON: []byte(`{"percent_per_step":33,"current_step":1,"total_steps":3}`)},
		"baseline": {servicesJSON: []byte(`{}`), rampScheduleJSON: []byte(`{"percent_per_step":33,"current_step":0,"total_steps":3}`), Baseline: PlanRampBaseline{}},
	} {
		target.RampSchedule.CurrentStep = map[string]int{"progress": 1, "baseline": 0}[name]
		_, _, err := preparePlanScheduleTargets(before, edit, target, false)
		require.ErrorContains(t, err, "cannot reshape", name)
	}
	unchanged := &PlanEditorSnapshot{servicesJSON: []byte(`{}`), rampScheduleJSON: []byte(`{"percent_per_step":25,"current_step":1,"total_steps":3}`), Baseline: PlanRampBaseline{}}
	target.RampSchedule = RampSchedule{PercentPerStep: 25, CurrentStep: 1, TotalSteps: 3}
	_, _, err := preparePlanScheduleTargets(unchanged, edit, target, false)
	require.NoError(t, err, "an unchanged percent never reshapes")
}
