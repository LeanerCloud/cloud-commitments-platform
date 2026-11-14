package config

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ==========================================
// ACCOUNT SERVICE OVERRIDES
// ==========================================

// GetAccountServiceOverride returns a single override, or nil if none exists.
func (s *PostgresStore) GetAccountServiceOverride(ctx context.Context, accountID, provider, service string) (*AccountServiceOverride, error) {
	query := `
		SELECT id, account_id, provider, service,
			enabled, term, payment, coverage, ramp_schedule,
			include_engines, exclude_engines, include_regions, exclude_regions,
			include_types, exclude_types,
			created_at, updated_at
		FROM account_service_overrides
		WHERE account_id = $1 AND provider = $2 AND service = $3
	`
	var o AccountServiceOverride
	var incEngines, excEngines, incRegions, excRegions, incTypes, excTypes []string
	err := s.db.QueryRow(ctx, query, accountID, provider, service).Scan(
		&o.ID, &o.AccountID, &o.Provider, &o.Service,
		&o.Enabled, &o.Term, &o.Payment, &o.Coverage, &o.RampSchedule,
		&incEngines, &excEngines, &incRegions, &excRegions, &incTypes, &excTypes,
		&o.CreatedAt, &o.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get service override: %w", err)
	}
	o.IncludeEngines = incEngines
	o.ExcludeEngines = excEngines
	o.IncludeRegions = incRegions
	o.ExcludeRegions = excRegions
	o.IncludeTypes = incTypes
	o.ExcludeTypes = excTypes
	return &o, nil
}

// SaveAccountServiceOverride upserts an account service override.
func (s *PostgresStore) SaveAccountServiceOverride(ctx context.Context, o *AccountServiceOverride) error {
	return saveAccountServiceOverride(ctx, s.db, o)
}

// overrideExecutor is satisfied by both the pool and a pgx.Tx.
type overrideExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func saveAccountServiceOverride(ctx context.Context, db overrideExecutor, o *AccountServiceOverride) error {
	if o.ID == "" {
		o.ID = uuid.New().String()
	}
	now := time.Now()
	// Only set CreatedAt for new records; preserve the original creation time on updates.
	if o.CreatedAt.IsZero() {
		o.CreatedAt = now
	}
	o.UpdatedAt = now

	query := `
		INSERT INTO account_service_overrides (
			id, account_id, provider, service,
			enabled, term, payment, coverage, ramp_schedule,
			include_engines, exclude_engines, include_regions, exclude_regions,
			include_types, exclude_types,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (account_id, provider, service) DO UPDATE SET
			enabled = $5, term = $6, payment = $7, coverage = $8, ramp_schedule = $9,
			include_engines = $10, exclude_engines = $11,
			include_regions = $12, exclude_regions = $13,
			include_types = $14, exclude_types = $15,
			updated_at = NOW()
	`
	_, err := db.Exec(ctx, query,
		o.ID, o.AccountID, o.Provider, o.Service,
		o.Enabled, o.Term, o.Payment, o.Coverage, o.RampSchedule,
		o.IncludeEngines, o.ExcludeEngines, o.IncludeRegions, o.ExcludeRegions,
		o.IncludeTypes, o.ExcludeTypes,
		o.CreatedAt, o.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to save service override: %w", err)
	}
	return nil
}

// DeleteAccountServiceOverride removes an override, reverting to global defaults.
func (s *PostgresStore) DeleteAccountServiceOverride(ctx context.Context, accountID, provider, service string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM account_service_overrides WHERE account_id = $1 AND provider = $2 AND service = $3`,
		accountID, provider, service,
	)
	if err != nil {
		return fmt.Errorf("failed to delete service override: %w", err)
	}
	return nil
}

// ListAccountServiceOverrides returns all overrides for an account.
func (s *PostgresStore) ListAccountServiceOverrides(ctx context.Context, accountID string) ([]AccountServiceOverride, error) {
	query := `
		SELECT id, account_id, provider, service,
			enabled, term, payment, coverage, ramp_schedule,
			include_engines, exclude_engines, include_regions, exclude_regions,
			include_types, exclude_types,
			created_at, updated_at
		FROM account_service_overrides
		WHERE account_id = $1
		ORDER BY provider, service
	`
	rows, err := s.db.Query(ctx, query, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to list service overrides: %w", err)
	}
	defer rows.Close()

	overrides := make([]AccountServiceOverride, 0)
	for rows.Next() {
		var o AccountServiceOverride
		var incEngines, excEngines, incRegions, excRegions, incTypes, excTypes []string
		if err := rows.Scan(
			&o.ID, &o.AccountID, &o.Provider, &o.Service,
			&o.Enabled, &o.Term, &o.Payment, &o.Coverage, &o.RampSchedule,
			&incEngines, &excEngines, &incRegions, &excRegions, &incTypes, &excTypes,
			&o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan service override: %w", err)
		}
		o.IncludeEngines = incEngines
		o.ExcludeEngines = excEngines
		o.IncludeRegions = incRegions
		o.ExcludeRegions = excRegions
		o.IncludeTypes = incTypes
		o.ExcludeTypes = excTypes
		overrides = append(overrides, o)
	}
	return overrides, rows.Err()
}
