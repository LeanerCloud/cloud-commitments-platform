package server

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/api"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
)

// authServiceAdapter adapts auth.Service to api.AuthServiceInterface.
type authServiceAdapter struct {
	service *auth.Service
}

func newAuthServiceAdapter(service *auth.Service) *authServiceAdapter {
	return &authServiceAdapter{service: service}
}

func (a *authServiceAdapter) Login(ctx context.Context, req api.LoginRequest) (*api.LoginResponse, error) {
	authReq := auth.LoginRequest{
		Email:    req.Email,
		Password: req.Password,
		MFACode:  req.MFACode,
		MFAToken: req.MFAToken,
	}
	resp, err := a.service.Login(ctx, authReq)
	if err != nil {
		return nil, err
	}
	return &api.LoginResponse{
		Token:     resp.Token,
		ExpiresAt: resp.ExpiresAt.Format(time.RFC3339),
		User: &api.UserInfo{
			ID:         resp.User.ID,
			Email:      resp.User.Email,
			Groups:     resp.User.Groups,
			MFAEnabled: resp.User.MFAEnabled,
		},
		CSRFToken: resp.CSRFToken,
	}, nil
}

func (a *authServiceAdapter) Logout(ctx context.Context, token string) error {
	return a.service.Logout(ctx, token)
}

func (a *authServiceAdapter) ValidateSession(ctx context.Context, token string) (*api.Session, error) {
	sess, err := a.service.ValidateSession(ctx, token)
	if err != nil {
		return nil, err
	}
	return &api.Session{
		UserID: sess.UserID,
		Email:  sess.Email,
	}, nil
}

func (a *authServiceAdapter) SetupAdmin(ctx context.Context, req api.SetupAdminRequest) (*api.LoginResponse, error) {
	authReq := auth.SetupAdminRequest{
		Email:    req.Email,
		Password: req.Password,
	}
	resp, err := a.service.SetupAdmin(ctx, authReq)
	if err != nil {
		return nil, err
	}
	return &api.LoginResponse{
		Token:     resp.Token,
		ExpiresAt: resp.ExpiresAt.Format(time.RFC3339),
		User: &api.UserInfo{
			ID:         resp.User.ID,
			Email:      resp.User.Email,
			Groups:     resp.User.Groups,
			MFAEnabled: resp.User.MFAEnabled,
		},
		CSRFToken: resp.CSRFToken,
	}, nil
}

func (a *authServiceAdapter) CheckAdminExists(ctx context.Context) (bool, error) {
	return a.service.CheckAdminExists(ctx)
}

func (a *authServiceAdapter) RequestPasswordReset(ctx context.Context, emailAddr string) error {
	return a.service.RequestPasswordReset(ctx, emailAddr)
}

func (a *authServiceAdapter) ConfirmPasswordReset(ctx context.Context, req api.PasswordResetConfirm) error {
	authReq := auth.PasswordResetConfirm{
		Token:       req.Token,
		NewPassword: req.NewPassword,
	}
	return a.service.ConfirmPasswordReset(ctx, authReq)
}

func (a *authServiceAdapter) ResetTokenStatus(ctx context.Context, token string) (string, string, error) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	state, flow, err := a.service.ResetTokenStatus(ctx, token)
	return string(state), string(flow), err
}

func (a *authServiceAdapter) GetUser(ctx context.Context, userID string) (*api.User, error) {
	user, err := a.service.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, auth.ErrUserNotFound
	}
	return &api.User{
		ID:         user.ID,
		Email:      user.Email,
		Groups:     user.GroupIDs,
		MFAEnabled: user.MFAEnabled,
	}, nil
}

func (a *authServiceAdapter) UpdateUserProfile(ctx context.Context, userID, emailAddr, currentPassword, newPassword string) error {
	return a.service.UpdateUserProfile(ctx, userID, emailAddr, currentPassword, newPassword)
}

// User management methods - delegate to auth service API methods.
func (a *authServiceAdapter) CreateUserAPI(ctx context.Context, actorUserID string, req auth.APICreateUserRequest) (*auth.APICreateUserResponse, error) {
	return a.service.CreateUserAPI(ctx, actorUserID, req)
}

func (a *authServiceAdapter) UpdateUserAPI(ctx context.Context, actorUserID, userID string, req auth.APIUpdateUserRequest) (*auth.APIUser, error) {
	return a.service.UpdateUserAPI(ctx, actorUserID, userID, req)
}

func (a *authServiceAdapter) DeleteUser(ctx context.Context, userID string) error {
	return a.service.DeleteUser(ctx, userID)
}

func (a *authServiceAdapter) ListUsersAPI(ctx context.Context) (any, error) {
	return a.service.ListUsersAPI(ctx)
}

func (a *authServiceAdapter) ChangePasswordAPI(ctx context.Context, userID, currentPassword, newPassword string) error {
	return a.service.ChangePasswordAPI(ctx, userID, currentPassword, newPassword)
}

// MFA lifecycle (issue #497).
func (a *authServiceAdapter) MFASetupAPI(ctx context.Context, userID, password string) (string, string, error) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	return a.service.MFASetupAPI(ctx, userID, password)
}

func (a *authServiceAdapter) MFAEnableAPI(ctx context.Context, userID, code string) ([]string, error) {
	return a.service.MFAEnableAPI(ctx, userID, code)
}

func (a *authServiceAdapter) MFADisableAPI(ctx context.Context, userID, password, codeOrRecovery string) error {
	return a.service.MFADisableAPI(ctx, userID, password, codeOrRecovery)
}

func (a *authServiceAdapter) MFARegenerateRecoveryCodesAPI(ctx context.Context, userID, code string) ([]string, error) {
	return a.service.MFARegenerateRecoveryCodesAPI(ctx, userID, code)
}

// Group management methods - delegate to auth service API methods.
func (a *authServiceAdapter) CreateGroupAPI(ctx context.Context, actorUserID string, req auth.APICreateGroupRequest) (*auth.APIGroup, error) {
	return a.service.CreateGroupAPI(ctx, actorUserID, req)
}

func (a *authServiceAdapter) UpdateGroupAPI(ctx context.Context, actorUserID, groupID string, req auth.APIUpdateGroupRequest) (*auth.APIGroup, error) {
	return a.service.UpdateGroupAPI(ctx, actorUserID, groupID, req)
}

func (a *authServiceAdapter) DeleteGroup(ctx context.Context, groupID string) error {
	return a.service.DeleteGroup(ctx, groupID)
}

func (a *authServiceAdapter) GetGroupAPI(ctx context.Context, groupID string) (any, error) {
	return a.service.GetGroupAPI(ctx, groupID)
}

func (a *authServiceAdapter) ListGroupsAPI(ctx context.Context) (any, error) {
	return a.service.ListGroupsAPI(ctx)
}

// Permission checking.
func (a *authServiceAdapter) HasPermissionAPI(ctx context.Context, userID, action, resource string) (bool, error) {
	return a.service.HasPermissionAPI(ctx, userID, action, resource)
}

func (a *authServiceAdapter) HasPermissionForConstraintsAPI(ctx context.Context, userID, action, resource string, constraintSets []auth.PermissionConstraints) (bool, error) {
	return a.service.HasPermissionForConstraintsAPI(ctx, userID, action, resource, constraintSets)
}

func (a *authServiceAdapter) GetUserPermissionsAPI(ctx context.Context, userID string) (any, error) {
	return a.service.GetUserPermissionsAPI(ctx, userID)
}

func (a *authServiceAdapter) GetAPIKeyEffectivePermissionsAPI(ctx context.Context, keyID, userID string) ([]auth.APIPermission, error) {
	return a.service.GetAPIKeyEffectivePermissionsAPI(ctx, keyID, userID)
}

// Account access.
func (a *authServiceAdapter) GetAllowedAccountsAPI(ctx context.Context, userID string) ([]string, error) {
	// ResolveAllowedAccounts rather than BuildAuthContext: it fails closed when
	// the scope cannot be established, instead of returning the empty list that
	// IsUnrestrictedAccess reads as "all accounts" (issue #1748).
	return a.service.ResolveAllowedAccounts(ctx, userID)
}

// CSRFTokenForSession re-derives the CSRF token of a live session (GET /api/auth/me).
func (a *authServiceAdapter) CSRFTokenForSession(ctx context.Context, sessionToken string) (string, error) {
	return a.service.CSRFTokenForSession(ctx, sessionToken)
}

// CSRF validation.
func (a *authServiceAdapter) ValidateCSRFToken(ctx context.Context, sessionToken, csrfToken string) error {
	return a.service.ValidateCSRFToken(ctx, sessionToken, csrfToken)
}

// API Key management.
func (a *authServiceAdapter) CreateAPIKeyAPI(ctx context.Context, userID string, req auth.APICreateAPIKeyRequest) (any, error) {
	return a.service.CreateAPIKeyAPI(ctx, userID, req)
}

func (a *authServiceAdapter) ListUserAPIKeysAPI(ctx context.Context, userID string) (any, error) {
	return a.service.ListUserAPIKeysAPI(ctx, userID)
}

func (a *authServiceAdapter) GetAPIKeysUsageStatsAPI(ctx context.Context, userID string) (any, error) {
	return a.service.GetAPIKeysUsageStatsAPI(ctx, userID)
}

func (a *authServiceAdapter) DeleteAPIKeyAPI(ctx context.Context, userID, keyID string) error {
	return a.service.DeleteAPIKeyAPI(ctx, userID, keyID)
}

func (a *authServiceAdapter) RevokeAPIKeyAPI(ctx context.Context, userID, keyID string) error {
	return a.service.RevokeAPIKeyAPI(ctx, userID, keyID)
}

func (a *authServiceAdapter) ValidateUserAPIKeyAPI(ctx context.Context, apiKey string) (any, any, error) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	return a.service.ValidateUserAPIKeyAPI(ctx, apiKey)
}

func (a *authServiceAdapter) RecordAPIKeyUsageAsync(keyID string) {
	a.service.RecordUsageAsync(keyID)
}

func (a *authServiceAdapter) HasAPIKeyPermissionAPI(ctx context.Context, apiKey, action, resource string) (userID, keyID string, allowed bool, err error) {
	userID, keyID, allowed, err = a.service.HasAPIKeyPermissionAPI(ctx, apiKey, action, resource)
	return
}

func (a *authServiceAdapter) HasAPIKeyPermissionForConstraintsAPI(ctx context.Context, keyID, userID, action, resource string, constraintSets []auth.PermissionConstraints) (bool, error) {
	return a.service.HasAPIKeyPermissionForConstraintsAPI(ctx, keyID, userID, action, resource, constraintSets)
}
