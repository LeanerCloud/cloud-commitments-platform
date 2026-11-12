package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-lambda-go/events"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/ctxutil"
)

// approveRIExchange handles approval of a pending RI exchange.
//
// Three-mode dispatch mirroring approvePurchase (issue #286, issue #300):
//
//  1. Session present AND RBAC-authorized (admin / approve-any / approve-own
//     match) -> session-authed approve, regardless of whether a token is also
//     in the URL. Closes issue #300.
//  2. token != "" -> legacy email-link flow. validateExchangeApproval enforces
//     the token-equality check; the permission-denied fall-through ensures a
//     logged-in user without approve-* can still use an email link they hold.
//  3. token == "": a session denial is returned as-is (issue #173); with no
//     session, approveRIExchangeViaSession's CSRF check fails first and the
//     request gets 403 (never 401).
func (h *Handler) approveRIExchange(ctx context.Context, req *events.LambdaFunctionURLRequest, id, token string) (any, error) {
	if session := h.tryGetSession(ctx, req); session != nil {
		// Quick RBAC pre-check (no record fetch needed): does this session hold
		// ANY approve right? If yes, hand off to approveRIExchangeViaSession
		// which will re-check ownership with the actual record. If 403, fall
		// through to the token branch so email-link holders can still approve.
		switch err := h.sessionHasApproveRight(ctx, session); {
		case err == nil:
			result, sessErr := h.approveRIExchangeViaSession(ctx, req, id, session)
			if sessErr == nil {
				return result, nil
			}
			// Record-level RBAC denied (e.g. approve-own user is not the creator).
			// If a token is present, preserve legacy token flow; otherwise surface the error.
			//
			// A CSRF rejection must NOT fall through, even with a token present.
			// fallsThroughToToken matches any 403, so without this it cannot tell a
			// forged cross-site request from a legitimate approve-own denial, and the
			// request would proceed on the token alone: the exchange executes, but as
			// an unattributed system approval (transitioned_by NULL, no approved_by
			// stamp) on a money path. Checked first so the sentinel is never reached
			// by the fall-through test below.
			if errors.Is(sessErr, errCSRFRejected) || !fallsThroughToToken(sessErr, token) {
				return nil, sessErr
			}
		case token != "" && isPermissionDenied(err):
			// Logged-in user without approve-* may still hold a valid email token.
		default:
			return nil, err
		}
	}

	if token != "" {
		return h.approveRIExchangeViaToken(ctx, req, id, token)
	}

	return h.approveRIExchangeViaSession(ctx, req, id, nil)
}

// approveRIExchangeViaToken is the legacy email-link branch of approveRIExchange.
// It validates the approval token, transitions the exchange to processing, and
// executes it. Extracted to keep approveRIExchange within cyclomatic-complexity limits.
func (h *Handler) approveRIExchangeViaToken(ctx context.Context, req *events.LambdaFunctionURLRequest, id, token string) (any, error) {
	record, err := h.validateExchangeApproval(ctx, id, token)
	if err != nil {
		return nil, err
	}
	// The deep link forces a login, so the approver's session is resolvable here.
	// When it is not (pure email-client flow), the actor stays NULL.
	session := h.tryGetSession(ctx, req)
	err = h.requireDifferentRIExchangeApprover(ctx, session, record)
	if err != nil {
		return nil, err
	}

	transitioned, err := h.config.TransitionRIExchangeStatus(ctx, id, "pending", "processing", resolveCreatorUserID(session))
	if err != nil {
		return nil, riExchangeTransitionError(err, riExchangeApproveConflictMsg)
	}
	if transitioned == nil {
		return nil, NewClientError(409, riExchangeApproveConflictMsg)
	}

	result, execErr := h.executeApprovedExchange(ctx, id, record)
	if execErr == nil && session != nil {
		h.stampRIExchangeApprover(ctx, id, session)
	}
	return result, execErr
}

// stampRIExchangeApprover records session.Email as approved_by. Best-effort: the
// exchange already executed, so a stamp failure is logged, not surfaced.
func (h *Handler) stampRIExchangeApprover(ctx context.Context, id string, session *Session) {
	// Runs after money moved, so the audit write must not die with the request (#790).
	ctx, cancel := ctxutil.DetachedWithTimeout(ctx, riExchangeTerminalSaveTimeout)
	defer cancel()
	if err := h.config.StampRIExchangeApprovedBy(ctx, id, session.Email); err != nil {
		logging.Errorf("failed to stamp approved_by on exchange %s: %v", id, err)
	}
}

// approveRIExchangeViaSession is the session-authed branch of approveRIExchange
// (issue #300). Enforces the approve-any/approve-own RBAC matrix, then atomically
// transitions pending -> processing and executes the exchange. Stamps
// session.Email onto the approved_by column as an audit trail.
//
// The session parameter may be non-nil (already validated by the caller) or nil
// (an absent session never reaches requireSession: the CSRF check above
// rejects it with 403).
func (h *Handler) approveRIExchangeViaSession(ctx context.Context, req *events.LambdaFunctionURLRequest, id string, session *Session) (any, error) {
	// This endpoint is AuthPublic so the outer middleware skips CSRF.
	// Enforce it here for the session-authed sub-path, mirroring
	// approvePurchaseViaSession / cancelPurchaseViaSession: the session
	// bearer token is cookie-equivalent and must be CSRF-protected (issue
	// #1757 -- this call was previously reachable with no CSRF token at
	// all, despite a comment claiming parity with the purchases path).
	if err := h.validateCSRF(ctx, req); err != nil {
		return nil, errCSRFRejected
	}

	var err error
	if session == nil {
		session, err = h.requireSession(ctx, req)
		if err != nil {
			return nil, err
		}
	}

	record, err := h.fetchAndAuthorizeRIExchange(ctx, session, id)
	if err != nil {
		return nil, err
	}
	err = h.requireDifferentRIExchangeApprover(ctx, session, record)
	if err != nil {
		return nil, err
	}

	// Session-authed approval: stamp the session user as the actor.
	transitioned, err := h.config.TransitionRIExchangeStatus(ctx, id, "pending", "processing", resolveCreatorUserID(session))
	if err != nil {
		return nil, riExchangeTransitionError(err, riExchangeApproveConflictMsg)
	}
	if transitioned == nil {
		return nil, NewClientError(409, riExchangeApproveConflictMsg)
	}

	result, execErr := h.executeApprovedExchange(ctx, id, record)

	if execErr == nil {
		h.stampRIExchangeApprover(ctx, id, session)
	}

	return result, execErr
}

// fetchAndAuthorizeRIExchange looks up the exchange record by id, verifies that
// session is authorized to approve it, and then checks that it is in "pending"
// state. Extracted from approveRIExchangeViaSession to keep that function under
// the cyclomatic-complexity limit.
//
// Ownership is checked before existence and status (issue #1428): an
// approve-own session gets the same 403 for a missing id and for another
// user's exchange, pending or not, so it cannot learn which ids exist or read
// their status. The 403 also falls through to the email token like any other
// ownership refusal. Approve-any sessions and the admin key may approve any
// exchange, so they keep the 404/409 distinction.
func (h *Handler) fetchAndAuthorizeRIExchange(ctx context.Context, session *Session, id string) (*config.RIExchangeRecord, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	// Same deployment-account scope as executeExchange (#93), checked before
	// the fetch so an out-of-scope id is indistinguishable from a missing one.
	inScope, err := h.reshapeCloudAccountInScope(ctx, session)
	if err != nil {
		return nil, err
	}
	if !inScope {
		return nil, errNotFound
	}

	record, err := h.getRIExchangeRecord(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := h.authorizeSessionApproveRIExchange(ctx, session, record); err != nil {
		return nil, err
	}
	if record == nil {
		return nil, NewClientError(404, "exchange record not found")
	}
	if record.Status != "pending" {
		return nil, NewClientError(409, fmt.Sprintf("exchange %s cannot be approved (status=%s)", id, record.Status))
	}

	return record, nil
}

// sessionHasApproveRight returns nil when the session holds ANY approve right
// on purchases (admin / approve-any / approve-own) without checking ownership.
// Used by the three-mode dispatch in approveRIExchange to decide whether to route
// to approveRIExchangeViaSession before fetching the record.
func (h *Handler) sessionHasApproveRight(ctx context.Context, session *Session) error {
	// Stateless admin API key: full access, no user row. Administrators-group
	// users pass via the approve-any check below.
	if session.UserID == apiKeyAdminUserID {
		return nil
	}
	if h.auth == nil {
		return NewClientError(500, "authentication service not configured")
	}
	hasAny, err := h.sessionHoldsVerb(ctx, session, auth.ActionApproveAny)
	if err != nil {
		return err
	}
	if hasAny {
		return nil
	}
	hasOwn, err := h.sessionHoldsVerb(ctx, session, auth.ActionApproveOwn)
	if err != nil {
		return err
	}
	if hasOwn {
		return nil
	}
	return NewClientError(403, "permission denied: requires approve-any or approve-own on purchases")
}

// authorizeSessionApproveRIExchange returns nil when the session is permitted to
// approve the given RI exchange record under the approve-any / approve-own RBAC rules
// (issue #300). Mirrors authorizeSessionApprove from handler_purchases_approve.go.
//
// The RI exchange shares ResourcePurchases because approval is conceptually
// "approving a purchase action on a different resource type" per the issue spec,
// which prefers reusing the existing verbs to keep the matrix small.
//
// record may be nil (no such exchange). An approve-own session is refused for
// it exactly as for another user's record (issue #1428); the admin key and
// approve-any pass, and the caller answers 404.
func (h *Handler) authorizeSessionApproveRIExchange(ctx context.Context, session *Session, record *config.RIExchangeRecord) error {
	// Stateless admin API key: full access, no user row. Administrators-group
	// users pass via the approve-any check below.
	if session.UserID == apiKeyAdminUserID {
		return nil
	}
	if h.auth == nil {
		return NewClientError(500, "authentication service not configured")
	}

	hasAny, err := h.sessionHoldsVerb(ctx, session, auth.ActionApproveAny)
	if err != nil {
		return err
	}
	if hasAny {
		return nil
	}

	hasOwn, err := h.sessionHoldsVerb(ctx, session, auth.ActionApproveOwn)
	if err != nil {
		return err
	}
	if !hasOwn {
		return NewClientError(403, "permission denied: requires approve-any or approve-own on purchases")
	}

	// approve-own: only allow if the session user created this exchange. A
	// missing record answers the same, so the refusal is no existence oracle.
	if record == nil || record.CreatedByUserID == nil || *record.CreatedByUserID != session.UserID {
		return NewClientError(403, "permission denied: cannot approve another user's pending exchange")
	}

	return nil
}

// rejectRIExchange handles rejection of a pending RI exchange via token.
func (h *Handler) rejectRIExchange(ctx context.Context, id, token string) (any, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}
	if token == "" {
		return nil, NewClientError(400, "rejection token is required")
	}

	record, err := h.getRIExchangeRecord(ctx, id)
	if err != nil {
		return nil, err
	}
	if tokenErr := checkExchangeToken(record, token, "rejection"); tokenErr != nil {
		return nil, tokenErr
	}

	// Token-based rejection: no session user, so transitioned_by = NULL.
	transitioned, err := h.config.TransitionRIExchangeStatus(ctx, id, "pending", config.StatusCanceled, nil)
	if err != nil {
		return nil, riExchangeTransitionError(err, riExchangeRejectConflictMsg)
	}
	if transitioned == nil {
		return nil, NewClientError(409, riExchangeRejectConflictMsg)
	}

	return map[string]string{"status": config.StatusCanceled}, nil
}
