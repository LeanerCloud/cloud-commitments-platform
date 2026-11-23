// Package api provides the HTTP API handlers for the CUDly dashboard.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/aws/aws-lambda-go/events"
)

// Auth handlers

func (h *Handler) login(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	if h.auth == nil {
		return nil, fmt.Errorf("authentication service not configured")
	}

	// Rate limiting: 5 attempts per IP per 15 minutes
	if err := h.checkRateLimitStrict(ctx, req, "login"); err != nil {
		return nil, err
	}

	var loginReq LoginRequest
	if err := json.Unmarshal([]byte(req.Body), &loginReq); err != nil {
		return nil, NewClientError(400, "invalid request body")
	}

	// Decode base64-encoded password
	decoded, err := decodeBase64Password(loginReq.Password)
	if err != nil {
		return nil, err
	}
	loginReq.Password = decoded
	if formErr := validateLoginForm(&loginReq); formErr != nil {
		return nil, formErr
	}

	if limitErr := h.checkLoginMFARateLimit(ctx, loginReq.Email, loginReq.MFACode); limitErr != nil {
		return nil, limitErr
	}

	response, err := h.auth.Login(ctx, loginReq)
	if err != nil {
		return nil, mapLoginError(err)
	}

	return response, nil
}

// validateLoginForm rejects a login body that mixes the two MFA forms: the
// mfa_token form (issue #134) carries email, mfa_token and mfa_code and no
// password; every other body is the password form.
func validateLoginForm(req *LoginRequest) error {
	if req.MFAToken == "" {
		return nil
	}
	if req.Password != "" {
		return NewClientError(400, "send either password or mfa_token, not both")
	}
	if req.Email == "" || req.MFACode == "" {
		return NewClientError(400, "mfa_token requires email and mfa_code")
	}
	return nil
}

// mapLoginError maps a Login failure to its client response. The MFA
// sentinels become machine-readable error codes so the frontend can detect
// "MFA required" / "invalid MFA code" / "start again" without
// substring-matching the human message (issues #497, #134). They keep the
// 401 status: the request is not authorized until the MFA leg passes too.
func mapLoginError(err error) error {
	var challenge *auth.MFARequiredError
	if errors.As(err, &challenge) {
		return NewClientErrorWithDetails(401, "mfa_required", map[string]any{
			"mfa_token":            challenge.Token,
			"mfa_token_expires_at": challenge.ExpiresAt.UTC().Format(time.RFC3339),
		})
	}
	if errors.Is(err, auth.ErrMFARequired) {
		return NewClientError(401, "mfa_required")
	}
	if errors.Is(err, auth.ErrInvalidMFACode) {
		return NewClientError(401, "invalid_mfa_code")
	}
	if errors.Is(err, auth.ErrInvalidMFAToken) {
		return NewClientError(401, "invalid_mfa_token")
	}
	// All other auth failures (wrong password, account not found, locked, etc.)
	// collapse to a single opaque 401. Never forward err.Error() verbatim - it
	// may reveal internal account state.
	return NewClientError(401, "invalid credentials")
}

func (h *Handler) logout(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	if h.auth == nil {
		return nil, fmt.Errorf("authentication service not configured")
	}

	// Get token from Authorization header
	token := h.extractBearerToken(req)
	if token == "" {
		return nil, NewClientError(401, "no authorization token provided")
	}

	if err := h.auth.Logout(ctx, token); err != nil {
		return nil, NewClientError(401, "invalid session")
	}

	return map[string]string{"status": "logged out"}, nil
}

func (h *Handler) getCurrentUser(ctx context.Context, req *events.LambdaFunctionURLRequest) (*CurrentUserResponse, error) {
	if h.auth == nil {
		return nil, fmt.Errorf("authentication service not configured")
	}

	session, err := h.requireSessionPrincipal(ctx, req)
	if err != nil {
		return nil, err
	}

	user, err := h.auth.GetUser(ctx, session.UserID)
	if err != nil {
		return nil, err
	}

	resp := &CurrentUserResponse{
		ID:         user.ID,
		Email:      user.Email,
		Groups:     user.Groups,
		MFAEnabled: user.MFAEnabled,
	}
	// A reloaded page holds no CSRF token; re-derive it from the session.
	if issuer, ok := h.auth.(csrfTokenIssuer); ok {
		if resp.CSRFToken, err = issuer.CSRFTokenForSession(ctx, h.extractBearerToken(req)); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

// getCurrentUserPermissions handles GET /api/auth/me/permissions.
// It returns the effective permission set of the authenticated caller, the
// same set the backend enforces for that caller:
//   - bearer-token session: the union of the user's group permissions
//     (GetUserPermissionsAPI -> GetUserPermissions);
//   - user API key: the key's permissions intersected with its owner's
//     (GetAPIKeyEffectivePermissionsAPI), never the owner's full set, so a
//     view-only key owned by an admin is not advertised as admin (issue #62);
//   - admin API key: {admin, *}.
//
// IsAdmin is derived from that same set. UnrestrictedAccountAccess reports
// whether the caller passes the account-scope gate that global actions such
// as POST /api/recommendations/refresh enforce (issue #619), so the frontend
// can skip an automatic refresh the backend would refuse with 403.
//
// Auth: the route is AuthUser, which admits admin API key, user API
// key, OR a bearer-token session (see router.go AuthLevel doc). The
// handler resolves the authenticated caller from any of those three
// credentials rather than re-requiring a bearer session — otherwise a
// caller that authenticated upstream with an API key would still get a
// 401 here.
func (h *Handler) getCurrentUserPermissions(ctx context.Context, req *events.LambdaFunctionURLRequest) (*UserPermissionsResponse, error) {
	if h.auth == nil {
		return nil, fmt.Errorf("authentication service not configured")
	}

	keyID, keyOwnerID, isUserAPIKey, err := h.userAPIKeyCaller(ctx, req)
	if err != nil {
		return nil, err
	}
	if isUserAPIKey {
		resp, keyErr := h.userAPIKeyPermissions(ctx, keyID, keyOwnerID)
		if keyErr != nil {
			return nil, keyErr
		}
		// Resolve the flag for the same session the refresh gate sees for
		// this key: authorizeAPIKey puts the owner's ID and the key's ID on
		// it, so the two stay in step if getAccountScope ever reads the key.
		resp.UnrestrictedAccountAccess = h.unrestrictedAccountAccess(ctx, &Session{UserID: keyOwnerID, UserAPIKeyID: keyID})
		return resp, nil
	}

	userID, err := h.resolveAuthenticatedUserID(ctx, req)
	if err != nil {
		return nil, err
	}

	// The stateless admin API key has no backing user row, so the
	// per-user permission lookup would fail. It is a full-access
	// infrastructure credential — surface it as {admin, *} + is_admin
	// (matches the requireAdmin / requirePermission short-circuit).
	if userID == apiKeyAdminUserID {
		return &UserPermissionsResponse{
			Permissions:               []PermissionEntry{{Action: auth.ActionAdmin, Resource: auth.ResourceAll}},
			IsAdmin:                   true,
			UnrestrictedAccountAccess: h.unrestrictedAccountAccess(ctx, &Session{UserID: userID}),
		}, nil
	}

	raw, err := h.auth.GetUserPermissionsAPI(ctx, userID)
	if err != nil {
		return nil, err
	}

	// GetUserPermissionsAPI returns []auth.APIPermission via any to avoid
	// an import cycle between the api and auth packages. Fail loudly on
	// an unexpected payload shape: a silent fall-through to an empty
	// slice would render as "user lost all access" in the frontend and
	// mask the real server bug.
	apiPerms, ok := raw.([]auth.APIPermission)
	if !ok {
		logging.Errorf("getCurrentUserPermissions: GetUserPermissionsAPI returned %T, want []auth.APIPermission", raw)
		return nil, fmt.Errorf("GetUserPermissionsAPI returned unexpected payload type %T", raw)
	}
	resp := permissionsResponse(apiPerms)
	resp.UnrestrictedAccountAccess = h.unrestrictedAccountAccess(ctx, &Session{UserID: userID})
	return resp, nil
}

// unrestrictedAccountAccess reports whether session passes
// requireGlobalScope on view:recommendations, the gate on POST
// /api/recommendations/refresh, by running that same check rather than a
// copy of it. A scope lookup failure is logged and reported as false: the flag
// only steers the UI (false skips an automatic refresh), the refresh endpoint
// still enforces the scope itself, and failing the whole permissions response
// would drop the frontend to its coarser group-based gating.
func (h *Handler) unrestrictedAccountAccess(ctx context.Context, session *Session) bool {
	err := h.requireGlobalScope(ctx, session, auth.ActionView, auth.ResourceRecommendations, refreshScopeMsg)
	if err == nil {
		return true
	}
	if ce, isClient := IsClientError(err); isClient && ce.code == 403 {
		return false // a restricted scope: the expected refusal
	}
	logging.Errorf("getCurrentUserPermissions: resolving account scope for user %s: %v", session.UserID, err)
	return false
}

// userAPIKeyCaller reports whether the request authenticated with a user API
// key and, if so, returns the key's database ID and its owner's user ID.
// With a principal in context (every routed request) it reads the principal:
// a user-API-key principal wins over a bearer session sent alongside it,
// exactly as requirePermission evaluates it. Without one it validates the
// x-api-key header itself; a key that does not validate is not a key caller
// (the bearer session is tried next, as resolveAuthenticatedUserID does), but
// a key that validates without a usable key/owner record is refused rather
// than described with its owner's permissions.
func (h *Handler) userAPIKeyCaller(ctx context.Context, req *events.LambdaFunctionURLRequest) (keyID, userID string, isUserAPIKey bool, err error) {
	if principal, ok := principalFromContext(ctx); ok {
		if principal.Kind != PrincipalUserAPIKey {
			return "", "", false, nil
		}
		return principal.APIKeyID, principal.UserID, true, nil
	}
	apiKey := extractAPIKey(req)
	if apiKey == "" || h.checkAdminAPIKey(apiKey) {
		return "", "", false, nil
	}
	keyRaw, userRaw, validateErr := h.auth.ValidateUserAPIKeyAPI(ctx, apiKey)
	if validateErr != nil {
		return "", "", false, nil
	}
	key, keyOK := keyRaw.(*auth.UserAPIKey)
	user, userOK := userRaw.(*auth.User)
	if !keyOK || !userOK || key == nil || user == nil {
		return "", "", false, NewClientError(401, "API key permission validation failed")
	}
	return key.ID, user.ID, true, nil
}

// userAPIKeyPermissions builds the /me/permissions response for a user API
// key from the key's effective permissions. Fail closed: a lookup error
// refuses the request instead of falling back to the owner's permissions.
func (h *Handler) userAPIKeyPermissions(ctx context.Context, keyID, userID string) (*UserPermissionsResponse, error) {
	apiPerms, err := h.auth.GetAPIKeyEffectivePermissionsAPI(ctx, keyID, userID)
	if err != nil {
		logging.Errorf("getCurrentUserPermissions: resolving API key %s effective permissions: %v", keyID, err)
		return nil, fmt.Errorf("failed to resolve API key permissions: %w", err)
	}
	return permissionsResponse(apiPerms), nil
}

// permissionsResponse converts an effective permission set to the wire
// shape, setting IsAdmin when the set holds the {admin, *} wildcard.
func permissionsResponse(apiPerms []auth.APIPermission) *UserPermissionsResponse {
	entries := make([]PermissionEntry, len(apiPerms))
	isAdmin := false
	for i, p := range apiPerms {
		entries[i] = PermissionEntry{Action: p.Action, Resource: p.Resource}
		if p.Action == auth.ActionAdmin && p.Resource == auth.ResourceAll {
			isAdmin = true
		}
	}
	return &UserPermissionsResponse{
		Permissions: entries,
		IsAdmin:     isAdmin,
	}
}

// resolveAuthenticatedUserID resolves the calling user's ID from any of
// the three auth modes admitted by AuthUser routes (admin API key, user
// API key, bearer-token session). Returns a 401 ClientError if no valid
// credential is present. The stateless admin API key returns the
// apiKeyAdminUserID sentinel — callers that need a real user row must
// special-case it (see getCurrentUserPermissions for the {admin, *}
// short-circuit).
func (h *Handler) resolveAuthenticatedUserID(ctx context.Context, req *events.LambdaFunctionURLRequest) (string, error) {
	if principal, ok := principalFromContext(ctx); ok {
		if principal.Kind == PrincipalAdminAPIKey {
			return apiKeyAdminUserID, nil
		}
		if principal.UserID == "" {
			return "", NewClientError(401, "authenticated principal has no user ID")
		}
		return principal.UserID, nil
	}
	return h.resolveAuthenticatedUserIDFromRequest(ctx, req)
}

func (h *Handler) resolveAuthenticatedUserIDFromRequest(ctx context.Context, req *events.LambdaFunctionURLRequest) (string, error) {
	// Admin API key first (stateless, no per-user lookup).
	apiKey := extractAPIKey(req)
	if h.checkAdminAPIKey(apiKey) {
		return apiKeyAdminUserID, nil
	}

	// User API key: resolves to the owning user row.
	if apiKey != "" {
		_, user, err := h.auth.ValidateUserAPIKeyAPI(ctx, apiKey)
		if err == nil {
			if u, ok := user.(*auth.User); ok && u != nil {
				return u.ID, nil
			}
		}
		// fall through to bearer-session if the API key didn't validate.
	}

	// Bearer-token session.
	token := h.extractBearerToken(req)
	if token == "" {
		return "", NewClientError(401, "no authorization token provided")
	}
	session, err := h.auth.ValidateSession(ctx, token)
	if err != nil || session == nil {
		return "", NewClientError(401, "invalid session")
	}
	return session.UserID, nil
}

func (h *Handler) checkAdminExists(ctx context.Context, req *events.LambdaFunctionURLRequest) (*AdminExistsResponse, error) { //nolint:unparam // req is part of the router handler signature
	if h.auth == nil {
		return nil, fmt.Errorf("authentication service not configured")
	}

	exists, err := h.auth.CheckAdminExists(ctx)
	if err != nil {
		return nil, err
	}

	return &AdminExistsResponse{AdminExists: exists}, nil
}

func (h *Handler) setupAdmin(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	if h.auth == nil {
		return nil, fmt.Errorf("authentication service not configured")
	}

	// Rate limiting: 5 attempts per IP per 15 minutes for admin setup
	if err := h.checkRateLimitStrict(ctx, req, "setup_admin"); err != nil {
		return nil, err
	}

	var setupReq SetupAdminRequest
	if err := json.Unmarshal([]byte(req.Body), &setupReq); err != nil {
		return nil, NewClientError(400, "invalid request body")
	}

	// Decode base64-encoded password (the frontend encodes it identically to
	// login/resetPassword; skipping this hashes the base64 text and locks the
	// bootstrap admin out on first login — see issue #224).
	decoded, err := decodeBase64Password(setupReq.Password)
	if err != nil {
		return nil, err
	}
	setupReq.Password = decoded

	response, err := h.auth.SetupAdmin(ctx, setupReq)
	if err != nil {
		// Share the sentinel→ClientError mapping with /api/users (issue #349)
		// so bootstrap failures surface as the right 4xx instead of a 500.
		return nil, mapAuthError(err)
	}

	return response, nil
}
