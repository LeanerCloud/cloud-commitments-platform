package config

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// RedshiftRetryClock is a row's age measured on the database clock.
// SinceStamp is nil when the row has no first_dispatch_at, so a missing stamp
// never reads as fresh.
type RedshiftRetryClock struct {
	SinceUpdate time.Duration
	SinceStamp  *time.Duration
}

// GetRedshiftRetryClock returns how long ago the row was last written and
// first dispatched, both computed from now() in SQL so app clock skew cannot
// shorten the settle wait or the horizon.
func (s *PostgresStore) GetRedshiftRetryClock(ctx context.Context, executionID string) (RedshiftRetryClock, error) {
	var sinceUpdate float64
	var sinceStamp *float64
	err := s.db.QueryRow(ctx, `
		SELECT EXTRACT(EPOCH FROM now() - updated_at)::float8,
		       EXTRACT(EPOCH FROM now() - first_dispatch_at)::float8
		FROM purchase_executions WHERE execution_id = $1`, executionID).Scan(&sinceUpdate, &sinceStamp)
	if errors.Is(err, pgx.ErrNoRows) {
		return RedshiftRetryClock{}, fmt.Errorf("%w: execution %s", ErrNotFound, executionID)
	}
	if err != nil {
		return RedshiftRetryClock{}, fmt.Errorf("read redshift retry clock of execution %s: %w", executionID, err)
	}
	clock := RedshiftRetryClock{SinceUpdate: secondsToDuration(sinceUpdate)}
	if sinceStamp != nil {
		d := secondsToDuration(*sinceStamp)
		clock.SinceStamp = &d
	}
	return clock, nil
}

func secondsToDuration(secs float64) time.Duration {
	return time.Duration(secs * float64(time.Second))
}
