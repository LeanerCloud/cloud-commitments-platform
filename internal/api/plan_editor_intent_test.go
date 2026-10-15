package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/require"
)

func editorIntentBody(t *testing.T, change func(map[string]any)) string {
	t.Helper()
	body := map[string]any{
		"command_id":        "11111111-1111-4111-8111-111111111111",
		"expected_revision": "sha256:" + strings.Repeat("a", 64),
		"plan": map[string]any{"name": "kept", "enabled": false, "max_step_commitment": nil,
			"services":      map[string]any{"aws/ec2": map[string]any{"coverage": 0, "include_engines": nil, "exclude_engines": []string{}, "include_regions": []string{"b", "a", "b"}}},
			"ramp_schedule": map[string]any{"start_date": "2000-01-01"}},
		"steps": []any{map[string]any{"step_number": 3, "scheduled_date": "2000-01-01"}, map[string]any{"step_number": 4, "scheduled_date": nil}},
	}
	change(body)
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	return string(raw)
}

func TestDecodePlanEditorIntentPresence(t *testing.T) {
	parsed, err := decodePlanEditorIntent(editorIntentBody(t, func(map[string]any) {}))
	require.NoError(t, err)
	require.True(t, parsed.Plan.CapSupplied)
	require.Nil(t, parsed.Plan.MaxStepCommitment)
	require.Equal(t, new(false), parsed.Plan.Enabled)
	service := parsed.Services["aws/ec2"]
	require.Equal(t, new(0.0), service.Coverage)
	require.NotNil(t, service.IncludeEngines)
	require.Nil(t, *service.IncludeEngines)
	require.NotNil(t, service.ExcludeEngines)
	require.NotNil(t, *service.ExcludeEngines)
	require.Empty(t, *service.ExcludeEngines)
	require.Equal(t, []string{"b", "a", "b"}, *service.IncludeRegions)
	require.Nil(t, service.ExcludeRegions)
	require.Nil(t, parsed.StepCount)
	require.Nil(t, parsed.AccountIDs)
	require.Equal(t, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), *parsed.Ramp.StartDate)
	require.Equal(t, *parsed.Ramp.StartDate, *parsed.Steps[0].ScheduledDate)
	require.Nil(t, parsed.Steps[1].ScheduledDate)
	require.Error(t, validatePlanEditorSaveProofs(parsed))
}

func TestDecodePlanEditorIntentCreateAndProofs(t *testing.T) {
	raw := editorIntentBody(t, func(b map[string]any) {
		b["expected_revision"] = nil
		b["step_count"] = 3
		b["plan"].(map[string]any)["target_accounts"] = []string{"33333333-3333-4333-8333-333333333333", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}
		b["reviewed_diff"] = "sha256:" + strings.Repeat("b", 64)
		b["acknowledged_cancellation_ids"] = []string{"44444444-4444-4444-8444-444444444444"}
		b["acknowledged_reapproval_ids"] = []string{}
	})
	parsed, err := decodePlanEditorIntent(raw)
	require.NoError(t, err)
	require.Nil(t, parsed.ExpectedRevision)
	require.Equal(t, new(3), parsed.StepCount)
	require.Equal(t, []string{"22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}, *parsed.AccountIDs)
	require.NoError(t, validatePlanEditorSaveProofs(parsed))
}

func TestDecodePlanEditorIntentRejectsMalformed(t *testing.T) {
	base := editorIntentBody(t, func(map[string]any) {})
	for _, tc := range []struct{ name, old, replacement string }{
		{"duplicate envelope", "\"command_id\":", "\"command_id\":null,\"command_id\":"},
		{"duplicate service", "\"coverage\":0", "\"coverage\":0,\"coverage\":1"},
		{"duplicate step", "\"step_number\":3", "\"step_number\":3,\"step_number\":4"},
		{"unknown", "\"name\":", "\"description\":null,\"name\":"},
		{"readonly ramp", "\"start_date\":", "\"current_step\":0,\"start_date\":"},
		{"scalar null", "\"enabled\":false", "\"enabled\":null"},
		{"scalar type", "\"coverage\":0", "\"coverage\":\"0\""},
		{"list type", "\"include_engines\":null", "\"include_engines\":[1]"},
		{"missing date", "\"scheduled_date\":\"2000-01-01\",", ""},
		{"bad date", "2000-01-01", "2000-02-30"},
		{"missing revision", "\"expected_revision\":\"sha256:" + strings.Repeat("a", 64) + "\",", ""},
		{"noncanonical UUID", "11111111-1111-4111-8111-111111111111", "11111111111141118111111111111111"},
		{"unknown envelope", "\"steps\":", "\"changes\":{},\"steps\":"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Contains(t, base, tc.old)
			_, err := decodePlanEditorIntent(strings.Replace(base, tc.old, tc.replacement, 1))
			require.Error(t, err)
		})
	}
	_, err := decodePlanEditorIntent(base + " {}")
	require.Error(t, err)
}

func TestDecodePlanEditorEnvelopeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"missing revision", func(b map[string]any) { delete(b, "expected_revision") }},
		{"empty revision", func(b map[string]any) { b["expected_revision"] = "" }},
		{"uppercase revision", func(b map[string]any) { b["expected_revision"] = "sha256:" + strings.Repeat("A", 64) }},
		{"missing command", func(b map[string]any) { delete(b, "command_id") }},
		{"nil command", func(b map[string]any) { b["command_id"] = "00000000-0000-0000-0000-000000000000" }},
		{"null command", func(b map[string]any) { b["command_id"] = nil }},
		{"missing plan", func(b map[string]any) { delete(b, "plan") }},
		{"null plan", func(b map[string]any) { b["plan"] = nil }},
		{"missing steps", func(b map[string]any) { delete(b, "steps") }},
		{"null steps", func(b map[string]any) { b["steps"] = nil }},
		{"zero count", func(b map[string]any) { b["step_count"] = 0 }},
		{"large count", func(b map[string]any) { b["step_count"] = config.MaxTotalSteps + 1 }},
		{"fractional count", func(b map[string]any) { b["step_count"] = 2.5 }},
		{"null count", func(b map[string]any) { b["step_count"] = nil }},
		{"create missing count", func(b map[string]any) { b["expected_revision"] = nil }},
		{"create missing accounts", func(b map[string]any) { b["expected_revision"], b["step_count"] = nil, 3 }},
		{"unknown actor", func(b map[string]any) { b["user_id"] = "11111111-1111-4111-8111-111111111111" }},
		{"bad acknowledgement", func(b map[string]any) { b["acknowledged_cancellation_ids"] = []string{"invalid"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodePlanEditorIntent(editorIntentBody(t, tc.change))
			require.Error(t, err)
		})
	}
}

func TestDecodePlanEditorOrderedStepBoundaries(t *testing.T) {
	for _, steps := range []any{
		[]any{map[string]any{"step_number": 0, "scheduled_date": "2000-01-01"}},
		[]any{map[string]any{"step_number": config.MaxTotalSteps + 1, "scheduled_date": "2000-01-01"}},
		[]any{map[string]any{"step_number": 3, "scheduled_date": "2000-01-01"}, map[string]any{"step_number": 3, "scheduled_date": nil}},
		[]any{map[string]any{"step_number": 4, "scheduled_date": "2000-01-01"}, map[string]any{"step_number": 3, "scheduled_date": nil}},
		[]any{map[string]any{"step_number": nil, "scheduled_date": "2000-01-01"}},
		[]any{map[string]any{"step_number": 3, "scheduled_date": 1}},
		[]any{map[string]any{"step_number": 3, "scheduled_date": nil, "execution_id": "11111111-1111-4111-8111-111111111111"}},
	} {
		raw := editorIntentBody(t, func(b map[string]any) { b["steps"] = steps })
		_, err := decodePlanEditorIntent(raw)
		require.Error(t, err, raw)
	}
}

func TestPlanEditorSaveProofSyntax(t *testing.T) {
	for _, token := range []string{"", "sha256:" + strings.Repeat("a", 63), "sha256:" + strings.Repeat("z", 64), "sha256:" + strings.Repeat("A", 64)} {
		raw := editorIntentBody(t, func(b map[string]any) {
			b["reviewed_diff"] = token
			b["acknowledged_cancellation_ids"], b["acknowledged_reapproval_ids"] = []string{}, []string{}
		})
		intent, err := decodePlanEditorIntent(raw)
		require.NoError(t, err)
		require.Error(t, validatePlanEditorSaveProofs(intent))
	}
}
