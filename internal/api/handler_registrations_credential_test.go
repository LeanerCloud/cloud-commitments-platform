package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

const (
	testRegID         = "33333333-3333-3333-3333-333333333333"
	testGCPFileConfig = `{"audience":"//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/q","credential_source":{"file":"/var/run/secrets/azure/tokens/azure-identity-token"},"service_account_impersonation_url":"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/sa@proj.iam.gserviceaccount.com:generateAccessToken","subject_token_type":"urn:ietf:params:oauth:token-type:id_token","token_url":"https://sts.googleapis.com/v1/token","type":"external_account"}`
)

// recordingCredentialStore counts credential saves; encrypt/decrypt are the
// passthrough behavior of MockCredentialStore.
type recordingCredentialStore struct {
	MockCredentialStore
	saved int
}

func (r *recordingCredentialStore) SaveCredential(_ context.Context, _, _ string, _ []byte) error {
	r.saved++
	return nil
}

func registrationBody(t *testing.T, credType, payload string) string {
	t.Helper()
	b, err := json.Marshal(RegistrationRequest{
		Provider: "gcp", ExternalID: "proj-1", AccountName: "n", ContactEmail: "a@example.com",
		CredentialType: credType, CredentialPayload: payload,
	})
	require.NoError(t, err)
	return string(b)
}

func TestSubmitRegistration_CredentialValidation(t *testing.T) {
	unsupportedToken := strings.Replace(testGCPFileConfig, "https://sts.googleapis.com/v1/token", "https://example.invalid/token", 1)
	unsupportedPath := strings.Replace(testGCPFileConfig, "/var/run/secrets/azure/tokens/azure-identity-token", "/etc/unsupported-path", 1)
	cases := []struct {
		name, credType, payload string
	}{
		{"unsupported token endpoint", "gcp_workload_identity_config", unsupportedToken},
		{"unsupported file path", "gcp_workload_identity_config", unsupportedPath},
		{"type without payload", "gcp_workload_identity_config", ""},
		{"payload without type", "", testGCPFileConfig},
		{"non-gcp type without payload", "aws_access_keys", ""},
		{"unknown credential type", "unknown_type", testGCPFileConfig},
		{"service account with unsupported token_uri", "gcp_service_account", `{"type":"service_account","token_uri":"https://example.invalid/token"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := new(MockConfigStore) // no expectations: any store write panics
			creds := &recordingCredentialStore{}
			h := &Handler{config: store, credStore: creds}
			_, err := h.submitRegistration(context.Background(), &events.LambdaFunctionURLRequest{Body: registrationBody(t, tc.credType, tc.payload)})
			var ce *clientError
			require.ErrorAs(t, err, &ce)
			assert.Equal(t, 400, ce.code)
			assert.NotContains(t, err.Error(), "example.invalid")
			store.AssertNotCalled(t, "CreateAccountRegistration", mock.Anything, mock.Anything)
		})
	}
}

func TestSubmitRegistration_AcceptsSupportedGCPConfig(t *testing.T) {
	store := new(MockConfigStore)
	store.On("CreateAccountRegistration", mock.Anything, mock.Anything).Return(nil)
	h := &Handler{config: store, credStore: &recordingCredentialStore{}}
	_, err := h.submitRegistration(context.Background(), &events.LambdaFunctionURLRequest{Body: registrationBody(t, "gcp_workload_identity_config", testGCPFileConfig)})
	require.NoError(t, err)
	store.AssertCalled(t, "CreateAccountRegistration", mock.Anything, mock.Anything)
}

func approveRequest() *events.LambdaFunctionURLRequest {
	return &events.LambdaFunctionURLRequest{
		Body: `{"name":"n","provider":"gcp","external_id":"proj-1","contact_email":"a@example.com","gcp_auth_mode":"service_account"}`,
	}
}

// TestApproveRegistration_PendingRowWithUnsupportedCredential covers a row
// stored before payload validation existed.
func TestApproveRegistration_PendingRowWithUnsupportedCredential(t *testing.T) {
	cases := map[string]config.AccountRegistration{
		"unsupported token endpoint": {ID: testRegID, Status: "pending", Provider: "gcp", RegCredentialType: "gcp_workload_identity_config",
			RegCredentialPayload: strings.Replace(testGCPFileConfig, "https://sts.googleapis.com/v1/token", "https://example.invalid/token", 1)},
		"incomplete credential": {ID: testRegID, Status: "pending", Provider: "gcp", RegCredentialType: "gcp_workload_identity_config"},
	}
	for name, reg := range cases {
		t.Run(name, func(t *testing.T) {
			reg := reg
			ctx := context.Background()
			store := new(MockConfigStore)
			store.On("GetAccountRegistration", ctx, testRegID).Return(&reg, nil)
			creates := 0
			store.CreateCloudAccountFn = func(context.Context, *config.CloudAccount) error { creates++; return nil }
			creds := &recordingCredentialStore{}
			h := &Handler{config: store, credStore: creds}

			_, err := h.approveRegistration(ctx, approveRequest(), testRegID)
			var ce *clientError
			require.ErrorAs(t, err, &ce)
			assert.Equal(t, 400, ce.code)
			assert.NotContains(t, err.Error(), "example.invalid")
			assert.Equal(t, "pending", reg.Status, "registration must stay pending")
			assert.Zero(t, creates, "no account may be created")
			assert.Zero(t, creds.saved, "nothing may be stored")
			store.AssertNotCalled(t, "TransitionRegistrationStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

type failingDecryptStore struct{ recordingCredentialStore }

func (failingDecryptStore) DecryptPayload(string) ([]byte, error) { return nil, assert.AnError }

func TestApproveRegistration_DecryptFailureStaysPending(t *testing.T) {
	ctx := context.Background()
	reg := &config.AccountRegistration{ID: testRegID, Status: "pending", Provider: "gcp", RegCredentialType: "gcp_workload_identity_config", RegCredentialPayload: "blob"}
	store := new(MockConfigStore)
	store.On("GetAccountRegistration", ctx, testRegID).Return(reg, nil)
	h := &Handler{config: store, credStore: &failingDecryptStore{}}

	_, err := h.approveRegistration(ctx, approveRequest(), testRegID)
	require.Error(t, err)
	assert.Equal(t, "pending", reg.Status)
	store.AssertNotCalled(t, "TransitionRegistrationStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestApproveRegistration_SupportedCredentialIsStoredAndEnabled(t *testing.T) {
	ctx := context.Background()
	reg := &config.AccountRegistration{ID: testRegID, Status: "pending", Provider: "gcp", RegCredentialType: "gcp_workload_identity_config", RegCredentialPayload: testGCPFileConfig}
	store := new(MockConfigStore)
	store.On("GetAccountRegistration", ctx, testRegID).Return(reg, nil)
	store.On("TransitionRegistrationStatus", ctx, reg, "pending", mock.Anything).Return(nil)
	store.On("UpdateAccountRegistration", ctx, reg).Return(nil)
	var created *config.CloudAccount
	store.CreateCloudAccountFn = func(_ context.Context, a *config.CloudAccount) error { created = a; return nil }
	creds := &recordingCredentialStore{}
	h := &Handler{config: store, credStore: creds}

	_, err := h.approveRegistration(ctx, approveRequest(), testRegID)
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.True(t, created.Enabled)
	assert.Equal(t, 1, creds.saved)
}

func TestValidateCredentialPayload_GCPUnsupportedConfig(t *testing.T) {
	var base map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(testGCPFileConfig), &base))
	mutate := func(f func(m map[string]interface{})) map[string]interface{} {
		m := map[string]interface{}{}
		for k, v := range base {
			m[k] = v
		}
		f(m)
		return m
	}
	cases := map[string]map[string]interface{}{
		"unsupported token endpoint": mutate(func(m map[string]interface{}) { m["token_url"] = "https://example.invalid/token" }),
		"unsupported credential source": mutate(func(m map[string]interface{}) {
			m["credential_source"] = map[string]interface{}{"file": "/etc/unsupported-path"}
		}),
		"case variant key": mutate(func(m map[string]interface{}) { m["Token_URL"] = "https://example.invalid/token" }),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			err := validateCredentialPayload("gcp_workload_identity_config", payload)
			var ce *clientError
			require.ErrorAs(t, err, &ce)
			assert.Equal(t, 400, ce.code)
		})
	}
	assert.NoError(t, validateCredentialPayload("gcp_workload_identity_config", base))
}
