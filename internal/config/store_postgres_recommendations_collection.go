package config

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/google/uuid"
)

// GetRecommendationsFreshness returns the singleton freshness row. The
// table is seeded with id=1 by the migration so a row always exists;
// LastCollectedAt, LastCollectionError, LastCollectionStartedAt and
// LastCollectionPartial can be NULL.
func (s *PostgresStore) GetRecommendationsFreshness(ctx context.Context) (*RecommendationsFreshness, error) {
	var (
		lastCollectedAt         *time.Time
		lastCollectionError     *string
		lastCollectionStartedAt *time.Time
		lastCollectionPartial   []byte
	)
	err := s.db.QueryRow(ctx, `
		SELECT last_collected_at, last_collection_error, last_collection_started_at, last_collection_partial
		  FROM recommendations_state
		 WHERE id = 1
	`).Scan(&lastCollectedAt, &lastCollectionError, &lastCollectionStartedAt, &lastCollectionPartial)
	if err != nil {
		return nil, fmt.Errorf("failed to read recommendations_state: %w", err)
	}
	partial, decodeErr := decodeCollectionPartial(lastCollectionPartial)
	if decodeErr != nil {
		logging.Warnf("recommendations_state.last_collection_partial is unreadable; reporting it as unreadable: %v", decodeErr)
	}
	return &RecommendationsFreshness{
		LastCollectedAt:                 lastCollectedAt,
		LastCollectionError:             lastCollectionError,
		LastCollectionStartedAt:         lastCollectionStartedAt,
		LastCollectionPartial:           partial,
		LastCollectionPartialUnreadable: decodeErr != nil,
	}, nil
}

// decodeCollectionPartial decodes the last_collection_partial JSONB column.
// NULL decodes to nil (the last collection was complete); JSON null and {} decode
// to an empty marker. JSONB cannot hold invalid text, so an undecodable value is
// valid JSON of the wrong shape, written by hand: it is an error, never "no
// marker", because the purchase gate would otherwise miss an Azure hold. The
// caller decides how to fail: the write path stops, the freshness read reports it.
func decodeCollectionPartial(raw []byte) (*RecommendationsCollectionPartial, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var partial RecommendationsCollectionPartial
	if err := json.Unmarshal(raw, &partial); err != nil {
		return nil, fmt.Errorf("decode last_collection_partial: %w", err)
	}
	return &partial, nil
}

// SetRecommendationsCollectionError records the most recent collection's
// error message without touching last_collected_at or
// last_collection_started_at. Used by the scheduler when a collect fails
// partially or fully so the frontend banner surfaces the issue while
// existing cached rows stay visible.
//
// This method must NOT clear last_collection_started_at (issue #261): it is
// called mid-run from persistCollection on every CollectRecommendations
// invocation that hits a provider error, including tokenless cron/cold-start/
// background runs. Clearing the marker here, unconditionally and with no
// owner check, previously let a tokenless run's routine provider error wipe
// a concurrent owner run's in-flight marker, reopening the exact race the
// compare-and-clear guard exists to close. Only the deferred, token-guarded
// clearCollectionStartedBestEffort (which runs on both the success and
// failure exit paths of CollectRecommendations) may clear started_at.
func (s *PostgresStore) SetRecommendationsCollectionError(ctx context.Context, errMsg string) error {
	if _, err := s.db.Exec(ctx, `
		UPDATE recommendations_state
		   SET last_collection_error = $1
		 WHERE id = 1
	`, errMsg); err != nil {
		return fmt.Errorf("failed to set collection error: %w", err)
	}
	return nil
}

// MarkCollectionStarted atomically sets last_collection_started_at = NOW()
// only when no in-flight collection is currently running. The WHERE clause
// treats a started_at older than 5 minutes as stale (the scheduler Lambda
// must have crashed) so a new collection can proceed rather than being
// permanently blocked. A fresh owner token is stamped into
// last_collection_owner_id alongside the timestamp so the caller that wins
// the race is the only one that can later clear the marker (issue #261
// compare-and-clear guard; see ClearCollectionStarted).
//
// Returns the token and true when this caller won the race (rowsAffected ==
// 1) and should proceed with the async invoke, threading the token through
// so it can be passed to ClearCollectionStarted later. Returns ("", false,
// nil) when another collection is already in flight (rowsAffected == 0),
// signaling the handler to return 409 Conflict.
func (s *PostgresStore) MarkCollectionStarted(ctx context.Context) (token string, ok bool, err error) {
	token = uuid.New().String()
	tag, err := s.db.Exec(ctx, `
		UPDATE recommendations_state
		   SET last_collection_started_at = NOW(),
		       last_collection_owner_id    = $1
		 WHERE id = 1
		   AND (
		           last_collection_started_at IS NULL
		        OR last_collection_started_at < NOW() - INTERVAL '5 minutes'
		       )
	`, token)
	if err != nil {
		return "", false, fmt.Errorf("failed to mark collection started: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return "", false, nil
	}
	return token, true, nil
}
