package config

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// riExchangeDirectResolveRow is the locked row the resolve rules run against.
// dbNow is the database clock after the lock was granted.
type riExchangeDirectResolveRow struct {
	status                  string
	createdAt               time.Time
	completedAt, resolvedAt *time.Time
	resolvedStatus, note    *string
	dbNow                   time.Time
}

// ResolveRIExchangeDirectExecution settles an AWS direct row an operator has
// verified at AWS. It records the resolution beside the row's original status
// and completed_at, which are never rewritten, and the in-flight guard then
// stops treating the row as holding its source RIs. One transaction locks the
// row, so two resolves and a concurrent finalize each see the other's commit.
// Every time comparison uses the database clock.
func (s *PostgresStore) ResolveRIExchangeDirectExecution(ctx context.Context, req RIExchangeDirectResolve) (*RIExchangeDirectResolveResult, error) {
	if err := validateRIExchangeDirectResolve(req); err != nil {
		return nil, err
	}
	res := &RIExchangeDirectResolveResult{ID: req.ID, ResolvedStatus: req.Outcome, Note: req.Note}
	err := s.WithTx(ctx, func(tx pgx.Tx) error {
		row, err := lockRIExchangeDirectForResolve(ctx, tx, req.ID)
		if err != nil {
			return err
		}
		if row.resolvedAt != nil {
			return replayRIExchangeDirectResolve(req, row, res)
		}
		if err := checkRIExchangeDirectResolve(req, row); err != nil {
			return err
		}
		return applyRIExchangeDirectResolve(ctx, tx, req, res)
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func validateRIExchangeDirectResolve(req RIExchangeDirectResolve) error {
	switch {
	case req.Outcome != RIExchangeDirectStatusCompleted && req.Outcome != RIExchangeDirectStatusFailed:
		return fmt.Errorf("%w: outcome must be %q or %q", ErrRIExchangeResolveInvalid, RIExchangeDirectStatusCompleted, RIExchangeDirectStatusFailed)
	case req.ExpectedStatus != RIExchangeDirectStatusSubmitted && req.ExpectedStatus != RIExchangeDirectStatusUnknown:
		return fmt.Errorf("%w: expected status must be %q or %q", ErrRIExchangeResolveInvalid, RIExchangeDirectStatusSubmitted, RIExchangeDirectStatusUnknown)
	case req.Note == "" || len(req.Note) > RIExchangeDirectResolveNoteMax:
		return fmt.Errorf("%w: note must be 1 to %d bytes", ErrRIExchangeResolveInvalid, RIExchangeDirectResolveNoteMax)
	case req.CheckedAt.IsZero():
		return fmt.Errorf("%w: the cloud check must name a time", ErrRIExchangeResolveInvalid)
	case req.ResolvedBy == "":
		return fmt.Errorf("%w: the resolver must be named", ErrRIExchangeResolveInvalid)
	}
	return nil
}

func lockRIExchangeDirectForResolve(ctx context.Context, tx pgx.Tx, id string) (riExchangeDirectResolveRow, error) {
	var row riExchangeDirectResolveRow
	err := tx.QueryRow(ctx, `
		SELECT status, created_at, completed_at, resolved_at, resolved_status, resolution_note, clock_timestamp()
		FROM ri_exchange_direct_executions
		WHERE id = $1 AND provider = 'aws'
		FOR UPDATE`, id,
	).Scan(&row.status, &row.createdAt, &row.completedAt, &row.resolvedAt, &row.resolvedStatus, &row.note, &row.dbNow)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, ErrRIExchangeResolveNotFound
	}
	if err != nil {
		return row, fmt.Errorf("failed to lock ri exchange direct execution %s: %w", id, err)
	}
	return row, nil
}

// replayRIExchangeDirectResolve answers a request that found the row already
// resolved: success only when it holds exactly what this request would have
// written, else a conflict.
func replayRIExchangeDirectResolve(req RIExchangeDirectResolve, row riExchangeDirectResolveRow, res *RIExchangeDirectResolveResult) error {
	if row.resolvedStatus != nil && *row.resolvedStatus == req.Outcome && row.note != nil && *row.note == req.Note {
		res.Status = row.status
		res.ResolvedAt = *row.resolvedAt
		res.Replayed = true
		return nil
	}
	return fmt.Errorf("%w: the exchange was already resolved as %s", ErrRIExchangeResolveConflict, derefString(row.resolvedStatus))
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func checkRIExchangeDirectResolve(req RIExchangeDirectResolve, row riExchangeDirectResolveRow) error {
	switch {
	case row.status != RIExchangeDirectStatusSubmitted && row.status != RIExchangeDirectStatusUnknown:
		return fmt.Errorf("%w: the exchange is already %s", ErrRIExchangeResolveConflict, row.status)
	case row.status != req.ExpectedStatus:
		return fmt.Errorf("%w: the exchange is %s, not %s", ErrRIExchangeResolveConflict, row.status, req.ExpectedStatus)
	}
	var earliest time.Time
	if row.status == RIExchangeDirectStatusSubmitted {
		earliest = row.createdAt.Add(RIExchangeDirectResolveSubmittedAfter)
		if row.dbNow.Before(earliest) {
			return fmt.Errorf("%w: the exchange may still be in flight; wait until %s",
				ErrRIExchangeResolveConflict, earliest.UTC().Format(time.RFC3339))
		}
	} else {
		if row.completedAt == nil {
			return errors.New("failed to resolve ri exchange direct execution: unknown row has no completed_at")
		}
		earliest = row.completedAt.Add(RIExchangeDirectResolveUnknownCheckAfter)
	}
	// Postgres keeps microseconds, so compare the operator's time at that
	// precision: a nanosecond tail must neither pass nor fail a bound.
	checked := req.CheckedAt.Truncate(time.Microsecond)
	switch {
	case checked.After(row.dbNow):
		return fmt.Errorf("%w: checked_at is in the future", ErrRIExchangeResolveInvalid)
	case checked.Before(earliest):
		return fmt.Errorf("%w: checked_at must be at or after %s", ErrRIExchangeResolveInvalid, earliest.UTC().Format(time.RFC3339Nano))
	}
	return nil
}

func applyRIExchangeDirectResolve(ctx context.Context, tx pgx.Tx, req RIExchangeDirectResolve, res *RIExchangeDirectResolveResult) error {
	err := tx.QueryRow(ctx, `
		UPDATE ri_exchange_direct_executions
		SET resolved_status = $2, resolved_by_user_id = $3::uuid, resolved_by = $4,
		    resolved_at = clock_timestamp(), resolution_note = $5
		WHERE id = $1 AND provider = 'aws' AND resolved_at IS NULL AND status = $6
		RETURNING status, resolved_at`,
		req.ID, req.Outcome, req.ResolvedByUserID, req.ResolvedBy, req.Note, req.ExpectedStatus,
	).Scan(&res.Status, &res.ResolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: the exchange changed while resolving", ErrRIExchangeResolveConflict)
	}
	if err != nil {
		return fmt.Errorf("failed to resolve ri exchange direct execution %s: %w", req.ID, err)
	}
	return nil
}
