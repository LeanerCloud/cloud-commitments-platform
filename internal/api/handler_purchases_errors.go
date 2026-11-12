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
