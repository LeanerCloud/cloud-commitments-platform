// Command audit-gcp-credentials is a read-only check of stored GCP credential
// configurations. It decrypts every gcp_workload_identity_config and
// gcp_service_account row in account_credentials and every pending
// registration with a GCP credential type, validates each, and prints one
// tab-separated line per row: source, id, credential type, category
// (ok, unsupported-token-endpoint, unsupported-credential-source,
// unsupported-key, invalid-json, decrypt-failed, other). Payloads are never
// printed.
//
// Usage (same environment as the server and cmd/rekey):
//
//	audit-gcp-credentials [-timeout 5m]
//
// Queries run in a read-only transaction; nothing is written.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/secrets"
)

const (
	accountsQuery = `SELECT account_id::text, credential_type, encrypted_blob FROM account_credentials
		WHERE credential_type IN ('gcp_workload_identity_config', 'gcp_service_account') ORDER BY account_id`
	registrationsQuery = `SELECT id::text, reg_credential_type, COALESCE(reg_credential_payload, '') FROM account_registrations
		WHERE status = 'pending' AND reg_credential_type IN ('gcp_workload_identity_config', 'gcp_service_account')
		AND COALESCE(reg_credential_payload, '') <> '' ORDER BY id`
)

func main() {
	timeout := flag.Duration("timeout", 5*time.Minute, "overall timeout")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := run(ctx); err != nil {
		log.Fatalf("audit-gcp-credentials: %v", err) //nolint:gocritic // exitAfterDefer: intentional fatal
	}
}

func run(ctx context.Context) error {
	resolver, err := secrets.NewResolver(ctx, secrets.LoadConfigFromEnv())
	if err != nil {
		return fmt.Errorf("build resolver: %w", err)
	}
	defer func() {
		if cerr := resolver.Close(); cerr != nil {
			log.Printf("audit-gcp-credentials: warning: resolver close: %v", cerr)
		}
	}()
	key, _, err := credentials.LoadKey(ctx, resolver)
	if err != nil {
		return fmt.Errorf("load key: %w", err)
	}

	dbConfig, err := database.LoadFromEnv()
	if err != nil {
		return fmt.Errorf("load db config: %w", err)
	}
	var sr database.SecretResolver
	if dbConfig.PasswordSecret != "" {
		sr = resolver
	}
	db, err := database.NewConnection(ctx, dbConfig, sr)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin read-only transaction: %w", err)
	}
	defer rollbackReadOnly(ctx, tx)

	if err := audit(ctx, tx, "account", accountsQuery, key); err != nil {
		return err
	}
	return audit(ctx, tx, "registration", registrationsQuery, key)
}

func audit(ctx context.Context, tx pgx.Tx, source, query string, key []byte) error {
	rows, err := tx.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("query %s: %w", source, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, credType, blob string
		if err := rows.Scan(&id, &credType, &blob); err != nil {
			return fmt.Errorf("scan %s: %w", source, err)
		}
		category := credentials.GCPAuditDecryptFailed
		if plain, derr := credentials.Decrypt(key, blob); derr == nil {
			category = credentials.ClassifyGCPCredential(credType, plain)
		}
		fmt.Println(credentials.FormatGCPAuditLine(source, id, credType, category))
	}
	return rows.Err()
}

// rollbackReadOnly ends the read-only transaction; there is nothing to commit.
func rollbackReadOnly(ctx context.Context, tx pgx.Tx) {
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		log.Printf("audit-gcp-credentials: warning: rollback: %v", err)
	}
}
