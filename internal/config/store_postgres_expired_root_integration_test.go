//go:build integration
// +build integration

package config

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const readoptDay = 24 * time.Hour

// readoptCase is one plan with one expired root at its next step. The list
// returns one flag per plan, so each case owns its plan and a probe on any one
// predicate flips exactly one case.
type readoptCase struct {
	name        string
	auto        bool
	disabled    bool
	managed     bool
	currentStep int           // plan current_step; the root sits at currentStep+1 unless step is set
	step        int           // execution step_number; 0 means the plan's next step
	status      string        // "" means expired
	age         time.Duration // time since expiry; 0 means one hour
	account     bool          // child row of a fanned-out step
	retryN      int           // retry_attempt_n
	retryLinked bool          // retry_execution_id set
	ladder      bool          // ladder_run_id set
	approvedBy  bool
	executed    bool
	completed   bool
	scheduled   bool // scheduled_execution_at set
	skipReason  bool // pre_approval_skip_reason set
	readopted   bool
	sibling     bool   // a second row at the same step
	creator     string // "" or "user" (a user), "none" (NULL, not system), "system"
	require     bool   // requireDifferentApprover passed to the store
	want        bool
}

type readoptFixture struct {
	t      *testing.T
	ctx    context.Context
	store  *PostgresStore
	now    time.Time
	userID string
}

func newReadoptFixture(ctx context.Context, t *testing.T) *readoptFixture {
	t.Helper()
	f := &readoptFixture{t: t, ctx: ctx, store: setupRampStepStore(ctx, t), now: time.Now().UTC().Truncate(time.Millisecond)}
	require.NoError(t, f.store.db.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, group_ids)
		 SELECT 'readopt-fixture@example.com', 'x', ARRAY[id] FROM groups ORDER BY name LIMIT 1 RETURNING id`).Scan(&f.userID))
	// The trigger stamps updated_at on every UPDATE; the cases need to set the
	// expiry time themselves.
	_, err := f.store.db.Exec(ctx, `ALTER TABLE purchase_executions DISABLE TRIGGER update_purchase_executions_updated_at`)
	require.NoError(t, err)
	return f
}

// seed creates the plan and its expired root (plus a sibling if asked) and
// returns the plan ID and the root's execution ID.
func (f *readoptFixture) seed(tc readoptCase) (planID, execID string) {
	f.t.Helper()
	maxStep := 1e9
	plan := &PurchasePlan{
		Name: "readopt-" + tc.name, Enabled: !tc.disabled, AutoPurchase: tc.auto, NotificationDaysBefore: 3, MaxStepCommitment: &maxStep,
		Services: map[string]ServiceConfig{},
		RampSchedule: RampSchedule{Type: "weekly", PercentPerStep: 25, StepIntervalDays: 7, CurrentStep: tc.currentStep,
			TotalSteps: 4, StartDate: f.now.AddDate(0, 0, -14), DatesManaged: tc.managed},
	}
	require.NoError(f.t, f.store.CreatePurchasePlan(f.ctx, plan))
	step := tc.step
	if step == 0 {
		step = tc.currentStep + 1
	}
	status := tc.status
	if status == "" {
		status = StatusExpired
	}
	exec := &PurchaseExecution{PlanID: plan.ID, ExecutionID: uuid.NewString(), StepNumber: step, Status: status,
		ScheduledDate: f.now.Add(-8 * readoptDay), RetryAttemptN: tc.retryN}
	switch tc.creator {
	case "", "user":
		exec.CreatedByUserID = &f.userID
	case "system":
		exec.CreatedBySystem = true
	}
	if tc.account {
		var acct string
		require.NoError(f.t, f.store.db.QueryRow(f.ctx,
			`INSERT INTO cloud_accounts (name, provider, external_id, enabled) VALUES ($1, 'aws', $2, true) RETURNING id`,
			"acct-"+tc.name, uuid.NewString()[:12]).Scan(&acct))
		exec.CloudAccountID = &acct
	}
	require.NoError(f.t, f.store.SavePurchaseExecution(f.ctx, exec))

	age := tc.age
	if age == 0 {
		age = time.Hour
	}
	f.exec(`UPDATE purchase_executions SET updated_at = $2 WHERE execution_id = $1`, exec.ExecutionID, f.now.Add(-age))
	if tc.retryLinked {
		other := f.otherExecution()
		f.exec(`UPDATE purchase_executions SET retry_execution_id = $2 WHERE execution_id = $1`, exec.ExecutionID, other)
	}
	if tc.ladder {
		var runID string
		require.NoError(f.t, f.store.db.QueryRow(f.ctx, `INSERT INTO ladder_runs (status) VALUES ('planned') RETURNING id`).Scan(&runID))
		f.exec(`UPDATE purchase_executions SET ladder_run_id = $2 WHERE execution_id = $1`, exec.ExecutionID, runID)
	}
	if tc.approvedBy {
		f.exec(`UPDATE purchase_executions SET approved_by = 'someone@example.com' WHERE execution_id = $1`, exec.ExecutionID)
	}
	if tc.executed {
		f.exec(`UPDATE purchase_executions SET executed_at = now() WHERE execution_id = $1`, exec.ExecutionID)
	}
	if tc.completed {
		f.exec(`UPDATE purchase_executions SET completed_at = now() WHERE execution_id = $1`, exec.ExecutionID)
	}
	if tc.scheduled {
		f.exec(`UPDATE purchase_executions SET scheduled_execution_at = now() WHERE execution_id = $1`, exec.ExecutionID)
	}
	if tc.skipReason {
		f.exec(`UPDATE purchase_executions SET pre_approval_skip_reason = 'x' WHERE execution_id = $1`, exec.ExecutionID)
	}
	if tc.readopted {
		f.exec(`UPDATE purchase_executions SET approval_readopted_at = now() WHERE execution_id = $1`, exec.ExecutionID)
	}
	if tc.sibling {
		require.NoError(f.t, f.store.SavePurchaseExecution(f.ctx, &PurchaseExecution{PlanID: plan.ID, ExecutionID: uuid.NewString(),
			StepNumber: step, Status: StatusFailed, ScheduledDate: f.now.Add(-8 * readoptDay), RetryAttemptN: 1}))
	}
	return plan.ID, exec.ExecutionID
}

func (f *readoptFixture) exec(query string, args ...any) {
	f.t.Helper()
	_, err := f.store.db.Exec(f.ctx, query, args...)
	require.NoError(f.t, err)
}

// otherExecution creates a row in a throwaway plan, as the target of a retry link.
func (f *readoptFixture) otherExecution() string {
	f.t.Helper()
	plan := &PurchasePlan{Name: "readopt-other-" + uuid.NewString()[:8], Services: map[string]ServiceConfig{},
		RampSchedule: RampSchedule{Type: "weekly", PercentPerStep: 25, StepIntervalDays: 7, TotalSteps: 4, StartDate: f.now}}
	require.NoError(f.t, f.store.CreatePurchasePlan(f.ctx, plan))
	id := uuid.NewString()
	require.NoError(f.t, f.store.SavePurchaseExecution(f.ctx, &PurchaseExecution{PlanID: plan.ID, ExecutionID: id,
		StepNumber: 9, Status: StatusFailed, ScheduledDate: f.now}))
	return id
}

// readoptCases are shared by the list test (want) and the CAS test (the same
// shapes must lose or win the same way).
func readoptCases() []readoptCase {
	return []readoptCase{
		{name: "flag_adoptable", want: true},
		{name: "flag_second_step_plan", currentStep: 1, want: true},
		{name: "flag_29_days_old", age: 29 * readoptDay, want: true},
		{name: "flag_manual_plan_no_4eyes", require: false, want: true},
		{name: "flag_manual_plan_4eyes", require: true, want: true},
		{name: "flag_auto_plan_4eyes_on", auto: true, require: true, want: true},
		{name: "flag_system_root_4eyes", creator: "system", require: true, want: true},
		{name: "flag_nil_creator_without_4eyes", creator: "none", require: false, want: true},

		{name: "skip_90_days_old", age: 90 * readoptDay},
		{name: "skip_nil_creator_with_4eyes", creator: "none", require: true},
		{name: "skip_auto_plan_4eyes_off", auto: true, require: false},
		{name: "skip_canceled_root", status: StatusCanceled},
		{name: "skip_failed_root", status: StatusFailed},
		{name: "skip_partially_completed_root", status: StatusPartiallyCompleted},
		{name: "skip_completed_root", status: StatusCompleted},
		{name: "skip_running_root", status: "running"},
		{name: "skip_approved_root", status: "approved"},
		{name: "skip_pending_root", status: "pending"},
		{name: "skip_notified_root", status: "notified"},
		{name: "skip_child_row_with_account", account: true},
		{name: "skip_retry_row", retryN: 1},
		{name: "skip_retry_linked_root", retryLinked: true},
		{name: "skip_ladder_row", ladder: true},
		{name: "skip_second_row_at_step", sibling: true},
		{name: "skip_not_the_next_step", step: 3},
		{name: "skip_dates_managed_plan", managed: true},
		{name: "skip_disabled_plan", disabled: true},
		{name: "skip_already_readopted", readopted: true},
		{name: "skip_approved_by_set", approvedBy: true},
		{name: "skip_executed_at_set", executed: true},
		{name: "skip_completed_at_set", completed: true},
		{name: "skip_scheduled_execution_at_set", scheduled: true},
		{name: "skip_pre_approval_skip_reason_set", skipReason: true},
	}
}

func TestListPlansWithReadoptableExpiredRoot_FlagsExactlyTheReadoptable(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	cases := readoptCases()
	ids := make(map[string]string, len(cases))
	for _, tc := range cases {
		ids[tc.name], _ = f.seed(tc)
	}

	// requireDifferentApprover is a per-call flag, so list once per value and
	// read each case from the call that matches its own setting.
	got := map[bool]map[string]struct{}{}
	for _, require4 := range []bool{false, true} {
		plans, err := f.store.ListPlansWithReadoptableExpiredRoot(ctx, f.now, require4)
		require.NoError(t, err)
		got[require4] = plans
	}
	for _, tc := range cases {
		_, flagged := got[tc.require][ids[tc.name]]
		require.Equal(t, tc.want, flagged, tc.name)
	}
}

// An auto plan is listed only with 4-eyes on; a manual plan either way. This
// is the defense-in-depth clause, so it gets its own plan in both modes.
func TestListPlansWithReadoptableExpiredRoot_AutoPlanNeedsFourEyes(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	autoPlan, _ := f.seed(readoptCase{name: "auto", auto: true})
	manualPlan, _ := f.seed(readoptCase{name: "manual"})

	off, err := f.store.ListPlansWithReadoptableExpiredRoot(ctx, f.now, false)
	require.NoError(t, err)
	require.NotContains(t, off, autoPlan)
	require.Contains(t, off, manualPlan)

	on, err := f.store.ListPlansWithReadoptableExpiredRoot(ctx, f.now, true)
	require.NoError(t, err)
	require.Contains(t, on, autoPlan)
	require.Contains(t, on, manualPlan)
}

// main deploys before the migration is applied: the list must not name the
// column and must report nothing (#1785).
func TestListPlansWithReadoptableExpiredRoot_BeforeTheMigration(t *testing.T) {
	ctx := context.Background()
	f := newReadoptFixture(ctx, t)
	f.seed(readoptCase{name: "adoptable"})

	conn, ok := f.store.db.(*database.Connection)
	require.True(t, ok)
	require.NoError(t, migrations.MigrateToVersion(ctx, conn.Pool(), getTestMigrationsPath(), migrationBelow(t, readoptMigrationVersion)))
	var cols int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'purchase_executions' AND column_name = 'approval_readopted_at'`).Scan(&cols))
	require.Zero(t, cols, "precondition: the column is gone")

	got, err := f.store.ListPlansWithReadoptableExpiredRoot(ctx, f.now, true)
	require.NoError(t, err)
	require.Empty(t, got)
}
