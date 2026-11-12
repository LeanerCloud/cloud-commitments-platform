package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"
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
