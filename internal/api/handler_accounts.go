package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"golang.org/x/oauth2"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/oidc"
	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// AccountServiceOverrideRequest is the request body for service override endpoints.
type AccountServiceOverrideRequest struct {
	Enabled        *bool    `json:"enabled,omitempty"`
	Term           *int     `json:"term,omitempty"`
	Payment        *string  `json:"payment,omitempty"`
	Coverage       *float64 `json:"coverage,omitempty"`
	RampSchedule   *string  `json:"ramp_schedule,omitempty"`
	IncludeEngines []string `json:"include_engines,omitempty"`
	ExcludeEngines []string `json:"exclude_engines,omitempty"`
	IncludeRegions []string `json:"include_regions,omitempty"`
	ExcludeRegions []string `json:"exclude_regions,omitempty"`
	IncludeTypes   []string `json:"include_types,omitempty"`
	ExcludeTypes   []string `json:"exclude_types,omitempty"`
}

// serviceOverrideOverlayKeys are the JSON keys of AccountServiceOverrideRequest.
// A key present in the PUT body replaces the stored value (null clears it); an
// absent key keeps it.
var serviceOverrideOverlayKeys = []string{
	"enabled", "term", "payment", "coverage", "ramp_schedule",
	"include_engines", "exclude_engines",
	"include_regions", "exclude_regions",
	"include_types", "exclude_types",
}

// validCredentialTypes is the set of accepted credential type names.
var validCredentialTypes = map[string]bool{
	"aws_access_keys":              true,
	"azure_client_secret":          true,
	"gcp_service_account":          true,
	"gcp_workload_identity_config": true,
}

// listAccounts handles GET /api/accounts.
func (h *Handler) listAccounts(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "view", "accounts")
	if err != nil {
		return nil, err
	}

	filter, err := buildAccountFilter(req.QueryStringParameters)
	if err != nil {
		return nil, err
	}

	accts, err := h.config.ListCloudAccounts(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}

	if accts == nil {
		accts = []config.CloudAccount{}
	}

	// Filter by allowed accts if the user has restricted access.
	// An empty list or one containing "*" grants unrestricted access.
	// Otherwise each entry is matched against the account's ID, or its Name
	// when no other registered account shares it (issue #433).
	allowedAccounts, err := h.getAccountScope(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if !allowedAccounts.AllowsAll() {
		filtered := accts[:0]
		for i := range accts {
			acct := accts[i]
			if allowedAccounts.Allows(acct.ID, acct.Name) {
				filtered = append(filtered, acct)
			}
		}
		accts = filtered
	}

	// Mark the self-account (the account matching CUDly's own host identity)
	// and attach the derived notices (issue #1194).
	h.markSelfAccount(ctx, accts)

	return accts, nil
}

// AccountSummary is the minimal-disclosure projection of a cloud account used
// by the global topbar filter and the create-plan-from-commitment target
// prefill (issues #949, #951). It deliberately omits every credential/config
// field (role ARNs, subscription IDs, client emails, auth modes, bastion IDs,
// the self-account marker) so the endpoint can be gated on a low read verb that
// Standard / Read-Only users already hold, without leaking sensitive account
// configuration. The full CloudAccount object stays behind GET /api/accounts
// (view:accounts, admin-grade).
type AccountSummary struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ExternalID string `json:"external_id"`
	Provider   string `json:"provider"`
}

// listAccountsMinimal handles GET /api/accounts/list.
//
// Returns the minimal AccountSummary projection scoped by the session's
// allowed_accounts list. Gated on view:recommendations (held by both the
// Standard Users and Read-Only Users groups) rather than view:accounts, so the
// account dropdown in the global filter and the plan-target prefill work for
// non-admin users without exposing the credential/config metadata carried by
// the full GET /api/accounts response. See issues #949 / #951.
func (h *Handler) listAccountsMinimal(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "view", "recommendations")
	if err != nil {
		return nil, err
	}

	filter, err := buildAccountFilter(req.QueryStringParameters)
	if err != nil {
		return nil, err
	}

	accts, err := h.config.ListCloudAccounts(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}

	allowedAccounts, err := h.getAccountScope(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	unrestricted := allowedAccounts.AllowsAll()

	// Build the minimal projection in place, applying allowed_accounts scoping
	// during the copy so a restricted user only ever sees their entitled rows.
	summaries := make([]AccountSummary, 0, len(accts))
	for i := range accts {
		acct := &accts[i]
		if !unrestricted && !allowedAccounts.Allows(acct.ID, acct.Name) {
			continue
		}
		summaries = append(summaries, AccountSummary{
			ID:         acct.ID,
			Name:       acct.Name,
			ExternalID: acct.ExternalID,
			Provider:   acct.Provider,
		})
	}

	return summaries, nil
}

// annotateAccountNotices sets the derived notices on acct, such as the aws-cn
// Savings Plans limitation (issue #1194). The partition comes from the
// account's role ARN; a self-account with no role ARN falls back to
// hostPartition, CUDly's own partition ("" when unknown).
func annotateAccountNotices(acct *config.CloudAccount, hostPartition string) {
	partition := acct.AWSPartition()
	if partition == "" && acct.IsSelf && acct.Provider == "aws" {
		partition = hostPartition
	}
	acct.Notices = config.AWSPartitionNotices(partition)
}

// buildAccountFilter constructs a CloudAccountFilter from query parameters.
// An unknown provider or an enabled value other than "true"/"false" is a 400
// instead of being silently ignored (issue #312); an empty value or
// provider "all" means no filter.
func buildAccountFilter(params map[string]string) (config.CloudAccountFilter, error) {
	var filter config.CloudAccountFilter

	provider, err := providerFilterParam(params["provider"])
	if err != nil {
		return filter, err
	}
	if provider != "" {
		filter.Provider = &provider
	}

	switch params["enabled"] {
	case "":
	case "true":
		t := true
		filter.Enabled = &t
	case "false":
		f := false
		filter.Enabled = &f
	default:
		return filter, NewClientError(400, "invalid enabled: must be true or false")
	}

	filter.Search = params["search"]

	return filter, nil
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

// getAccount handles GET /api/accounts/:id.
func (h *Handler) getAccount(ctx context.Context, req *events.LambdaFunctionURLRequest, id string) (any, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	session, err := h.requirePermission(ctx, req, "view", "accounts")
	if err != nil {
		return nil, err
	}

	account, err := h.requireAccountAccess(ctx, session, id)
	if err != nil {
		return nil, err
	}

	annotateAccountNotices(account, "")
	return account, nil
}

// updateAccount handles PUT /api/accounts/:id.
func (h *Handler) updateAccount(ctx context.Context, httpReq *events.LambdaFunctionURLRequest, id string) (any, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	session, err := h.requirePermission(ctx, httpReq, "update", "accounts")
	if err != nil {
		return nil, err
	}

	var req CloudAccountRequest
	err = json.Unmarshal([]byte(httpReq.Body), &req)
	if err != nil {
		return nil, NewClientError(400, "invalid request body")
	}

	err = validateCloudAccountRequest(req)
	if err != nil {
		return nil, err
	}

	existing, err := h.requireAccountAccess(ctx, session, id)
	if err != nil {
		return nil, err
	}
	// After the access check, so an out-of-scope caller gets the 404
	// without triggering an STS call.
	if err := h.requireHostForEmptyRoleARN(ctx, id, &req); err != nil {
		return nil, err
	}

	// The provider is fixed when the account is created: UpdateCloudAccount
	// does not write it, and credentials, service overrides and plan
	// memberships all depend on it. Refuse a change rather than echo back a
	// provider that was never saved.
	if req.Provider != existing.Provider {
		return nil, NewClientError(400, fmt.Sprintf(
			"provider cannot be changed (this account is %s); delete the account and add it again under the new provider", existing.Provider))
	}

	account := cloudAccountFromRequest(req)
	keepOmittedAccountFlags(account, existing, req)
	account.ID = id
	account.CreatedAt = existing.CreatedAt
	account.CreatedBy = existing.CreatedBy
	account.UpdatedAt = time.Now()

	if err := h.config.UpdateCloudAccount(ctx, account); err != nil {
		return nil, classifyStoreError(err, fmt.Sprintf("an account with external ID %q already exists for %s", req.ExternalID, req.Provider))
	}

	annotateAccountNotices(account, "")
	return account, nil
}

// deleteAccount handles DELETE /api/accounts/:id.
//
// Preflights the delete against the purchase_executions that still need the
// account (preflightAccountDelete). Executions in a terminal status do not
// block: DeleteCloudAccount detaches them (cloud_account_id = NULL) and keeps
// them as history (issue #265). Migration 000053 also enforces ON DELETE
// RESTRICT at the DB level, so a live execution that races past the
// preflight still can't be orphaned.
func (h *Handler) deleteAccount(ctx context.Context, req *events.LambdaFunctionURLRequest, id string) (any, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	session, err := h.requirePermission(ctx, req, "delete", "accounts")
	if err != nil {
		return nil, err
	}

	// Verify the user can access this account AND that it exists. Returns 404
	// for both "doesn't exist" and "out of scope" to avoid existence leakage.
	_, err = h.requireAccountAccess(ctx, session, id)
	if err != nil {
		return nil, err
	}

	if err := h.preflightAccountDelete(ctx, id); err != nil {
		return nil, err
	}

	if err := h.config.DeleteCloudAccount(ctx, id); err != nil {
		// Race: the preflight found no live execution but one was inserted
		// concurrently before we issued the DELETE. Migration 000053 enforces
		// ON DELETE RESTRICT, so Postgres raises SQLSTATE 23503
		// (foreign_key_violation). Terminal executions never cause this:
		// DeleteCloudAccount detaches them in the same transaction. Re-run the
		// preflight so the client gets the same structured 409 (pending with
		// counts and ids, or active), and fall back to a bare pending 409 if
		// the re-query fails or no longer sees the blocker.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			if perr := h.preflightAccountDelete(ctx, id); perr != nil {
				if ce, ok := IsClientError(perr); ok {
					return nil, ce
				}
			}
			return nil, NewClientErrorWithDetails(409,
				"cannot delete account: pending purchase(s) must be canceled first",
				map[string]any{
					"reason": "pending_executions",
				})
		}
		return nil, fmt.Errorf("accounts: %w", err)
	}

	return nil, nil
}

// preflightAccountDelete refuses the delete with a structured 409 while any
// purchase execution still needs the account:
//
//   - pending/notified rows (reason "pending_executions", issue #606): the
//     frontend uses pending_count + pending_execution_ids to drive the
//     Cancel-All-Then-Delete UX.
//   - any other non-terminal row, e.g. approved, running, scheduled or
//     paused (reason "active_executions", issue #265). These can't be
//     canceled from the account page, so the operator has to let them
//     finish, or revoke or delete them, first.
//
// Terminal rows are not counted: DeleteCloudAccount detaches them and keeps
// them as history.
func (h *Handler) preflightAccountDelete(ctx context.Context, id string) error {
	pendingCount, err := h.config.CountPendingExecutionsForAccount(ctx, id)
	if err != nil {
		return fmt.Errorf("accounts: %w", err)
	}
	if pendingCount > 0 {
		return h.pendingExecutionsConflict(ctx, id, pendingCount)
	}

	liveCount, err := h.config.CountLiveExecutionsForAccount(ctx, id)
	if err != nil {
		return fmt.Errorf("accounts: %w", err)
	}
	if liveCount > 0 {
		return NewClientErrorWithDetails(409,
			fmt.Sprintf("cannot delete account: %d in-progress purchase(s) (approved, running, scheduled or paused) still use it; "+
				"wait for them to finish, or revoke or delete them, first", liveCount),
			map[string]any{
				"active_count": liveCount,
				"reason":       "active_executions",
			})
	}
	return nil
}

// pendingExecutionsConflict builds the issue #606 409 for pendingCount
// pending/notified executions, with their IDs when they can be listed.
func (h *Handler) pendingExecutionsConflict(ctx context.Context, id string, pendingCount int) error {
	msg := fmt.Sprintf("cannot delete account: %d pending purchase(s) must be canceled first", pendingCount)
	execIDs, listErr := h.config.ListPendingExecutionIDsForAccount(ctx, id)
	if listErr != nil {
		// Count succeeded but list failed — still surface the 409 so the
		// operator gets a clear "cancel them first" message instead of
		// the raw FK error from the eventual DB delete. The list payload
		// is omitted; the frontend falls back to a generic message.
		return NewClientErrorWithDetails(409, msg, map[string]any{
			"pending_count": pendingCount,
			"reason":        "pending_executions",
		})
	}
	return NewClientErrorWithDetails(409, msg, map[string]any{
		"pending_count":         pendingCount,
		"pending_execution_ids": execIDs,
		"reason":                "pending_executions",
	})
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

// listAccountServiceOverrides handles GET /api/accounts/:id/service-overrides.
func (h *Handler) listAccountServiceOverrides(ctx context.Context, req *events.LambdaFunctionURLRequest, id string) (any, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	session, err := h.requirePermission(ctx, req, "view", "accounts")
	if err != nil {
		return nil, err
	}

	_, err = h.requireAccountAccess(ctx, session, id)
	if err != nil {
		return nil, err
	}

	overrides, err := h.config.ListAccountServiceOverrides(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}

	if overrides == nil {
		overrides = []config.AccountServiceOverride{}
	}

	return overrides, nil
}

// saveAccountServiceOverride handles PUT /api/accounts/:id/service-overrides/:provider/:service.
// providerServicePath contains the remaining path segment: "uuid/service-overrides/provider/service".
func (h *Handler) saveAccountServiceOverride(ctx context.Context, httpReq *events.LambdaFunctionURLRequest, providerServicePath string) (any, error) {
	accountID, provider, service, err := parseServiceOverridePath(providerServicePath)
	if err != nil {
		return nil, NewClientError(400, err.Error())
	}

	session, err := h.requirePermission(ctx, httpReq, "update", "accounts")
	if err != nil {
		return nil, err
	}

	account, err := h.requireAccountAccess(ctx, session, accountID)
	if err != nil {
		return nil, err
	}
	// The store's lock key is built from the id, so every spelling of one
	// account must reach it as the stored (canonical) one.
	accountID = account.ID

	var req AccountServiceOverrideRequest
	err = json.Unmarshal([]byte(httpReq.Body), &req)
	if err != nil {
		return nil, NewClientError(400, "invalid request body")
	}

	present, err := presentKeys(httpReq.Body, serviceOverrideOverlayKeys)
	if err != nil {
		return nil, err
	}

	// Fetch the commitment options before the transaction opens: the merge
	// callback runs while it holds a pooled connection, so it must not take
	// another (see commitmentOptsSnapshot).
	combos := h.prefetchCommitmentOptions(ctx, provider)

	// The read, merge and write run in one locked transaction (issue #1775),
	// so two single-field PUTs cannot merge onto the same stale row and lose
	// one change. The audit event commits with the write (issue #1493).
	audit := config.AuditContext{Actor: configAuditActor(session)}
	override, err := h.config.UpdateAccountServiceOverrideAtomic(ctx, accountID, provider, service, audit,
		func(existing *config.AccountServiceOverride) (*config.AccountServiceOverride, error) {
			return mergeAndValidateServiceOverride(accountID, provider, service, req, existing, present, combos)
		})
	if err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}

	return override, nil
}

// mergeAndValidateServiceOverride is the read-merge step of an override PUT,
// run inside the store's locked transaction: it merges the request onto the
// stored row, then rejects an invalid (term, payment) combo on the merged
// result. The check is permissive when no probe data exists, as for the
// service-config PUT. It does no I/O.
func mergeAndValidateServiceOverride(accountID, provider, service string, req AccountServiceOverrideRequest, existing *config.AccountServiceOverride, present map[string]bool, combos commitmentOptsSnapshot) (*config.AccountServiceOverride, error) {
	override := buildServiceOverride(accountID, provider, service, req, existing, present, time.Now())
	if err := combos.check(config.ServiceConfig{
		Provider: override.Provider,
		Service:  override.Service,
		Term:     derefInt(override.Term),
		Payment:  derefString(override.Payment),
	}); err != nil {
		return nil, err
	}
	return override, nil
}

// buildServiceOverride constructs an AccountServiceOverride by overlaying the
// request onto the stored row (or an empty one). present names the request
// keys the body carried: only those replace the stored value, so a one-field
// PUT keeps the rest (#1775). A present key with a null value clears it.
func buildServiceOverride(accountID, provider, service string, req AccountServiceOverrideRequest, existing *config.AccountServiceOverride, present map[string]bool, now time.Time) *config.AccountServiceOverride {
	override := &config.AccountServiceOverride{}
	if existing != nil {
		*override = *existing
	} else {
		override.ID = uuid.New().String()
		override.CreatedAt = now
	}
	override.AccountID, override.Provider, override.Service = accountID, provider, service
	override.UpdatedAt = now

	overlayOverrideScalars(override, req, present)
	overlayOverrideSlices(override, req, present)

	return override
}

func overlayOverrideScalars(o *config.AccountServiceOverride, req AccountServiceOverrideRequest, present map[string]bool) {
	if present["enabled"] {
		o.Enabled = req.Enabled
	}
	if present["term"] {
		o.Term = req.Term
	}
	if present["payment"] {
		o.Payment = req.Payment
	}
	if present["coverage"] {
		o.Coverage = req.Coverage
	}
	if present["ramp_schedule"] {
		o.RampSchedule = req.RampSchedule
	}
}

func overlayOverrideSlices(o *config.AccountServiceOverride, req AccountServiceOverrideRequest, present map[string]bool) {
	if present["include_engines"] {
		o.IncludeEngines = req.IncludeEngines
	}
	if present["exclude_engines"] {
		o.ExcludeEngines = req.ExcludeEngines
	}
	if present["include_regions"] {
		o.IncludeRegions = req.IncludeRegions
	}
	if present["exclude_regions"] {
		o.ExcludeRegions = req.ExcludeRegions
	}
	if present["include_types"] {
		o.IncludeTypes = req.IncludeTypes
	}
	if present["exclude_types"] {
		o.ExcludeTypes = req.ExcludeTypes
	}
}

// derefInt dereferences an *int pointer, returning 0 for nil.
func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// derefString dereferences a *string pointer, returning "" for nil.
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// deleteAccountServiceOverride handles DELETE /api/accounts/:id/service-overrides/:provider/:service.
func (h *Handler) deleteAccountServiceOverride(ctx context.Context, req *events.LambdaFunctionURLRequest, providerServicePath string) (any, error) {
	accountID, provider, service, err := parseServiceOverridePath(providerServicePath)
	if err != nil {
		return nil, NewClientError(400, err.Error())
	}

	session, err := h.requirePermission(ctx, req, "delete", "accounts")
	if err != nil {
		return nil, err
	}

	if _, err := h.requireAccountAccess(ctx, session, accountID); err != nil {
		return nil, err
	}

	audit := config.AuditContext{Actor: configAuditActor(session)}
	if err := h.config.WithTx(ctx, func(tx pgx.Tx) error {
		return h.config.DeleteAccountServiceOverrideAuditedTx(ctx, tx, accountID, provider, service, audit)
	}); err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}

	return nil, nil
}

// parseServiceOverridePath parses "uuid/service-overrides/provider/service"
// and returns accountID, provider, service.
func parseServiceOverridePath(path string) (accountID, provider, service string, err error) {
	// Strip leading slash if present
	path = strings.TrimPrefix(path, "/")

	parts := strings.Split(path, "/")
	// Expect: [accountID, "service-overrides", provider, service]
	if len(parts) != 4 {
		return "", "", "", fmt.Errorf("invalid service override path: expected uuid/service-overrides/provider/service")
	}

	accountID = parts[0]
	if _, parseErr := uuid.Parse(accountID); parseErr != nil {
		return "", "", "", fmt.Errorf("invalid account ID in path")
	}

	if parts[1] != "service-overrides" {
		return "", "", "", fmt.Errorf("invalid service override path: missing service-overrides segment")
	}

	provider = parts[2]
	service = parts[3]

	if !validAccountProviders[provider] {
		return "", "", "", fmt.Errorf("invalid provider %q: must be one of aws, azure, gcp", provider)
	}
	if service == "" || !serviceNameRegex.MatchString(service) {
		return "", "", "", fmt.Errorf("invalid service name %q: must be 1-64 lowercase alphanumeric characters or hyphens", service)
	}

	return accountID, provider, service, nil
}
