//go:build integration

package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPlanEditorCapturedForeignRetry(t *testing.T) {
	f := newEditorReadFixture(t)
	ctx := t.Context()
	permissions := editorFieldPermissions(auth.Permission{Action: auth.ActionUpdate, Resource: auth.ResourcePlans},
		auth.Permission{Action: auth.ActionUpdate, Resource: auth.ResourcePurchases}, auth.Permission{Action: auth.ActionUpdateAny, Resource: auth.ResourcePurchases})
	_, creator, _ := f.credential(t, permissions, nil, []string{"*"})
	before := editorFieldSnapshot(t, f, creator)
	req, _, _ := f.credential(t, permissions, nil, before.AccountIDs)
	session, err := f.h.requirePlanEditorReadSession(ctx, req)
	require.NoError(t, err)
	project := func(s *config.PlanEditorSnapshot) *planEditorFieldPolicyResult {
		t.Helper()
		require.NoError(t, f.h.authorizePlanEditorSnapshotRead(ctx, session, s))
		result, policyErr := f.h.buildPlanEditorFieldPolicy(ctx, req, session, s, time.Now().UTC())
		require.NoError(t, policyErr)
		return result
	}
	require.True(t, project(before).Graph.Steps[0].Editable)
	foreignAccount := &config.CloudAccount{Name: uuid.NewString(), Provider: "aws", ExternalID: uuid.NewString()}
	require.NoError(t, f.store.CreateCloudAccount(ctx, foreignAccount))
	foreignPlan := &config.PurchasePlan{Name: "foreign fixture", Services: map[string]config.ServiceConfig{}}
	require.NoError(t, f.store.CreatePurchasePlan(ctx, foreignPlan))
	require.NoError(t, f.store.SetPlanAccounts(ctx, foreignPlan.ID, []string{foreignAccount.ID}))
	foreignID, ownID := uuid.NewString(), before.Executions[0].ExecutionID
	_, err = f.db.Pool().Exec(ctx, `INSERT INTO purchase_executions (execution_id,plan_id,status,scheduled_date,step_number,cloud_account_id,retry_execution_id,error) VALUES ($1,$2,'pending',NOW()+INTERVAL '20 days',1,$3,$4,'')`, foreignID, foreignPlan.ID, foreignAccount.ID, ownID)
	require.NoError(t, err)
	captured, err := f.store.ReadPlanEditorSnapshot(ctx, before.Plan.ID)
	require.NoError(t, err)
	require.True(t, captured.HasIncomingRetryRoot(ownID))
	require.False(t, captured.HasIncomingRetryRoot(foreignID))
	require.Equal(t, before.Plan, captured.Plan)
	require.Equal(t, before.Executions, captured.Executions)
	require.Equal(t, before.Accounts, captured.Accounts)
	oldRevision, err := before.Revision()
	require.NoError(t, err)
	newRevision, err := captured.Revision()
	require.NoError(t, err)
	require.NotEqual(t, oldRevision, newRevision, "foreign insertion changed no captured own row")
	policy := project(captured)
	require.False(t, policy.Graph.Steps[0].Editable)
	require.Equal(t, PlanEditorFixed, policy.Graph.Steps[0].Capability)
	require.Equal(t, PlanEditorOccupiedCapacity, *policy.Graph.Steps[0].LockReason)
	require.True(t, policy.Graph.Steps[1].Editable)
	require.Len(t, policy.Graph.History, len(before.Executions))
	raw, err := json.Marshal(policy)
	require.NoError(t, err)
	require.NotContains(t, string(raw), foreignID)
	require.NotContains(t, string(raw), foreignAccount.ID)
	require.NotContains(t, string(raw), foreignPlan.ID)
	_, err = f.db.Pool().Exec(ctx, `DELETE FROM purchase_executions WHERE execution_id=$1`, foreignID)
	require.NoError(t, err)
	require.True(t, captured.HasIncomingRetryRoot(ownID), "captured presence stays conservative after foreign deletion")
	require.False(t, project(captured).Graph.Steps[0].Editable)
	cleared, err := f.store.ReadPlanEditorSnapshot(ctx, before.Plan.ID)
	require.NoError(t, err)
	require.False(t, cleared.HasIncomingRetryRoot(ownID))
	require.True(t, project(cleared).Graph.Steps[0].Editable)
	restored, err := cleared.Revision()
	require.NoError(t, err)
	require.Equal(t, oldRevision, restored, "equivalent current state is not a global event counter")
}
