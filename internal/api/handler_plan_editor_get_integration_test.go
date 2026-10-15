//go:build integration

package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func editorGET(t *testing.T, f editorReadFixture, req *events.LambdaFunctionURLRequest, id string, status int) map[string]json.RawMessage {
	t.Helper()
	req.RequestContext.HTTP.Method = "GET"
	req.RequestContext.HTTP.Path = "/api/plans/" + id + "/editor"
	response, err := f.h.HandleRequest(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, status, response.StatusCode, "editor response: %s", response.Body)
	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(response.Body), &body))
	return body
}

func TestPlanEditorGETCompleteStoredValues(t *testing.T) {
	f := newEditorReadFixture(t)
	req, _, _ := f.credential(t, editorReadPermissions(nil), nil, []string{"*"})
	start := time.Date(2026, 9, 1, 13, 14, 15, 0, time.UTC)
	plan := &config.PurchasePlan{Name: "complete editor", RampSchedule: config.RampSchedule{Type: "custom", PercentPerStep: 33, TotalSteps: 3, StartDate: start}}
	require.NoError(t, f.store.CreatePurchasePlan(t.Context(), plan))
	services := `{"disabled":{"provider":"aws","service":"ec2","enabled":false,"term":0,"payment":"legacy-payment","coverage":0,"ramp_schedule":"legacy-ramp","include_engines":null,"exclude_engines":[],"include_regions":[],"exclude_regions":null,"include_types":null,"exclude_types":[],"min_count":0,"private_marker":"must-stay-private"}}`
	_, err := f.db.Exec(t.Context(), `UPDATE purchase_plans SET services=$2 WHERE id=$1`, plan.ID, services)
	require.NoError(t, err)
	before := editorDomainState(t, f)
	body := editorGET(t, f, req, plan.ID, 200)
	require.Equal(t, before, editorDomainState(t, f), "GET must preserve domain rows byte-for-byte")
	require.ElementsMatch(t, []string{"plan", "account_ids", "accounts", "revision", "history", "graph", "graph_unavailable_reason", "ledger_witness", "field_policy"}, editorJSONKeys(body))
	var stored map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body["plan"], &stored))
	require.ElementsMatch(t, []string{"id", "name", "enabled", "auto_purchase", "notification_days_before", "services", "ramp_schedule", "created_at", "updated_at", "next_execution_date", "last_execution_date", "last_notification_sent", "max_step_commitment"}, editorJSONKeys(stored))
	require.JSONEq(t, `false`, string(stored["enabled"]))
	require.JSONEq(t, `false`, string(stored["auto_purchase"]))
	require.JSONEq(t, `0`, string(stored["notification_days_before"]))
	for _, key := range []string{"next_execution_date", "last_execution_date", "last_notification_sent", "max_step_commitment"} {
		require.Equal(t, "null", string(stored[key]), key)
	}
	var all map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(stored["services"], &all))
	require.Len(t, all, 1)
	service := all["disabled"]
	require.ElementsMatch(t, []string{"provider", "service", "enabled", "term", "payment", "coverage", "ramp_schedule", "include_engines", "exclude_engines", "include_regions", "exclude_regions", "include_types", "exclude_types", "min_count"}, editorJSONKeys(service))
	require.Equal(t, "null", string(service["include_engines"]))
	require.Equal(t, "[]", string(service["exclude_engines"]))
	require.Equal(t, "[]", string(service["include_regions"]))
	require.Equal(t, "null", string(service["exclude_regions"]))
	require.Equal(t, "null", string(service["include_types"]))
	require.Equal(t, "[]", string(service["exclude_types"]))
	require.Equal(t, "0", string(service["min_count"]))
	require.JSONEq(t, `"legacy-payment"`, string(service["payment"]))
	require.NotContains(t, string(body["plan"]), "must-stay-private")
	require.Equal(t, "[]", string(body["account_ids"]))
	require.Equal(t, "[]", string(body["accounts"]))
	require.Equal(t, "[]", string(body["history"]))
	var ramp map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(stored["ramp_schedule"], &ramp))
	require.ElementsMatch(t, []string{"type", "percent_per_step", "step_interval_days", "current_step", "total_steps", "start_date"}, editorJSONKeys(ramp))
	require.JSONEq(t, `"2026-09-01T13:14:15Z"`, string(ramp["start_date"]))
	require.Equal(t, "33", string(ramp["percent_per_step"]))
	var graph map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body["graph"], &graph))
	require.ElementsMatch(t, []string{"current_step", "total_steps", "structural_shape_change_allowed", "shape_lock_reason", "steps"}, editorJSONKeys(graph))
	var slots []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(graph["steps"], &slots))
	require.Len(t, slots, 3)
	require.Equal(t, "99", string(slots[2]["cumulative_target_percent"]))
	for _, raw := range slots {
		require.ElementsMatch(t, []string{"step_number", "logical_completed", "logical_completion_source", "execution_id", "scheduled_date", "status", "cumulative_target_percent", "root_state", "approval_state", "capability", "lock_reason", "editable", "requires_approved_edits", "display"}, editorJSONKeys(raw))
	}
	for _, raw := range []string{"null", "{}"} {
		_, err = f.db.Exec(t.Context(), `UPDATE purchase_plans SET services=$2, ramp_schedule=jsonb_set(ramp_schedule,'{total_steps}','0') WHERE id=$1`, plan.ID, raw)
		require.NoError(t, err)
		ordinary := editorGET(t, f, req, plan.ID, 200)
		var p map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(ordinary["plan"], &p))
		require.JSONEq(t, raw, string(p["services"]))
		require.Equal(t, "null", string(ordinary["graph"]))
		require.NotEqual(t, "null", string(ordinary["graph_unavailable_reason"]))
		require.NotEqual(t, "null", string(ordinary["field_policy"]))
		require.Equal(t, "[]", string(ordinary["history"]))
	}
}

func editorJSONKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func editorDomainState(t *testing.T, f editorReadFixture) string {
	t.Helper()
	var state string
	err := f.db.QueryRow(t.Context(), `SELECT jsonb_build_object(
		'plans',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM purchase_plans p),
		'accounts',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM cloud_accounts a),
		'associations',(SELECT jsonb_agg(to_jsonb(a) ORDER BY plan_id,account_id) FROM plan_accounts a),
		'executions',(SELECT jsonb_agg(to_jsonb(e) ORDER BY execution_id) FROM purchase_executions e),
		'suppressions',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM purchase_suppressions s))::text`).Scan(&state)
	require.NoError(t, err)
	return state
}

func TestPlanEditorGETReadAuthority(t *testing.T) {
	f := newEditorReadFixture(t)
	accounts := make([]config.CloudAccount, 2)
	for i := range accounts {
		accounts[i] = config.CloudAccount{Name: uuid.NewString(), Provider: "aws", ExternalID: uuid.NewString()}
		require.NoError(t, f.store.CreateCloudAccount(t.Context(), &accounts[i]))
	}
	plan := &config.PurchasePlan{Name: "historical scope", Services: map[string]config.ServiceConfig{"all": {Provider: "aws", Service: "ec2"}}, RampSchedule: config.RampSchedule{Type: "immediate", TotalSteps: 1}}
	require.NoError(t, f.store.CreatePurchasePlan(t.Context(), plan))
	require.NoError(t, f.store.SetPlanAccounts(t.Context(), plan.ID, []string{accounts[0].ID}))
	root := &config.PurchaseExecution{PlanID: plan.ID, ExecutionID: uuid.NewString(), Status: config.StatusCanceled, CloudAccountID: &accounts[1].ID}
	require.NoError(t, f.store.SavePurchaseExecution(t.Context(), root))
	both := editorReadPermissions(nil)
	bounded := editorReadPermissions(&auth.PermissionConstraints{AccountIDs: []string{accounts[0].ID}})
	for _, tc := range []struct {
		name       string
		owner, key []auth.Permission
		allowed    []string
		status     int
	}{
		{"complete", both, both, []string{"*"}, 200},
		{"key plans only", both, both[:1], []string{"*"}, 403},
		{"key purchases only", both, both[1:], []string{"*"}, 403},
		{"owner plans only", both[:1], both, []string{"*"}, 403},
		{"key historical bound", both, bounded, []string{"*"}, 403},
		{"owner historical bound", bounded, both, []string{"*"}, 403},
		{"owner historical scope", both, both, []string{accounts[0].ID}, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _, key := f.credential(t, tc.owner, tc.key, tc.allowed)
			body := editorGET(t, f, req, plan.ID, tc.status)
			if tc.status != 200 {
				require.NotContains(t, body, "plan", "denial must not return a partial editor")
				return
			}
			var labels []map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(body["accounts"], &labels))
			require.Len(t, labels, 2)
			for _, label := range labels {
				require.ElementsMatch(t, []string{"id", "name", "provider"}, editorJSONKeys(label))
			}
			require.JSONEq(t, `["`+accounts[0].ID+`"]`, string(body["account_ids"]))
			require.NotEqual(t, "null", string(body["graph"]), "immediate one-step remains supported")
			snapshot, err := f.store.ReadPlanEditorSnapshot(t.Context(), plan.ID)
			require.NoError(t, err)
			revision, err := snapshot.Revision()
			require.NoError(t, err)
			require.JSONEq(t, `"`+revision+`"`, string(body["revision"]))
			require.Eventually(t, func() bool {
				var count int
				err := f.db.QueryRow(t.Context(), `SELECT request_count_total FROM api_keys WHERE id=$1`, key).Scan(&count)
				return err == nil && count == 1
			}, 3*time.Second, 10*time.Millisecond, "existing key usage telemetry must remain enabled")
		})
	}
	keyReq, _, _ := f.credential(t, both, bounded, []string{"*"})
	bearer, _, _ := f.credential(t, both, nil, []string{"*"})
	keyReq.Headers["authorization"] = bearer.Headers["authorization"]
	editorGET(t, f, keyReq, plan.ID, 403)
	editorGET(t, f, bearer, uuid.NewString(), 404)
	editorGET(t, f, bearer, "not-a-uuid", 400)
	_, err := f.db.Exec(t.Context(), `UPDATE purchase_plans SET ramp_schedule=jsonb_set(ramp_schedule,'{current_step}','2') WHERE id=$1`, plan.ID)
	require.NoError(t, err)
	editorGET(t, f, bearer, plan.ID, 409)
}
