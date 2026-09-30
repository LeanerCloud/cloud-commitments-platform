package auth

import (
	"context"
	"fmt"
)

func (s *PostgresStore) RecordFailedLogin(ctx context.Context, userID string) error {
	result, err := s.db.Exec(ctx, `
		UPDATE users SET
			failed_login_attempts = failed_login_attempts + 1,
			locked_until = CASE WHEN failed_login_attempts + 1 >= $2
				THEN NOW() + make_interval(secs => $3) ELSE locked_until END,
			updated_at = NOW()
		WHERE id = $1
	`, userID, MaxFailedLoginAttempts, AccountLockoutDuration.Seconds())
	if err != nil {
		return fmt.Errorf("failed to record failed login: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("user not found: %s", userID)
	}
	return nil
}

func (s *PostgresStore) RecordSuccessfulLogin(ctx context.Context, userID string) error {
	result, err := s.db.Exec(ctx, `
		UPDATE users SET last_login_at = NOW(), failed_login_attempts = 0,
			locked_until = NULL, updated_at = NOW()
		WHERE id = $1
	`, userID)
	if err != nil {
		return fmt.Errorf("failed to record successful login: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("user not found: %s", userID)
	}
	return nil
}
