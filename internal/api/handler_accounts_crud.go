package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"
)

// CloudAccountRequest is the request body for create/update account endpoints.
// Enabled and AWSIsOrgRoot are pointers so an omitted (or null) key can be
// told apart from an explicit false: create treats absent as false, update
// keeps the stored value (issue #151, A02-013).
type CloudAccountRequest struct {
	Enabled                 *bool  `json:"enabled"`
	AWSIsOrgRoot            *bool  `json:"aws_is_org_root"`
	AWSWebIdentityTokenFile string `json:"aws_web_identity_token_file"`
	AzureClientID           string `json:"azure_client_id"`
	Provider                string `json:"provider"`
	Name                    string `json:"name"`
	Description             string `json:"description"`
	AWSAuthMode             string `json:"aws_auth_mode"`
	AWSRoleARN              string `json:"aws_role_arn"`
	AWSExternalID           string `json:"aws_external_id"`
	ContactEmail            string `json:"contact_email"`
	GCPWIFAudience          string `json:"gcp_wif_audience"`
	ExternalID              string `json:"external_id"`
	AzureSubscriptionID     string `json:"azure_subscription_id"`
	AzureTenantID           string `json:"azure_tenant_id"`
	AWSBastionID            string `json:"aws_bastion_id"`
	AzureAuthMode           string `json:"azure_auth_mode"`
	GCPProjectID            string `json:"gcp_project_id"`
	GCPClientEmail          string `json:"gcp_client_email"`
	GCPAuthMode             string `json:"gcp_auth_mode"`
}

// isDuplicateKeyError matches Postgres unique-constraint violations bubbled
// up by internal/config. Tolerates both the human-readable "duplicate key"
// prefix and the raw SQLSTATE 23505 token.
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "duplicate key") || strings.Contains(s, "23505")
}

// classifyStoreError wraps a config-store error so handler callers can
// surface a 409 with a friendly message when the root cause is a duplicate-
// key violation. Non-duplicate errors pass through as `accounts: <err>`
// (matching the prior wrap) so existing callers still get a 500.
func classifyStoreError(err error, msg409 string) error {
	if isDuplicateKeyError(err) {
		return NewClientError(409, msg409)
	}
	return fmt.Errorf("accounts: %w", err)
}

// createAccount handles POST /api/accounts.
func (h *Handler) createAccount(ctx context.Context, httpReq *events.LambdaFunctionURLRequest) (any, error) {
	if _, err := h.requirePermission(ctx, httpReq, "create", "accounts"); err != nil {
		return nil, err
	}

	var req CloudAccountRequest
	if err := json.Unmarshal([]byte(httpReq.Body), &req); err != nil {
		return nil, NewClientError(400, "invalid request body")
	}

	if err := validateCloudAccountRequest(req); err != nil {
		return nil, err
	}
	if err := h.requireHostForEmptyRoleARN(ctx, "(new)", &req); err != nil {
		return nil, err
	}

	now := time.Now()
	account := cloudAccountFromRequest(req)
	account.ID = uuid.New().String()
	account.CreatedAt = now
	account.UpdatedAt = now

	if err := h.config.CreateCloudAccount(ctx, account); err != nil {
		return nil, classifyStoreError(err, fmt.Sprintf("an account with external ID %q already exists for %s", req.ExternalID, req.Provider))
	}

	annotateAccountNotices(account, "")
	return account, nil
}

// cloudAccountFromRequest maps a CloudAccountRequest to a config.CloudAccount.
func cloudAccountFromRequest(req CloudAccountRequest) *config.CloudAccount {
	a := &config.CloudAccount{
		Name:                    req.Name,
		Description:             req.Description,
		ContactEmail:            req.ContactEmail,
		Provider:                req.Provider,
		ExternalID:              req.ExternalID,
		AWSAuthMode:             req.AWSAuthMode,
		AWSRoleARN:              req.AWSRoleARN,
		AWSExternalID:           req.AWSExternalID,
		AWSBastionID:            req.AWSBastionID,
		AWSWebIdentityTokenFile: req.AWSWebIdentityTokenFile,
		AzureSubscriptionID:     req.AzureSubscriptionID,
		AzureTenantID:           req.AzureTenantID,
		AzureClientID:           req.AzureClientID,
		AzureAuthMode:           req.AzureAuthMode,
		GCPProjectID:            req.GCPProjectID,
		GCPClientEmail:          req.GCPClientEmail,
		GCPAuthMode:             req.GCPAuthMode,
		GCPWIFAudience:          req.GCPWIFAudience,
	}

	if req.Enabled != nil {
		a.Enabled = *req.Enabled
	}
	if req.AWSIsOrgRoot != nil {
		a.AWSIsOrgRoot = *req.AWSIsOrgRoot
	}

	return a
}

// keepOmittedAccountFlags copies the stored enabled and aws_is_org_root
// values onto an update built by cloudAccountFromRequest when the request
// omitted them (or sent null), so a PUT that only renames an account or
// rotates its role does not silently disable it or clear its org-root flag.
// An explicit false in the request is still applied.
func keepOmittedAccountFlags(account, existing *config.CloudAccount, req CloudAccountRequest) {
	if req.Enabled == nil {
		account.Enabled = existing.Enabled
	}
	if req.AWSIsOrgRoot == nil {
		account.AWSIsOrgRoot = existing.AWSIsOrgRoot
	}
}
