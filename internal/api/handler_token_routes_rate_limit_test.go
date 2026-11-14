package api

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The token-only approve/cancel/revoke routes are guarded by the limiter alone
// against token guessing, so a limiter error must refuse the request instead
// of letting it through unthrottled (#100).
func TestTokenGuardedRoutes_RateLimiterErrorFailsClosed(t *testing.T) {
	ctx := context.Background()
	handlers := map[string]func(*Router) func(context.Context, *events.LambdaFunctionURLRequest, map[string]string) (any, error){
		"approve purchase": func(r *Router) func(context.Context, *events.LambdaFunctionURLRequest, map[string]string) (any, error) {
			return r.approvePurchaseHandler
		},
		"approval details": func(r *Router) func(context.Context, *events.LambdaFunctionURLRequest, map[string]string) (any, error) {
			return r.approvalDetailsHandler
		},
		"cancel purchase": func(r *Router) func(context.Context, *events.LambdaFunctionURLRequest, map[string]string) (any, error) {
			return r.cancelPurchaseHandler
		},
		"revoke via email": func(r *Router) func(context.Context, *events.LambdaFunctionURLRequest, map[string]string) (any, error) {
			return r.revokeViaEmailTokenHandler
		},
		"approve exchange": func(r *Router) func(context.Context, *events.LambdaFunctionURLRequest, map[string]string) (any, error) {
			return r.approveRIExchangeHandler
		},
		"reject exchange": func(r *Router) func(context.Context, *events.LambdaFunctionURLRequest, map[string]string) (any, error) {
			return r.rejectRIExchangeHandler
		},
	}
	for name, pick := range handlers {
		t.Run(name, func(t *testing.T) {
			mockRL := new(MockRateLimiter)
			mockRL.On("AllowWithIP", ctx, "1.2.3.4", "approve_cancel_public").Return(false, errors.New("pool exhausted"))
			r := newTestRouter(&Handler{rateLimiter: mockRL})
			req := &events.LambdaFunctionURLRequest{
				RequestContext: events.LambdaFunctionURLRequestContext{
					HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{SourceIP: "1.2.3.4"},
				},
				QueryStringParameters: map[string]string{"token": "any-token"},
			}

			_, err := pick(r)(ctx, req, map[string]string{"id": "12345678-1234-1234-1234-123456789abc"})

			ce, ok := IsClientError(err)
			require.True(t, ok)
			assert.Equal(t, 503, ce.code)
			mockRL.AssertExpectations(t)
		})
	}
}
