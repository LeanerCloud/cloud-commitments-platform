package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ladderRunInsertQuery INSERTs one ladder_runs row and RETURNs the full row for
// scanLadderRun. Shared by SaveLadderRun and SaveLadderRunWithTranches so the
// column list has a single definition.
const ladderRunInsertQuery = `
	INSERT INTO ladder_runs (
		id, config_id, started_at, completed_at, status, mode, cadence,
		baseline_usd_hr, target_usd_hr, existing_usd_hr, gap_usd_hr,
		plan, total_hourly_commit, total_upfront_cost, estimated_savings,
		approval_token_hash, approval_token_expires_at,
		approved_by, cancelled_by, fire_at,
		created_at, updated_at
	) VALUES (
		$1, $2, $3, $4, $5, $6, $7,
		$8, $9, $10, $11,
		$12, $13, $14, $15,
		$16, $17, $18, $19, $20,
		NOW(), NOW()
	)
	RETURNING id, config_id, started_at, completed_at, status, mode, cadence,
	          baseline_usd_hr, target_usd_hr, existing_usd_hr, gap_usd_hr,
	          plan, total_hourly_commit, total_upfront_cost, estimated_savings,
	          approval_token_hash, approval_token_expires_at,
	          approved_by, cancelled_by, fire_at,
	          created_at, updated_at
`

// ladderRunPlanJSON returns the plan blob to persist, defaulting an empty plan
// to the JSONB '{}' the schema expects (NOT NULL DEFAULT '{}').
func ladderRunPlanJSON(run *LadderRunDB) json.RawMessage {
	if len(run.Plan) == 0 {
		return json.RawMessage(`{}`)
	}
	return run.Plan
}

// ladderRunInsertArgs returns the positional args for ladderRunInsertQuery in
// column order. Nullable monetary fields stay as *float64 so NULL != $0.
func ladderRunInsertArgs(run *LadderRunDB, planJSON json.RawMessage) []any {
	return []any{
		run.ID,
		run.ConfigID,
		run.StartedAt,
		run.CompletedAt,
		run.Status,
		run.Mode,
		run.Cadence,
		run.BaselineUSDHr,
		run.TargetUSDHr,
		run.ExistingUSDHr,
		run.GapUSDHr,
		planJSON,
		run.TotalHourlyCommit,
		run.TotalUpfrontCost,
		run.EstimatedSavings,
		run.ApprovalTokenHash,
		run.ApprovalTokenExpiresAt,
		run.ApprovedBy,
		run.CancelledBy,
		run.FireAt,
	}
}

// SaveLadderRun inserts a new ladder_runs row. If run.ID is empty a fresh UUID
// is generated. Returns the persisted row with all DB-stamped fields populated.
func (s *PostgresStore) SaveLadderRun(ctx context.Context, run *LadderRunDB) (*LadderRunDB, error) {
	if run.ID == "" {
		run.ID = uuid.New().String()
	}
	row := s.db.QueryRow(ctx, ladderRunInsertQuery, ladderRunInsertArgs(run, ladderRunPlanJSON(run))...)
	result, err := scanLadderRun(row)
	if err != nil {
		return nil, fmt.Errorf("failed to insert ladder_run id=%s: %w", run.ID, err)
	}
	return &result, nil
}

// GetLadderRun returns the ladder_runs row for the given id, or (nil, nil)
// when no row exists.
func (s *PostgresStore) GetLadderRun(ctx context.Context, id string) (*LadderRunDB, error) {
	query := `
		SELECT id, config_id, started_at, completed_at, status, mode, cadence,
		       baseline_usd_hr, target_usd_hr, existing_usd_hr, gap_usd_hr,
		       plan, total_hourly_commit, total_upfront_cost, estimated_savings,
		       approval_token_hash, approval_token_expires_at,
		       approved_by, cancelled_by, fire_at,
		       created_at, updated_at
		FROM ladder_runs
		WHERE id = $1
	`
	row := s.db.QueryRow(ctx, query, id)
	result, err := scanLadderRun(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get ladder_run id=%s: %w", id, err)
	}
	return &result, nil
}

// scanLadderRun scans a single ladder_runs row from a pgx.Row or pgx.Rows.
// Both implement the scannable interface (store_postgres_registrations.go).
func scanLadderRun(row scannable) (LadderRunDB, error) {
	var r LadderRunDB
	var planJSON []byte

	err := row.Scan(
		&r.ID,
		&r.ConfigID,
		&r.StartedAt,
		&r.CompletedAt,
		&r.Status,
		&r.Mode,
		&r.Cadence,
		&r.BaselineUSDHr,
		&r.TargetUSDHr,
		&r.ExistingUSDHr,
		&r.GapUSDHr,
		&planJSON,
		&r.TotalHourlyCommit,
		&r.TotalUpfrontCost,
		&r.EstimatedSavings,
		&r.ApprovalTokenHash,
		&r.ApprovalTokenExpiresAt,
		&r.ApprovedBy,
		&r.CancelledBy,
		&r.FireAt,
		&r.CreatedAt,
		&r.UpdatedAt,
	)
	if err != nil {
		return LadderRunDB{}, err
	}
	if planJSON != nil {
		r.Plan = json.RawMessage(planJSON)
	}
	return r, nil
}

// marshalRampSchedule ensures the ramp_schedule value stored in the DB is
// valid JSON. Nil/empty input is rejected; already-valid JSON is passed
// through unchanged. Non-JSON input is rejected with a descriptive error.
func marshalRampSchedule(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("ramp_schedule must not be empty")
	}
	// Validate it is parseable JSON before writing.
	if !json.Valid(raw) {
		return nil, fmt.Errorf("ramp_schedule is not valid JSON")
	}
	return raw, nil
}
