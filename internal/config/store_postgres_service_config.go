package config

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ==========================================
// SERVICE CONFIGURATION
// ==========================================

// serviceConfigColumns is the column list every service_configs read scans,
// in scanServiceConfig order.
const serviceConfigColumns = `provider, service, enabled, term, payment, coverage, ramp_schedule,
		       include_engines, exclude_engines, include_regions, exclude_regions,
		       include_types, exclude_types, min_count, created_at, updated_at`

const getServiceConfigQuery = `SELECT ` + serviceConfigColumns + `
		FROM service_configs
		WHERE provider = $1 AND service = $2`

// serviceConfigLockPrefix namespaces the per-service advisory lock
// UpdateServiceConfigAtomic takes. It is a distinct text input to the same
// hashtextextended(text, 0) bigint key space as the ladder-config and
// plan-step provider locks; a 64-bit hash collision could only cause a
// spurious wait, never a correctness break.
const serviceConfigLockPrefix = "cudly:service-config:"

// GetServiceConfig retrieves configuration for a specific service.
func (s *PostgresStore) GetServiceConfig(ctx context.Context, provider, service string) (*ServiceConfig, error) {
	return getServiceConfigFrom(ctx, s.db, getServiceConfigQuery, provider, service)
}

// getServiceConfigFrom runs query (getServiceConfigQuery, optionally with a
// locking clause) via q (the pool or a transaction). A missing row is reported
// as an error wrapping ErrNotFound.
func getServiceConfigFrom(ctx context.Context, q globalConfigExecutor, query, provider, service string) (*ServiceConfig, error) {
	config, err := scanServiceConfig(q.QueryRow(ctx, query, provider, service))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("service config not found for %s:%s: %w", provider, service, ErrNotFound)
		}
		return nil, fmt.Errorf("failed to get service config: %w", err)
	}
	return config, nil
}

// scanServiceConfig scans one row selected with serviceConfigColumns,
// including the row's created_at and updated_at stamps (issue #8).
func scanServiceConfig(row pgx.Row) (*ServiceConfig, error) {
	var config ServiceConfig
	var createdAt, updatedAt time.Time
	err := row.Scan(
		&config.Provider,
		&config.Service,
		&config.Enabled,
		&config.Term,
		&config.Payment,
		&config.Coverage,
		&config.RampSchedule,
		&config.IncludeEngines,
		&config.ExcludeEngines,
		&config.IncludeRegions,
		&config.ExcludeRegions,
		&config.IncludeTypes,
		&config.ExcludeTypes,
		&config.MinCount,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return nil, err
	}
	config.CreatedAt = &createdAt
	config.UpdatedAt = &updatedAt
	return &config, nil
}

// SaveServiceConfig saves configuration for a service.
func (s *PostgresStore) SaveServiceConfig(ctx context.Context, config *ServiceConfig) error {
	return saveServiceConfigWith(ctx, s.db, config)
}

// ListServiceConfigs lists all service configurations.
//
// LIMIT 1000 caps the result set at three orders of magnitude above the
// realistic upper bound (each cloud has a bounded set of services, so the
// total is roughly (providers × service-types × per-service-variants),
// which stays under ~150 even with generous provider growth). The cap is
// defense-in-depth against a compromised admin inserting millions of rows
// and matches the sibling GetPendingExecutions limit.
func (s *PostgresStore) ListServiceConfigs(ctx context.Context) ([]ServiceConfig, error) {
	query := `SELECT ` + serviceConfigColumns + `
		FROM service_configs
		ORDER BY provider, service
		LIMIT 1000
	`

	rows, err := s.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list service configs: %w", err)
	}
	defer rows.Close()

	configs := make([]ServiceConfig, 0)
	for rows.Next() {
		config, err := scanServiceConfig(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan service config: %w", err)
		}
		configs = append(configs, *config)
	}

	return configs, rows.Err()
}
