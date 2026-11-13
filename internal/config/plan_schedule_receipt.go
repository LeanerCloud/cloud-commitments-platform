package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrPlanScheduleReceiptExists means a receipt with this command id is already
// stored. Receipts are immutable, so the caller replays instead of rewriting.
var ErrPlanScheduleReceiptExists = errors.New("plan schedule command receipt already exists")

// PlanScheduleReceipt is the immutable record of one schedule-editor command.
// RequestHash is the caller's hash of the canonical request; Outcome is the
// response a replay of the same command returns verbatim.
type PlanScheduleReceipt struct {
	CommandID   string
	PlanID      string
	Actor       string
	RequestHash string
	Outcome     json.RawMessage
	CreatedAt   time.Time
}

// GetPlanScheduleReceiptTx reads a receipt on tx. A missing receipt is (nil, nil).
func (s *PostgresStore) GetPlanScheduleReceiptTx(ctx context.Context, tx pgx.Tx, commandID string) (*PlanScheduleReceipt, error) {
	var r PlanScheduleReceipt
	err := tx.QueryRow(ctx, `SELECT command_id::text, plan_id::text, actor, request_hash, outcome, created_at
		FROM plan_schedule_command_receipts WHERE command_id = $1`, commandID).
		Scan(&r.CommandID, &r.PlanID, &r.Actor, &r.RequestHash, &r.Outcome, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read plan schedule receipt %s: %w", commandID, err)
	}
	return &r, nil
}

// InsertPlanScheduleReceiptTx stores r on tx. A duplicate command id reports
// ErrPlanScheduleReceiptExists.
func (s *PostgresStore) InsertPlanScheduleReceiptTx(ctx context.Context, tx pgx.Tx, r *PlanScheduleReceipt) error {
	err := tx.QueryRow(ctx, `INSERT INTO plan_schedule_command_receipts
		(command_id, plan_id, actor, request_hash, outcome) VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at`, r.CommandID, r.PlanID, r.Actor, r.RequestHash, r.Outcome).Scan(&r.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrPlanScheduleReceiptExists, r.CommandID)
	}
	if err != nil {
		return fmt.Errorf("failed to insert plan schedule receipt %s: %w", r.CommandID, err)
	}
	return nil
}
