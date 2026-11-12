package api

import (
	"context"
	"strings"

	"github.com/aws/aws-lambda-go/events"
)

// Route finds and executes the matching route handler.
//
// Authentication enforcement is defense-in-depth: authentication middleware
// already runs in the middleware pipeline before dispatch,
// but Router.Route also enforces the per-route Auth level so routes stay
// protected even if middleware ordering changes or a new code path
// bypasses validateSecurityContext. AuthAdmin routes require admin access;
// AuthUser routes require any authenticated user; AuthPublic routes are
// unauthenticated. There is no implicit default — every route declares
// its level at registration time and NewRouter rejects authUnset.
func (r *Router) Route(ctx context.Context, method, path string, req *events.LambdaFunctionURLRequest) (any, error) {
	for _, route := range r.routes {
		if r.matches(route, method, path) {
			switch route.Auth {
			case AuthAdmin:
				_, hadPrincipal := principalFromContext(ctx)
				session, err := r.h.requireAdmin(ctx, req)
				if err != nil {
					return nil, err
				}
				if !hadPrincipal {
					if session.UserID == apiKeyAdminUserID {
						ctx = contextWithPrincipal(ctx, &Principal{Kind: PrincipalAdminAPIKey})
					} else {
						ctx = contextWithPrincipal(ctx, &Principal{
							Kind:    PrincipalSession,
							Session: session,
							UserID:  session.UserID,
							Email:   session.Email,
						})
					}
				}
			case AuthUser:
				principal, err := r.h.requireAuth(ctx, req)
				if err != nil {
					return nil, err
				}
				ctx = contextWithPrincipal(ctx, principal)
			case AuthPublic:
				// no auth check; relied upon by middleware via isPublicEndpoint
			default:
				// authUnset / unknown level — NewRouter should have already
				// panicked at startup. Defense in depth: refuse to dispatch.
				return nil, NewClientError(500, "internal routing error")
			}
			params := r.extractParams(route, path)
			return route.Handler(ctx, req, params)
		}
	}
	return nil, errNotFound
}

// matches checks if a route matches the given method and path.
func (r *Router) matches(route Route, method, path string) bool {
	// Check method (if specified)
	if route.Method != "" && route.Method != method {
		return false
	}

	// Check exact path match
	if route.ExactPath != "" {
		return route.ExactPath == path
	}

	// Check prefix and suffix
	if route.PathPrefix != "" && !strings.HasPrefix(path, route.PathPrefix) {
		return false
	}
	if route.PathSuffix != "" && !strings.HasSuffix(path, route.PathSuffix) {
		return false
	}

	// If we have a prefix or suffix, we matched
	return route.PathPrefix != "" || route.PathSuffix != ""
}

// extractParams extracts path parameters from the route.
func (r *Router) extractParams(route Route, path string) map[string]string {
	params := make(map[string]string)

	// Extract ID from prefix-based routes
	if route.PathPrefix != "" {
		remaining := strings.TrimPrefix(path, route.PathPrefix)
		if route.PathSuffix != "" {
			remaining = strings.TrimSuffix(remaining, route.PathSuffix)
		}
		if remaining != "" {
			params["id"] = remaining
		}
	}

	return params
}
