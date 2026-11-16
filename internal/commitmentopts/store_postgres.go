package commitmentopts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// dbConn is the minimal pgx-style surface PostgresStore uses. Both
// *database.Connection and the pgxmock mocks satisfy it. Mirrors the
// interface shape in internal/config/store_postgres.go so the two stores
// can share test-container scaffolding.
type dbConn interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// PostgresStore is the Postgres-backed commitmentopts.Store implementation.
type PostgresStore struct {
	db dbConn
}

// NewPostgresStore returns a store backed by the given connection. The
// connection must already have the 000176 migration applied.
func NewPostgresStore(db *database.Connection) *PostgresStore {
	return &PostgresStore{db: db}
}

// Verify PostgresStore implements Store.
var _ Store = (*PostgresStore)(nil)

// Get returns the persisted combos grouped by provider and service, limited
// to providers that have a probe run row, plus a boolean that is true iff any
// run row exists. The combos map may be empty even when the boolean is true —
// that legitimately means "we probed and nothing matched our normalizer".
func (s *PostgresStore) Get(ctx context.Context) (Options, bool, error) {
	var hasData bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM commitment_options_probe_runs)`,
	).Scan(&hasData); err != nil {
		return nil, false, fmt.Errorf("check commitment_options_probe_runs: %w", err)
	}
	if !hasData {
		return nil, false, nil
	}

	rows, err := s.db.Query(ctx,
		`SELECT c.provider, c.service, c.term_years, c.payment_option
		   FROM commitment_options_combos c
		   JOIN commitment_options_probe_runs r ON r.provider = c.provider`,
	)
	if err != nil {
		return nil, false, fmt.Errorf("query commitment_options_combos: %w", err)
	}
	defer rows.Close()

	opts := make(Options)
	for rows.Next() {
		var c Combo
		if err := rows.Scan(&c.Provider, &c.Service, &c.TermYears, &c.Payment); err != nil {
			return nil, false, fmt.Errorf("scan commitment_options_combos row: %w", err)
		}
		byService := opts[c.Provider]
		if byService == nil {
			byService = make(map[string][]Combo)
			opts[c.Provider] = byService
		}
		byService[c.Service] = append(byService[c.Service], c)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate commitment_options_combos: %w", err)
	}
	return opts, true, nil
}

// HasData reports whether a probe run row exists for provider.
func (s *PostgresStore) HasData(ctx context.Context, provider string) (bool, error) {
	var exists bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM commitment_options_probe_runs WHERE provider = $1)`,
		provider,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("check commitment_options_probe_runs: %w", err)
	}
	return exists, nil
}

// ProbedAt returns provider's probe run time; the bool is false without a row.
func (s *PostgresStore) ProbedAt(ctx context.Context, provider string) (time.Time, bool, error) {
	var at time.Time
	err := s.db.QueryRow(ctx,
		`SELECT probed_at FROM commitment_options_probe_runs WHERE provider = $1`,
		provider,
	).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read commitment_options_probe_runs.probed_at: %w", err)
	}
	return at, true, nil
}

// Save persists provider's probe run row and every combo transactionally.
// If the provider's run row already exists (a second process raced us to
// persist), the first row's probed_at and source win, but this call's combos
// are still added next to the existing ones (ON CONFLICT DO NOTHING). Replace
// is the path that overwrites the row and swaps the combos. Other providers'
// rows and combos are never touched.
func (s *PostgresStore) Save(ctx context.Context, provider string, combos []Combo, sourceAccountID string) error {
	return s.write(ctx, provider, combos, sourceAccountID, false)
}

// Replace overwrites provider's run row (new probed_at and source) and swaps
// its combos for the given ones in one transaction, so a reader never sees the
// provider with no combos and a failed write keeps the previous snapshot.
func (s *PostgresStore) Replace(ctx context.Context, provider string, combos []Combo, sourceAccountID string) error {
	return s.write(ctx, provider, combos, sourceAccountID, true)
}

func (s *PostgresStore) write(ctx context.Context, provider string, combos []Combo, sourceAccountID string, replace bool) error {
	for _, c := range combos {
		if c.Provider != provider {
			return fmt.Errorf("combo provider %q does not match saved provider %q", c.Provider, provider)
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	// After a successful Commit, Rollback returns pgx.ErrTxClosed; swallow
	// that single sentinel so the happy path stays quiet and only real
	// rollback failures on the error path get logged.
	defer func() {
		if rErr := tx.Rollback(ctx); rErr != nil && !errors.Is(rErr, pgx.ErrTxClosed) {
			logging.Warnf("commitmentopts.Save: rollback failed: %v", rErr)
		}
	}()

	if err := writeRunRow(ctx, tx, provider, sourceAccountID, replace); err != nil {
		return err
	}

	for _, c := range combos {
		if _, err := tx.Exec(ctx,
			`INSERT INTO commitment_options_combos (provider, service, term_years, payment_option)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT DO NOTHING`,
			provider, c.Service, c.TermYears, c.Payment,
		); err != nil {
			return fmt.Errorf("insert commitment_options_combos: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// writeRunRow upserts provider's run row; replace also overwrites the existing
// row and clears its combos. The upsert comes first on purpose: it takes the
// row lock, so a concurrent Replace waits here and its DELETE then runs on a
// fresh snapshot that sees the winner's combos. Deleting first would let the
// two writers' combos mix.
func writeRunRow(ctx context.Context, tx pgx.Tx, provider, sourceAccountID string, replace bool) error {
	onConflict := "DO NOTHING"
	if replace {
		onConflict = "DO UPDATE SET probed_at = EXCLUDED.probed_at, source_account_id = EXCLUDED.source_account_id"
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO commitment_options_probe_runs (provider, probed_at, source_account_id)
		 VALUES ($1, NOW(), $2)
		 ON CONFLICT (provider) `+onConflict,
		provider, sourceAccountID,
	); err != nil {
		return fmt.Errorf("insert commitment_options_probe_runs: %w", err)
	}
	if replace {
		if _, err := tx.Exec(ctx, `DELETE FROM commitment_options_combos WHERE provider = $1`, provider); err != nil {
			return fmt.Errorf("delete commitment_options_combos: %w", err)
		}
	}
	return nil
}
