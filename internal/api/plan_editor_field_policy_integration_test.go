//go:build integration

package api

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func editorFieldPermissions(extra ...auth.Permission) []auth.Permission {
	return append(editorReadPermissions(nil), extra...)
}

func editorFieldSnapshot(t *testing.T, f editorReadFixture, user string) *config.PlanEditorSnapshot {
	t.Helper()
	ctx := t.Context()
	account := &config.CloudAccount{Name: uuid.NewString(), Provider: "aws", ExternalID: uuid.NewString()}
	require.NoError(t, f.store.CreateCloudAccount(ctx, account))
	service := config.ServiceConfig{Enabled: true, Provider: "aws", Service: "ec2", Term: 1, Payment: "no-upfront", IncludeRegions: []string{"us-east-1"}}
	disabled := service
	disabled.Enabled = false
	plan := &config.PurchasePlan{Name: "policy", Enabled: true, Services: map[string]config.ServiceConfig{"primary": service, "disabled": disabled},
		RampSchedule: config.RampSchedule{Type: "weekly", PercentPerStep: 25, TotalSteps: 4, StepIntervalDays: 7}}
	require.NoError(t, f.store.CreatePurchasePlan(ctx, plan))
	require.NoError(t, f.store.SetPlanAccounts(ctx, plan.ID, []string{account.ID}))
	_, other, _ := f.credential(t, editorReadPermissions(nil), nil, []string{"*"})
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}
	slices.Sort(ids)
	for i, creator := range []string{user, other, user, other} {
		date := time.Now().UTC().AddDate(0, 0, 10+i)
		row := &config.PurchaseExecution{PlanID: plan.ID, ExecutionID: ids[i], Status: "pending", StepNumber: i + 1,
			ScheduledDate: date, CreatedByUserID: &creator, Recommendations: []config.RecommendationRecord{{CloudAccountID: &account.ID, Provider: "aws", Service: "ec2", Region: "us-east-1", ResourceType: "m5.large", Term: 1, Payment: "no-upfront"}}}
		if i >= 2 {
			row.Status, row.ScheduledExecutionAt = "scheduled", &date
		}
		require.NoError(t, f.store.SavePurchaseExecution(ctx, row))
	}
	snapshot, err := f.store.ReadPlanEditorSnapshot(ctx, plan.ID)
	require.NoError(t, err)
	return snapshot
}

func TestPlanEditorField_RealGrants(t *testing.T) {
	f := newEditorReadFixture(t)
	up := auth.Permission{Action: auth.ActionUpdate, Resource: auth.ResourcePlans}
	buy := auth.Permission{Action: auth.ActionUpdate, Resource: auth.ResourcePurchases}
	any := auth.Permission{Action: auth.ActionUpdateAny, Resource: auth.ResourcePurchases}
	own := auth.Permission{Action: auth.ActionRevokeOwn, Resource: auth.ResourcePurchases}
	revoke := auth.Permission{Action: auth.ActionRevokeAny, Resource: auth.ResourcePurchases}
	execute := auth.Permission{Action: auth.ActionExecute, Resource: auth.ResourcePurchases}
	all := editorFieldPermissions(up, buy, any, own, revoke, execute)
	for _, tc := range []struct {
		name                                                                     string
		key                                                                      []auth.Permission
		pendingOwn, pendingOther, scheduledOwn, scheduledOther, update, add, arm bool
	}{
		{"read only", editorFieldPermissions(), false, false, false, false, false, false, false},
		{"no plan update", editorFieldPermissions(buy, any, revoke), false, false, false, false, false, false, false},
		{"pending own", editorFieldPermissions(up, buy), true, false, false, false, true, true, false},
		{"update any", editorFieldPermissions(up, buy, any), true, true, false, false, true, true, false},
		{"revoke own", editorFieldPermissions(up, buy, own), true, false, true, false, true, true, false},
		{"revoke any lacks management", editorFieldPermissions(up, buy, revoke), true, false, true, false, true, true, false},
		{"revoke lacks update", editorFieldPermissions(up, revoke), false, false, false, false, true, false, false},
		{"cancel is not replace", editorFieldPermissions(up, auth.Permission{Action: auth.ActionCancelAny, Resource: auth.ResourcePurchases}), false, false, false, false, true, false, false},
		{"all", all, true, true, true, true, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, user, _ := f.credential(t, all, tc.key, []string{"*"})
			snapshot := editorFieldSnapshot(t, f, user)
			session, err := f.h.requirePlanEditorReadSession(t.Context(), req)
			require.NoError(t, err)
			require.NoError(t, f.h.authorizePlanEditorSnapshotRead(t.Context(), session, snapshot))
			f.h.config = nil
			before, err := json.Marshal(snapshot)
			require.NoError(t, err)
			result, err := f.h.buildPlanEditorMutationAuthority(t.Context(), req, session, snapshot)
			require.NoError(t, err)
			graph, err := buildPlanEditorProjection(snapshot, result.Roots, time.Now().UTC())
			require.NoError(t, err)
			for _, slot := range graph.Steps {
				expected := []bool{tc.pendingOwn, tc.pendingOther, tc.scheduledOwn, tc.scheduledOther}[slot.StepNumber-1]
				if slot.StepNumber <= 2 {
					require.Equal(t, expected, slot.Editable, "pending step%d", slot.StepNumber)
				} else {
					require.False(t, slot.Editable)
					require.Equal(t, expected, slot.RequiresApprovedEdits, "scheduled step%d", slot.StepNumber)
				}
			}
			require.Equal(t, tc.update, result.CanUpdatePlan)
			require.Equal(t, tc.add, result.Roots.AddMissingRoot)
			require.Equal(t, tc.arm, result.CanArmOrRaiseCap)
			after, err := json.Marshal(snapshot)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestPlanEditorField_ActionConstraints(t *testing.T) {
	f := newEditorReadFixture(t)
	up := auth.Permission{Action: auth.ActionUpdate, Resource: auth.ResourcePlans}
	buy := auth.Permission{Action: auth.ActionUpdate, Resource: auth.ResourcePurchases}
	any := auth.Permission{Action: auth.ActionUpdateAny, Resource: auth.ResourcePurchases}
	revoke := auth.Permission{Action: auth.ActionRevokeAny, Resource: auth.ResourcePurchases}
	all := editorFieldPermissions(up, buy, any, revoke)
	for _, tc := range []struct {
		name   string
		change func(*config.PlanEditorSnapshot)
	}{
		{"per ID region", func(s *config.PlanEditorSnapshot) { s.Executions[0].Recommendations[0].Region = "us-west-2" }},
		{"unknown service", func(s *config.PlanEditorSnapshot) { s.Executions[0].Recommendations[0].Service = "" }},
		{"empty root", func(s *config.PlanEditorSnapshot) { s.Executions[0].Recommendations = nil }},
		{"provider mismatch", func(s *config.PlanEditorSnapshot) { s.Executions[0].Recommendations[0].Provider = "azure" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := editorFieldPermissions(up, buy, any, revoke)
			key[3].Constraints = &auth.PermissionConstraints{Providers: []string{"aws"}, Services: []string{"ec2"}, Regions: []string{"us-east-1"}}
			req, user, _ := f.credential(t, all, key, []string{"*"})
			snapshot := editorFieldSnapshot(t, f, user)
			tc.change(snapshot)
			session, err := f.h.requirePlanEditorReadSession(t.Context(), req)
			require.NoError(t, err)
			require.NoError(t, f.h.authorizePlanEditorSnapshotRead(t.Context(), session, snapshot))
			result, err := f.h.buildPlanEditorMutationAuthority(t.Context(), req, session, snapshot)
			require.NoError(t, err)
			require.False(t, result.Roots.Rows[snapshot.Executions[0].ExecutionID].ReplacePending)
			require.True(t, result.Roots.Rows[snapshot.Executions[1].ExecutionID].ReplacePending, "other ID keeps its independent known-context grant")
		})
	}
	req, user, _ := f.credential(t, all, editorFieldPermissions(up, buy), []string{"*"})
	snapshot := editorFieldSnapshot(t, f, user)
	snapshot.Executions[0].CreatedByUserID = nil
	session, err := f.h.requirePlanEditorReadSession(t.Context(), req)
	require.NoError(t, err)
	result, err := f.h.buildPlanEditorMutationAuthority(t.Context(), req, session, snapshot)
	require.NoError(t, err)
	require.False(t, result.Roots.Rows[snapshot.Executions[0].ExecutionID].ReplacePending, "NULL creator is not an own grant")
	req, _, _ = f.credential(t, editorReadPermissions(nil), all, []string{"*"})
	session, err = f.h.requirePlanEditorReadSession(t.Context(), req)
	require.NoError(t, err)
	result, err = f.h.buildPlanEditorMutationAuthority(t.Context(), req, session, snapshot)
	require.NoError(t, err)
	require.False(t, result.CanUpdatePlan, "key cannot exceed owner update grants")
	for _, action := range []string{auth.ActionUpdateAny, auth.ActionRevokeAny} {
		key := editorFieldPermissions(up, buy, any, revoke)
		for i := range key {
			if key[i].Action == action {
				key[i].Constraints = &auth.PermissionConstraints{Regions: []string{"us-west-2"}}
			}
		}
		scopedReq, scopedUser, _ := f.credential(t, all, key, []string{"*"})
		scopedSnapshot := editorFieldSnapshot(t, f, scopedUser)
		scopedSession, scopedErr := f.h.requirePlanEditorReadSession(t.Context(), scopedReq)
		require.NoError(t, scopedErr)
		scopedResult, scopedErr := f.h.buildPlanEditorMutationAuthority(t.Context(), scopedReq, scopedSession, scopedSnapshot)
		require.NoError(t, scopedErr)
		require.True(t, scopedResult.Roots.Rows[scopedSnapshot.Executions[0].ExecutionID].ReplacePending)
		require.False(t, scopedResult.Roots.Rows[scopedSnapshot.Executions[3].ExecutionID].ReplaceScheduled, action)
	}
	scopedUp := up
	scopedUp.Constraints = &auth.PermissionConstraints{Regions: []string{"us-west-2"}}
	req, _, _ = f.credential(t, editorFieldPermissions(scopedUp, buy, any, revoke), all, []string{"*"})
	session, err = f.h.requirePlanEditorReadSession(t.Context(), req)
	require.NoError(t, err)
	result, err = f.h.buildPlanEditorMutationAuthority(t.Context(), req, session, snapshot)
	require.NoError(t, err)
	require.False(t, result.CanUpdatePlan, "owner constraints independently limit the broader key")
	execute := auth.Permission{Action: auth.ActionExecute, Resource: auth.ResourcePurchases}
	scopedExecute := execute
	scopedExecute.Constraints = &auth.PermissionConstraints{Regions: []string{"us-west-2"}}
	req, _, _ = f.credential(t, append(all, execute), append(editorFieldPermissions(up, buy, any, revoke), scopedExecute), []string{"*"})
	session, err = f.h.requirePlanEditorReadSession(t.Context(), req)
	require.NoError(t, err)
	result, err = f.h.buildPlanEditorMutationAuthority(t.Context(), req, session, snapshot)
	require.NoError(t, err)
	require.True(t, result.CanUpdatePlan)
	require.False(t, result.CanArmOrRaiseCap, "execute constraints apply to current captured facts")
	accountB := &config.CloudAccount{Name: uuid.NewString(), Provider: "aws", ExternalID: uuid.NewString()}
	require.NoError(t, f.store.CreateCloudAccount(t.Context(), accountB))
	rowB := snapshot.Executions[1]
	rowB.Recommendations[0].CloudAccountID = &accountB.ID
	require.NoError(t, f.store.SavePurchaseExecution(t.Context(), &rowB))
	captured, err := f.store.ReadPlanEditorSnapshot(t.Context(), snapshot.Plan.ID)
	require.NoError(t, err)
	require.Len(t, captured.Accounts, 2)
	boundedBuy := buy
	boundedBuy.Constraints = &auth.PermissionConstraints{AccountIDs: []string{snapshot.AccountIDs[0]}}
	req, _, _ = f.credential(t, all, editorFieldPermissions(up, boundedBuy, any, revoke), []string{"*"})
	session, err = f.h.requirePlanEditorReadSession(t.Context(), req)
	require.NoError(t, err)
	require.NoError(t, f.h.authorizePlanEditorSnapshotRead(t.Context(), session, captured))
	result, err = f.h.buildPlanEditorMutationAuthority(t.Context(), req, session, captured)
	require.NoError(t, err)
	require.True(t, result.Roots.Rows[captured.Executions[0].ExecutionID].ReplacePending, "unrelated B must not contaminate row A")
	require.False(t, result.Roots.Rows[captured.Executions[1].ExecutionID].ReplacePending, "per-row account ceiling denies B")
}

func TestPlanEditorField_RevocationAndInfrastructure(t *testing.T) {
	f := newEditorReadFixture(t)
	all := editorFieldPermissions(auth.Permission{Action: auth.ActionAdmin, Resource: "*"})
	req, user, keyID := f.credential(t, all, all, []string{"*"})
	snapshot := editorFieldSnapshot(t, f, user)
	session, err := f.h.requirePlanEditorReadSession(t.Context(), req)
	require.NoError(t, err)
	require.NoError(t, f.service.RevokeAPIKey(t.Context(), user, keyID))
	result, err := f.h.buildPlanEditorMutationAuthority(t.Context(), req, session, snapshot)
	require.Error(t, err)
	require.Nil(t, result)
	req, user, keyID = f.credential(t, all, all, []string{"*"})
	bearer, _, _ := f.credential(t, all, nil, []string{"*"})
	req.Headers["authorization"] = bearer.Headers["authorization"]
	session, err = f.h.requirePlanEditorReadSession(t.Context(), req)
	require.NoError(t, err)
	require.NoError(t, f.service.RevokeAPIKey(t.Context(), user, keyID))
	result, err = f.h.buildPlanEditorMutationAuthority(t.Context(), req, session, snapshot)
	require.Error(t, err, "presented key must not become a different bearer identity")
	require.Nil(t, result)
	bearerSession, err := f.h.requirePlanEditorReadSession(t.Context(), bearer)
	require.NoError(t, err)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	result, err = f.h.buildPlanEditorMutationAuthority(canceled, bearer, bearerSession, snapshot)
	require.Error(t, err)
	require.Nil(t, result)
	failure := errors.New("synthetic permission constraint lookup")
	original := f.h.auth
	f.h.auth = editorReadConstraintFailure{f.h.auth, failure}
	result, err = f.h.buildPlanEditorMutationAuthority(t.Context(), bearer, bearerSession, snapshot)
	require.ErrorIs(t, err, failure)
	require.Nil(t, result)
	f.h.auth = original
	req.Headers = map[string]string{"x-api-key": f.h.apiKey}
	session, err = f.h.requirePlanEditorReadSession(t.Context(), req)
	require.NoError(t, err)
	result, err = f.h.buildPlanEditorMutationAuthority(t.Context(), req, session, snapshot)
	require.NoError(t, err)
	require.True(t, result.CanUpdatePlan, "disarming is still possible")
	require.False(t, result.CanArmOrRaiseCap, "infrastructure credential cannot arm")
}
