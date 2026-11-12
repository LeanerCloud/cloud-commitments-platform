package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
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

func (h *Handler) deletePlannedPurchase(ctx context.Context, req *events.LambdaFunctionURLRequest, executionID string) (*DeletePlannedPurchaseResponse, error) {
	if err := validateUUID(executionID); err != nil {
		return nil, err
	}

	// Accept delete:purchases (management), cancel-any:purchases, or
	// cancel-own:purchases (issue #1400: Standard users must be able to
	// cancel their own upcoming purchases from the Home page).
	session, err := h.requireDeleteOrCancelPurchasePermission(ctx, req)
	if err != nil {
		return nil, err
	}
	err = h.requireExecutionAccess(ctx, session, executionID)
	if err != nil {
		return nil, err
	}
	err = h.authorizePlannedPurchaseCancel(ctx, session, executionID)
	if err != nil {
		return nil, err
	}
	err = h.requirePlannedCancelPlanScope(ctx, session, executionID)
	if err != nil {
		return nil, err
	}

	err = h.requirePlanUpdateIfPlanBearing(ctx, req, executionID)
	if err != nil {
		return nil, err
	}

	canceled, err := h.cancelOrRecoverExecution(ctx, executionID, resolveCreatorUserID(session))
	if err != nil {
		return nil, err
	}

	// Set the parent plan's enabled flag to false so the Plans page toggle
	// reflects the disable action immediately. Issue #774: previously the
	// execution was canceled but plan.enabled was left true, causing
	// inconsistent state between the Scheduled Purchases and Plans views.
	resp := &DeletePlannedPurchaseResponse{Status: "canceled"}
	if canceled.PlanID != "" {
		if err := h.disablePlanAfterCancel(ctx, session, canceled.PlanID); err != nil {
			return nil, err
		}
		disabled := true
		resp.PlanDisabled = &disabled
	}

	return resp, nil
}

// errPlannedCancelPlanOutOfScope refuses a planned purchase cancel whose plan
// disable the caller may not perform. The caller can already see the
// execution and its plan, so the 403 confirms nothing new except that the
// plan reaches accounts it cannot act on, which it needs to know.
var errPlannedCancelPlanOutOfScope = NewClientError(403,
	"permission denied: canceling this scheduled purchase also disables its plan, "+
		"which requires access to every account on the plan; nothing was changed. "+
		"Ask an administrator with access to all of the plan's accounts to disable it")

// requirePlannedCancelPlanScope runs before anything is written. Canceling a
// planned purchase also disables its plan, and a single-account child passes
// requireExecutionAccess on its own account while its plan may span accounts
// outside the caller's scope (issue #520). Disabling the plan changes those
// accounts too, so it needs the every-account check of the plan mutation
// routes. When that check fails the whole request is refused (issue #623):
// canceling and silently leaving the plan enabled would keep the scheduler
// buying on every plan account while the caller believes it stopped.
// Unrestricted sessions return before any store read.
func (h *Handler) requirePlannedCancelPlanScope(ctx context.Context, session *Session, executionID string) error {
	scope, err := h.getAccountScope(ctx, session)
	if err != nil {
		return fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if scope.AllowsAll() {
		return nil
	}
	execution, err := h.loadPlannedExecution(ctx, executionID)
	if err != nil {
		return err
	}
	if execution.PlanID == "" {
		return nil
	}
	err = h.requirePlanMutationAccess(ctx, session, execution.PlanID)
	if IsNotFoundError(err) {
		return errPlannedCancelPlanOutOfScope
	}
	return err
}

// requirePlanUpdateIfPlanBearing requires update:plans when the execution has a
// plan, because canceling it also disables that plan. Call before any write.
func (h *Handler) requirePlanUpdateIfPlanBearing(ctx context.Context, req *events.LambdaFunctionURLRequest, executionID string) error {
	execution, err := h.loadPlannedExecution(ctx, executionID)
	if err != nil {
		return err
	}
	if execution.PlanID == "" {
		return nil
	}
	_, err = h.requirePermission(ctx, req, auth.ActionUpdate, auth.ResourcePlans)
	return err
}

// disablePlanAfterCancel re-runs the every-account check and disables the
// plan. requirePlannedCancelPlanScope already passed, but the plan's accounts
// can change before this point (an out-of-scope account attached while the
// request is in flight). That refusal comes after the cancel, so it is a 409
// that says so rather than a 200 that hides the plan staying enabled.
func (h *Handler) disablePlanAfterCancel(ctx context.Context, session *Session, planID string) error {
	err := h.requirePlanMutationAccess(ctx, session, planID)
	if IsNotFoundError(err) {
		return NewClientError(409,
			"this scheduled purchase was canceled, but its plan was not disabled: "+
				"the plan changed during the request and now requires access to accounts outside yours. "+
				"Ask an administrator with access to all of the plan's accounts to disable it")
	}
	if err != nil {
		return err
	}
	return h.disablePlan(ctx, planID)
}

// releaseCanceledSuppressions releases the suppressions of a row the cancel
// just transitioned. A recommendation-bearing row owns suppressions (written
// with its recommendations); release them like the other cancel paths. The
// delete is idempotent and also runs on cancelOrRecoverExecution's recovery
// path, so a retry heals a failure here.
func (h *Handler) releaseCanceledSuppressions(ctx context.Context, canceled *config.PurchaseExecution, executionID string) (*config.PurchaseExecution, error) {
	if delErr := h.config.DeleteSuppressionsByExecution(ctx, executionID); delErr != nil {
		return nil, fmt.Errorf("cancel execution %s: failed to release suppressions: %w", executionID, delErr)
	}
	return canceled, nil
}

// cancelOrRecoverExecution transitions the execution to "canceled" if it is
// still in {pending, notified, paused}. If a prior attempt already canceled it
// (ErrExecutionNotInExpectedStatus), it fetches the row instead so the caller
// can still drive the plan-disable side-effect, keeping the operation
// idempotent across retries.
// actor is the UUID of the user initiating the cancel (nil for system-initiated paths).
func (h *Handler) cancelOrRecoverExecution(ctx context.Context, executionID string, actor *string) (*config.PurchaseExecution, error) {
	canceled, err := h.config.TransitionExecutionStatus(ctx, executionID, []string{"pending", "notified", "paused"}, "canceled", actor)
	if err == nil {
		return h.releaseCanceledSuppressions(ctx, canceled, executionID)
	}
	if !errors.Is(err, config.ErrExecutionNotInExpectedStatus) {
		// Not a CAS conflict: a real server-side failure (DB error, transient
		// fault). Surface it as a plain wrapped error so the router logs the
		// raw detail and returns a generic 500 -- a 409 here would misclassify
		// a retriable backend failure as the caller's fault and leak backend
		// text (feedback_http_status_classification; same class as PR #1276).
		return nil, fmt.Errorf("cancel execution %s: %w", executionID, err)
	}
	existing, getErr := h.config.GetExecutionByID(ctx, executionID)
	if errors.Is(getErr, config.ErrNotFound) {
		return nil, NewClientError(404, fmt.Sprintf("execution %s not found", executionID))
	}
	if getErr != nil {
		return nil, fmt.Errorf("disable plan: failed to get execution %s after conflict: %w", executionID, getErr)
	}
	// Accept both spellings: during the expand-contract rename (migration
	// 000089) a concurrent legacy cancel may have written the legacy value
	// before the rolling deploy completes. The contract migration (#1278)
	// normalizes the data, after which LegacyStatusCanceled can be removed.
	if existing.Status != config.StatusCanceled && existing.Status != config.LegacyStatusCanceled {
		return nil, NewClientError(409, fmt.Sprintf(
			"execution %s cannot be canceled (status=%s)",
			executionID, existing.Status))
	}
	// Normalize the response Status so the idempotent recovery path returns the
	// canonical US spelling even when the DB row still carries the legacy value.
	// Without this, a caller that received an in-flight 200 from this branch
	// would see status="canceled" while a caller that hit the happy-path
	// transition above would see status="canceled" for the same execution_id
	// during the rolling deploy. The legacy value is preserved in storage --
	// only the in-memory copy returned to the handler is normalized -- so the
	// contract migration's authoritative status backfill still observes every
	// legacy row.
	if existing.Status == config.LegacyStatusCanceled {
		existing.Status = config.StatusCanceled
	}
	if delErr := h.config.DeleteSuppressionsByExecution(ctx, executionID); delErr != nil {
		return nil, fmt.Errorf("cancel execution %s: failed to release suppressions: %w", executionID, delErr)
	}
	return existing, nil
}

// disablePlan fetches the plan identified by planID and sets Enabled=false if
// it is currently true. It is idempotent: calling it against an already-
// disabled plan is a no-op. Returns a 404 ClientError when the plan does not
// exist, and a 500-wrapped error for any other store failure.
func (h *Handler) disablePlan(ctx context.Context, planID string) error {
	plan, err := h.config.GetPurchasePlan(ctx, planID)
	if err != nil {
		if errors.Is(err, config.ErrNotFound) {
			return NewClientError(404, fmt.Sprintf("disable plan: plan %s not found", planID))
		}
		// Do NOT wrap err with the raw plan UUID: a non-ClientError propagates
		// to the router's logging.Errorf("API error: %v"), leaking the plan
		// UUID and the raw DB error string (issue #965, same shape as the
		// validatePlanAccountProviders fix).
		return mapCreatePlanStorageError(err,
			"plan not found", "failed to disable plan",
			"disablePlan: GetPurchasePlan failed")
	}
	if plan.Enabled {
		plan.Enabled = false
		if err := h.config.UpdatePurchasePlan(ctx, plan); err != nil {
			// Same rationale: avoid leaking planID + raw DB error through the
			// router's API error log.
			return mapCreatePlanStorageError(err,
				"plan not found", "failed to disable plan",
				"disablePlan: UpdatePurchasePlan failed")
		}
	}
	return nil
}

// rejectOrphanExecution runs the issue-#609 orphan preflight: non-AWS
// executions whose account no longer exists are rejected with a 409 before
// the cloud SDK is reached. Delegates to the centralized predicate in the
// purchase package so the logic is maintained in one place. It runs only
// once the session is authorized for the execution, so the 409 cannot tell
// an unauthorized caller that the execution exists (issue #597). The token
// branch needs no copy: an orphan has no per-account contact_email, so
// authorizeApprovalAction never authorizes it, and ApproveExecution runs
// the same predicate itself.
func rejectOrphanExecution(execution *config.PurchaseExecution) error {
	if err := purchase.OrphanExecutionError(execution); err != nil {
		return NewClientError(409, err.Error())
	}
	return nil
}
