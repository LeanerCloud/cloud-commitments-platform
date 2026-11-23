package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/redact"
	"github.com/jackc/pgx/v5"
)

// resetRecentlyIssued reports whether a reset token was issued within
// PasswordResetRateLimit. The caller then returns silently so the existing
// token stays valid: this prevents a griefing attack where an adversary who
// knows the victim's email repeatedly requests resets to perpetually
// invalidate the victim's legitimate link.
//
// The stored token is either a reset token (PasswordResetExpiry) or an invite
// token (PasswordSetupExpiry), and only its expiry is stored. A reset token
// never has more than PasswordResetExpiry left (plus resetClockSkewMargin for
// clock differences between instances), so a longer remaining window
// identifies an invite; deriving an invite's age from the reset lifetime made
// it negative for the whole week, silently dropping every "re-send my setup
// link" request (audit finding A03-009). A remaining window beyond even the
// invite lifetime still yields a negative age, so it stays rate-limited.
func resetRecentlyIssued(user *User, email string) bool {
	if user.PasswordResetExpiry == nil {
		return false
	}
	remaining := time.Until(*user.PasswordResetExpiry)
	lifetime := PasswordResetExpiry
	if remaining > PasswordResetExpiry+resetClockSkewMargin {
		lifetime = PasswordSetupExpiry
	}
	tokenAge := lifetime - remaining
	if tokenAge < PasswordResetRateLimit {
		logging.Debugf("Password reset rate-limited for %s (token age %s < %s)",
			redact.Email(email), tokenAge.Round(time.Second), PasswordResetRateLimit)
		return true
	}
	return false
}

// RequestPasswordReset initiates a password reset.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) error {
	user, err := s.store.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Don't reveal if email exists
			logging.Debugf("Password reset requested for non-existent email: %s", redact.Email(email))
			return nil
		}
		return err
	}
	if user == nil {
		// Don't reveal if email exists
		logging.Debugf("Password reset requested for non-existent email: %s", redact.Email(email))
		return nil
	}

	// A deactivated account gets no reset link (A03-006), answered exactly like
	// an unknown email so the endpoint cannot tell the two apart.
	if user.DeactivatedAt != nil {
		logging.Debugf("Password reset requested for deactivated account: %s", redact.Email(email))
		return nil
	}

	if resetRecentlyIssued(user, email) {
		return nil
	}

	// Generate reset token
	token, err := generateToken()
	if err != nil {
		return fmt.Errorf("failed to generate reset token: %w", err)
	}

	// Hash the token before storing (security best practice)
	tokenHash := hashSessionToken(token)

	// Set expiry based on configured duration
	expiry := time.Now().Add(PasswordResetExpiry)
	readExpiry := user.PasswordResetExpiry
	user.PasswordResetToken = tokenHash
	user.PasswordResetExpiry = &expiry

	if err := s.store.SetPasswordResetToken(ctx, user, readExpiry); err != nil {
		if errors.Is(err, ErrUserChanged) {
			// Deactivated, re-addressed or reset concurrently: answer as for an ineligible account.
			logging.Debugf("Password reset skipped for concurrently changed account: %s", redact.Email(email))
			return nil
		}
		return err
	}

	s.sendPasswordResetEmail(ctx, user.Email, token)
	return nil
}

// sendPasswordResetEmail is best-effort so RequestPasswordReset's response
// never reveals whether an email was sent.
func (s *Service) sendPasswordResetEmail(ctx context.Context, email, token string) {
	// Skip the email entirely if dashboardURL is unconfigured — a broken
	// relative link in an inbox is worse than no email; the operator's
	// startup-time WARN already names the missing env var. Don't return an
	// error to the caller to preserve the email-enumeration protection
	// (RequestPasswordReset must look identical whether or not the email
	// exists, and that includes whether or not a send happened). Issue #355.
	if s.dashboardURL == "" {
		logging.Errorf("RequestPasswordReset: skipping send — DashboardURL empty would produce a broken relative link (set DASHBOARD_URL).")
		return
	}

	// Send reset email (use unhashed token in URL)
	resetURL := fmt.Sprintf("%s/reset-password?token=%s", s.dashboardURL, token)
	if err := s.emailSender.SendPasswordResetEmail(ctx, email, resetURL); err != nil {
		logging.Errorf("Failed to send password reset email: %v", err)
	}
}

// ConfirmPasswordReset completes a password reset.
func (s *Service) ConfirmPasswordReset(ctx context.Context, req PasswordResetConfirm) error {
	user, err := s.validateResetToken(ctx, req.Token)
	if err != nil {
		return err
	}

	readResetToken, readPasswordHash := user.PasswordResetToken, user.PasswordHash

	// An admin-deactivated account must not reactivate itself through a reset
	// (A03-006). The token is still consumed so the link cannot be replayed,
	// and the caller gets the unknown-token error so the response does not
	// reveal that the account is deactivated (issue #595).
	if user.DeactivatedAt != nil {
		if updateErr := s.store.ConsumePasswordResetToken(ctx, user.ID, readResetToken); updateErr != nil {
			logging.Warnf("Failed to invalidate reset token for deactivated user %s: %v", user.ID, updateErr)
		}
		return ErrInvalidResetToken
	}

	if err := s.processPasswordReset(user, req.NewPassword); err != nil {
		// Token is consumed even on validation failure (one-time use)
		if updateErr := s.store.ConsumePasswordResetToken(ctx, user.ID, readResetToken); updateErr != nil {
			logging.Warnf("Failed to invalidate reset token after password validation failure: %v", updateErr)
		}
		return err
	}

	// Activate user on first password set (invite flow). Deactivated accounts
	// returned above, so !Active here means "never activated".
	if !user.Active {
		user.Active = true
	}

	if err := s.store.CompletePasswordReset(ctx, user, readResetToken, readPasswordHash); err != nil {
		return err
	}

	// See the matching comment in ChangePassword: invalidate only after the
	// new password is persisted, or a failed write leaves the caller
	// with revoked credentials for a password that never actually changed.
	s.invalidateUserCredentialsBestEffort(ctx, user.ID, "password reset")
	s.notifyPasswordChange(ctx, user.ID, req.NewPassword)
	return nil
}
