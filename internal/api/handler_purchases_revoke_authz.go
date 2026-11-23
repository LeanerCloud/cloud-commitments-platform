package api

import (
	"context"

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
