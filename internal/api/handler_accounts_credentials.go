package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/oidc"
	"github.com/aws/aws-lambda-go/events"
	"golang.org/x/oauth2"
)

// CredentialsRequest is the request body for the save-credentials endpoint.
type CredentialsRequest struct {
	Payload        map[string]interface{} `json:"payload"`
	CredentialType string                 `json:"credential_type"`
}

// AccountTestResult is the response for the test-credentials endpoint.
type AccountTestResult struct {
	Message string `json:"message"`
	OK      bool   `json:"ok"`
}

// validCredentialTypes is the set of accepted credential type names.
var validCredentialTypes = map[string]bool{
	"aws_access_keys":              true,
	"azure_client_secret":          true,
	"gcp_service_account":          true,
	"gcp_workload_identity_config": true,
}

// parseAndValidateCredentialsRequest performs all input validation that must
// run before we touch the database: UUID format, permission, body parse,
// credential_type allowlist, and per-type payload schema. Pulled out of
// saveAccountCredentials to keep that function under the cyclomatic limit.
// Returns the parsed request and the session so the caller can scope the
// subsequent account lookup against allowed_accounts.
func (h *Handler) parseAndValidateCredentialsRequest(ctx context.Context, httpReq *events.LambdaFunctionURLRequest, id string) (*CredentialsRequest, *Session, error) {
	if err := validateUUID(id); err != nil {
		return nil, nil, err
	}
	session, err := h.requirePermission(ctx, httpReq, "update", "accounts")
	if err != nil {
		return nil, nil, err
	}
	var req CredentialsRequest
	if err := json.Unmarshal([]byte(httpReq.Body), &req); err != nil {
		return nil, nil, NewClientError(400, "invalid request body")
	}
	if !validCredentialTypes[req.CredentialType] {
		return nil, nil, NewClientError(400, "credential_type must be one of: aws_access_keys, azure_client_secret, gcp_service_account, gcp_workload_identity_config")
	}
	if err := validateCredentialPayload(req.CredentialType, req.Payload); err != nil {
		return nil, nil, err
	}
	return &req, session, nil
}

// saveAccountCredentials handles POST /api/accounts/:id/credentials.
func (h *Handler) saveAccountCredentials(ctx context.Context, httpReq *events.LambdaFunctionURLRequest, id string) (any, error) {
	req, session, err := h.parseAndValidateCredentialsRequest(ctx, httpReq, id)
	if err != nil {
		return nil, err
	}

	// Scope the account to the user's allowed_accounts AND verify existence.
	// Must precede the credStore-nil check so missing/out-of-scope accounts
	// return 404 rather than a 500 about credential store configuration.
	// Returns errNotFound for both cases to avoid existence disclosure.
	_, err = h.requireAccountAccess(ctx, session, id)
	if err != nil {
		return nil, err
	}

	if h.credStore == nil {
		return nil, fmt.Errorf("accounts: credential store not configured")
	}

	payloadBytes, err := json.Marshal(req.Payload)
	if err != nil {
		return nil, NewClientError(400, "invalid credentials payload")
	}

	if err := h.credStore.SaveCredential(ctx, id, req.CredentialType, payloadBytes); err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}

	return nil, nil
}

// ambientCredResult returns the test result for Azure / GCP auth modes that
// don't require a stored credential (managed identity, application default).
// AWS accounts never reach it: they are tested end-to-end through the
// credential resolver by awsResolverCredResult. The second return value is
// true when the check was handled and the caller should return immediately.
func ambientCredResult(acct *config.CloudAccount) (AccountTestResult, bool) {
	switch acct.Provider {
	case "azure":
		if acct.AzureAuthMode == "managed_identity" {
			return AccountTestResult{OK: true, Message: "managed identity configured (no stored credential required)"}, true
		}
	case "gcp":
		if acct.GCPAuthMode == "application_default" {
			return AccountTestResult{OK: true, Message: "application default credentials configured (no stored credential required)"}, true
		}
	}
	return AccountTestResult{}, false
}

// testAccountCredentials handles POST /api/accounts/:id/test.
// This operation performs live outbound cloud API calls / credential probing,
// which is a write-class side effect. Gate on update:accounts (not view:accounts)
// so the handler is self-protecting if the route Auth level is ever relaxed (02-M5).
func (h *Handler) testAccountCredentials(ctx context.Context, req *events.LambdaFunctionURLRequest, id string) (any, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}
	session, err := h.requirePermission(ctx, req, "update", "accounts")
	if err != nil {
		return nil, err
	}
	acct, err := h.requireAccountAccess(ctx, session, id)
	if err != nil {
		return nil, err
	}
	if acct.Provider == "aws" {
		// Exercise the same resolver collection and purchases use, then
		// probe sts:GetCallerIdentity, so the test cannot report OK for a
		// config real use rejects (#722).
		return h.awsResolverCredResult(ctx, acct), nil
	}
	if res, ok := ambientCredResult(acct); ok {
		return res, nil
	}
	if res, ok := h.azureFederatedCredResult(ctx, acct); ok {
		return res, nil
	}
	if res, ok := h.gcpFederatedCredResult(ctx, acct); ok {
		return res, nil
	}
	res, err := h.checkCredentialPresence(ctx, acct)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// gcpFederatedCredResult exercises the secret-free GCP federated
// credential path end-to-end. Guards on provider/mode/signer/issuer/
// audience and delegates the actual token exchange to
// runGCPFederatedTokenExchange so this top-level branch stays under
// gocyclo's 10-branch threshold. Returns (result, true) when this
// path applies; (_, false) means the caller should fall back to the
// presence check.
func (h *Handler) gcpFederatedCredResult(ctx context.Context, acct *config.CloudAccount) (AccountTestResult, bool) {
	if !h.canUseGCPFederated(ctx, acct) {
		return AccountTestResult{}, false
	}
	ts, err := credentials.BuildGCPFederatedCredential(ctx, h.signer, oidc.IssuerURL(), acct.GCPWIFAudience, acct.GCPClientEmail)
	if err != nil {
		return AccountTestResult{OK: false, Message: fmt.Sprintf("gcp federated credential build failed: %v", err)}, true
	}
	return runGCPFederatedTokenExchange(ctx, ts), true
}

// canUseGCPFederated returns true when all preconditions for the
// federated GCP test path are met. Factored out of
// gcpFederatedCredResult to keep that function's cyclomatic
// complexity below gocyclo's threshold.
func (h *Handler) canUseGCPFederated(ctx context.Context, acct *config.CloudAccount) bool {
	if acct.Provider != "gcp" || acct.GCPAuthMode != "workload_identity_federation" {
		return false
	}
	if h.signer == nil || acct.GCPWIFAudience == "" {
		return false
	}
	if oidc.IssuerURL() == "" {
		return false
	}
	// If a legacy stored WIF JSON is present, defer to the presence
	// check so legacy accounts keep reporting the same shape.
	if h.credStore != nil {
		has, err := h.credStore.HasCredential(ctx, acct.ID, credentials.CredTypeGCPWIFConfig)
		if err != nil {
			logging.Warnf("canUseGCPFederated: credential store check failed, disabling federated: %v", err)
			return false
		}
		if has {
			return false
		}
	}
	return true
}

// runGCPFederatedTokenExchange calls Token() on the federated token
// source with a per-attempt 15-second deadline (oauth2.TokenSource.Token()
// is not context-aware, so we plumb the timeout through a goroutine +
// select). A successful non-empty token proves the KMS → GCP STS →
// SA-impersonation chain is healthy.
//
// Retries on IAM propagation errors: freshly-bound WIF principals and
// iam.workloadIdentityUser grants on the impersonated service account
// take up to ~30s to become visible to GCP's token exchange. During
// that window iamcredentials.generateAccessToken returns
// IAM_PERMISSION_DENIED / "The caller does not have permission" even
// though the policy is correct. We retry up to 3 times with a 10s
// pause, but only when the error actually looks like that race.
// Other failure modes (KMS unauthorized, JWT rejected, wrong
// audience) fail fast on the first attempt.
func runGCPFederatedTokenExchange(ctx context.Context, ts oauth2.TokenSource) AccountTestResult {
	const maxAttempts = 3
	const retryDelay = 10 * time.Second

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		res, err, retriable := gcpTokenExchangeAttempt(ctx, ts)
		if err == nil {
			return res
		}
		lastErr = err
		if !retriable {
			return AccountTestResult{OK: false, Message: fmt.Sprintf("gcp token exchange failed: %v", err)}
		}
		if attempt == maxAttempts {
			break
		}
		select {
		case <-time.After(retryDelay):
		case <-ctx.Done():
			return AccountTestResult{OK: false, Message: fmt.Sprintf("gcp token exchange aborted while waiting for IAM propagation: %v", ctx.Err())}
		}
	}
	return AccountTestResult{OK: false, Message: fmt.Sprintf("gcp token exchange failed after %d attempts (IAM propagation): %v", maxAttempts, lastErr)}
}

// gcpTokenExchangeAttempt runs one Token() call with a 15s deadline.
// Returns (result, nil, _) on success, (_, err, true) on a retriable
// IAM propagation error, (_, err, false) on any other failure.
func gcpTokenExchangeAttempt(ctx context.Context, ts oauth2.TokenSource) (AccountTestResult, error, bool) { //nolint:revive // error-return: error position is part of established calling convention
	tokCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tokenChan := make(chan tokenResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Warnf("handler_accounts: gcp token-exchange goroutine panic: %v", r)
				tokenChan <- tokenResult{err: fmt.Errorf("token exchange panic: %v", r)}
			}
		}()
		tok, err := ts.Token()
		tokenChan <- tokenResult{tok: tok, err: err}
	}()
	select {
	case r := <-tokenChan:
		if r.err != nil {
			return AccountTestResult{}, r.err, isGCPIAMPropagationError(r.err)
		}
		if r.tok == nil || r.tok.AccessToken == "" {
			return AccountTestResult{OK: false, Message: "gcp token exchange returned empty token"}, nil, false
		}
		return AccountTestResult{OK: true, Message: "federated credential validated (KMS-signed JWT accepted by GCP STS, SA impersonation succeeded)"}, nil, false
	case <-tokCtx.Done():
		return AccountTestResult{}, fmt.Errorf("timed out after 15s"), true
	}
}

// isGCPIAMPropagationError reports whether err looks like the
// IAM-binding-not-yet-visible race that follows a fresh WIF provider
// or roles/iam.workloadIdentityUser grant. Matched by substring
// because the underlying error is wrapped through oauth2 →
// externalaccount → iamcredentials, and the leaf type isn't exported.
func isGCPIAMPropagationError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "IAM_PERMISSION_DENIED") ||
		strings.Contains(msg, "iam.serviceAccounts.getAccessToken") ||
		strings.Contains(msg, "The caller does not have permission") ||
		strings.Contains(msg, "Permission 'iam.serviceAccounts.getAccessToken' denied")
}

// tokenResult bundles the return of oauth2.TokenSource.Token() for
// plumbing through a select for timeout handling, since oauth2's
// TokenSource interface does not take a context.
type tokenResult struct {
	tok *oauth2.Token
	err error
}

// azureFederatedCredResult exercises the secret-free Azure federated
// credential path end-to-end for accounts in workload_identity_federation
// mode whose CUDly deployment has an OIDC signer + issuer configured.
// It mints a client_assertion JWT via the KMS signer, exchanges it at
// Azure AD's token endpoint, and reports the result. Returns
// (result, true) when this path applies; (_, false) means the caller
// should fall back to the presence check (client_secret mode only).
func (h *Handler) azureFederatedCredResult(ctx context.Context, acct *config.CloudAccount) (AccountTestResult, bool) {
	if acct.Provider != "azure" || acct.AzureAuthMode != "workload_identity_federation" {
		return AccountTestResult{}, false
	}
	if h.signer == nil {
		return AccountTestResult{}, false
	}
	issuer := oidc.IssuerURL()
	if issuer == "" {
		return AccountTestResult{}, false
	}

	cred, err := credentials.BuildAzureFederatedCredential(h.signer, issuer, acct.AzureTenantID, acct.AzureClientID)
	if err != nil {
		return AccountTestResult{OK: false, Message: fmt.Sprintf("azure federated credential build failed: %v", err)}, true
	}

	// Actually exchange the JWT for an access token. This is the only
	// path that proves the whole chain — KMS sign → Azure AD federated
	// credential validation → access token — is healthy.
	tokCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tok, err := cred.GetToken(tokCtx, policy.TokenRequestOptions{
		Scopes: []string{"https://management.azure.com/.default"},
	})
	if err != nil {
		return AccountTestResult{OK: false, Message: fmt.Sprintf("azure token exchange failed: %v", err)}, true
	}
	if tok.Token == "" {
		return AccountTestResult{OK: false, Message: "azure token exchange returned empty token"}, true
	}
	return AccountTestResult{OK: true, Message: "federated credential validated (KMS-signed JWT accepted by Azure AD)"}, true
}

// checkCredentialPresence checks whether the expected credential is stored for an account.
func (h *Handler) checkCredentialPresence(ctx context.Context, acct *config.CloudAccount) (AccountTestResult, error) {
	if h.credStore != nil {
		credType := credTypeForAccount(acct)
		// Empty credType means this auth mode isn't backed by a stored
		// credential — e.g. Azure workload_identity_federation on a
		// deployment where the OIDC signer wasn't wired (so
		// azureFederatedCredResult returned ok=false and we fell
		// through to here). Report an operator-facing message instead
		// of querying for "no  credential stored".
		if credType == "" {
			return AccountTestResult{OK: false, Message: "this account's auth mode is not backed by a stored credential — check the deployment's OIDC issuer wiring"}, nil
		}
		has, err := h.credStore.HasCredential(ctx, acct.ID, credType)
		if err != nil {
			return AccountTestResult{}, fmt.Errorf("accounts: check credential: %w", err)
		}
		if has {
			return AccountTestResult{OK: true, Message: "credentials are configured"}, nil
		}
		return AccountTestResult{OK: false, Message: fmt.Sprintf("no %s credential stored", credType)}, nil
	}

	// Fallback when credStore is not wired (e.g. in unit tests).
	ok, err := h.config.HasAccountCredentials(ctx, acct.ID)
	if err != nil {
		return AccountTestResult{}, fmt.Errorf("accounts: %w", err)
	}
	if ok {
		return AccountTestResult{OK: true, Message: "credentials are configured"}, nil
	}
	return AccountTestResult{OK: false, Message: "no credentials configured"}, nil
}

// credTypeForAccount returns the expected credential_type for an account
// based on its provider and auth mode. Returns "" for auth modes that
// aren't backed by a stored credential (e.g. Azure
// workload_identity_federation, which uses the deployment's OIDC signer
// at request time and stores nothing per-account).
func credTypeForAccount(acct *config.CloudAccount) string {
	switch acct.Provider {
	case "azure":
		if acct.AzureAuthMode == "workload_identity_federation" {
			return ""
		}
		return "azure_client_secret"
	case "gcp":
		if acct.GCPAuthMode == "workload_identity_federation" {
			return "gcp_workload_identity_config"
		}
		return "gcp_service_account"
	default: // aws
		return "aws_access_keys"
	}
}
