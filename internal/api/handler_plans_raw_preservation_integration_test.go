//go:build integration

package api

import (
	"encoding/json"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLegacyPlanPUTPreservesStoredServiceJSON(t *testing.T) {
	f := newEditorReadFixture(t)
	ctx := t.Context()
	permissions := []auth.Permission{{Action: auth.ActionUpdate, Resource: auth.ResourcePlans}}
	req, _, _ := f.credential(t, permissions, permissions, []string{"*"})
	account := &config.CloudAccount{Name: uuid.NewString(), Provider: "aws", ExternalID: uuid.NewString()}
	require.NoError(t, f.store.CreateCloudAccount(ctx, account))
	plan := &config.PurchasePlan{Name: "stored", Services: map[string]config.ServiceConfig{"aws/ec2": {Provider: "aws", Service: "ec2", Term: 1, Payment: "no-upfront", Coverage: 80}}, RampSchedule: config.PresetRampSchedules["immediate"]}
	require.NoError(t, f.store.CreatePurchasePlan(ctx, plan))
	require.NoError(t, f.store.SetPlanAccounts(ctx, plan.ID, []string{account.ID}))
	raw := `{"aws/ec2":{"provider":"aws","service":"ec2","enabled":false,"term":1,"payment":"no-upfront","coverage":80,"ramp_schedule":"custom","include_engines":[],"exclude_engines":["engine"],"include_regions":null,"exclude_regions":[],"include_types":["type"],"exclude_types":null,"min_count":3,"future":{"exact":9007199254740993}}}`
	_, err := f.db.Pool().Exec(ctx, `UPDATE purchase_plans SET services=$2 WHERE id=$1`, plan.ID, raw)
	require.NoError(t, err)
	req.Headers["content-type"] = "application/json"
	req.RawPath = "/api/plans/" + plan.ID
	req.RequestContext.HTTP = events.LambdaFunctionURLRequestContextHTTPDescription{Method: "PUT", Path: req.RawPath}
	req.Body = `{"name":"rename","provider":"aws","service":"ec2","term":1,"payment":"no-upfront","target_coverage":80,"ramp_schedule":"immediate","enabled":false,"auto_purchase":false,"notification_days_before":0}`
	response, err := f.h.HandleRequest(ctx, req)
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode, response.Body)
	var stored string
	require.NoError(t, f.db.Pool().QueryRow(ctx, `SELECT services::text FROM purchase_plans WHERE id=$1`, plan.ID).Scan(&stored))
	require.JSONEq(t, raw, stored)
	require.Contains(t, stored, "9007199254740993")
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(response.Body), &payload))
	require.JSONEq(t, `"rename"`, string(payload["name"]))
	req.Body = `{"name":"lossy replacement","provider":"aws","service":"rds","ramp_schedule":"immediate","notification_days_before":0}`
	response, err = f.h.HandleRequest(ctx, req)
	require.NoError(t, err)
	require.Equal(t, 409, response.StatusCode, response.Body)
	require.NoError(t, f.db.Pool().QueryRow(ctx, `SELECT services::text FROM purchase_plans WHERE id=$1`, plan.ID).Scan(&stored))
	require.JSONEq(t, raw, stored)
}
