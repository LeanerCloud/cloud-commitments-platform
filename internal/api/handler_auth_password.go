package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/redact"
	"github.com/aws/aws-lambda-go/events"
)

func (h *Handler) forgotPassword(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	if h.auth == nil {
		return nil, fmt.Errorf("authentication service not configured")
	}

	// Per-IP cap: 10 attempts per IP per 15 minutes, checked before the body
	// is parsed so malformed requests spend the budget too. Without it one
	// client could walk any number of addresses, one request each, past the
	// per-email bucket below (#13). The client IP is the request's SourceIP,
	// the same key every other per-IP bucket uses. Fail-open like the
	// per-email bucket (02-M1); the 429 depends only on this caller's own
	// request count, never on whether an account exists.
	if err := h.checkRateLimit(ctx, req, "forgot_password_ip"); err != nil {
		return nil, err
	}

	var pwdReq PasswordResetRequest
	if err := json.Unmarshal([]byte(req.Body), &pwdReq); err != nil {
		return nil, NewClientError(400, "invalid request body")
	}

	// Per-email cap: 10 attempts per email per 5 minutes to prevent enumeration attacks.
	// On rate-limiter error: emit a high-severity alert (02-M1) but continue with the
	// request. Failing closed here would reveal infrastructure state (503 vs 200) to
	// an enumerating attacker and harm legitimate users more than the enumeration risk.
	if h.rateLimiter != nil {
		allowed, err := h.rateLimiter.AllowWithEmail(ctx, strings.ToLower(strings.TrimSpace(pwdReq.Email)), "forgot_password")
		if err != nil {
			logging.Errorf("ALERT: rate limiter error on forgot_password for %s; proceeding fail-open (02-M1): %v",
				redact.Email(pwdReq.Email), err)
		} else if !allowed {
			logging.Warnf("Rate limit exceeded for forgot password: %s", redact.Email(pwdReq.Email))
			// Always return success message to prevent email enumeration
			return map[string]string{"status": "if the email exists, a reset link has been sent"}, nil
		}
	}

	// Always return success to prevent email enumeration
	if err := h.auth.RequestPasswordReset(ctx, pwdReq.Email); err != nil {
		logging.Warnf("Password reset request error: %v", err)
	}

	return map[string]string{"status": "if the email exists, a reset link has been sent"}, nil
}

// isResetPasswordClientError reports whether err originates from a user-correctable
// condition during password reset. The substring list is intentionally narrow and
// is matched against the error messages produced by internal/auth/service_password.go.
// When adding a new client-correctable error in the service, add its substring here too.
func isResetPasswordClientError(err error) bool {
	s := err.Error()
	return strings.Contains(s, "current password") ||
		strings.Contains(s, "used recently") ||
		strings.Contains(s, "invalid or expired reset token") ||
		strings.Contains(s, "reset token has expired") ||
		strings.Contains(s, "password must")
}

// resetPasswordStatus returns the runtime state of a reset token without
// consuming it. The frontend hits this before rendering the reset-
// password form so it can show an "expired" or "already used" view
// instead of a form that can never submit (issues #460, #461).
//
// Response shape is uniform: 200 with {state, flow} for every state,
// including "used" (covers both consumed and never-issued tokens; the
// row is wiped on consumption, so the store can't distinguish them).
// A non-200 means an infrastructure failure, not a token-state signal.
func (h *Handler) resetPasswordStatus(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	if h.auth == nil {
		return nil, fmt.Errorf("authentication service not configured")
	}

	// Same rate-limit bucket as the submit endpoint: a token-probing
	// attacker would otherwise get a free oracle to test tokens.
	if err := h.checkRateLimitStrict(ctx, req, "reset_password"); err != nil {
		return nil, err
	}

	token := ""
	if req != nil {
		token = req.QueryStringParameters["token"]
	}
	if token == "" {
		return nil, NewClientError(400, "token is required")
	}

	state, flow, err := h.auth.ResetTokenStatus(ctx, token)
	if err != nil {
		return nil, err
	}

	return map[string]string{
		"state": state,
		"flow":  flow,
	}, nil
}

func (h *Handler) resetPassword(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	if h.auth == nil {
		return nil, fmt.Errorf("authentication service not configured")
	}

	// Rate limiting: 10 attempts per IP per 15 minutes
	if err := h.checkRateLimitStrict(ctx, req, "reset_password"); err != nil {
		return nil, err
	}

	var pwdResetReq PasswordResetConfirm
	if err := json.Unmarshal([]byte(req.Body), &pwdResetReq); err != nil {
		return nil, NewClientError(400, "invalid request body")
	}

	// The frontend base64-encodes new_password (see frontend/src/api/auth.ts:
	// resetPassword) — same pattern as login / change-password / update-profile.
	// Decode it back to plaintext before handing to the service or the bcrypt
	// hash stored represents the base64 string, leaving the user unable to log
	// in with the password they thought they set. See issue #356.
	decoded, err := decodeBase64Password(pwdResetReq.NewPassword)
	if err != nil {
		return nil, err
	}
	pwdResetReq.NewPassword = decoded

	if err := h.auth.ConfirmPasswordReset(ctx, pwdResetReq); err != nil {
		if errors.Is(err, auth.ErrUserChanged) {
			return nil, NewClientError(409, err.Error())
		}
		if isResetPasswordClientError(err) {
			return nil, NewClientError(400, err.Error())
		}
		return nil, err
	}

	return map[string]string{"status": "password reset successful"}, nil
}
