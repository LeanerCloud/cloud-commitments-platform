//go:build integration

package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type editorReadFixture struct {
	h         *Handler
	store     *config.PostgresStore
	authStore *auth.PostgresStore
	service   *auth.Service
	csrf      []byte
	db        *database.Connection
}

func newEditorReadFixture(t *testing.T) editorReadFixture {
	t.Helper()
	pg, err := testhelpers.SetupPostgresContainer(t.Context(), t)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Cleanup(context.Background())) })
	require.NoError(t, migrations.RunMigrations(t.Context(), pg.DB.Pool(), "../database/postgres/migrations", "", ""))
	store, authStore := config.NewPostgresStore(pg.DB), auth.NewPostgresStore(pg.DB)
	csrf := []byte(strings.Repeat("e", 32))
	service := auth.NewService(auth.ServiceConfig{Store: authStore, CSRFKey: csrf})
	h := NewHandler(HandlerConfig{ConfigStore: store, AuthService: &marketplaceAuthFixture{service: service}})
	h.apiKey = uuid.NewString()
	return editorReadFixture{h, store, authStore, service, csrf, pg.DB}
}

func editorReadPermissions(c *auth.PermissionConstraints) []auth.Permission {
	return []auth.Permission{{Action: auth.ActionView, Resource: auth.ResourcePlans, Constraints: c}, {Action: auth.ActionView, Resource: auth.ResourcePurchases, Constraints: c}}
}

func (f editorReadFixture) credential(t *testing.T, owner, keyPermissions []auth.Permission, allowed []string) (*events.LambdaFunctionURLRequest, string, string) {
	t.Helper()
	user, token, _ := marketplaceSession(t, f.authStore, owner, allowed, f.csrf)
	req := &events.LambdaFunctionURLRequest{Headers: map[string]string{"authorization": "Bearer " + token}}
	if keyPermissions == nil {
		return req, user, ""
	}
	key := "cudly_" + uuid.NewString()
	hash := sha256.Sum256([]byte(key))
	stored := &auth.UserAPIKey{UserID: user, Name: "editor", KeyPrefix: key[:12], KeyHash: base64.RawURLEncoding.EncodeToString(hash[:]), IsActive: true, Permissions: keyPermissions}
	require.NoError(t, f.authStore.CreateAPIKey(t.Context(), stored))
	req.Headers = map[string]string{"x-api-key": key}
	return req, user, stored.ID
}

func TestPlanEditorRead_RequiresBothResources(t *testing.T) {
	f := newEditorReadFixture(t)
	both := editorReadPermissions(nil)
	for _, key := range [][]auth.Permission{both[:1], both[1:]} {
		req, _, _ := f.credential(t, both, key, []string{"*"})
		session, err := f.h.requirePlanEditorReadSession(t.Context(), req)
		require.Error(t, err, "either missing read grant must deny")
		require.Nil(t, session)
		clientErr, ok := IsClientError(err)
		require.True(t, ok)
		require.Equal(t, 403, clientErr.code)
	}
	req, user, keyID := f.credential(t, both, both, []string{"*"})
	session, err := f.h.requirePlanEditorReadSession(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, user, session.UserID)
	require.Equal(t, keyID, session.UserAPIKeyID)
	require.NoError(t, f.service.RevokeAPIKey(t.Context(), user, keyID))
	session, err = f.h.requirePlanEditorReadSession(t.Context(), req)
	require.Error(t, err)
	require.Nil(t, session)
	clientErr, ok := IsClientError(err)
	require.True(t, ok)
	require.Equal(t, 401, clientErr.code, "key-only revocation cannot fall back to bearer")
}

func TestPlanEditorRead_CapturedHistoricalScope(t *testing.T) {
	f := newEditorReadFixture(t)
	ctx := t.Context()
	accounts := make([]config.CloudAccount, 4)
	for i := range accounts {
		accounts[i] = config.CloudAccount{Name: uuid.NewString(), Provider: "aws", ExternalID: uuid.NewString()}
		require.NoError(t, f.store.CreateCloudAccount(ctx, &accounts[i]))
	}
	service := config.ServiceConfig{Provider: "aws", Service: "ec2", IncludeRegions: []string{"us-east-1"}}
	plan := &config.PurchasePlan{Name: "editor", Services: map[string]config.ServiceConfig{"ec2": service}, RampSchedule: config.RampSchedule{Type: "weekly", PercentPerStep: 25, TotalSteps: 4, StepIntervalDays: 7}}
	require.NoError(t, f.store.CreatePurchasePlan(ctx, plan))
	require.NoError(t, f.store.SetPlanAccounts(ctx, plan.ID, []string{accounts[0].ID}))
	rec := config.RecommendationRecord{Provider: "aws", Service: "ec2", Region: "us-east-1", ResourceType: "m5.large", Term: 1, Payment: "no-upfront", CloudAccountID: &accounts[2].ID}
	require.NoError(t, f.store.SavePurchaseExecution(ctx, &config.PurchaseExecution{PlanID: plan.ID, ExecutionID: uuid.NewString(), Status: config.StatusCanceled, CloudAccountID: &accounts[1].ID, StepNumber: 1}))
	require.NoError(t, f.store.SavePurchaseExecution(ctx, &config.PurchaseExecution{PlanID: plan.ID, ExecutionID: uuid.NewString(), Status: config.StatusCanceled, Recommendations: []config.RecommendationRecord{rec}, StepNumber: 2}))
	rec.CloudAccountID = &accounts[3].ID
	identity, err := config.RecIdentityKey(&rec)
	require.NoError(t, err)
	raw, err := json.Marshal(config.PlanRampBaseline{identity: {Count: 100}})
	require.NoError(t, err)
	_, err = f.db.Exec(ctx, `UPDATE purchase_plans SET ramp_baseline=$2 WHERE id=$1`, plan.ID, raw)
	require.NoError(t, err)
	full, err := f.store.ReadPlanEditorSnapshot(ctx, plan.ID)
	require.NoError(t, err)
	require.Len(t, full.Accounts, 4, "current, removed row, unselected rec and baseline-only accounts")
	knownPlan := &config.PurchasePlan{Name: "known", Services: plan.Services, RampSchedule: plan.RampSchedule}
	require.NoError(t, f.store.CreatePurchasePlan(ctx, knownPlan))
	require.NoError(t, f.store.SetPlanAccounts(ctx, knownPlan.ID, []string{accounts[0].ID}))
	known, err := f.store.ReadPlanEditorSnapshot(ctx, knownPlan.ID)
	require.NoError(t, err)
	require.NoError(t, f.store.SavePurchaseExecution(ctx, &config.PurchaseExecution{PlanID: knownPlan.ID, ExecutionID: uuid.NewString(), Status: "pending", StepNumber: 1}))
	emptyRoot, err := f.store.ReadPlanEditorSnapshot(ctx, knownPlan.ID)
	require.NoError(t, err)
	unassignedPlan := &config.PurchasePlan{Name: "unassigned", Services: map[string]config.ServiceConfig{"disabled": {Provider: "azure", Service: "compute", IncludeRegions: []string{"eastus"}}}}
	require.NoError(t, f.store.CreatePurchasePlan(ctx, unassignedPlan))
	unassigned, err := f.store.ReadPlanEditorSnapshot(ctx, unassignedPlan.ID)
	require.NoError(t, err)
	unmatched := *known
	unmatched.Plan = &config.PurchasePlan{Services: unassignedPlan.Services}
	excluded := *known
	excluded.Plan = &config.PurchasePlan{Services: map[string]config.ServiceConfig{"ec2": {Provider: "aws", Service: "ec2", ExcludeRegions: []string{"us-west-2"}}}}
	mixedKnown := *known
	mixedKnown.Accounts = []config.PlanEditorAccount{{ID: accounts[0].ID, Name: accounts[0].Name, Provider: "azure"}}
	mixedKnown.Plan = &config.PurchasePlan{Services: unassignedPlan.Services}
	mixed := mixedKnown
	rec.CloudAccountID = &accounts[0].ID
	mixed.Executions = []config.PurchaseExecution{{Recommendations: []config.RecommendationRecord{rec}}}
	mixedBounds := &auth.PermissionConstraints{Providers: []string{"aws", "azure"}, Services: []string{"ec2", "compute"}, Regions: []string{"us-east-1", "eastus"}}
	ledger := *known
	ledger.Committed = map[string]config.PlanRampQuantity{strings.Replace(strings.Replace(identity, accounts[3].ID, accounts[0].ID, 1), "us-east-1", "us-west-2", 1): {Count: 1}}
	empty := &config.PlanEditorSnapshot{Plan: &config.PurchasePlan{}}
	sets, err := planEditorReadConstraintSets(full)
	require.NoError(t, err)
	for _, id := range []string{accounts[2].ID, accounts[3].ID} {
		require.Contains(t, sets, revokeConstraintSet(id, "aws", "ec2", "us-east-1"), "unselected and baseline contexts retain known dimensions")
	}
	require.Contains(t, sets, revokeConstraintSet(accounts[1].ID, "aws", "", ""))
	bounded := &auth.PermissionConstraints{AccountIDs: []string{accounts[0].ID}, Providers: []string{"aws"}, Services: []string{"ec2"}, Regions: []string{"us-east-1"}}
	azure := &auth.PermissionConstraints{Providers: []string{"azure"}, Services: []string{"compute"}, Regions: []string{"eastus"}}
	f.h.config = nil // Authorization uses captured config and real auth storage only.
	for _, tc := range []struct {
		name       string
		snapshot   *config.PlanEditorSnapshot
		owner, key *auth.PermissionConstraints
		allowed    []string
		want       int
	}{
		{"all history", full, nil, nil, []string{"*"}, 0},
		{"key historical union", full, nil, bounded, []string{"*"}, 403},
		{"owner historical constraint", full, bounded, nil, []string{"*"}, 403},
		{"owner historical scope", full, nil, nil, []string{accounts[0].ID}, 404},
		{"captured display name", known, nil, nil, []string{accounts[0].Name}, 0},
		{"known strict", known, bounded, bounded, []string{"*"}, 0},
		{"exclusion is not a region bound", &excluded, nil, bounded, []string{"*"}, 403},
		{"mismatched recorded provider", &mixed, nil, mixedBounds, []string{"*"}, 403},
		{"known provider control", &mixedKnown, nil, mixedBounds, []string{"*"}, 0},
		{"ledger recorded region", &ledger, nil, bounded, []string{"*"}, 403},
		{"row unknown", full, nil, &auth.PermissionConstraints{Providers: []string{"aws"}, Services: []string{"ec2"}, Regions: []string{"us-east-1"}}, []string{"*"}, 403},
		{"nil root unknown", emptyRoot, nil, bounded, []string{"*"}, 403},
		{"nil root unbounded dimensions", emptyRoot, nil, &auth.PermissionConstraints{AccountIDs: []string{accounts[0].ID}, Providers: []string{"aws"}}, []string{"*"}, 0},
		{"unmatched disabled provider", &unmatched, nil, &auth.PermissionConstraints{Providers: []string{"aws"}}, []string{"*"}, 403},
		{"unassigned known", unassigned, nil, azure, []string{"*"}, 0},
		{"unassigned wrong provider", unassigned, nil, bounded, []string{"*"}, 403},
		{"unassigned wrong service", unassigned, nil, &auth.PermissionConstraints{Providers: []string{"azure"}, Services: []string{"ec2"}}, []string{"*"}, 403},
		{"unassigned wrong region", unassigned, nil, &auth.PermissionConstraints{Providers: []string{"azure"}, Regions: []string{"westus"}}, []string{"*"}, 403},
		{"unassigned scoped owner", unassigned, nil, nil, []string{accounts[0].ID}, 404},
		{"empty restricted credential", empty, nil, bounded, []string{"*"}, 403},
		{"empty unrestricted", empty, nil, nil, []string{"*"}, 0},
		{"empty scoped owner", empty, nil, nil, []string{accounts[0].ID}, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, user, keyID := f.credential(t, editorReadPermissions(tc.owner), editorReadPermissions(tc.key), tc.allowed)
			session, readErr := f.h.requirePlanEditorReadSession(ctx, req)
			require.NoError(t, readErr)
			require.Equal(t, &Session{UserID: user, UserAPIKeyID: keyID}, session)
			before, marshalErr := json.Marshal(tc.snapshot)
			require.NoError(t, marshalErr)
			readErr = f.h.authorizePlanEditorSnapshotRead(ctx, session, tc.snapshot)
			switch tc.want {
			case 404:
				require.ErrorIs(t, readErr, errNotFound)
			case 403:
				ce, isClient := IsClientError(readErr)
				require.True(t, isClient)
				require.Equal(t, 403, ce.code)
			default:
				require.NoError(t, readErr)
			}
			after, marshalErr := json.Marshal(tc.snapshot)
			require.NoError(t, marshalErr)
			require.Equal(t, before, after)
		})
	}
	for _, bad := range []*config.PlanEditorSnapshot{
		{Plan: &config.PurchasePlan{}, AccountIDs: []string{"missing"}},
		{Plan: &config.PurchasePlan{}, Baseline: config.PlanRampBaseline{"broken": {Count: 1}}},
		{Plan: &config.PurchasePlan{}, Executions: []config.PurchaseExecution{{Recommendations: []config.RecommendationRecord{{Provider: "aws"}}}}},
		{Plan: &config.PurchasePlan{}, Executions: []config.PurchaseExecution{{CloudAccountID: &accounts[0].ID}}},
	} {
		sets, contextErr := planEditorReadConstraintSets(bad)
		require.ErrorIs(t, contextErr, config.ErrPlanEditorAttribution)
		require.Nil(t, sets)
	}
	req, _, _ := f.credential(t, editorReadPermissions(nil), nil, []string{"*"})
	session, err := f.h.requirePlanEditorReadSession(ctx, req)
	require.NoError(t, err)
	require.Empty(t, session.UserAPIKeyID, "real bearer session remains a session")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.Error(t, f.h.authorizePlanEditorSnapshotRead(canceled, session, known))
	failure := errors.New("synthetic constraint lookup failure")
	f.h.auth = editorReadConstraintFailure{f.h.auth, failure}
	require.ErrorIs(t, f.h.authorizePlanEditorSnapshotRead(ctx, session, known), failure)
}

type editorReadConstraintFailure struct {
	AuthServiceInterface
	err error
}

func (a editorReadConstraintFailure) HasPermissionForConstraintsAPI(context.Context, string, string, string, []auth.PermissionConstraints) (bool, error) {
	return false, a.err
}
