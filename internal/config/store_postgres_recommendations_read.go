package config

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) checkAzurePricingMigration(ctx context.Context, required bool) error {
	if !required {
		return nil
	}
	version, dirty, found, err := readSchemaVersion(ctx, s.db)
	if err != nil {
		return fmt.Errorf("check Azure pricing migration: %w", err)
	}
	if !found || version < azurePricingMigrationVersion || dirty {
		return ErrAzurePricingNotReady
	}
	return nil
}

// CheckAZConfigMigration returns ErrAZConfigMigrationNotReady (wrapped, with
// the schema version found) until both az_config expand migrations are applied.
func (s *PostgresStore) CheckAZConfigMigration(ctx context.Context) error {
	return checkAZConfigSchema(ctx, s.db)
}

// checkAZConfigMigrationTx is CheckAZConfigMigration on the caller's open
// transaction, so a write inside a tx does not take a second pool connection.
func checkAZConfigMigrationTx(ctx context.Context, tx pgx.Tx) error {
	return checkAZConfigSchema(ctx, tx)
}

func checkAZConfigSchema(ctx context.Context, q rowQuerier) error {
	version, dirty, found, err := readSchemaVersion(ctx, q)
	if err != nil {
		return fmt.Errorf("check az_config migration: %w", err)
	}
	if !found {
		return ErrAZConfigMigrationNotReady
	}
	if version < azConfigExpandMigrationVersion || dirty {
		return fmt.Errorf("%w: schema version %d (dirty=%t), need >= %d", ErrAZConfigMigrationNotReady, version, dirty, azConfigExpandMigrationVersion)
	}
	return nil
}

// ListStoredRecommendations reads recommendations matching the filter.
// SQL-pushed conditions (Provider, Service, Region, AccountIDs,
// MinSavingsUSD) are applied in SQL so Postgres prunes the rows; the
// MinSavingsPct filter is applied in-process because the on-demand
// baseline lives inside the JSONB payload (not a native column).
func (s *PostgresStore) ListStoredRecommendations(ctx context.Context, filter RecommendationFilter) ([]RecommendationRecord, error) {
	if err := s.checkAzurePricingMigration(ctx, filter.RequireAzurePricingMigration); err != nil {
		return nil, err
	}
	whereClause, args := buildRecommendationFilter(filter)
	rows, err := s.db.Query(ctx, `SELECT payload FROM recommendations`+whereClause, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query recommendations: %w", err)
	}
	defer rows.Close()

	var out []RecommendationRecord
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("failed to scan payload: %w", err)
		}
		rec, keep, err := decodeStoredRecommendation(payload, filter)
		if err != nil {
			return nil, err
		}
		if keep {
			out = append(out, rec)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}
	return out, nil
}

// decodeStoredRecommendation unmarshals one stored payload and applies the
// in-process MinSavingsPct floor. keep is false when the floor drops the rec.
// Recs where the percentage cannot be computed (missing on-demand baseline)
// pass through: a rec is only dropped when pct is computable AND below the
// threshold.
func decodeStoredRecommendation(payload []byte, filter RecommendationFilter) (rec RecommendationRecord, keep bool, err error) {
	if err := json.Unmarshal(payload, &rec); err != nil {
		return RecommendationRecord{}, false, fmt.Errorf("failed to unmarshal payload: %w", err)
	}
	if filter.MinSavingsPct > 0 {
		if pct, ok := recEffectiveSavingsPct(&rec); ok && pct < filter.MinSavingsPct {
			return rec, false, nil
		}
	}
	return rec, true, nil
}
