package config

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func mergeServices(t *testing.T, raw string, patches map[string]PlanScheduleServicePatch, create bool) (string, error) {
	t.Helper()
	out, err := mergePlanScheduleServices([]byte(raw), patches, create)
	return string(out), err
}

func mergeRamp(raw string, intent PlanScheduleIntent, create, replaceRoots bool) (string, error) {
	out, err := mergePlanScheduleRamp([]byte(raw), intent, create, replaceRoots)
	return string(out), err
}

func TestMergePlanScheduleServicesAllFieldsAndLists(t *testing.T) {
	raw := `{"aws/ec2":{"provider":"aws","service":"ec2","enabled":true,"term":3,"payment":"all-upfront","coverage":90,
		"ramp_schedule":"x","include_engines":["a"],"exclude_engines":null,"include_regions":[],"exclude_regions":["r"],
		"include_types":["t"],"exclude_types":["u"],"min_count":5,"future":{"keep":[1]}},
		"gcp/disabled":{"provider":"gcp","service":"disabled","enabled":false,"payment":"legacy","extra":1}}`
	null, empty, pop := []string(nil), []string{}, []string{"z", "z"}
	out, err := mergeServices(t, raw, map[string]PlanScheduleServicePatch{"aws/ec2": {
		Provider: new("aws"), Service: new("ec2"), Enabled: new(false), Term: new(0), Payment: new(""),
		Coverage: new(0.0), RampSchedule: new(""), IncludeEngines: &null, ExcludeEngines: &empty,
		IncludeRegions: &pop, ExcludeRegions: &null, IncludeTypes: &empty, ExcludeTypes: &pop, MinCount: new(0)}}, false)
	require.NoError(t, err)
	require.JSONEq(t, `{"aws/ec2":{"provider":"aws","service":"ec2","enabled":false,"term":0,"payment":"","coverage":0,
		"ramp_schedule":"","include_engines":null,"exclude_engines":[],"include_regions":["z","z"],"exclude_regions":null,
		"include_types":[],"exclude_types":["z","z"],"min_count":0,"future":{"keep":[1]}},
		"gcp/disabled":{"provider":"gcp","service":"disabled","enabled":false,"payment":"legacy","extra":1}}`, out)

	out, err = mergeServices(t, raw, map[string]PlanScheduleServicePatch{"aws/ec2": {Term: new(1)}}, false)
	require.NoError(t, err)
	var got map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Equal(t, "null", string(got["aws/ec2"]["exclude_engines"]))
	require.Equal(t, "[]", string(got["aws/ec2"]["include_regions"]))
	require.Equal(t, `["a"]`, string(got["aws/ec2"]["include_engines"]))
	require.Equal(t, "1", string(got["aws/ec2"]["term"]))
	require.JSONEq(t, `{"keep":[1]}`, string(got["aws/ec2"]["future"]))
}

func TestMergePlanScheduleServicesIdentity(t *testing.T) {
	raw := `{"opaque":{"provider":"aws","service":"ec2","enabled":true,"x":{"y":1}},
		"aws/rds":{"provider":"aws","service":"rds","enabled":false},"gcp/a":{"provider":"gcp","service":"a"}}`
	out, err := mergeServices(t, raw, map[string]PlanScheduleServicePatch{"opaque": {
		Provider: new("aws"), Service: new("ec2"), Term: new(1)}}, false)
	require.NoError(t, err)
	require.Contains(t, out, `"opaque":{`, "same identity must not canonicalize an opaque key")

	out, err = mergeServices(t, raw, map[string]PlanScheduleServicePatch{"opaque": {Service: new("elasticache")}}, false)
	require.NoError(t, err)
	require.JSONEq(t, `{"aws/elasticache":{"provider":"aws","service":"elasticache","enabled":true,"x":{"y":1}},
		"aws/rds":{"provider":"aws","service":"rds","enabled":false},"gcp/a":{"provider":"gcp","service":"a"}}`, out)

	for name, patches := range map[string]map[string]PlanScheduleServicePatch{
		"disabled collision": {"opaque": {Service: new("rds")}},
		"patched pair":       {"opaque": {Service: new("s")}, "gcp/a": {Provider: new("aws"), Service: new("s")}},
		"unknown key":        {"missing": {Term: new(1)}},
	} {
		_, err = mergeServices(t, raw, patches, false)
		require.ErrorIs(t, err, ErrPurchasePlanConflict, name)
	}
	_, err = mergeServices(t, `{"aws/x":{"provider":"aws","service":"x"},"gcp/a":{"provider":"gcp","service":"a"}}`,
		map[string]PlanScheduleServicePatch{"aws/x": {Service: new("y")}, "gcp/a": {Provider: new("aws"), Service: new("x")}}, false)
	require.ErrorIs(t, err, ErrPurchasePlanConflict, "a vacated original key is still a collision")
	out, err = mergeServices(t, `{}`, map[string]PlanScheduleServicePatch{"aws/ec2": {Provider: new("aws"), Service: new("ec2")}}, true)
	require.NoError(t, err)
	require.JSONEq(t, `{"aws/ec2":{"provider":"aws","service":"ec2"}}`, out)
}

func TestMergePlanScheduleRamp(t *testing.T) {
	start := time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC)
	legacy := `{"type":"weekly","percent_per_step":33,"step_interval_days":7,"current_step":2,"total_steps":3,"start_date":"2029-01-01T00:00:00Z","future":{"k":1}}`
	out, err := mergeRamp(legacy, PlanScheduleIntent{Ramp: PlanScheduleRampPatch{
		Type: new("monthly"), PercentPerStep: new(40.0), StepIntervalDays: new(30), StartDate: &start}}, false, false)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"monthly","percent_per_step":40,"step_interval_days":30,"current_step":2,"total_steps":3,
		"start_date":"2030-01-02T00:00:00Z","future":{"k":1}}`, out)

	out, err = mergeRamp(legacy, PlanScheduleIntent{}, false, false)
	require.NoError(t, err)
	require.JSONEq(t, legacy, out, "omitted count keeps the legacy 33*3 allocation")

	out, err = mergeRamp(legacy, PlanScheduleIntent{StepCount: new(4)}, false, true)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"weekly","percent_per_step":25,"step_interval_days":7,"current_step":2,"total_steps":4,
		"start_date":"2029-01-01T00:00:00Z","future":{"k":1},"allocation_mode":"equal_count","dates_managed":true}`, out)

	out, err = mergeRamp(`{}`, PlanScheduleIntent{StepCount: new(3)}, true, false)
	require.NoError(t, err)
	require.JSONEq(t, `{"current_step":0,"total_steps":3,"percent_per_step":33.333333333333336,"allocation_mode":"equal_count"}`, out)

	for name, tc := range map[string]struct {
		raw    string
		intent PlanScheduleIntent
		create bool
	}{
		"create without count":     {`{}`, PlanScheduleIntent{}, true},
		"count below current step": {legacy, PlanScheduleIntent{StepCount: new(1)}, false},
		"zero count":               {`{"current_step":0}`, PlanScheduleIntent{StepCount: new(0)}, false},
		"count above max":          {legacy, PlanScheduleIntent{StepCount: new(MaxTotalSteps + 1)}, false},
		"percent with count":       {legacy, PlanScheduleIntent{StepCount: new(3), Ramp: PlanScheduleRampPatch{PercentPerStep: new(50.0)}}, false},
	} {
		_, err = mergeRamp(tc.raw, tc.intent, tc.create, false)
		require.ErrorIs(t, err, ErrPurchasePlanConflict, name)
	}
	_, err = mergeRamp(legacy, PlanScheduleIntent{StepCount: new(2)}, false, false)
	require.NoError(t, err, "count equal to current step is allowed")
}

func TestPreparePlanScheduleTargetsCreateAndMismatch(t *testing.T) {
	start := time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC)
	exclude := []string{}
	intent := PlanScheduleIntent{StepCount: new(2), Services: map[string]PlanScheduleServicePatch{"aws/ec2": {
		Provider: new("aws"), Service: new("ec2"), Enabled: new(true), Term: new(1), Payment: new("no-upfront"),
		Coverage: new(80.0), ExcludeEngines: &exclude}},
		Ramp: PlanScheduleRampPatch{Type: new("weekly"), StepIntervalDays: new(7), StartDate: &start}}
	plan := func() *PurchasePlan {
		return &PurchasePlan{ID: "p", Name: "n", Enabled: true,
			Services: map[string]ServiceConfig{"aws/ec2": {Provider: "aws", Service: "ec2", Enabled: true, Term: 1,
				Payment: "no-upfront", Coverage: 80, ExcludeEngines: []string{}}},
			RampSchedule: RampSchedule{Type: "weekly", PercentPerStep: 50, StepIntervalDays: 7, TotalSteps: 2, StartDate: start,
				DatesManaged: true, AllocationMode: RampAllocationEqualCount}}
	}
	services, ramp, err := preparePlanScheduleTargets(nil, intent, plan(), true)
	require.NoError(t, err)
	require.JSONEq(t, `{"aws/ec2":{"provider":"aws","service":"ec2","enabled":true,"term":1,"payment":"no-upfront","coverage":80,"exclude_engines":[]}}`, string(services))
	require.JSONEq(t, `{"type":"weekly","percent_per_step":50,"step_interval_days":7,"current_step":0,"total_steps":2,
		"start_date":"2030-01-02T00:00:00Z","allocation_mode":"equal_count","dates_managed":true}`, string(ramp))

	diverged := plan()
	diverged.Services["aws/ec2"] = ServiceConfig{Provider: "aws", Service: "ec2", Enabled: true, Term: 3}
	_, _, err = preparePlanScheduleTargets(nil, intent, diverged, true)
	require.ErrorIs(t, err, ErrPurchasePlanConflict)

	invalid := plan()
	invalid.Name = ""
	_, _, err = preparePlanScheduleTargets(nil, intent, invalid, true)
	require.Error(t, err, "create validates the complete target")

	before := &PlanEditorSnapshot{servicesJSON: services, rampScheduleJSON: ramp}
	services2, ramp2, err := preparePlanScheduleTargets(before, PlanScheduleIntent{}, plan(), false)
	require.NoError(t, err)
	require.JSONEq(t, string(services), string(services2))
	require.JSONEq(t, string(ramp), string(ramp2))
}

func TestMergePlanScheduleKeepsRawBytesWithoutHTMLEscaping(t *testing.T) {
	out, err := mergeServices(t, `{"aws/ec2":{"provider":"aws","service":"ec2","note":"<&>"}}`,
		map[string]PlanScheduleServicePatch{"aws/ec2": {Term: new(1)}}, false)
	require.NoError(t, err)
	require.Contains(t, out, `"note":"<&>"`)
	out, err = mergeRamp(`{"note":"<&>","total_steps":2}`, PlanScheduleIntent{Ramp: PlanScheduleRampPatch{Type: new("weekly")}}, false, false)
	require.NoError(t, err)
	require.Contains(t, out, `"note":"<&>"`)
	require.NotContains(t, out, "\n")
}
