package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/aws/aws-lambda-go/events"
)

// executeFailure answers a failed run-now, session approve or direct execute.
// A partially_completed run bought part of its commitments, so it gets the
// executed email with the revocation token the funnel minted for it, as a
// completed run does, before the 502 (issue #532). final is the row the
// funnel returned, carrying the per-rec outcome and completed_at.
func (h *Handler) executeFailure(ctx context.Context, req *events.LambdaFunctionURLRequest, executionID, action string, final *config.PurchaseExecution, revocationToken, executedBy string, err error) error {
	h.emailCommittedPartialRun(ctx, req, final, revocationToken, executedBy, err)
	return executeFailureError(executionID, action, final, err)
}

func (h *Handler) emailCommittedPartialRun(ctx context.Context, req *events.LambdaFunctionURLRequest, final *config.PurchaseExecution, revocationToken, executedBy string, err error) {
	if purchase.IsCommittedPartialRun(final, err) {
		h.sendPurchaseExecutedEmail(ctx, req, final, revocationToken, executedBy)
	}
}

// tokenFailureError is executeFailureError with fixed messages: a token
// approver is not scoped to every account on the execution, so the run
// error's text stays in the server log. Statuses and details are unchanged.
func tokenFailureError(executionID string, final *config.PurchaseExecution, err error) error {
	if final == nil || errors.Is(err, purchase.ErrFourEyesDenied) {
		// No row, or a static policy denial: nothing from the run to hide.
		return executeFailureError(executionID, "approved", final, err)
	}
	// Every branch below is fixed text; audit loss is first because it wraps
	// whatever else the run failed with.
	details := map[string]any{"execution_id": executionID, "status": final.Status}
	switch {
	case errors.Is(err, config.ErrAuditLoss):
		return NewClientError(500, "purchase audit could not be completed")
	case errors.Is(err, purchase.ErrStaleAzurePricing):
		return NewClientErrorWithDetails(409, purchase.ErrStaleAzurePricing.Error(), details)
	case errors.Is(err, purchase.ErrClaimedRowRefused):
		code := 500
		if ce, ok := IsClientError(err); ok {
			code = ce.code
		}
		return NewClientErrorWithDetails(code, "the purchase changed after it was authorized and no longer passes your permission checks, so nothing was bought and it is now marked failed", details)
	case purchase.IsCommittedPartialRun(final, err):
		return NewClientErrorWithDetails(502, "purchase partially completed: some commitments were purchased and can be revoked, others failed", details)
	default:
		return NewClientErrorWithDetails(502, "purchase execution failed", details)
	}
}

// Stale Azure prices are refused before purchase, even after a successful claim.
func executeFailureError(executionID, action string, final *config.PurchaseExecution, err error) error {
	if errors.Is(err, purchase.ErrFourEyesDenied) {
		return NewClientError(403, fmt.Sprintf("execution %s cannot be %s: %v", executionID, action, err))
	}
	if final == nil {
		return unclaimedExecuteError(executionID, action, err)
	}
	switch {
	case errors.Is(err, config.ErrAuditLoss):
		logRawExecuteFailure(executionID, err)
		return NewClientError(500, fmt.Sprintf("execution %s ran but its final status could not be saved (reference: execution %s)", executionID, executionID))
	case errors.Is(err, purchase.ErrClaimedRowRefused):
		return claimedRowRefusalError(final, err)
	case errors.Is(err, purchase.ErrStaleAzurePricing):
		return NewClientError(409, purchase.ErrStaleAzurePricing.Error())
	case purchase.IsCommittedPartialRun(final, err):
		category, msg := summarizeExecuteFailure(executionID, err)
		return NewClientErrorWithDetails(502, fmt.Sprintf("execution %s partially completed: some commitments were purchased and can be revoked, others failed: %s (reference: execution %s)", executionID, msg, executionID),
			map[string]any{"execution_id": executionID, "status": final.Status, "category": category})
	default:
		category, msg := summarizeExecuteFailure(executionID, err)
		return NewClientErrorWithDetails(502, fmt.Sprintf("execution %s failed: %s (reference: execution %s)", executionID, msg, executionID),
			map[string]any{"execution_id": executionID, "status": final.Status, "category": category})
	}
}

// logRawExecuteFailure keeps the provider's own error text in the server log
// only; responses carry a category message and the execution ID.
func logRawExecuteFailure(executionID string, err error) {
	logging.Errorf("purchase[%s]: execution failed: %v", executionID, err)
}

func summarizeExecuteFailure(executionID string, err error) (category, message string) {
	logRawExecuteFailure(executionID, err)
	return purchase.SummarizeFailure(err)
}

// unclaimedExecuteError answers a failure where the funnel returned no row:
// a lost claim race or a vanished row is a 409, anything else is a plain error.
func unclaimedExecuteError(executionID, action string, err error) error {
	if errors.Is(err, config.ErrExecutionNotInExpectedStatus) || errors.Is(err, config.ErrNotFound) {
		return NewClientError(409, fmt.Sprintf("execution %s cannot be %s: %v", executionID, action, err))
	}
	return fmt.Errorf("execution %s could not be %s: %w", executionID, action, err)
}

// executionNotFound is the answer for an execution ID that does not exist
// on the approve, cancel and revoke routes. concealExecutionDenial returns
// the same error for an execution the caller may not act on, so the two
// cases have the same status, headers and body (issue #597).
func executionNotFound() *clientError {
	return &clientError{code: 404, message: "execution not found"}
}

// concealedDenialError is the "execution not found" 404 returned in place of
// a final authorization denial. It responds exactly like executionNotFound
// (IsClientError resolves it through Unwrap) and keeps the denial it hides
// in cause, for logs and tests only.
type concealedDenialError struct {
	*clientError
	cause error
}

func (e *concealedDenialError) Unwrap() error { return e.clientError }

// concealExecutionDenial maps a final authorization denial on an existing
// execution (an account-scope miss, an RBAC or contact_email 403, or a 401
// from the contact_email gate) to the 404 a missing execution gets, so a
// signed-in caller cannot use the approve, cancel and revoke routes to
// learn which execution IDs exist (issue #597). Only direct *clientError
// values are mapped: a Constraints denial (constraintDeniedError) goes to a
// caller who already holds approve-* for the execution, and wrapped
// backend failures must keep surfacing as 500s.
func concealExecutionDenial(err error) error {
	ce, ok := err.(*clientError) //nolint:errorlint // strict (unwrapped) assertion is deliberate; see isPermissionDenied
	if !errors.Is(err, errNotFound) && (!ok || (ce.code != 401 && ce.code != 403)) {
		return err
	}
	logging.Infof("purchase action denied, answered as execution not found: %v", err)
	return &concealedDenialError{clientError: executionNotFound(), cause: err}
}

// tokenActionError maps the email-link approve/cancel failures that are the
// caller's fault to a 4xx: a wrong token is 403 (as on the RI exchange
// approve path), as is a token rotated or expired between the snapshot check
// and the status CAS (config.ErrApprovalTokenChanged, issue #1190), an expired
// token is 410, and an execution whose status no longer allows the action is
// 409. Anything else stays a server error.
func tokenActionError(err error) error {
	switch {
	case errors.Is(err, config.ErrAuditLoss):
		return err
	case errors.Is(err, purchase.ErrClaimedRowRefused):
		return claimedRowRefusalError(nil, err)
	case errors.Is(err, purchase.ErrStaleAzurePricing):
		return NewClientError(409, purchase.ErrStaleAzurePricing.Error())
	case errors.Is(err, purchase.ErrInvalidApprovalToken), errors.Is(err, config.ErrApprovalTokenChanged):
		return NewClientError(403, "invalid approval token")
	case errors.Is(err, purchase.ErrApprovalTokenExpired):
		return NewClientError(410, "approval token has expired")
	case errors.Is(err, config.ErrExecutionNotInExpectedStatus):
		return NewClientError(409, err.Error())
	default:
		return err
	}
}

// constraintDeniedError marks a 403 produced by requirePermissionConstraints
// (a granted verb whose own per-permission Constraints reject the request)
// so isPermissionDenied's strict, unwrapped *clientError assertion does NOT
// recognize it (issue #60 review follow-up, same mechanism as errCSRFRejected
// above). This matters because approvePurchase's session-vs-token dispatch
// treats any isPermissionDenied 403 as "no approve-* grant at all, fall
// through to the token/contact_email flow" -- correct for a bare missing-verb
// denial, but wrong for a Constraints-exceeded denial: approveViaToken never
// evaluates Constraints, so a session holding a capped approve-any/
// approve-own would otherwise bypass its own cap by using the email
// deep-link (which forces a login and therefore always carries a session)
// instead of the dashboard Approve button. Wrapping forces the dispatch's
// `default:` branch (propagate, terminal) instead of the fall-through
// branch. IsClientError still resolves this to the right HTTP code and
// message via errors.As + Unwrap, so response mapping is unaffected.
type constraintDeniedError struct {
	*clientError
}

func (e *constraintDeniedError) Unwrap() error { return e.clientError }

// wrapConstraintDenied marks a non-nil requirePermissionConstraints /
// enforcePurchaseConstraints error as terminal (see constraintDeniedError).
// A nil err, or an err that isn't a *clientError (e.g. an auth-service
// failure wrapped with fmt.Errorf), passes through unchanged -- the latter
// is already un-recognized by isPermissionDenied's own strict assertion, so
// it already hits the propagate branch without needing to be wrapped here.
func wrapConstraintDenied(err error) error {
	if err == nil {
		return nil
	}
	ce, ok := err.(*clientError) //nolint:errorlint // wrapping the concrete constructor's return; see constraintDeniedError doc
	if !ok {
		return err
	}
	return &constraintDeniedError{clientError: ce}
}

// fallsThroughToToken reports whether a session-authorization failure should
// hand the request to the email-token branch: an RBAC denial (403) or an
// account-scope miss (404), and only when a token is present. The token
// branch's contact_email gate is deliberately not account-scoped (issue #92).
// Without a token the denial is final (issue #173): nothing else can authorize
// the request, and the 404 must not let later status guards reveal an
// out-of-scope execution. Both matches are strict, like isPermissionDenied,
// so a wrapped error from deeper in the chain propagates.
func fallsThroughToToken(err error, token string) bool {
	return token != "" && (isPermissionDenied(err) || err == errNotFound) //nolint:errorlint // strict sentinel identity is deliberate
}

// isPermissionDenied reports whether err is *directly* a 403 ClientError
// (not merely something that wraps one). Used by the cancel-from-email
// session pre-check to distinguish a legitimate "your session lacks
// cancel-* permission" answer (fall through to the token branch's
// contact_email gate) from a transient auth-service failure (propagate so
// the caller sees the real cause). CR feedback on PR #216.
//
// Strict (un-wrapped) type assertion is deliberate: if a future caller
// wraps a 403 to add context (fmt.Errorf("permission check failed: %w",
// ...)), that wrapped error represents the *outer* failure mode (the
// wrapper's intent), not "this is still a 403". errors.As-style unwrapping
// would erase that distinction and silently route wrapped backend
// failures into the contact_email gate — exactly the misclassification
// the propagate-vs-fall-through split is meant to prevent. constraintDeniedError
// above deliberately exploits the same strictness from the other direction.
func isPermissionDenied(err error) bool {
	ce, ok := err.(*clientError) //nolint:errorlint // strict (unwrapped) assertion is deliberate; see comment above
	return ok && ce.code == 403
}

// emailFailureNotRecordedError is the 500 finalizePurchaseStatus returns when
// the approval email failed and the write recording that failure failed too
// (issue #54). The body names the execution and carries the email reason so
// the caller can find the row and cancel it. The store error stays out of
// the body but is wrapped so it remains reachable with errors.Is.
func emailFailureNotRecordedError(executionID, emailReason string, saveErr error) error {
	ce := NewClientErrorWithDetails(500,
		fmt.Sprintf("AUDIT LOSS: the approval email for execution %s could not be sent, and saving that failure on the execution also failed; "+
			"it may still show as pending with no approval email on the way, so cancel it from History", executionID),
		map[string]any{
			"execution_id": executionID,
			"email_sent":   false,
			"email_reason": emailReason,
		})
	return fmt.Errorf("%w: %w", ce, saveErr)
}
