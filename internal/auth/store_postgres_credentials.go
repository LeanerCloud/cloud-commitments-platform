package auth

import (
	"context"
	"fmt"
	"time"
)

// UpdateUserCredentials writes only the email and password columns, and only
// while the row still holds the email and hash the caller read (issue #474).
func (s *PostgresStore) UpdateUserCredentials(ctx context.Context, user *User, readEmail, readPasswordHash string) error {
	result, err := s.db.Exec(ctx, `
		UPDATE users SET email = $4, password_hash = $5, salt = $6,
			password_history = $7, updated_at = NOW()
		WHERE id = $1 AND email = $2 AND password_hash = $3
	`, user.ID, readEmail, readPasswordHash, user.Email, user.PasswordHash, user.Salt, user.PasswordHistory)
	if err != nil {
		return fmt.Errorf("failed to update user credentials: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrUserChanged
	}
	return nil
}

// CompletePasswordReset writes the new password, activates the account and
// consumes the reset token, only while the row still holds the token and hash
// the caller read and the account is not deactivated (issue #493).
func (s *PostgresStore) CompletePasswordReset(ctx context.Context, user *User, readResetToken, readPasswordHash string) error {
	result, err := s.db.Exec(ctx, `
		UPDATE users SET password_hash = $4, salt = $5, password_history = $6, active = $7,
			password_reset_token = NULL, password_reset_expiry = NULL, updated_at = NOW()
		WHERE id = $1 AND password_reset_token = $2 AND password_hash = $3 AND deactivated_at IS NULL
	`, user.ID, readResetToken, readPasswordHash, user.PasswordHash, user.Salt, user.PasswordHistory, user.Active)
	if err != nil {
		return fmt.Errorf("failed to complete password reset: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrUserChanged
	}
	return nil
}

// ConsumePasswordResetToken clears the reset token columns only while the row
// still holds the token the caller read.
func (s *PostgresStore) ConsumePasswordResetToken(ctx context.Context, userID, readResetToken string) error {
	result, err := s.db.Exec(ctx, `
		UPDATE users SET password_reset_token = NULL, password_reset_expiry = NULL, updated_at = NOW()
		WHERE id = $1 AND password_reset_token = $2
	`, userID, readResetToken)
	if err != nil {
		return fmt.Errorf("failed to consume reset token: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrUserChanged
	}
	return nil
}

// SetPasswordResetToken writes only the reset token columns, and only while the
// account is still active under the same email with the reset expiry the caller
// read, so a concurrent deactivation or reset issuance wins.
func (s *PostgresStore) SetPasswordResetToken(ctx context.Context, user *User, readExpiry *time.Time) error {
	result, err := s.db.Exec(ctx, `
		UPDATE users SET password_reset_token = $3, password_reset_expiry = $4, updated_at = NOW()
		WHERE id = $1 AND email = $2 AND deactivated_at IS NULL
			AND password_reset_expiry IS NOT DISTINCT FROM $5
	`, user.ID, user.Email, user.PasswordResetToken, user.PasswordResetExpiry, readExpiry)
	if err != nil {
		return fmt.Errorf("failed to save reset token: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrUserChanged
	}
	return nil
}
