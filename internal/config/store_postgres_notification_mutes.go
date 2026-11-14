package config

import (
	"context"
	"fmt"
)

// ==========================================
// NOTIFICATION MUTES (issue #297)
// ==========================================

// UpsertNotificationMute inserts or replaces the mute row for (recipientEmail,
// scope). ON CONFLICT updates muted_at and unmute_token so a repeated
// one-click opt-out resets the audit timestamp without error.
func (s *PostgresStore) UpsertNotificationMute(ctx context.Context, recipientEmail, scope, unmuteToken string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO muted_recipients (recipient_email, scope, muted_at, unmute_token)
		VALUES (LOWER($1), $2, NOW(), $3)
		ON CONFLICT (recipient_email, scope)
		DO UPDATE SET muted_at = NOW(), unmute_token = EXCLUDED.unmute_token
	`, recipientEmail, scope, unmuteToken)
	if err != nil {
		return fmt.Errorf("upsert notification mute: %w", err)
	}
	return nil
}

// IsNotificationMuted returns true when (email, scope) has a matching row
// in muted_recipients. The email lookup is case-insensitive (LOWER on insert
// plus LOWER($1) here).
func (s *PostgresStore) IsNotificationMuted(ctx context.Context, recipientEmail, scope string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM muted_recipients
			WHERE recipient_email = LOWER($1) AND scope = $2
		)
	`, recipientEmail, scope).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check notification mute: %w", err)
	}
	return exists, nil
}
