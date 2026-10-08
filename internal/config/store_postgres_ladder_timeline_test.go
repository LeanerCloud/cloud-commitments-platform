package config

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestLadderTimelinePagination(t *testing.T) {
	pool, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer pool.Close()
	store := &PostgresStore{db: pool}
	stamp := time.Now().UTC()
	events := make([]LadderTimelineEvent, LadderTimelinePageSize+1)
	for i := range events {
		events[i] = LadderTimelineEvent{ID: uuid.NewString(), CreatedAt: stamp, AmountUSDHr: "0.123456", Revision: int64(i)}
	}
	raw, err := json.Marshal(events)
	require.NoError(t, err)
	pool.ExpectQuery(regexp.QuoteMeta(ladderTimelineQuery)).
		WithArgs("account", "aws", nil, nil, LadderTimelinePageSize+1).
		WillReturnRows(pgxmock.NewRows([]string{"count", "sum", "page"}).AddRow(int64(101), "12.469056", raw))
	page, err := store.ListLadderTimeline(context.Background(), "account", "aws", nil)
	require.NoError(t, err)
	require.Len(t, page.Events, LadderTimelinePageSize)
	require.Equal(t, int64(101), page.TotalCount)
	require.Equal(t, "12.469056", page.TotalUSDHr)
	require.Equal(t, "0.123456", page.Events[0].AmountUSDHr)
	require.Equal(t, int64(1), page.Events[1].Revision)
	require.Equal(t, events[99].ID, page.NextCursor.ID)
	require.NoError(t, pool.ExpectationsWereMet())
}

func TestLadderTimelineQueryFailureIsNotEmpty(t *testing.T) {
	pool, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer pool.Close()
	store := &PostgresStore{db: pool}
	pool.ExpectQuery(regexp.QuoteMeta(ladderTimelineQuery)).
		WithArgs("account", "aws", nil, nil, LadderTimelinePageSize+1).
		WillReturnError(errors.New("database unavailable"))
	page, err := store.ListLadderTimeline(context.Background(), "account", "aws", nil)
	require.ErrorContains(t, err, "database unavailable")
	require.Nil(t, page)
	require.NoError(t, pool.ExpectationsWereMet())
}

func TestLadderTimelineRunsPaginationAndFailure(t *testing.T) {
	pool, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer pool.Close()
	store := &PostgresStore{db: pool}
	stamp := time.Now().UTC()
	runs := make([]LadderTimelineRun, LadderTimelinePageSize+1)
	for i := range runs {
		runs[i] = LadderTimelineRun{ID: uuid.NewString(), CreatedAt: stamp}
	}
	raw, err := json.Marshal(runs)
	require.NoError(t, err)
	pool.ExpectQuery(regexp.QuoteMeta(ladderTimelineRunsQuery)).
		WithArgs("account", "aws", nil, nil, LadderTimelinePageSize+1).
		WillReturnRows(pgxmock.NewRows([]string{"count", "page"}).AddRow(int64(101), raw))
	page, err := store.ListLadderTimelineRuns(context.Background(), "account", "aws", nil)
	require.NoError(t, err)
	require.Len(t, page.Runs, 100)
	require.Equal(t, runs[99].ID, page.NextCursor.ID)
	pool.ExpectQuery(regexp.QuoteMeta(ladderTimelineRunsQuery)).
		WithArgs("account", "aws", stamp, runs[99].ID, LadderTimelinePageSize+1).
		WillReturnError(errors.New("database unavailable"))
	page, err = store.ListLadderTimelineRuns(context.Background(), "account", "aws", page.NextCursor)
	require.ErrorContains(t, err, "database unavailable")
	require.Nil(t, page)
	require.NoError(t, pool.ExpectationsWereMet())
}
