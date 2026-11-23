package api

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/aws/aws-lambda-go/events"
)

// Local aliases for the auth-package MFA sentinels so test files in
// this package can reference them without re-importing the auth
// package. The login handler maps these via errors.Is() to the
// machine-readable response codes "mfa_required" / "invalid_mfa_code".
var (
	mfaRequiredSentinel = auth.ErrMFARequired
	mfaInvalidSentinel  = auth.ErrInvalidMFACode
)

// mapMFAServiceError maps a service-layer MFA error to the right
// client-facing status + message. The auth package exports typed
// sentinel errors for each user-correctable condition; we match via
// errors.Is so a renamed error message in the service never silently
// drifts the HTTP status. See issue #512.
func mapMFAServiceError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, auth.ErrMFAInvalidPassword),
		errors.Is(err, auth.ErrMFAInvalidCode),
		errors.Is(err, auth.ErrMFACodeRequired),
		errors.Is(err, auth.ErrMFANoEnrollmentInProgress),
		errors.Is(err, auth.ErrMFAEnrollmentExpired),
		errors.Is(err, auth.ErrMFANotEnabled):
		return NewClientError(400, err.Error())
	case errors.Is(err, auth.ErrMFAAlreadyEnabled):
		return NewClientError(409, auth.ErrMFAAlreadyEnabled.Error())
	case errors.Is(err, auth.ErrUserChanged):
		return NewClientError(409, auth.ErrUserChanged.Error())
	case errors.Is(err, auth.ErrMFAAuthFailed):
		// Opaque 401 to prevent user enumeration: the service returns this
		// for both "user not found" and "DB lookup failed" paths so callers
		// cannot distinguish the two.
		return NewClientError(401, err.Error())
	}
	return err
}

// mfaSetup handles POST /api/auth/mfa/setup. Begins a fresh MFA
// enrollment for the current user. Requires the current password in
// the body (base64-encoded, same convention as login). Returns the
// generated secret + otpauth provisioning URI for QR display.
func (h *Handler) mfaSetup(ctx context.Context, req *events.LambdaFunctionURLRequest) (*MFASetupResponse, error) {
	session, err := h.requireMFASession(ctx, req)
	if err != nil {
		return nil, err
	}
	var body MFASetupRequest
	err = json.Unmarshal([]byte(req.Body), &body)
	if err != nil {
		return nil, NewClientError(400, "invalid request body")
	}
	password, err := decodeBase64Password(body.Password)
	if err != nil {
		return nil, err
	}
	secret, uri, err := h.auth.MFASetupAPI(ctx, session.UserID, password)
	if err != nil {
		return nil, mapMFAServiceError(err)
	}
	return &MFASetupResponse{Secret: secret, ProvisioningURI: uri}, nil
}

// mfaEnable handles POST /api/auth/mfa/enable. Validates the supplied
// TOTP code against the pending secret + flips MFAEnabled=true on
// success. Returns the plaintext recovery codes once.
func (h *Handler) mfaEnable(ctx context.Context, req *events.LambdaFunctionURLRequest) (*MFAEnableResponse, error) {
	session, err := h.requireMFASession(ctx, req)
	if err != nil {
		return nil, err
	}
	var body MFAEnableRequest
	err = json.Unmarshal([]byte(req.Body), &body)
	if err != nil {
		return nil, NewClientError(400, "invalid request body")
	}
	codes, err := h.auth.MFAEnableAPI(ctx, session.UserID, body.Code)
	if err != nil {
		return nil, mapMFAServiceError(err)
	}
	return &MFAEnableResponse{RecoveryCodes: codes}, nil
}

// mfaDisable handles POST /api/auth/mfa/disable. Requires both the
// current password AND a fresh proof-of-possession (TOTP or recovery
// code). On success: clears the secret + recovery codes, flips
// MFAEnabled=false.
func (h *Handler) mfaDisable(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requireMFASession(ctx, req)
	if err != nil {
		return nil, err
	}
	var body MFADisableRequest
	err = json.Unmarshal([]byte(req.Body), &body)
	if err != nil {
		return nil, NewClientError(400, "invalid request body")
	}
	password, err := decodeBase64Password(body.Password)
	if err != nil {
		return nil, err
	}
	err = h.auth.MFADisableAPI(ctx, session.UserID, password, body.Code)
	if err != nil {
		return nil, mapMFAServiceError(err)
	}
	return &StatusResponse{Status: "mfa disabled"}, nil
}

// mfaRegenerateRecoveryCodes handles POST
// /api/auth/mfa/regenerate-recovery-codes. Replaces all stored
// recovery codes with a fresh batch. Requires a current TOTP code
// (NOT a recovery code).
func (h *Handler) mfaRegenerateRecoveryCodes(ctx context.Context, req *events.LambdaFunctionURLRequest) (*MFARegenerateResponse, error) {
	session, err := h.requireMFASession(ctx, req)
	if err != nil {
		return nil, err
	}
	var body MFARegenerateRequest
	err = json.Unmarshal([]byte(req.Body), &body)
	if err != nil {
		return nil, NewClientError(400, "invalid request body")
	}
	codes, err := h.auth.MFARegenerateRecoveryCodesAPI(ctx, session.UserID, body.Code)
	if err != nil {
		return nil, mapMFAServiceError(err)
	}
	return &MFARegenerateResponse{RecoveryCodes: codes}, nil
}
