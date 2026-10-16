//go:build integration

package config

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func rootsFixture(ctx context.Context, t *testing.T, set string) (*rampFixture, *PurchaseExecution, string) {
	t.Helper()
	f := newRampFixture(ctx, t, 1, 0, 3)
	root := f.row(ctx, t, 1, nil, "pending")
	f.row(ctx, t, 9, nil, "pending")
	for _, assign := range []string{`scheduled_date=now()+interval '20 days'`, `updated_at=updated_at` + set} {
		_, err := f.store.db.Exec(ctx, `UPDATE purchase_executions SET `+assign+` WHERE execution_id=$1`, root.ExecutionID)
		require.NoError(t, err)
	}
	var actor string
	require.NoError(t, f.store.db.QueryRow(ctx, `INSERT INTO users (email, password_hash, salt, group_ids) VALUES ($1,'x','x',ARRAY[uuid_generate_v4()]) RETURNING id::text`, uuid.NewString()+"@example.test").Scan(&actor))
	return f, root, actor
}

// applyRoots runs cancel then insert in one transaction against a freshly captured window.
func applyRoots(ctx context.Context, f *rampFixture, actor string, tweak func(planScheduleWindows), changes []PlanScheduleRootChange) ([]*string, error) {
	var ids []*string
	err := f.store.WithTx(ctx, func(tx pgx.Tx) error {
		before, err := f.store.readPlanEditorSnapshotTx(ctx, tx, f.planID)
		if err != nil {
			return err
		}
		windows := capturePlanScheduleWindows(before)
		if tweak != nil {
			tweak(windows)
		}
		if err = f.store.cancelPlanScheduleRootsTx(ctx, tx, windows, changes, &actor); err != nil {
			return err
		}
		ids, err = insertPlanScheduleRootsTx(ctx, tx, f.planID, changes, &actor)
		return err
	})
	return ids, err
}

type windowTweak = func(*rampFixture, *PurchaseExecution, planScheduleWindows)

func errOf(_ []*string, err error) error { return err }

func replaceRoot(root *PurchaseExecution) []PlanScheduleRootChange {
	next := time.Now().UTC().AddDate(0, 0, 40)
	return []PlanScheduleRootChange{{OldExecutionID: &root.ExecutionID, StepNumber: root.StepNumber, ScheduledDate: &next}}
}

func TestPlanScheduleRootCancelCASAcceptsEligibleRootsAndInsertsReplacement(t *testing.T) {
	ctx := context.Background()
	for name, set := range map[string]string{"pending": "", "scheduled": `, status='scheduled', scheduled_execution_at=now()+interval '5 days'`} {
		t.Run(name, func(t *testing.T) {
			f, root, actor := rootsFixture(ctx, t, set)
			require.NoError(t, f.store.CreateSuppression(ctx, &PurchaseSuppression{ExecutionID: root.ExecutionID, AccountID: f.accounts[0],
				Provider: "aws", Service: "ec2", Region: "r", ResourceType: "t", SuppressedCount: 1, ExpiresAt: time.Now().Add(time.Hour)}))
			changes := replaceRoot(root)
			ids, err := applyRoots(ctx, f, actor, nil, changes)
			require.NoError(t, err)
			var by string
			var suppressions int
			require.NoError(t, f.store.db.QueryRow(ctx, `SELECT canceled_by, (SELECT count(*) FROM purchase_suppressions WHERE execution_id=$1)
				FROM purchase_executions WHERE execution_id=$1`, root.ExecutionID).Scan(&by, &suppressions))
			require.Equal(t, actor, by)
			require.Zero(t, suppressions)
			var id, creator string
			var step int
			var date time.Time
			require.NoError(t, f.store.db.QueryRow(ctx, `SELECT execution_id::text, step_number, scheduled_date, created_by_user_id::text
				FROM purchase_executions WHERE plan_id=$1 AND status='pending' AND step_number<9`, f.planID).Scan(&id, &step, &date, &creator))
			require.Equal(t, [3]any{*ids[0], 1, actor}, [3]any{id, step, creator})
			require.True(t, changes[0].ScheduledDate.Equal(date))
		})
	}
}

func TestPlanScheduleRootCancelCASLosesAndRollsBackEverything(t *testing.T) {
	ctx := context.Background()
	link := func(f *rampFixture, root *PurchaseExecution, _ planScheduleWindows) {
		_, err := f.store.db.Exec(ctx, `UPDATE purchase_executions SET retry_attempt_n=1, retry_execution_id=$2 WHERE execution_id=$1`, f.row(ctx, t, 2, nil, "pending").ExecutionID, root.ExecutionID)
		require.NoError(t, err)
	}
	edit := func(fn func(*planScheduleWindow)) windowTweak {
		return func(_ *rampFixture, root *PurchaseExecution, w planScheduleWindows) {
			window := w[root.ExecutionID]
			fn(&window)
			w[root.ExecutionID] = window
		}
	}
	cases := map[string]struct {
		set   string
		tweak windowTweak
	}{
		"cloud account":      {set: `, cloud_account_id=(SELECT id FROM cloud_accounts LIMIT 1)`},
		"retry attempt":      {set: `, retry_attempt_n=1`},
		"idempotency":        {set: `, idempotency_key='plan:1'`},
		"executed":           {set: `, executed_at=now()`},
		"completed":          {set: `, completed_at=now()`},
		"skip reason":        {set: `, pre_approval_skip_reason='held'`},
		"past date":          {set: `, scheduled_date=now()-interval '1 day'`},
		"status":             {set: `, status='completed'`},
		"pending fire":       {set: `, scheduled_execution_at=now()+interval '5 days'`},
		"scheduled stale":    {set: `, status='scheduled', scheduled_execution_at=now()-interval '1 minute'`},
		"retry target":       {set: `, retry_execution_id=(SELECT execution_id FROM purchase_executions WHERE step_number=9)`},
		"incoming reference": {tweak: link},
		"updated_at":         {tweak: edit(func(w *planScheduleWindow) { w.version = w.version.Add(-time.Microsecond) })},
		"captured status":    {tweak: edit(func(w *planScheduleWindow) { w.execution.Status = "notified" })},
		"plan":               {tweak: edit(func(w *planScheduleWindow) { w.execution.PlanID = uuid.NewString() })},
		"step":               {tweak: edit(func(w *planScheduleWindow) { w.execution.StepNumber = 2 })},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f, root, actor := rootsFixture(ctx, t, "")
			sibling := f.row(ctx, t, 1, nil, "pending")
			_, err := f.store.db.Exec(ctx, `UPDATE purchase_executions SET scheduled_date=now()+interval '20 days' WHERE execution_id=$1`, sibling.ExecutionID)
			require.NoError(t, err)
			_, err = f.store.db.Exec(ctx, `UPDATE purchase_executions SET updated_at=updated_at`+c.set+` WHERE execution_id=$1`, root.ExecutionID)
			require.NoError(t, err)
			_, err = applyRoots(ctx, f, actor, func(w planScheduleWindows) {
				if c.tweak != nil {
					c.tweak(f, root, w)
				}
			}, append([]PlanScheduleRootChange{{OldExecutionID: &sibling.ExecutionID, StepNumber: 1}}, replaceRoot(root)...))
			require.ErrorIs(t, err, ErrPurchasePlanConflict)
			var siblingStatus string
			require.NoError(t, f.store.db.QueryRow(ctx, `SELECT status FROM purchase_executions WHERE execution_id=$1`, sibling.ExecutionID).Scan(&siblingStatus))
			require.Equal(t, "pending", siblingStatus, "the earlier cancel must roll back")
			var canceled, inserted int
			require.NoError(t, f.store.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='canceled'), count(*) FILTER (WHERE scheduled_date>now()+interval '30 days')
				FROM purchase_executions WHERE plan_id=$1`, f.planID).Scan(&canceled, &inserted))
			require.Zero(t, canceled+inserted)
		})
	}
}

func TestPlanScheduleRootsRefuseDuplicateOldIDAndBadSteps(t *testing.T) {
	ctx := context.Background()
	f, root, actor := rootsFixture(ctx, t, "")
	changes := append(replaceRoot(root), PlanScheduleRootChange{OldExecutionID: &root.ExecutionID, StepNumber: 1})
	require.ErrorIs(t, errOf(applyRoots(ctx, f, actor, nil, changes)), ErrPurchasePlanConflict)
	var status string
	require.NoError(t, f.store.db.QueryRow(ctx, `SELECT status FROM purchase_executions WHERE execution_id=$1`, root.ExecutionID).Scan(&status))
	require.Equal(t, "pending", status, "the first cancel must roll back with the failing second")
	date := time.Now().UTC().AddDate(0, 0, 40)
	require.ErrorIs(t, errOf(applyRoots(ctx, f, actor, nil, []PlanScheduleRootChange{{StepNumber: 0, ScheduledDate: &date}})), ErrPurchasePlanConflict)
	require.NoError(t, errOf(applyRoots(ctx, f, actor, nil, []PlanScheduleRootChange{{StepNumber: 2, ScheduledDate: &date}, {StepNumber: 3}})))
	var n int
	require.NoError(t, f.store.db.QueryRow(ctx, `SELECT count(*) FROM purchase_executions WHERE plan_id=$1 AND step_number BETWEEN 2 AND 8`, f.planID).Scan(&n))
	require.Equal(t, 1, n, "a change without a date inserts nothing")
}

func TestGuardPlanScheduleClockTxRechecksEveryWindow(t *testing.T) {
	ctx := context.Background()
	store := setupRampStepStore(ctx, t)
	day := func(hours float64) *time.Time {
		v := time.Now().Add(time.Duration(hours * float64(time.Hour)))
		return &v
	}
	old := func(date, fire *time.Time) planScheduleWindows {
		return planScheduleWindows{"old": {execution: PurchaseExecution{ScheduledDate: *date, ScheduledExecutionAt: fire}}}
	}
	id := "old"
	cases := map[string]struct {
		windows planScheduleWindows
		notice  int
		change  PlanScheduleRootChange
		ok      bool
	}{
		"beyond notice":       {nil, 2, PlanScheduleRootChange{ScheduledDate: day(72)}, true},
		"inside notice":       {nil, 2, PlanScheduleRootChange{ScheduledDate: day(36)}, false},
		"past":                {nil, 0, PlanScheduleRootChange{ScheduledDate: day(-1)}, false},
		"notice negative":     {nil, -1, PlanScheduleRootChange{ScheduledDate: day(72)}, false},
		"notice too large":    {nil, MaxNotificationDaysBefore + 1, PlanScheduleRootChange{ScheduledDate: day(24 * 90)}, false},
		"cancel only, notice": {old(day(48), nil), 99, PlanScheduleRootChange{OldExecutionID: &id}, true},
		"old date past":       {old(day(-1), nil), 1, PlanScheduleRootChange{OldExecutionID: &id}, false},
		"old fire past":       {old(day(48), day(-1)), 1, PlanScheduleRootChange{OldExecutionID: &id}, false},
		"old fire future":     {old(day(48), day(5)), 1, PlanScheduleRootChange{OldExecutionID: &id}, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := store.WithTx(ctx, func(tx pgx.Tx) error {
				return guardPlanScheduleClockTx(ctx, tx, c.windows, c.notice, []PlanScheduleRootChange{c.change})
			})
			if c.ok {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrPurchasePlanConflict)
			}
		})
	}
}
