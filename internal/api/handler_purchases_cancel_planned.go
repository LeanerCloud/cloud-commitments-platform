package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

// requireDeleteOrCancelPurchasePermission validates the request session and
// returns it when the caller holds any of: delete:purchases (management),
// update-any:purchases (full-scope management), cancel-any:purchases
// (operator-scope cancel), or cancel-own:purchases (creator-scope cancel,
// issue #1400). update-any is included here (not just in
// authorizePlannedPurchaseCancel) so a custom group holding ONLY update-any
// still passes this entry gate instead of 403ing before ownership is even
// checked; it widens nothing semantically since update-any already grants
// full-scope cancel one step later. Ownership is enforced separately by
// authorizePlannedPurchaseCancel; this gate is the minimum-verb check.
//
// Delegates to the shared requireAnyPermission dispatch (admin API key ->
// user API key -> bearer-token session) so this multi-verb gate cannot drift
// from the single-verb requirePermission path used by every other endpoint.
// A prior hand-rolled version of this gate skipped the user-API-key branch
// entirely, 401-ing scoped API-key automation clients on delete (follow-up to
// #1421).
func (h *Handler) requireDeleteOrCancelPurchasePermission(ctx context.Context, req *events.LambdaFunctionURLRequest) (*Session, error) {
	verbs := []string{auth.ActionDelete, auth.ActionUpdateAny, auth.ActionCancelAny, auth.ActionCancelOwn}
	return h.requireAnyPermission(ctx, req, verbs, auth.ResourcePurchases)
}

// authorizePlannedPurchaseCancel enforces the ownership + cancel permission
// check for DELETE /api/purchases/planned/{id} (issue #1400). It extends
// authorizeExecutionManagement to also accept cancel-any:purchases for
// full-scope cancel (without requiring delete:purchases), and allows the
// creator-scope path without a prior delete:purchases check.
//
// Gate logic:
//   - stateless admin API key: always permitted.
//   - update-any:purchases, then cancel-any:purchases: each is permitted
//     without an owner check when its Constraints cover every recommendation
//     (issues #961 and #678). A Constraints denial falls through to the next
//     full-scope verb and then to the creator branch.
//   - otherwise: require creator match, then apply cancel-own Constraints if
//     the creator also holds cancel-own. Legacy rows with a NULL creator are
//     out of reach for non-privileged users (matching cancel-own /
//     approve-own / retry-own behavior).
func (h *Handler) authorizePlannedPurchaseCancel(ctx context.Context, session *Session, executionID string) error {
	if session.UserID == apiKeyAdminUserID {
		return nil
	}
	if h.auth == nil {
		return NewClientError(500, "authentication service not configured")
	}
	execution, err := h.loadPlannedExecution(ctx, executionID)
	if err != nil {
		return err
	}
	granted, denied, err := h.authorizePlannedFullScope(ctx, session, execution)
	if err != nil || granted {
		return err
	}
	return h.authorizePlannedCreatorCancel(ctx, session, execution, denied)
}

// plannedFullScopeVerbs are the verbs that let DELETE planned act on another
// user's row, in the order they are tried.
var plannedFullScopeVerbs = []string{auth.ActionUpdateAny, auth.ActionCancelAny}

// authorizePlannedFullScope tries each full-scope verb the session holds
// against the strict per-recommendation Constraints of the STORED row
// (revokeExecutionConstraintSets), never the request. granted reports that one
// of them covers the row. denied is the last Constraints 403, returned to a
// non-creator in place of the ownership 403 so the refusal stays terminal; it
// is nil when the session holds neither verb. Any other error (a probe or
// constraint-store failure) is returned as err and never read as a denial, so
// it cannot fall through to a weaker branch. Both verbs are evaluated against
// the session's effective permissions: the key's for a user API key, never the
// owner's alone (issue #979).
func (h *Handler) authorizePlannedFullScope(ctx context.Context, session *Session, execution *config.PurchaseExecution) (granted bool, denied, err error) {
	sets := revokeExecutionConstraintSets(execution)
	for _, verb := range plannedFullScopeVerbs {
		held, probeErr := h.sessionHoldsVerb(ctx, session, verb)
		if probeErr != nil {
			return false, nil, probeErr
		}
		if !held {
			continue
		}
		// Wrapped for consistency; DELETE planned has no token fall-through.
		verbErr := wrapConstraintDenied(h.requirePermissionConstraints(ctx, session, verb, auth.ResourcePurchases, sets))
		if verbErr == nil {
			return true, nil, nil
		}
		if !isForbidden(verbErr) {
			return false, nil, verbErr
		}
		denied = verbErr
	}
	return false, denied, nil
}

// loadPlannedExecution fetches the row, mapping a missing one to errNotFound.
func (h *Handler) loadPlannedExecution(ctx context.Context, executionID string) (*config.PurchaseExecution, error) {
	execution, err := h.config.GetExecutionByID(ctx, executionID)
	if errors.Is(err, config.ErrNotFound) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get execution: %w", err)
	}
	return execution, nil
}

// authorizePlannedCreatorCancel is the creator branch of
// authorizePlannedPurchaseCancel. anyErr is the update-any or cancel-any
// Constraints denial (nil when the session holds neither); it is returned
// instead of the ownership 403 so a constraint refusal stays terminal.
func (h *Handler) authorizePlannedCreatorCancel(ctx context.Context, session *Session, execution *config.PurchaseExecution, anyErr error) error {
	// requireDeleteOrCancelPurchasePermission already validated the session;
	// UserID is either the API-key sentinel (handled by the caller) or a real
	// user UUID from ValidateSession. The nil/empty guard here is defensive.
	if execution.CreatedByUserID == nil || *execution.CreatedByUserID != session.UserID {
		if anyErr != nil {
			return anyErr
		}
		return NewClientError(403, "permission denied: cannot cancel another user's scheduled purchase")
	}
	// A creator match alone is enough (delete:purchases holders need no
	// cancel-own, #961); a creator who also holds cancel-own is bounded by it.
	hasOwn, err := h.sessionHoldsVerb(ctx, session, auth.ActionCancelOwn)
	if err != nil {
		return err
	}
	if !hasOwn {
		return nil
	}
	return wrapConstraintDenied(h.requirePermissionConstraints(ctx, session, auth.ActionCancelOwn, auth.ResourcePurchases, revokeExecutionConstraintSets(execution)))
}

// sessionHoldsVerb reports whether the principal that requirePermissionConstraints
// evaluates holds verb on purchases: the owner for a user session, the KEY's
// effective permissions for a user-API-key session. The key path probes with an
// empty, non-strict set, which any held permission satisfies whatever its
// Constraints, so a denial means the verb is not held.
func (h *Handler) sessionHoldsVerb(ctx context.Context, session *Session, verb string) (bool, error) {
	var has bool
	var err error
	if session.UserAPIKeyID != "" {
		has, err = h.auth.HasAPIKeyPermissionForConstraintsAPI(ctx, session.UserAPIKeyID, session.UserID, verb, auth.ResourcePurchases, []auth.PermissionConstraints{{}})
	} else {
		has, err = h.auth.HasPermissionAPI(ctx, session.UserID, verb, auth.ResourcePurchases)
	}
	if err != nil {
		return false, fmt.Errorf("permission check failed: %w", err)
	}
	return has, nil
}
