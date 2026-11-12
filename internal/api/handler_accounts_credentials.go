package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
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
