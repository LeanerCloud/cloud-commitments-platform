package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/aws/aws-lambda-go/events"
)

func (h *Handler) updateProfile(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	if h.auth == nil {
		return nil, fmt.Errorf("authentication service not configured")
	}

	session, err := h.requireSessionPrincipal(ctx, req)
	if err != nil {
		return nil, err
	}

	// Parse request body
	var profileReq ProfileUpdateRequest
	err = json.Unmarshal([]byte(req.Body), &profileReq)
	if err != nil {
		return nil, NewClientError(400, "invalid request body")
	}

	// Validate email format before decoding passwords (cheap check first).
	err = validateEmailFormat(profileReq.Email)
	if err != nil {
		return nil, err
	}

	// Decode base64-encoded passwords if provided
	currentPassword, newPassword, err := decodeProfilePasswords(profileReq)
	if err != nil {
		return nil, err
	}

	// Update profile through auth service
	err = h.auth.UpdateUserProfile(ctx, session.UserID, profileReq.Email, currentPassword, newPassword)
	if err != nil {
		return nil, mapProfileUpdateError(err)
	}

	return map[string]string{"status": "profile updated"}, nil
}

// mapProfileUpdateError converts profile-update service errors to the correct
// HTTP status code and a user-facing message.
//
//   - ErrCurrentPasswordIncorrect -> 401 with a precise message (the acting
//     user is verifying their own credential, so specificity is safe).
//   - ErrEmailInUse -> 409 with a neutral message that does NOT confirm whether
//     another account holds the address; prevents account enumeration via the
//     profile-update path (issue #929).
//   - ErrUserChanged -> 409: the account changed after it was read (issue #474).
//   - ErrInvalidEmail -> 400: the address is not a bare addr-spec (issue #104).
//   - All other errors pass through unchanged for handleRequestError to render
//     as 500.
func mapProfileUpdateError(err error) error {
	switch {
	case errors.Is(err, auth.ErrCurrentPasswordIncorrect):
		return NewClientError(401, err.Error())
	case errors.Is(err, auth.ErrEmailInUse):
		return NewClientError(409, "Unable to update email")
	case errors.Is(err, auth.ErrUserChanged):
		return NewClientError(409, auth.ErrUserChanged.Error())
	case errors.Is(err, auth.ErrInvalidEmail):
		return NewClientError(400, auth.ErrInvalidEmail.Error())
	}
	return err
}

// decodeProfilePasswords decodes the optional base64-encoded current and new
// passwords from a ProfileUpdateRequest. Pulled out of updateProfile to keep
// that function under the cyclomatic limit.
func decodeProfilePasswords(req ProfileUpdateRequest) (current, next string, err error) {
	if req.CurrentPassword != "" {
		current, err = decodeBase64Password(req.CurrentPassword)
		if err != nil {
			return "", "", err
		}
	}
	if req.NewPassword != "" {
		next, err = decodeBase64Password(req.NewPassword)
		if err != nil {
			return "", "", err
		}
	}
	return current, next, nil
}

// decodeChangePasswordRequest validates and decodes both passwords from a ChangePasswordRequest.
func decodeChangePasswordRequest(pwdReq ChangePasswordRequest) (current, next string, err error) {
	if pwdReq.CurrentPassword == "" || pwdReq.NewPassword == "" {
		return "", "", NewClientError(400, "current password and new password are required")
	}
	current, err = decodeBase64Password(pwdReq.CurrentPassword)
	if err != nil {
		return "", "", err
	}
	next, err = decodeBase64Password(pwdReq.NewPassword)
	if err != nil {
		return "", "", err
	}
	return current, next, nil
}

// changePassword handles POST /api/auth/change-password.
func (h *Handler) changePassword(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	if h.auth == nil {
		return nil, fmt.Errorf("authentication service not configured")
	}

	// Rate limiting: 10 attempts per IP per 15 minutes
	if err := h.checkRateLimitStrict(ctx, req, "change_password"); err != nil {
		return nil, err
	}

	session, err := h.requireSessionPrincipal(ctx, req)
	if err != nil {
		return nil, err
	}

	var pwdReq ChangePasswordRequest
	err = json.Unmarshal([]byte(req.Body), &pwdReq)
	if err != nil {
		return nil, NewClientError(400, "invalid request body")
	}

	currentPassword, newPassword, err := decodeChangePasswordRequest(pwdReq)
	if err != nil {
		return nil, err
	}

	err = h.auth.ChangePasswordAPI(ctx, session.UserID, currentPassword, newPassword)
	if errors.Is(err, auth.ErrUserChanged) {
		return nil, NewClientError(409, err.Error())
	}
	if err != nil {
		return nil, err
	}

	return map[string]string{"status": "password changed"}, nil
}
