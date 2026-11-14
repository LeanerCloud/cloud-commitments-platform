package config

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// UpdateServiceConfigAtomic performs a serialized read-merge-write of one
// service_configs row (issue #1488). In one transaction it takes a
// transaction-scoped advisory lock on (provider, service), reads the row
// FOR UPDATE, hands it to apply and upserts the row apply returns.
//
// Both locks are needed. The advisory lock serializes two saves of a row that
// does not exist yet, which a row lock cannot do because there is nothing to
// lock: without it both would read nothing and the second insert would
// overwrite the first. The row lock makes writers that do not take the
// advisory lock, such as the global-defaults propagation in
// UpdateGlobalConfigAtomic, wait for this transaction (and this transaction
// wait for them), so the row read here is the row the write replaces.
//
// apply receives nil when no row exists. It must return the full row to
// write, keyed on the same provider and service; an error it returns aborts
// the transaction and is propagated unchanged.
func (s *PostgresStore) UpdateServiceConfigAtomic(ctx context.Context, provider, service string, apply func(existing *ServiceConfig) (*ServiceConfig, error)) (*ServiceConfig, error) {
	var saved *ServiceConfig
	err := s.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", serviceConfigLockPrefix+provider+"/"+service); err != nil {
			return fmt.Errorf("failed to acquire service config lock: %w", err)
		}
		existing, err := getServiceConfigFrom(ctx, tx, getServiceConfigQuery+" FOR UPDATE", provider, service)
		if errors.Is(err, ErrNotFound) {
			existing = nil
		} else if err != nil {
			return err
		}
		next, err := apply(existing)
		if err != nil {
			return err
		}
		if next == nil || next.Provider != provider || next.Service != service {
			return fmt.Errorf("service config update for %s/%s must return a row with the same key", provider, service)
		}
		if err := saveServiceConfigWith(ctx, tx, next); err != nil {
			return err
		}
		saved = next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// saveServiceConfigWith upserts one service_configs row via q (the pool or a
// transaction).
func saveServiceConfigWith(ctx context.Context, q globalConfigExecutor, config *ServiceConfig) error {
	query := `
		INSERT INTO service_configs (
			provider, service, enabled, term, payment, coverage, ramp_schedule,
			include_engines, exclude_engines, include_regions, exclude_regions,
			include_types, exclude_types, min_count
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (provider, service) DO UPDATE SET
			enabled = $3,
			term = $4,
			payment = $5,
			coverage = $6,
			ramp_schedule = $7,
			include_engines = $8,
			exclude_engines = $9,
			include_regions = $10,
			exclude_regions = $11,
			include_types = $12,
			exclude_types = $13,
			min_count = $14,
			updated_at = NOW()
		RETURNING created_at, updated_at
	`

	var createdAt, updatedAt time.Time
	err := q.QueryRow(ctx, query,
		config.Provider,
		config.Service,
		config.Enabled,
		config.Term,
		config.Payment,
		config.Coverage,
		config.RampSchedule,
		config.IncludeEngines,
		config.ExcludeEngines,
		config.IncludeRegions,
		config.ExcludeRegions,
		config.IncludeTypes,
		config.ExcludeTypes,
		config.MinCount,
	).Scan(&createdAt, &updatedAt)

	if err != nil {
		return fmt.Errorf("failed to save service config: %w", err)
	}

	// The stamps always come from the row as written, so a caller-supplied
	// value (a PUT body carrying created_at) never survives the save.
	config.CreatedAt = &createdAt
	config.UpdatedAt = &updatedAt
	return nil
}
