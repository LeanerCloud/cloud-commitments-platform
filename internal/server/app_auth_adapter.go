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
