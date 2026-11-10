package config

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// requireSweepCeilingSchema fails with ErrLadderCeilingUnavailable while the
// ceiling column is missing.
func requireSweepCeilingSchema(ctx context.Context, q rowQuerier) error {
	ready, err := sweepCeilingSchemaReady(ctx, q)
	if err != nil {
		return err
	}
	if !ready {
		return ErrLadderCeilingUnavailable
	}
	return nil
}

// GetLadderSweepCeiling returns the config's rolling-24h commitment ceiling in
// exact micro-dollars per hour. nil means not set, which the sweep must treat
// as refuse, never as unlimited. An unmigrated schema returns
// ErrLadderCeilingUnavailable.
func (s *PostgresStore) GetLadderSweepCeiling(ctx context.Context, configID string) (*int64, error) {
	if err := requireSweepCeilingSchema(ctx, s.db); err != nil {
		return nil, err
	}
	var micros *int64
	err := s.db.QueryRow(ctx,
		`SELECT (max_hourly_commit_per_sweep * 1000000)::bigint FROM ladder_configs WHERE id = $1`,
		configID).Scan(&micros)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("ladder config %s not found: %w", configID, err)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read ladder sweep ceiling config_id=%s: %w", configID, err)
	}
	return micros, nil
}

// SetLadderSweepCeiling stores the ceiling in USD per hour of commitment; nil
// clears it. The value must pass the same validation as the per-run cap and is
// written as an exact decimal built from micro-dollars, never the float.
func (s *PostgresStore) SetLadderSweepCeiling(ctx context.Context, configID string, usdPerHr *float64) error {
	var arg *string
	if usdPerHr != nil {
		micros, err := ladderHourlyMicros("max_hourly_commit_per_sweep", *usdPerHr)
		if err != nil {
			return err
		}
		exact := fmt.Sprintf("%d.%06d", micros/1_000_000, micros%1_000_000)
		arg = &exact
	}
	if err := requireSweepCeilingSchema(ctx, s.db); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx,
		`UPDATE ladder_configs SET max_hourly_commit_per_sweep = $2::text::numeric WHERE id = $1`,
		configID, arg)
	if err != nil {
		return fmt.Errorf("failed to set ladder sweep ceiling config_id=%s: %w", configID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("ladder config %s not found", configID)
	}
	return nil
}
