package api

import (
	"context"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// authorizeExecutionManagement enforces creator-scope ownership on the
// scheduled-purchase management handlers (pause / resume / run / delete),
// closing the authz hole in issue #950 where any holder of update:purchases
// could act on another user's scheduled purchase. It runs AFTER the
// per-handler verb check (update / execute / delete) and the account-scope
// check (requireExecutionAccess); those still apply unchanged. Pause, resume
// and run reach it through authorizeBoundedExecutionManagement, which bounds
// update-any by its Constraints first (issues #1098, #1129).
//
// Gate logic (mirrors authorizeSessionCancel / authorizeSessionApprove):
//   - stateless admin API key: always permitted (apiKeyAdminUserID sentinel).
//   - update-any:purchases: permitted regardless of creator. Administrators-
//     group users pass here because {admin, *} matches ActionUpdateAny
//     (update-any is not in adminCarvedOuts).
//   - otherwise: permitted only when the execution's CreatedByUserID is
//     non-nil and equals a non-empty session.UserID (the caller created it).
//     Legacy rows with a NULL creator are out of reach for non-update-any
//     users, matching the cancel-own / approve-own / retry-own model.
//   - any other case: 403 fail-closed. A nil auth component is a 500 per
//     feedback_fail_closed_middleware.md.
//
// Only fetches the execution on the creator-match path: admin and update-any
// callers are authorized without a store round-trip (and admin sessions have
// unrestricted access, so requireExecutionAccess skipped the fetch too).
func (h *Handler) authorizeExecutionManagement(ctx context.Context, session *Session, executionID string) error {
	if session.UserID == apiKeyAdminUserID {
		return nil
	}
	if h.auth == nil {
		return NewClientError(500, "authentication service not configured")
	}

	// Evaluated against the session's effective permissions: for a user API
	// key that is the key (intersected with its owner), not the owner alone,
	// so a key without update-any cannot borrow the owner's (issue #979).
	hasAny, err := h.sessionHoldsVerb(ctx, session, auth.ActionUpdateAny)
	if err != nil {
		return err
	}
	if hasAny {
		return nil
	}

	execution, err := h.loadPlannedExecution(ctx, executionID)
	if err != nil {
		return err
	}

	// Creator match: both IDs must be non-empty and equal. An empty-string
	// collision (legacy NULL creator + missing session UserID) must not
	// grant access.
	if session.UserID == "" || execution.CreatedByUserID == nil || *execution.CreatedByUserID != session.UserID {
		return NewClientError(403, "permission denied: cannot manage another user's scheduled purchase")
	}
	return nil
}

// authorizeBoundedExecutionManagement is authorizeExecutionManagement with
// update-any bounded by its Constraints, used by pause, resume (issue #1098)
// and run (issue #1129). Sessions without update-any take the unchanged
// authorizeExecutionManagement path; a session holding update-any is checked
// against the stored row by authorizeUpdateAnyOnRow.
func (h *Handler) authorizeBoundedExecutionManagement(ctx context.Context, session *Session, executionID string) error {
	if session.UserID == apiKeyAdminUserID {
		return nil
	}
	if h.auth == nil {
		return NewClientError(500, "authentication service not configured")
	}
	hasAny, err := h.sessionHoldsVerb(ctx, session, auth.ActionUpdateAny)
	if err != nil {
		return err
	}
	if !hasAny {
		return h.authorizeExecutionManagement(ctx, session, executionID)
	}
	execution, err := h.loadPlannedExecution(ctx, executionID)
	if err != nil {
		return err
	}
	_, err = h.authorizeUpdateAnyOnRow(ctx, session, execution)
	return err
}

// authorizeUpdateAnyOnRow checks a session holding update-any against the
// strict per-recommendation scope of the STORED row
// (revokeExecutionConstraintSets), mirroring the planned-purchase cancel
// gates. An in-scope grant is permitted without an owner check. A Constraints
// denial falls back to the creator match and otherwise stays the refusal; any
// other error (a probe or constraint-store failure) is returned and never read
// as a denial, so it cannot fall through to a weaker branch.
func (h *Handler) authorizeUpdateAnyOnRow(ctx context.Context, session *Session, execution *config.PurchaseExecution) (creatorFallback bool, err error) {
	anyErr := h.requirePermissionConstraints(ctx, session, auth.ActionUpdateAny, auth.ResourcePurchases, revokeExecutionConstraintSets(execution))
	if anyErr == nil || !isForbidden(anyErr) {
		return false, anyErr
	}
	if isExecutionCreator(session, execution) {
		return true, nil
	}
	return false, anyErr
}

// authorizeClaimedRowManagement re-runs authorizeBoundedExecutionManagement's
// decision on a row already in hand: the row run's claim CAS returned
// (issue #415), so an edit landing between the pre-claim read and the claim
// cannot carry the row out of a bounded update-any's scope.
func (h *Handler) authorizeClaimedRowManagement(ctx context.Context, session *Session, execution *config.PurchaseExecution) error {
	_, err := h.authorizeRowManagement(ctx, session, execution)
	return err
}

// authorizeRowManagement is the decision behind authorizeClaimedRowManagement
// that also reports the verb that authorized the caller: "" for the admin API
// key (unrestricted), auth.ActionUpdateAny when the update-any scope check
// passed, auth.ActionUpdate when the session has no update-any or the creator
// match won after an update-any scope denial. Resume bounds the row's amount
// by that verb's cap.
func (h *Handler) authorizeRowManagement(ctx context.Context, session *Session, execution *config.PurchaseExecution) (verb string, err error) {
	if session.UserID == apiKeyAdminUserID {
		return "", nil
	}
	if h.auth == nil {
		return "", NewClientError(500, "authentication service not configured")
	}
	hasAny, err := h.sessionHoldsVerb(ctx, session, auth.ActionUpdateAny)
	if err != nil {
		return "", err
	}
	if hasAny {
		creatorFallback, anyErr := h.authorizeUpdateAnyOnRow(ctx, session, execution)
		if anyErr != nil {
			return "", anyErr
		}
		if creatorFallback {
			return auth.ActionUpdate, nil
		}
		return auth.ActionUpdateAny, nil
	}
	if !isExecutionCreator(session, execution) {
		return "", NewClientError(403, "permission denied: cannot manage another user's scheduled purchase")
	}
	return auth.ActionUpdate, nil
}

// isExecutionCreator requires both IDs to be non-empty and equal, so an
// empty-string collision (legacy NULL creator, missing session UserID) never
// matches.
func isExecutionCreator(session *Session, execution *config.PurchaseExecution) bool {
	return session.UserID != "" && execution.CreatedByUserID != nil && *execution.CreatedByUserID == session.UserID
}
