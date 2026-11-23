package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ResetTokenState describes the runtime state of a password-reset token.
// One of "valid", "expired", "used". "used" doubles as the fallback for
// tokens that never existed: the row is wiped on consumption (one-time
// use), so the store cannot reliably distinguish "consumed" from
// "never issued". Surfacing both as "used" matches the dominant
// real-world case (stale link from an old email) and lets the frontend
// branch on a single state.
type ResetTokenState string

const (
	// ResetTokenStateValid means the token matches an issued, unexpired row.
	ResetTokenStateValid ResetTokenState = "valid"
	// ResetTokenStateExpired means the token matches but its expiry has passed.
	ResetTokenStateExpired ResetTokenState = "expired"
	// ResetTokenStateUsed covers consumed, never-issued, and
	// deactivated-account tokens.
	ResetTokenStateUsed ResetTokenState = "used"
)

// ResetTokenFlow describes whether the matched token belongs to an
// invite flow (user has never been activated) or a normal password-reset
// flow. The frontend uses this to swap "Set your password" vs "Reset your
// password" wording (issue #461).
type ResetTokenFlow string

const (
	// ResetTokenFlowReset is the flow for active and admin-deactivated users.
	ResetTokenFlowReset ResetTokenFlow = "reset"
	// ResetTokenFlowInvite is the bootstrap flow for an account that has
	// never been activated (Active false, DeactivatedAt nil).
	ResetTokenFlowInvite ResetTokenFlow = "invite"
)

// resetTokenFlowFor reports "invite" only for never-activated accounts; a
// deactivated account is also !Active but must not get invite wording.
func resetTokenFlowFor(user *User) ResetTokenFlow {
	if !user.Active && user.DeactivatedAt == nil {
		return ResetTokenFlowInvite
	}
	return ResetTokenFlowReset
}

// ResetTokenStatus returns the state of a reset token without consuming
// it. The frontend calls this before rendering the reset-password form
// so it can show an "expired" or "already used" view instead of a form
// the user can never submit (issues #460, #461). For "used" / never-
// issued, flow defaults to "reset" since there is no user to inspect.
func (s *Service) ResetTokenStatus(ctx context.Context, token string) (ResetTokenState, ResetTokenFlow, error) {
	if token == "" {
		return ResetTokenStateUsed, ResetTokenFlowReset, nil
	}

	tokenHash := hashSessionToken(token)
	user, err := s.store.GetUserByResetToken(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ResetTokenStateUsed, ResetTokenFlowReset, nil
		}
		return "", "", err
	}
	if user == nil {
		return ResetTokenStateUsed, ResetTokenFlowReset, nil
	}

	// Store-integrity check, not a timing defense (issue #313): the row was
	// selected by `WHERE password_reset_token = $1` on tokenHash, so this only
	// fails if the store returned a row for a different token. See the note
	// in ValidateSession on why the lookup's equality timing is harmless.
	if subtle.ConstantTimeCompare([]byte(user.PasswordResetToken), []byte(tokenHash)) != 1 {
		return ResetTokenStateUsed, ResetTokenFlowReset, nil
	}

	if user.PasswordResetExpiry == nil || time.Now().After(*user.PasswordResetExpiry) {
		return ResetTokenStateExpired, resetTokenFlowFor(user), nil
	}

	// ConfirmPasswordReset refuses admin-deactivated accounts, so report the
	// token as used (terminal view, no form that can never succeed). Using
	// "used" keeps the endpoint from revealing that the account is deactivated.
	if user.DeactivatedAt != nil {
		return ResetTokenStateUsed, ResetTokenFlowReset, nil
	}

	return ResetTokenStateValid, resetTokenFlowFor(user), nil
}

func (s *Service) validateResetToken(ctx context.Context, token string) (*User, error) {
	tokenHash := hashSessionToken(token)

	user, err := s.store.GetUserByResetToken(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidResetToken
		}
		return nil, err
	}
	if user == nil {
		return nil, ErrInvalidResetToken
	}

	// Store-integrity check, not a timing defense (issue #313): the row was
	// selected by `WHERE password_reset_token = $1` on tokenHash, so this only
	// fails if the store returned a row for a different token. See the note
	// in ValidateSession on why the lookup's equality timing is harmless.
	if subtle.ConstantTimeCompare([]byte(user.PasswordResetToken), []byte(tokenHash)) != 1 {
		return nil, ErrInvalidResetToken
	}

	if user.PasswordResetExpiry == nil || time.Now().After(*user.PasswordResetExpiry) {
		return nil, fmt.Errorf("reset token has expired")
	}

	return user, nil
}

func (s *Service) processPasswordReset(user *User, newPassword string) error {
	if err := s.validatePassword(newPassword); err != nil {
		return err
	}

	if err := s.checkPasswordHistory(newPassword, user.PasswordHash, user.PasswordHistory); err != nil {
		return err
	}

	passwordHash, err := s.hashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	if user.PasswordHash != "" {
		user.PasswordHistory = addToPasswordHistory(user.PasswordHash, user.PasswordHistory)
	}

	user.PasswordHash = passwordHash
	return nil
}
