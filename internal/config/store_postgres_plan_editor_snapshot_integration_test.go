//go:build integration

package config

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestPlanEditorSnapshot_ContentAndProvenance(t *testing.T) {
	ctx := context.Background()
	f := newRampFixture(ctx, t, 4, 0, 4)
	r := baselineRec(f.accounts[2], "m5.large", 20)
	r.Selected = false
	key := baselineKey(t, baselineRec(f.accounts[3], "m5.large", 100))
	var failed *PurchaseExecution
	for i, status := range []string{"pending", StatusCanceled, StatusFailed} {
		failed = f.rowWithRecs(ctx, t, i+1, &f.accounts[1], status, false, baselineRec(f.accounts[0], "m5.large", 20), r)
	}
	f.retryInOneTx(ctx, t, failed, uuid.NewString(), StatusCompleted)
	f.row(ctx, t, 1, nil, "pending")
	f.row(ctx, t, 2, nil, StatusCanceled)
	require.NoError(t, f.store.SetPlanAccounts(ctx, f.planID, f.accounts[:1]))
	require.NoError(t, f.store.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := f.store.SetPlanRampBaselineTx(ctx, tx, f.planID, PlanRampBaseline{key: {Count: 100}})
		return err
	}))
	stored := func() string {
		var raw string
		require.NoError(t, f.store.db.QueryRow(ctx, `SELECT jsonb_build_array((SELECT jsonb_agg(p) FROM purchase_plans p), (SELECT jsonb_agg(e ORDER BY execution_id) FROM purchase_executions e), (SELECT jsonb_agg(a ORDER BY id) FROM cloud_accounts a), (SELECT jsonb_agg(pa ORDER BY account_id) FROM plan_accounts pa))::text`).Scan(&raw))
		return raw
	}
	beforeRead := stored()
	first, err := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
	require.NoError(t, err)
	require.NotNil(t, first.Plan, "reader must return the persisted plan")
	require.Len(t, first.Executions, 6)
	require.Len(t, first.Accounts, 4, "association, row-only, recommendation-only and baseline-only accounts")
	require.Equal(t, f.accounts[:1], first.AccountIDs)
	require.Equal(t, f.baseline(ctx, t), first.Baseline)
	require.Equal(t, f.committed(ctx, t), first.Committed)
	second, err := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
	require.NoError(t, err)
	require.Equal(t, first, second, "unchanged reads must preserve all captured state")
	require.Equal(t, beforeRead, stored())
	require.Len(t, first.executionVersions, len(first.Executions))
	for i, version := range first.executionVersions {
		require.Equal(t, first.Executions[i].ExecutionID, version.ID)
		require.True(t, f.updatedAt(ctx, t, version.ID).Equal(version.UpdatedAt))
	}
	for i := 1; i < len(first.Executions); i++ {
		require.Less(t, first.Executions[i-1].ExecutionID, first.Executions[i].ExecutionID)
	}
	fail := func(planID string, want error) {
		before := stored()
		s, readErr := f.store.ReadPlanEditorSnapshot(ctx, planID)
		require.ErrorIs(t, readErr, want)
		require.Nil(t, s)
		require.Equal(t, before, stored(), "failed reads must not repair persisted rows")
		require.Zero(t, f.store.db.(*database.Connection).Pool().Stat().AcquiredConns())
	}
	for _, bad := range []string{"", "broken", strings.Replace(key, f.accounts[3], "", 1), strings.Replace(key, f.accounts[3], uuid.NewString(), 1)} {
		raw, marshalErr := json.Marshal(PlanRampBaseline{bad: {Count: 1}})
		require.NoError(t, marshalErr)
		_, err = f.store.db.Exec(ctx, `UPDATE purchase_plans SET ramp_baseline=$2 WHERE id=$1`, f.planID, raw)
		require.NoError(t, err)
		fail(f.planID, ErrPlanEditorAttribution)
	}
	_, err = f.store.db.Exec(ctx, `UPDATE purchase_plans SET ramp_baseline=NULL WHERE id=$1`, f.planID)
	require.NoError(t, err)
	for _, malformedDetails := range []bool{false, true} {
		bad := r
		bad.Selected = false
		if malformedDetails {
			bad.Details = json.RawMessage(`{"platform":42}`)
		} else {
			bad.CloudAccountID = nil
		}
		e := f.rowWithRecs(ctx, t, 1, &f.accounts[0], StatusCanceled, false, bad)
		fail(f.planID, ErrPlanEditorAttribution)
		e.Recommendations = nil
		require.NoError(t, f.store.SavePurchaseExecution(ctx, e))
	}
	require.NoError(t, f.store.SetPlanAccounts(ctx, f.planID, nil))
	fail(f.planID, ErrPlanEditorAttribution)
	fail(uuid.NewString(), ErrNotFound)
}

type editorPausedDB struct {
	dbConn
	read, resume chan struct{}
}

func (d editorPausedDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.dbConn.Begin(ctx)
	return editorPausedTx{Tx: tx, read: d.read, resume: d.resume}, err
}

type editorPausedTx struct {
	pgx.Tx
	read, resume chan struct{}
}

func (tx editorPausedTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	if strings.Contains(query, "FROM plan_accounts") {
		close(tx.read)
		select {
		case <-tx.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return tx.Tx.Query(ctx, query, args...)
}

func TestPlanEditorSnapshot_RepeatableReadAndCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newRampFixture(ctx, t, 2, 0, 4)
	e := f.row(ctx, t, 1, &f.accounts[0], "pending")
	db := f.store.db
	pool := db.(*database.Connection).Pool()
	for _, abort := range []bool{false, true} {
		before, err := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
		require.NoError(t, err)
		paused := editorPausedDB{dbConn: db, read: make(chan struct{}), resume: make(chan struct{})}
		reader := &PostgresStore{db: paused}
		readCtx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		var got *PlanEditorSnapshot
		var readErr error
		go func() { defer close(done); got, readErr = reader.ReadPlanEditorSnapshot(readCtx, f.planID) }()
		select {
		case <-paused.read:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if abort {
			stop()
		} else {
			require.NoError(t, f.store.WithTx(ctx, func(tx pgx.Tx) error {
				_, writeErr := tx.Exec(ctx, `UPDATE purchase_plans SET name='concurrent' WHERE id=$1`, f.planID)
				if writeErr != nil {
					return writeErr
				}
				_, writeErr = tx.Exec(ctx, `UPDATE purchase_executions SET status='completed' WHERE execution_id=$1`, e.ExecutionID)
				if writeErr != nil {
					return writeErr
				}
				_, writeErr = tx.Exec(ctx, `DELETE FROM plan_accounts WHERE plan_id=$1 AND account_id=$2`, f.planID, f.accounts[1])
				return writeErr
			}))
			close(paused.resume)
		}
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		stop()
		after, afterErr := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
		require.NoError(t, afterErr)
		if abort {
			require.Error(t, readErr)
			require.Nil(t, got)
			require.Equal(t, before, after, "canceled read must leave state unchanged")
		} else {
			require.NoError(t, readErr)
			require.Equal(t, before, got, "all fields must be from before the atomic writer")
			require.Equal(t, "concurrent", after.Plan.Name)
			require.Len(t, after.AccountIDs, 1)
			require.Equal(t, StatusCompleted, after.Executions[0].Status)
		}
		require.Eventually(t, func() bool { return pool.Stat().AcquiredConns() == 0 }, time.Second, time.Millisecond)
	}
}

func TestPlanEditorSnapshot_CompleteOrFailBound(t *testing.T) {
	ctx := context.Background()
	f := newRampFixture(ctx, t, 1, 0, 4)
	_, err := f.store.db.Exec(ctx, `INSERT INTO purchase_executions(plan_id, execution_id, status, step_number, scheduled_date, recommendations, error) SELECT $1, gen_random_uuid(), 'pending', 1, NOW(), '[]'::jsonb, '' FROM generate_series(1,$2)`, f.planID, MaxListLimit+1)
	require.NoError(t, err)
	s, err := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
	require.ErrorIs(t, err, ErrPlanEditorUnsupportedSize)
	require.Nil(t, s, "oversize history must not return a partial graph")
	require.Empty(t, f.committed(ctx, t), "existing unbounded ledger still succeeds")
}

func TestPlanEditorRevision_ActualPersistedABA(t *testing.T) {
	ctx := context.Background()
	f := newRampFixture(ctx, t, 1, 0, 4)
	e := f.rowWithRecs(ctx, t, 1, &f.accounts[0], "pending", false, baselineRec(f.accounts[0], "m5.large", 20))
	read := func() (*PlanEditorSnapshot, string) {
		s, err := f.store.ReadPlanEditorSnapshot(ctx, f.planID)
		require.NoError(t, err)
		revision, err := s.Revision()
		require.NoError(t, err)
		return s, revision
	}
	initial, initialRevision := read()
	repeated, repeatedRevision := read()
	require.Equal(t, initial, repeated)
	require.Equal(t, initialRevision, repeatedRevision)
	edited := *initial.Plan
	edited.Name += " changed"
	require.NoError(t, f.store.UpdatePurchasePlan(ctx, &edited))
	changed, changedRevision := read()
	require.True(t, changed.Plan.UpdatedAt.After(initial.Plan.UpdatedAt))
	require.NotEqual(t, initialRevision, changedRevision)
	edited.Name = initial.Plan.Name
	require.NoError(t, f.store.UpdatePurchasePlan(ctx, &edited))
	restored, restoredRevision := read()
	require.True(t, restored.Plan.UpdatedAt.After(changed.Plan.UpdatedAt))
	require.NotEqual(t, changedRevision, restoredRevision)
	require.NotEqual(t, initialRevision, restoredRevision, "restored plan content retains its advanced row version")
	content := *restored.Plan
	content.UpdatedAt = initial.Plan.UpdatedAt
	require.Equal(t, initial.Plan, &content)
	e.Error = "intermediate execution state"
	require.NoError(t, f.store.SavePurchaseExecution(ctx, e))
	executionChanged, executionChangedRevision := read()
	require.False(t, executionChanged.executionVersions[0].UpdatedAt.Equal(restored.executionVersions[0].UpdatedAt))
	require.NotEqual(t, restoredRevision, executionChangedRevision)
	e.Error = ""
	require.NoError(t, f.store.SavePurchaseExecution(ctx, e))
	executionRestored, executionRestoredRevision := read()
	require.False(t, executionRestored.executionVersions[0].UpdatedAt.Equal(executionChanged.executionVersions[0].UpdatedAt))
	require.Equal(t, restored.Executions, executionRestored.Executions)
	require.Equal(t, restored.Plan, executionRestored.Plan)
	require.NotEqual(t, executionChangedRevision, executionRestoredRevision)
	require.NotEqual(t, restoredRevision, executionRestoredRevision, "observed execution version change survives restored content")
}
