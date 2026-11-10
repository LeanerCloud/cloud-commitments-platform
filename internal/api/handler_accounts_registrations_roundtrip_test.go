package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// POST /api/accounts/{id}/credentials must hand the credential store exactly the
// payload the caller sent, under the account and credential type it named. The
// existing success test used a store that accepts anything, so a handler that
// stored a different payload, type or account would still pass.
func TestSaveAccountCredentials_StoresTheSentPayloadUnderTheAccountAndType(t *testing.T) {
	const accountID = "9f1c2d3e-4a5b-4c6d-8e7f-0a1b2c3d4e5f"
	cases := []struct {
		name, credType, payload string
	}{
		{"aws access keys", "aws_access_keys", `{"access_key_id":"AKIAEXAMPLE","secret_access_key":"example-secret"}`},
		{"azure client secret", "azure_client_secret", `{"client_secret":"example-client-secret"}`},
		{"gcp service account, every allowed key", "gcp_service_account",
			`{"type":"service_account","project_id":"example-project","private_key":"example-key","client_email":"sa@example-project.iam.gserviceaccount.com",` +
				`"private_key_id":"kid","client_id":"cid","auth_uri":"https://accounts.example/auth","token_uri":"https://accounts.example/token",` +
				`"auth_provider_x509_cert_url":"https://certs.example/provider","client_x509_cert_url":"https://certs.example/client","universe_domain":"example.com"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mockAuth := new(MockAuthService)
			t.Cleanup(func() { mockAuth.AssertExpectations(t) })
			setupAdminAuth(ctx, mockAuth)
			store := setupAdminMock(ctx)
			t.Cleanup(func() { store.AssertExpectations(t) })
			creds := &recordingCredStore{}
			h := &Handler{auth: mockAuth, config: store, credStore: creds}

			result, err := h.saveAccountCredentials(ctx, adminRequest(`{"credential_type":"`+tc.credType+`","payload":`+tc.payload+`}`), accountID)

			require.NoError(t, err)
			assert.Nil(t, result)
			require.Len(t, creds.saved, 1, "exactly one credential must be stored")
			stored, ok := creds.saved[accountID+"/"+tc.credType]
			require.True(t, ok, "stored under %v", creds.saved)
			assert.JSONEq(t, tc.payload, string(stored))
		})
	}
}

// PUT /api/plans/{id}/accounts must store the plan id from the path and every
// account id from the body, in order. The older happy-path test checks one id and
// ignores the plan id.
func TestSetPlanAccounts_StoresThePlanAndEveryAccountInOrder(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })
	setupAdminAuth(ctx, mockAuth)

	var gotPlan string
	var gotIDs []string
	store := setupAdminMock(ctx)
	t.Cleanup(func() { store.AssertExpectations(t) })
	store.GetPurchasePlanFn = func(_ context.Context, _ string) (*config.PurchasePlan, error) {
		return &config.PurchasePlan{ID: planID209, Name: "two-cloud", Services: map[string]config.ServiceConfig{"aws/ec2": {}, "azure/vm": {}}}, nil
	}
	store.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
		provider := "aws"
		if id == azureAcct1 {
			provider = "azure"
		}
		return &config.CloudAccount{ID: id, Name: "acct-" + provider, Provider: provider}, nil
	}
	store.SetPlanAccountsFn = func(_ context.Context, plan string, ids []string) error {
		gotPlan, gotIDs = plan, ids
		return nil
	}
	h := &Handler{auth: mockAuth, config: store}

	_, err := h.setPlanAccounts(ctx, adminRequest(`{"account_ids":["`+azureAcct1+`","`+awsAcct209+`"]}`), planID209)

	require.NoError(t, err)
	assert.Equal(t, planID209, gotPlan)
	assert.Equal(t, []string{azureAcct1, awsAcct209}, gotIDs)
}

// POST /api/registrations/{id}/reject must record the reason on the rejected
// registration and return it.
func TestRejectRegistration_StoresAndReturnsTheReason(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })
	setupAdminAuth(ctx, mockAuth)
	store := new(MockConfigStore)
	t.Cleanup(func() { store.AssertExpectations(t) })
	store.On("GetAccountRegistration", ctx, approveRegID).Return(pendingRegistrationFixture(), nil)
	var saved *config.AccountRegistration
	store.On("TransitionRegistrationStatus", ctx, mock.AnythingOfType("*config.AccountRegistration"), "pending", mock.Anything).Run(func(args mock.Arguments) {
		r := *args.Get(1).(*config.AccountRegistration)
		saved = &r
	}).Return(nil)
	h := &Handler{auth: mockAuth, config: store}

	result, err := h.rejectRegistration(ctx, adminRequest(`{"reason":"Duplicate of an existing account"}`), approveRegID)

	require.NoError(t, err)
	require.NotNil(t, saved, "the registration was not transitioned")
	assert.Equal(t, "rejected", saved.Status)
	assert.Equal(t, "Duplicate of an existing account", saved.RejectionReason)
	view, ok := result.(map[string]any)
	require.True(t, ok, "got %T", result)
	assert.Equal(t, "Duplicate of an existing account", view["rejection_reason"])
	assert.Equal(t, "rejected", view["status"])
}
