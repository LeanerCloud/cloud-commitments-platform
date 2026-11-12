package api

import (
	"context"
	"errors"

	"github.com/aws/aws-lambda-go/events"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
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
