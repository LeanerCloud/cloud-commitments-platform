package api

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// authorizeSessionRevokeExecution enforces the revoke-any / revoke-own RBAC
// matrix for scheduled executions (pre-SDK-call state). Mirrors
// authorizeSessionRevoke for completed purchases but operates on a
// PurchaseExecution (which has CreatedByUserID) rather than a
// PurchaseHistoryRecord (which has CloudAccountID). Each verb is also checked
// against the Constraints of the permission that grants it; a constrained
// revoke-any falls back to revoke-own when the creator matches.
func (h *Handler) authorizeSessionRevokeExecution(ctx context.Context, session *Session, execution *config.PurchaseExecution) error {
	if session.UserID == apiKeyAdminUserID {
		return nil
	}

	hasAny, err := h.sessionHoldsVerb(ctx, session, auth.ActionRevokeAny)
	if err != nil {
		return err
	}
	var anyErr error
	if hasAny {
		anyErr = h.requirePermissionConstraints(ctx, session, auth.ActionRevokeAny, auth.ResourcePurchases, revokeExecutionConstraintSets(execution))
		if anyErr == nil || !isForbidden(anyErr) {
			return anyErr
		}
	}

	hasOwn, err := h.sessionHoldsVerb(ctx, session, auth.ActionRevokeOwn)
	if err != nil {
		return err
	}
	if !hasOwn {
		if anyErr != nil {
			return anyErr
		}
		return NewClientError(403, "permission denied: requires revoke-any or revoke-own on purchases")
	}

	return h.authorizeRevokeOwnExecution(ctx, session, execution)
}

// authorizeRevokeOwnExecution requires the execution to have been created by
// this user, then checks revoke-own Constraints. NULL CreatedByUserID means a
// non-human or legacy creator: deny rather than allow an unscoped revoke
// (fail-closed).
func (h *Handler) authorizeRevokeOwnExecution(ctx context.Context, session *Session, execution *config.PurchaseExecution) error {
	if execution.CreatedByUserID == nil || *execution.CreatedByUserID != session.UserID {
		return NewClientError(403, "permission denied: cannot revoke another user's scheduled purchase")
	}
	return h.requirePermissionConstraints(ctx, session, auth.ActionRevokeOwn, auth.ResourcePurchases, revokeExecutionConstraintSets(execution))
}

// revokeConstraintSet is the strict request-side constraint set for a revoke:
// a dimension the stored row does not know is left empty, which a constrained
// grant on that dimension refuses. No MaxPurchaseAmount: a revoke is a refund,
// not a spend.
func revokeConstraintSet(accountID, provider, service, region string) auth.PermissionConstraints {
	return auth.PermissionConstraints{
		StrictScope: true, AccountIDs: knownScopeValue(accountID), Providers: knownScopeValue(provider),
		Services: knownScopeValue(service), Regions: knownScopeValue(region),
	}
}

// revokeExecutionConstraintSets builds one set per recommendation. An
// execution with none gets a single empty strict set so a constrained grant
// still denies instead of the empty slice failing the permission check loudly.
// The session cancel paths (issue #678) reuse it.
func revokeExecutionConstraintSets(execution *config.PurchaseExecution) []auth.PermissionConstraints {
	if len(execution.Recommendations) == 0 {
		return []auth.PermissionConstraints{revokeConstraintSet("", "", "", "")}
	}
	sets := make([]auth.PermissionConstraints, 0, len(execution.Recommendations))
	for i := range execution.Recommendations {
		rec := &execution.Recommendations[i]
		sets = append(sets, revokeConstraintSet(derefString(rec.CloudAccountID), rec.Provider, rec.Service, rec.Region))
	}
	return sets
}

func isForbidden(err error) bool {
	ce, ok := IsClientError(err)
	return ok && ce.code == 403
}

// authorizeSessionRevoke enforces the revoke-any / revoke-own RBAC matrix, then
// the Constraints of the granting permission against the stored record. A
// revoke-any that fails on account access or constraints falls back to
// revoke-own, as requireSessionPurchaseAction does.
// Mirror of authorizeSessionCancel / authorizeSessionApprove patterns.
func (h *Handler) authorizeSessionRevoke(ctx context.Context, session *Session, record *config.PurchaseHistoryRecord) error {
	// The stateless admin API key has full access and no user row to resolve
	// permissions from. Administrators-group users fall through and pass via
	// the revoke-any check below, since {admin, *} matches
	// any requested permission.
	if session.UserID == apiKeyAdminUserID {
		return nil
	}

	hasAny, err := h.sessionHoldsVerb(ctx, session, auth.ActionRevokeAny)
	if err != nil {
		return err
	}
	var anyErr error
	if hasAny {
		anyErr = h.authorizeRevokeVerb(ctx, session, record, auth.ActionRevokeAny)
		if anyErr == nil || !isForbidden(anyErr) {
			return anyErr
		}
	}

	hasOwn, err := h.sessionHoldsVerb(ctx, session, auth.ActionRevokeOwn)
	if err != nil {
		return err
	}
	if !hasOwn {
		if anyErr != nil {
			return anyErr
		}
		return NewClientError(403, "permission denied: requires revoke-any or revoke-own on purchases")
	}
	return h.authorizeRevokeVerb(ctx, session, record, auth.ActionRevokeOwn)
}

// authorizeRevokeVerb applies account access and permission Constraints for
// one held verb. Values come from the stored record, never the request body.
func (h *Handler) authorizeRevokeVerb(ctx context.Context, session *Session, record *config.PurchaseHistoryRecord, verb string) error {
	if err := h.checkRevokeAccountAccess(ctx, session, record, verb == auth.ActionRevokeAny); err != nil {
		return err
	}
	return h.requirePermissionConstraints(ctx, session, verb, auth.ResourcePurchases, []auth.PermissionConstraints{
		revokeConstraintSet(derefString(record.CloudAccountID), record.Provider, record.Service, record.Region),
	})
}

// checkRevokeAccountAccess requires the purchase to be in a cloud account the
// session may access. revoke-any lifts no account scope (issue #386, the #92
// class): only an unrestricted revoke-any caller skips the check.
func (h *Handler) checkRevokeAccountAccess(ctx context.Context, session *Session, record *config.PurchaseHistoryRecord, revokeAny bool) error {
	// Goes through getAccountScope rather than calling GetAllowedAccountsAPI
	// directly: the direct call skipped the admin-API-key and nil-auth
	// branches, and the `len(allowed) > 0 &&` guard read an empty list as
	// unrestricted independently of any producer (issue #1748).
	scope, err := h.getAccountScope(ctx, session)
	if err != nil {
		return fmt.Errorf("account access check failed: %w", err)
	}
	if revokeAny && scope.AllowsAll() {
		return nil
	}
	// Purchase history rows pre-date created_by_user_id, so ownership is via
	// account access (creator scope: issue #950). Unattributed rows fail closed.
	// Match the way History does: a row may carry only the external account
	// id, and allow-lists may name accounts rather than list ids (issue #534).
	// A set CloudAccountID is authoritative; the external id is only a
	// fallback when it is absent, and is resolved within the row's provider
	// because external ids are unique per (provider, external_id) only.
	cloudID := derefString(record.CloudAccountID)
	if cloudID == "" && record.AccountID == "" {
		return NewClientError(403, "permission denied: cannot verify ownership for this purchase")
	}
	accounts, listErr := h.config.ListCloudAccounts(ctx, config.CloudAccountFilter{})
	if cloudID != "" {
		return revokeScopeResult(scope.Allows(cloudID, accountNameByID(accounts, cloudID)))
	}
	name := ""
	if listErr == nil {
		name = accountNameByExternalID(accounts, record.Provider, record.AccountID)
	}
	return revokeScopeResult(scope.Allows(record.AccountID, name))
}

func revokeScopeResult(allowed bool) error {
	if allowed {
		return nil
	}
	return NewClientError(403, "permission denied: purchase is in an account you do not have access to")
}

func accountNameByID(accounts []config.CloudAccount, id string) string {
	for i := range accounts {
		if accounts[i].ID == id {
			return accounts[i].Name
		}
	}
	return ""
}

// accountNameByExternalID returns the name of the single account with this
// provider and external id, or "" when none or more than one matches, so the
// caller cannot name-match an ambiguous account.
func accountNameByExternalID(accounts []config.CloudAccount, provider, externalID string) string {
	if a := uniqueAccountByExternalID(accounts, provider, externalID); a != nil {
		return a.Name
	}
	return ""
}

// uniqueAccountByExternalID returns the single account with this provider and
// external id, or nil when none or more than one matches.
func uniqueAccountByExternalID(accounts []config.CloudAccount, provider, externalID string) *config.CloudAccount {
	var found *config.CloudAccount
	for i := range accounts {
		if accounts[i].Provider != provider || accounts[i].ExternalID != externalID {
			continue
		}
		if found != nil {
			return nil
		}
		found = &accounts[i]
	}
	return found
}
