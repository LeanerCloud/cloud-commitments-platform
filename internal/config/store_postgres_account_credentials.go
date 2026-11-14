package config

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ==========================================
// ACCOUNT CREDENTIALS
// ==========================================

// SaveAccountCredential upserts an encrypted credential blob for an account.
func (s *PostgresStore) SaveAccountCredential(ctx context.Context, accountID, credentialType, encryptedBlob string) error {
	query := `
		INSERT INTO account_credentials (id, account_id, credential_type, encrypted_blob)
		VALUES (uuid_generate_v4(), $1, $2, $3)
		ON CONFLICT (account_id, credential_type) DO UPDATE SET
			encrypted_blob = $3,
			updated_at = NOW()
	`
	_, err := s.db.Exec(ctx, query, accountID, credentialType, encryptedBlob)
	if err != nil {
		return fmt.Errorf("failed to save account credential: %w", err)
	}
	return nil
}

// GetAccountCredential returns the encrypted blob for an account credential.
// A missing row returns ErrNotFound, never ("", nil): that is what an empty
// stored blob returns, and a caller that read an empty result as "no
// credential, use the ambient identity" would act in the wrong account (#120).
func (s *PostgresStore) GetAccountCredential(ctx context.Context, accountID, credentialType string) (string, error) {
	var blob string
	err := s.db.QueryRow(ctx,
		`SELECT encrypted_blob FROM account_credentials WHERE account_id = $1 AND credential_type = $2`,
		accountID, credentialType,
	).Scan(&blob)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("failed to get account credential: %w", err)
	}
	return blob, nil
}

// DeleteAccountCredentials removes all credential records for an account.
func (s *PostgresStore) DeleteAccountCredentials(ctx context.Context, accountID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM account_credentials WHERE account_id = $1`, accountID)
	if err != nil {
		return fmt.Errorf("failed to delete account credentials: %w", err)
	}
	return nil
}

// HasAccountCredentials returns true if at least one credential exists for the account.
func (s *PostgresStore) HasAccountCredentials(ctx context.Context, accountID string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM account_credentials WHERE account_id = $1)`,
		accountID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check account credentials: %w", err)
	}
	return exists, nil
}
