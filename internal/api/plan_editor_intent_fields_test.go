package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/require"
)

func TestPlanEditorServiceFieldIntent(t *testing.T) {
	for _, value := range []string{"", "null", "[]", `["b","a","b"]`, `[""]`} {
		t.Run(value, func(t *testing.T) {
			fields := map[string]json.RawMessage{}
			for _, key := range []string{"include_engines", "exclude_engines", "include_regions", "exclude_regions", "include_types", "exclude_types"} {
				if value != "" {
					fields[key] = json.RawMessage(value)
				}
			}
			raw, err := json.Marshal(fields)
			require.NoError(t, err)
			patch, err := decodePlanEditorService(raw)
			require.NoError(t, err)
			normalized, err := json.Marshal(patch)
			require.NoError(t, err)
			require.JSONEq(t, string(raw), string(normalized))
		})
	}
	raw := `{"provider":"aws","service":"ec2","enabled":false,"term":1,"payment":"no-upfront","coverage":0,"ramp_schedule":"","min_count":0}`
	patch, err := decodePlanEditorService([]byte(raw))
	require.NoError(t, err)
	require.Equal(t, config.PlanScheduleServicePatch{Provider: new("aws"), Service: new("ec2"), Enabled: new(false), Term: new(1),
		Payment: new("no-upfront"), Coverage: new(0.0), RampSchedule: new(""), MinCount: new(0)}, patch)
}

func TestPlanEditorPlanFieldIntent(t *testing.T) {
	for _, tc := range []struct {
		value    string
		expected *float64
	}{{"", nil}, {"null", nil}, {"0", new(0.0)}, {"12.5", new(12.5)}} {
		capValue := tc.value
		raw := `{"name":"","enabled":false,"auto_purchase":false,"notification_days_before":0`
		if capValue != "" {
			raw += `,"max_step_commitment":` + capValue
		}
		patch, err := decodePlanEditorPlan([]byte(raw + "}"))
		require.NoError(t, err)
		require.Equal(t, new(""), patch.Plan.Name)
		require.Equal(t, new(false), patch.Plan.Enabled)
		require.Equal(t, new(false), patch.Plan.AutoPurchase)
		require.Equal(t, new(0), patch.Plan.NotificationDaysBefore)
		require.Equal(t, capValue != "", patch.Plan.CapSupplied)
		require.Equal(t, tc.expected, patch.Plan.MaxStepCommitment)
	}
	patch, err := decodePlanEditorPlan([]byte(`{"services":{"opaque/key":{}},"ramp_schedule":{"type":"custom","percent_per_step":0,"step_interval_days":0,"start_date":"2000-01-01"},"target_accounts":[]}`))
	require.NoError(t, err)
	require.Contains(t, patch.Services, "opaque/key")
	require.Equal(t, config.PlanScheduleRampPatch{Type: new("custom"), PercentPerStep: new(0.0), StepIntervalDays: new(0),
		StartDate: new(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))}, patch.Ramp)
	require.NotNil(t, patch.AccountIDs)
	require.NotNil(t, *patch.AccountIDs)
}

func TestPlanEditorFieldIntentRejectsMalformed(t *testing.T) {
	for _, raw := range []string{
		`{"name":null}`, `{"name":"","name":"again"}`, `{"Name":""}`, `{"description":""}`,
		`{"services":null}`, `{"services":{"key":{},"key":{}}}`, `{"services":{"key":null}}`,
		`{"services":{"key":{"coverage":0,"coverage":1}}}`, `{"services":{"key":{"coverage":"0"}}}`,
		`{"services":{"key":{"include_engines":[1]}}}`, `{"services":{"key":{"provider":"other"}}}`,
		`{"services":{"key":{"service":""}}}`, `{"services":{"key":{"term":2}}}`, `{"services":{"key":{"payment":"other"}}}`,
		`{"ramp_schedule":{"start_date":"2000-02-30"}}`, `{"ramp_schedule":{"start_date":"2000-01-01T00:00:00Z"}}`,
		`{"ramp_schedule":{"type":"other"}}`, `{"ramp_schedule":{"type":"custom","type":"weekly"}}`,
		`{"ramp_schedule":{"current_step":0}}`, `{"ramp_schedule":{"total_steps":3}}`,
		`{"target_accounts":["11111111111141118111111111111111"]}`, `{"target_accounts":null}`, `{} {}`, `[]`,
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := decodePlanEditorPlan([]byte(raw))
			require.Error(t, err)
		})
	}
	for _, key := range []string{"include_engines", "exclude_engines", "include_regions", "exclude_regions", "include_types", "exclude_types"} {
		for _, value := range []string{`[null]`, `["a",null,"b"]`} {
			t.Run(key+"/"+value, func(t *testing.T) {
				_, err := decodePlanEditorService([]byte(`{"` + key + `":` + value + `}`))
				require.Error(t, err)
			})
		}
	}
	for _, key := range []string{"provider", "service", "enabled", "term", "payment", "coverage", "ramp_schedule", "min_count"} {
		_, err := decodePlanEditorService([]byte(`{"` + key + `":null}`))
		require.Error(t, err, key)
	}
}
