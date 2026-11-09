//go:build integration
// +build integration

package config

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func (f *readoptFixture) pool() interface {
	Begin(context.Context) (pgx.Tx, error)
} {
	f.t.Helper()
	conn, ok := f.store.db.(*database.Connection)
	require.True(f.t, ok)
	return conn.Pool()
}

// readopt runs the CAS in its own committed transaction.
func (f *readoptFixture) readopt(execID string, date time.Time, requireDifferentApprover bool) (*PurchaseExecution, error) {
	f.t.Helper()
	tx, err := f.pool().Begin(f.ctx)
	require.NoError(f.t, err)
	got, err := f.store.ReadoptExpiredRootTx(f.ctx, tx, execID, date, f.now, requireDifferentApprover)
	if err != nil {
		require.NoError(f.t, tx.Rollback(f.ctx))
		return nil, err
	}
	require.NoError(f.t, tx.Commit(f.ctx))
	return got, nil
}

type readoptStored struct {
	status, hash, source, idemKey string
	recs                          string
	upfront                       float64
	marked                        bool
	hasTokenExpiry, hasNotifSent  bool
	date                          time.Time
	creator                       *string
	system                        bool
}

func (f *readoptFixture) stored(execID string) readoptStored {
	f.t.Helper()
	var s readoptStored
	require.NoError(f.t, f.store.db.QueryRow(f.ctx, `
		SELECT status, COALESCE(approval_token_hash, ''), source, idempotency_key, recommendations::text,
		       total_upfront_cost, approval_readopted_at IS NOT NULL, approval_token_expires_at IS NOT NULL,
		       notification_sent IS NOT NULL, scheduled_date, created_by_user_id::text, created_by_system
		  FROM purchase_executions WHERE execution_id = $1`, execID).Scan(
		&s.status, &s.hash, &s.source, &s.idemKey, &s.recs, &s.upfront, &s.marked, &s.hasTokenExpiry,
		&s.hasNotifSent, &s.date, &s.creator, &s.system))
	return s
}

// dirty gives an expired root the leftovers of its first life: recommendations,
// a token and a notification stamp.
func (f *readoptFixture) dirty(execID string) {
	f.t.Helper()
	f.exec(`UPDATE purchase_executions SET recommendations = '[{"id":"old"}]'::jsonb, approval_token_hash = 'old-hash',
		approval_token_expires_at = now() + interval '1 day', notification_sent = now(), total_upfront_cost = 5 WHERE execution_id = $1`, execID)
}

func TestReadoptExpiredRootTx_WinsOnceAndResetsOnlyTheApprovalState(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	_, id := f.seed(readoptCase{name: "wins"})
	f.dirty(id)
	before := f.stored(id)
	newDate := f.now.Add(3 * readoptDay)

	got, err := f.readopt(id, newDate, true)
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status)
	require.True(t, newDate.Equal(got.ScheduledDate), "the date is the one given")
	require.Empty(t, got.ApprovalToken)
	require.Nil(t, got.ApprovalTokenExpiresAt)
	require.Empty(t, got.Recommendations)

	after := f.stored(id)
	require.Equal(t, "pending", after.status)
	require.Empty(t, after.hash, "the old token must not validate")
	require.False(t, after.hasTokenExpiry)
	require.False(t, after.hasNotifSent)
	require.Equal(t, "[]", after.recs)
	require.Zero(t, after.upfront)
	require.True(t, after.marked)
	require.True(t, newDate.Equal(after.date))
	// 4-eyes identity and provenance are untouched.
	require.Equal(t, before.creator, after.creator)
	require.Equal(t, before.system, after.system)
	require.Equal(t, before.source, after.source)
	require.Equal(t, before.idemKey, after.idemKey)

	// The once: a second call loses, even when the row is expired again.
	_, err = f.readopt(id, newDate, true)
	require.ErrorIs(t, err, ErrExecutionNotInExpectedStatus)
	f.exec(`UPDATE purchase_executions SET status = 'expired', updated_at = $2 WHERE execution_id = $1`, id, f.now.Add(-time.Hour))
	_, err = f.readopt(id, newDate, true)
	require.ErrorIs(t, err, ErrExecutionNotInExpectedStatus)
	require.True(t, f.stored(id).marked)
}

// Every row-level predicate loses the CAS and leaves the row untouched. The
// plan-level gates (managed, disabled, next step, auto + 4-eyes) live in the
// caller and are not asserted here.
func TestReadoptExpiredRootTx_LosesOnEachRowPredicate(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	cases := []readoptCase{
		{name: "not_expired_pending", status: "pending"},
		{name: "not_expired_notified", status: "notified"},
		{name: "not_expired_failed", status: StatusFailed},
		{name: "not_expired_canceled", status: StatusCanceled},
		{name: "readopted", readopted: true},
		{name: "90_days_old", age: 90 * readoptDay},
		{name: "account_row", account: true},
		{name: "retry_row", retryN: 1},
		{name: "retry_linked", retryLinked: true},
		{name: "ladder_row", ladder: true},
		{name: "executed", executed: true},
		{name: "approved_by", approvedBy: true},
		{name: "completed", completed: true},
		{name: "scheduled_execution_at", scheduled: true},
		{name: "pre_approval_skip_reason", skipReason: true},
		{name: "second_row_at_step", sibling: true},
		{name: "nil_creator_under_4eyes", creator: "none", require: true},
	}
	for _, tc := range cases {
		_, id := f.seed(tc)
		f.dirty(id)
		before := f.stored(id)
		_, err := f.readopt(id, f.now.Add(3*readoptDay), tc.require)
		require.ErrorIs(t, err, ErrExecutionNotInExpectedStatus, tc.name)
		require.Equal(t, before, f.stored(id), tc.name+": a lost CAS writes nothing")
	}
}

// The same shapes that win: a nil creator without 4-eyes, and a system root.
func TestReadoptExpiredRootTx_WinsForApprovableRoots(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	for _, tc := range []readoptCase{
		{name: "nil_creator_without_4eyes", creator: "none"},
		{name: "system_root_with_4eyes", creator: "system", require: true},
		{name: "29_days_old", age: 29 * readoptDay},
	} {
		_, id := f.seed(tc)
		_, err := f.readopt(id, f.now.Add(readoptDay), tc.require)
		require.NoError(t, err, tc.name)
	}
}

// Before the migration the CAS must refuse without naming the column.
func TestReadoptExpiredRootTx_BeforeTheMigration(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	_, id := f.seed(readoptCase{name: "adoptable"})

	conn, ok := f.store.db.(*database.Connection)
	require.True(t, ok)
	require.NoError(t, migrations.MigrateToVersion(ctx, conn.Pool(), getTestMigrationsPath(), migrationBelow(t, readoptMigrationVersion)))

	_, err := f.readopt(id, f.now.Add(readoptDay), true)
	require.ErrorIs(t, err, ErrReadoptNotReady)
}
