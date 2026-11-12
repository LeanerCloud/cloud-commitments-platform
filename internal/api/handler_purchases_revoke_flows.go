package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

// revokeViaEmailToken is the one-click revocation handler embedded in the
// post-execution notification email (issue #291). It accepts the same
// three-mode dispatch shape as approvePurchase / cancelPurchase:
//
//  1. Session present AND RBAC-authorized (admin / cancel-any /
//     cancel-own) → session-authed path.
//  2. token != "" → token-authed path via the email one-click link.
//  3. No token, no qualifying session → 401.
//
// The revocation window check is intentionally limited in this
// iteration: because the sibling "AWS RI/SP revocation via support case"
// issue has not yet landed its dedicated revocation_window_closes_at
// column, revokeViaEmailToken reuses the ApprovalToken. Once the sibling lands
// and adds a RevocationToken + RevocationWindowClosesAt column, this
// handler should validate RevocationWindowClosesAt here before
// attempting the cancellation.
//
// At this scope the revoke action is equivalent to a cancel on a
// completed/completed execution — the underlying cloud revocation
// (AWS support-case path) is out of scope for this issue and is handled
// by the sibling "AWS RI/SP revocation" issue.
//
// Present-day behavior: calling this route within the AWS revocation
// window requests the cancellation and returns {"status":"revocation_requested"}.
// Past the window it returns a friendly 409 with a plain-language message
// rather than a stack trace.
// revokeConfirmPageTmpl is the minimal HTML form rendered on GET /revoke.
// The form POSTs the token from a hidden input so the actual mutation never
// fires via a GET request (prevents email prefetchers from accidentally
// triggering the revocation).
const revokeConfirmPageTmpl = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>Confirm Revocation</title>
<style>body{font-family:sans-serif;max-width:480px;margin:60px auto;padding:0 16px}
.btn{display:inline-block;padding:10px 24px;background:#dc2626;color:#fff;border:none;
border-radius:4px;font-size:15px;cursor:pointer;text-decoration:none}
.btn:hover{background:#b91c1c}.note{color:#64748b;font-size:13px;margin-top:12px}</style>
</head>
<body>
<h2>Confirm Revocation</h2>
<p>You are about to request revocation for purchase execution <code>{{.ExecutionID}}</code>.</p>
<p>This will record a revocation request. Contact AWS Support to complete the
cancellation within the allowed window.</p>
<form method="POST" action="/api/purchases/revoke/{{.ExecutionID}}">
  <input type="hidden" name="token" value="{{.Token}}">
  <button type="submit" class="btn">Confirm Revoke</button>
</form>
<p class="note">If you did not request this, ignore this page. No action has been taken.</p>
</body>
</html>`

func (h *Handler) revokeViaEmailToken(ctx context.Context, req *events.LambdaFunctionURLRequest, execID, token string) (any, error) {
	if err := validateUUID(execID); err != nil {
		return nil, err
	}

	// GET: render a confirmation page so email prefetchers cannot auto-trigger
	// the revocation. The token travels in the URL query string (unavoidable for
	// the email one-click link), but no mutation occurs on GET.
	if req.RequestContext.HTTP.Method == "GET" {
		return renderRevokeConfirmPage(execID, token), nil
	}

	if err := h.requireSessionBeforeLookup(ctx, req); err != nil {
		return nil, err
	}

	execution, err := h.getExecutionOr404(ctx, execID)
	if err != nil {
		return nil, err
	}

	// Three-mode dispatch — same shape as cancelPurchase.
	result, handled, revokeErr := h.tryRevokeViaSession(ctx, req, execution, token)
	if handled {
		return result, revokeErr
	}

	if token == "" {
		return nil, NewClientError(401, "sign in or use the revocation link from the notification email")
	}

	// One-click limitation (issue #291): the email link carries a token, but
	// authorizeApprovalAction derives the actor from the *session*
	// (tryResolveActorEmail), not from the token's bound contact email. A
	// recipient who clicks the link while logged out therefore gets a 401 and
	// must sign in with the matching contact email first. This intentionally
	// matches the existing approve/cancel email-link behavior -- we do not
	// widen the authz model here. True tokenless one-click (deriving the actor
	// from a single-use, contact-bound token) is deferred to the sibling
	// "AWS RI/SP revocation" work, which adds a dedicated revocation token.
	return h.revokeViaToken(ctx, req, execution, token)
}

// revokeViaToken is the token-authed tail of revokeViaEmailToken: confirm the
// caller is the authorized contact email, validate the token (reusing the
// approval token for this iteration), then check the status. Same gate order
// as before extraction.
func (h *Handler) revokeViaToken(ctx context.Context, req *events.LambdaFunctionURLRequest, execution *config.PurchaseExecution, token string) (any, error) {
	actor, err := h.authorizeApprovalAction(ctx, req, execution)
	if err != nil {
		return nil, concealExecutionDenial(err)
	}
	if err := validateRevokeToken(execution, token); err != nil {
		return nil, err
	}
	if err := checkRevokableStatus(execution); err != nil {
		return nil, err
	}
	return h.revokeViaSession(ctx, execution, actor, resolveCreatorUserID(h.tryGetSession(ctx, req)))
}

// getExecutionOr404 loads an execution, answering 404 for a missing one
// (ErrNotFound or a nil row) and leaving real store errors as 500s.
func (h *Handler) getExecutionOr404(ctx context.Context, execID string) (*config.PurchaseExecution, error) {
	execution, err := h.config.GetExecutionByID(ctx, execID)
	if errors.Is(err, config.ErrNotFound) || (err == nil && execution == nil) {
		return nil, executionNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get execution: %w", err)
	}
	return execution, nil
}

// renderRevokeConfirmPage returns a rawResponse containing the HTML
// confirmation form for GET /revoke. The form POSTs the token so the actual
// mutation only happens when the user explicitly clicks "Confirm Revoke".
func renderRevokeConfirmPage(execID, token string) *rawResponse {
	// Manual substitution to avoid importing html/template just for this one
	// small page. The values substituted here (execID and token) are both
	// treated as opaque strings — execID is a UUID (hex+hyphens only),
	// token is a generated random string. HTML-escape them defensively anyway.
	escaped := func(s string) string {
		s = strings.ReplaceAll(s, "&", "&amp;")
		s = strings.ReplaceAll(s, "<", "&lt;")
		s = strings.ReplaceAll(s, ">", "&gt;")
		s = strings.ReplaceAll(s, "\"", "&#34;")
		return s
	}
	page := strings.ReplaceAll(revokeConfirmPageTmpl, "{{.ExecutionID}}", escaped(execID))
	page = strings.ReplaceAll(page, "{{.Token}}", escaped(token))
	return &rawResponse{
		contentType: "text/html; charset=utf-8",
		body:        page,
	}
}
