package api

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/aws/aws-lambda-go/events"
)

func (r *Router) createPlannedPurchasesHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.createPlannedPurchases(ctx, req, params["id"])
}

func (r *Router) executePurchaseHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.executePurchase(ctx, req)
}

func (r *Router) getPurchaseDetailsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getPurchaseDetails(ctx, req, params["id"])
}

// resolveApprovalToken extracts the approval token for approve/cancel/revoke
// actions (issue #398). For POST requests the token is read from the request
// body so it does not appear in the Lambda Function URL access logs (which log
// the rawQueryString but not the body). Two body encodings are supported:
//
//   - application/json: {"token": "<value>"}
//   - application/x-www-form-urlencoded: token=<value>  (used by the revoke
//     confirmation form rendered by the GET /revoke handler)
//
// The query string is still accepted as a fallback so legacy GET clicks from
// email links (which carry the token in the URL) continue to work.
//
// Priority: POST body (JSON) > POST body (form) > query string.
func resolveApprovalToken(req *events.LambdaFunctionURLRequest) string {
	if req.RequestContext.HTTP.Method == "POST" && req.Body != "" {
		// Try JSON body first.
		var body struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal([]byte(req.Body), &body); err == nil && body.Token != "" {
			return body.Token
		}
		// Fall through to form-encoded body (used by the HTML confirmation form).
		vals, err := url.ParseQuery(req.Body)
		if err == nil {
			if t := vals.Get("token"); t != "" {
				return t
			}
		}
	}
	return req.QueryStringParameters["token"]
}

func (r *Router) approvePurchaseHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	if err := r.h.checkRateLimit(ctx, req, "approve_cancel_public"); err != nil {
		return nil, err
	}
	token := resolveApprovalToken(req)
	return r.h.approvePurchase(ctx, req, params["id"], token)
}

func (r *Router) approvalDetailsHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	if err := r.h.checkRateLimit(ctx, req, "approve_cancel_public"); err != nil {
		return nil, err
	}
	return r.h.getApprovalDetails(ctx, req, params["id"], resolveApprovalToken(req))
}

func (r *Router) cancelPurchaseHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	if err := r.h.checkRateLimit(ctx, req, "approve_cancel_public"); err != nil {
		return nil, err
	}
	token := resolveApprovalToken(req)
	return r.h.cancelPurchase(ctx, req, params["id"], token)
}

func (r *Router) settleExecutionHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.settleExecution(ctx, req, params["id"])
}

func (r *Router) retryPurchaseHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.retryPurchase(ctx, req, params["id"])
}

// revokeViaEmailTokenHandler handles GET/POST /api/purchases/revoke/{execID}
// (issue #291 email one-click link). GET renders a confirmation form;
// POST executes the token-based revocation.
func (r *Router) revokeViaEmailTokenHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	if err := r.h.checkRateLimit(ctx, req, "approve_cancel_public"); err != nil {
		return nil, err
	}
	token := resolveApprovalToken(req)
	return r.h.revokeViaEmailToken(ctx, req, params["id"], token)
}

// revokePurchaseHandler handles POST /api/purchases/{id}/revoke
// (issue #290 in-app session-authenticated revoke within the provider's
// free-cancel window).
func (r *Router) revokePurchaseHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.revokePurchase(ctx, req, params["id"])
}

func (r *Router) calculateRevokeHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.calculateAzureRevoke(ctx, req, params["id"])
}

func (r *Router) marketplaceListHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.marketplaceList(ctx, req, params["id"])
}

func (r *Router) marketplaceCancelHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.marketplaceCancel(ctx, req, params["id"])
}
