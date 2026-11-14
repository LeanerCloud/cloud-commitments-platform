package api

import (
	"context"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

const (
	groupAcctAWS   = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	groupAcctAzure = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	groupAcctOther = "dddddddd-dddd-dddd-dddd-dddddddddddd"
)

type groupCreateFixture struct {
	h       *Handler
	store   *MockConfigStore
	created *config.PurchasePlan
}

func newGroupCreateFixture(t *testing.T, awsRecs, azureRecs []config.RecommendationRecord) *groupCreateFixture {
	t.Helper()
	ctx := context.Background()
	f := &groupCreateFixture{store: new(MockConfigStore)}
	t.Cleanup(func() { f.store.AssertExpectations(t) })
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })
	mockAuth.On("ValidateSession", ctx, "admin-token").Return(&Session{UserID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Email: "admin@example.com"}, nil).Maybe()
	mockAuth.grantAdminPurchaser()
	f.store.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
		return &config.CloudAccount{ID: id, Name: "acct", Provider: "aws"}, nil
	}
	f.store.On("ListStoredRecommendations", ctx, mock.MatchedBy(func(fl config.RecommendationFilter) bool { return fl.Provider == "aws" })).Return(awsRecs, nil).Maybe()
	f.store.On("ListStoredRecommendations", ctx, mock.MatchedBy(func(fl config.RecommendationFilter) bool { return fl.Provider == "azure" })).Return(azureRecs, nil).Maybe()
	f.store.On("CreatePurchasePlanWithAccounts", ctx, mock.AnythingOfType("*config.PurchasePlan"), mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		p := *args.Get(1).(*config.PurchasePlan)
		f.created = &p
	}).Return(nil).Maybe()
	f.h = &Handler{config: f.store, auth: mockAuth}
	return f
}

func (f *groupCreateFixture) create(body string) error {
	_, err := f.h.createPlan(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"Authorization": "Bearer admin-token"}, Body: body,
	})
	return err
}

func createBody(groups ...string) string {
	out := `{"name":"sel","enabled":true,"ramp_schedule":"weekly-25pct","target_accounts":["` + groupAcctAWS + `","` + groupAcctAzure + `"],"groups":[`
	for i, s := range groups {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out + "]}"
}

const (
	awsGroup   = `{"provider":"aws","service":"ec2","term":3,"payment":"all-upfront","coverage":70,"recommendation_ids":["r1","r2"]}`
	azureGroup = `{"provider":"azure","service":"compute","term":1,"payment":"no-upfront","recommendation_ids":["r3"]}`
)

func groupRecs() (aws, azure []config.RecommendationRecord) {
	aws = []config.RecommendationRecord{
		groupRec("r1", "aws", "ec2", "us-east-1", "m5.large", 3, "all-upfront", groupAcctAWS),
		groupRec("r2", "aws", "ec2", "us-west-2", "m5.large", 3, "all-upfront", groupAcctAWS),
		groupRec("unselected", "aws", "ec2", "eu-west-1", "c5.large", 3, "all-upfront", groupAcctAWS),
	}
	azure = []config.RecommendationRecord{groupRec("r3", "azure", "compute", "westeurope", "Standard_D2s_v3", 1, "monthly", groupAcctAzure)}
	return aws, azure
}

func TestCreatePlan_GroupsBindSelectedRecommendations(t *testing.T) {
	t.Parallel()
	aws, azure := groupRecs()
	f := newGroupCreateFixture(t, aws, azure)
	require.NoError(t, f.create(createBody(awsGroup, azureGroup)))
	require.NotNil(t, f.created)

	assert.Equal(t, config.RampAllocationBoundGroups, f.created.RampSchedule.AllocationMode)
	require.Len(t, f.created.Services, 2)
	f.store.AssertNumberOfCalls(t, "ListStoredRecommendations", 2)
	bound := map[string][]string{}
	for key, svc := range f.created.Services {
		assert.Equal(t, svc.Provider+"/"+svc.Service+"#"+svc.GroupID, key)
		bound[svc.Provider] = svc.BoundIdentities
	}
	assert.Equal(t, []string{identityOf(t, aws[0]), identityOf(t, aws[1])}, bound["aws"], "the unselected rec is not bound")
	assert.Equal(t, []string{identityOf(t, azure[0])}, bound["azure"])
}

func TestCreatePlan_GroupsRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, group, msg string
		mutate           func(aws, azure []config.RecommendationRecord) ([]config.RecommendationRecord, []config.RecommendationRecord)
	}{
		{"missing id", `{"provider":"aws","service":"ec2","term":3,"payment":"all-upfront","recommendation_ids":["r1","nope"]}`, `recommendation "nope" was not found`, nil},
		{"wrong term", `{"provider":"aws","service":"ec2","term":1,"payment":"all-upfront","recommendation_ids":["r1"]}`, "does not match", nil},
		{"wrong payment", `{"provider":"aws","service":"ec2","term":3,"payment":"no-upfront","recommendation_ids":["r1"]}`, "does not match", nil},
		{"empty ids", `{"provider":"aws","service":"ec2","term":3,"payment":"all-upfront","recommendation_ids":[]}`, "non-empty recommendation_ids", nil},
		{"account outside plan", `{"provider":"aws","service":"ec2","term":3,"payment":"all-upfront","recommendation_ids":["r1"]}`, "does not match", func(a, z []config.RecommendationRecord) ([]config.RecommendationRecord, []config.RecommendationRecord) {
			other := groupAcctOther
			a[0].CloudAccountID = &other
			return a, z
		}},
		{"ambiguous id", `{"provider":"aws","service":"ec2","term":3,"payment":"all-upfront","recommendation_ids":["r1"]}`, "ambiguous", func(a, z []config.RecommendationRecord) ([]config.RecommendationRecord, []config.RecommendationRecord) {
			return append(a, a[0]), z
		}},
		{"two groups same shape", `{"provider":"aws","service":"ec2","term":3,"payment":"all-upfront","recommendation_ids":["r2"]}`, "same provider, service, term and payment", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			aws, azure := groupRecs()
			if tc.mutate != nil {
				aws, azure = tc.mutate(aws, azure)
			}
			f := newGroupCreateFixture(t, aws, azure)
			body := createBody(tc.group)
			if tc.name == "two groups same shape" {
				body = createBody(`{"provider":"aws","service":"ec2","term":3,"payment":"all-upfront","recommendation_ids":["r1"]}`, tc.group)
			}
			err := f.create(body)
			ce, ok := IsClientError(err)
			require.True(t, ok, "%v", err)
			assert.Equal(t, 400, ce.code)
			assert.Contains(t, ce.message, tc.msg)
			assert.Nil(t, f.created, "no partial plan")
		})
	}
}

func TestCreatePlan_GroupsArmedCreateReachesTheGate(t *testing.T) {
	t.Parallel()
	aws, azure := groupRecs()
	f := newGroupCreateFixture(t, aws, azure)
	noExecute := new(MockAuthService)
	noExecute.On("ValidateSession", mock.Anything, "admin-token").Return(&Session{UserID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Email: "admin@example.com"}, nil)
	noExecute.grantAdmin()
	f.h.auth = noExecute
	body := `{"name":"armed","enabled":true,"auto_purchase":true,"max_step_commitment":500,"ramp_schedule":"weekly-25pct","target_accounts":["` + groupAcctAWS + `"],"groups":[` + awsGroup + `]}`
	ce, ok := IsClientError(f.create(body))
	require.True(t, ok)
	assert.Equal(t, 403, ce.code)
	assert.Nil(t, f.created)
}

func TestUpdatePlan_RefusesGroups(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, mockAuth := new(MockConfigStore), new(MockAuthService)
	mockAuth.On("ValidateSession", ctx, "admin-token").Return(&Session{UserID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Email: "admin@example.com"}, nil)
	mockAuth.grantAdmin()
	store.On("GetPurchasePlan", mock.Anything, mock.Anything).Return(&config.PurchasePlan{ID: "12345678-1234-1234-1234-123456789abc", Name: "p"}, nil).Maybe()
	_, err := (&Handler{config: store, auth: mockAuth}).updatePlan(ctx, &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"Authorization": "Bearer admin-token"},
		Body:    `{"name":"p","groups":[` + awsGroup + `]}`,
	}, "12345678-1234-1234-1234-123456789abc")
	ce, ok := IsClientError(err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, 400, ce.code)
	assert.Equal(t, planGroupsNotSupportedCode, ce.Details()["code"])
	store.AssertNotCalled(t, "UpdatePurchasePlan", mock.Anything, mock.Anything)
}
