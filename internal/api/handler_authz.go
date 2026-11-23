package api

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/aws/aws-lambda-go/events"
)

// apiKeyAdminUserID is the sentinel UserID assigned to the stateless admin
// API-key session. It has no backing user row in the auth store, so any code
// that would otherwise resolve group-derived permissions for it must treat it
// as full-access up front (the API key is an infrastructure credential, not a
// user). See requirePermission / requireAdmin / getAccountScope.
// It is also the actor identity threaded into the group-write grant ceiling,
// which recognizes it and measures the key against a bare {admin, *} holding
// (auth.AdminAPIKeyActorID) rather than failing the user lookup.
const apiKeyAdminUserID = auth.AdminAPIKeyActorID

// requirePermission validates authentication and checks if the user holds the
// specified permission. The stateless admin API key bypasses the per-user
// permission lookup (it is a full-access infrastructure credential). A user
// API key is checked against its effective permissions: the intersection of
// the key's scoped permissions with the owning user's group-derived
// permissions (issue #1142 - previously user API keys 401'd here and per-key
// scoping was never enforced). Every other caller is checked against their
// group-derived permissions: a member of the Administrators group holds
// {admin, *} and passes any check, while a user with no groups holds no
// permissions and is denied (fail closed). Returns the session on success so
// callers can read session.UserID for account filtering.
func (h *Handler) requirePermission(ctx context.Context, req *events.LambdaFunctionURLRequest, action, resource string) (*Session, error) {
	if principal, ok := principalFromContext(ctx); ok {
		return h.requirePrincipalPermission(ctx, req, principal, action, resource)
	}

	apiKey := extractAPIKey(req)
	if h.checkAdminAPIKey(apiKey) {
		return &Session{UserID: apiKeyAdminUserID}, nil
	}

	if h.auth == nil {
		return nil, fmt.Errorf("authentication service not configured")
	}

	// User API key: authorize against the key's effective permissions. A
	// valid key that lacks the permission is denied (fail closed). A key
	// that fails validation falls through to bearer-token auth, matching
	// resolveAuthenticatedUserID, so a stale x-api-key header cannot lock
	// out a caller that also presents a valid session.
	session, apiKeyErr := h.authorizeAPIKey(ctx, apiKey, action, resource)
	if apiKeyErr != nil {
		return nil, apiKeyErr
	}
	if session != nil {
		return session, nil
	}

	token := h.extractBearerToken(req)
	if token == "" {
		return nil, NewClientError(401, "no authorization token provided")
	}

	session, err := h.auth.ValidateSession(ctx, token)
	if err != nil {
		return nil, NewClientError(401, "invalid session")
	}

	return h.requireSessionPermission(ctx, session, action, resource)
}

func (h *Handler) requirePrincipalPermission(ctx context.Context, req *events.LambdaFunctionURLRequest, principal *Principal, action, resource string) (*Session, error) {
	switch principal.Kind {
	case PrincipalAdminAPIKey:
		return &Session{UserID: apiKeyAdminUserID}, nil
	case PrincipalUserAPIKey:
		session, err := h.authorizeAPIKey(ctx, extractAPIKey(req), action, resource)
		if err != nil {
			return nil, err
		}
		if session == nil {
			return nil, NewClientError(401, "API key permission validation failed")
		}
		return session, nil
	case PrincipalSession:
		return h.requireSessionPermission(ctx, principal.Session, action, resource)
	default:
		return nil, NewClientError(401, "unsupported authentication principal")
	}
}

func (h *Handler) requireSessionPermission(ctx context.Context, session *Session, action, resource string) (*Session, error) {
	if session == nil || session.UserID == "" {
		return nil, NewClientError(401, "invalid session")
	}
	has, err := h.auth.HasPermissionAPI(ctx, session.UserID, action, resource)
	if err != nil {
		return nil, fmt.Errorf("permission check failed: %w", err)
	}
	if !has {
		return nil, NewClientError(403, fmt.Sprintf("permission denied: requires %s on %s", action, resource))
	}

	return session, nil
}

// authorizeAPIKey checks whether the given API key value has the required
// permission. It returns:
//   - (nil, nil)         if apiKey is absent or fails validation (caller falls
//     through to bearer-token auth)
//   - (session, nil)     if the key grants the permission and identity is valid
//   - (nil, 403 error)   if the key is present and actively denies the request
//   - (nil, other error) if an invariant is violated (e.g. incomplete identity)
//
// Fail closed: empty userID or keyID after a successful grant is an internal
// error, not a fall-through, so the caller cannot silently skip key constraints.
func (h *Handler) authorizeAPIKey(ctx context.Context, apiKey, action, resource string) (*Session, error) {
	if apiKey == "" {
		return nil, nil
	}
	userID, keyID, has, err := h.auth.HasAPIKeyPermissionAPI(ctx, apiKey, action, resource)
	if err != nil {
		logging.Debugf("User API key permission check failed: %v", err)
		return nil, nil // validation error: fall through to bearer-token auth
	}
	if !has {
		return nil, NewClientError(403, fmt.Sprintf("permission denied: requires %s on %s", action, resource))
	}
	if userID == "" || keyID == "" {
		return nil, fmt.Errorf("API key permission check returned incomplete identity")
	}
	// Thread the key's database ID so requirePermissionConstraints can evaluate
	// constraints against the key's effective permissions (not just the owning
	// user's group permissions).
	return &Session{UserID: userID, UserAPIKeyID: keyID}, nil
}

// requireAnyPermission is the multi-verb counterpart to requirePermission: it
// grants access when the caller holds ANY of verbs on resource, returning the
// session for the first verb that matches. It mirrors requirePermission's
// auth-dispatch order (principal-in-context fast path, then admin API key,
// then user API key, then bearer-token session) so a multi-verb gate (e.g.
// "delete OR cancel-any OR cancel-own") cannot drift from the single-verb
// dispatch order every other endpoint relies on (follow-up to #1421, which
// hand-rolled a gate that dropped the user-API-key branch entirely).
func (h *Handler) requireAnyPermission(ctx context.Context, req *events.LambdaFunctionURLRequest, verbs []string, resource string) (*Session, error) {
	if principal, ok := principalFromContext(ctx); ok {
		return h.requireAnyPrincipalPermission(ctx, req, principal, verbs, resource)
	}

	apiKey := extractAPIKey(req)
	if h.checkAdminAPIKey(apiKey) {
		return &Session{UserID: apiKeyAdminUserID}, nil
	}

	if h.auth == nil {
		return nil, fmt.Errorf("authentication service not configured")
	}

	session, apiKeyErr := h.authorizeAPIKeyAny(ctx, apiKey, verbs, resource)
	if apiKeyErr != nil {
		return nil, apiKeyErr
	}
	if session != nil {
		return session, nil
	}

	token := h.extractBearerToken(req)
	if token == "" {
		return nil, NewClientError(401, "no authorization token provided")
	}

	session, err := h.auth.ValidateSession(ctx, token)
	if err != nil {
		return nil, NewClientError(401, "invalid session")
	}

	return h.requireAnySessionPermission(ctx, session, verbs, resource)
}
