//go:build integration

package config

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type persistFixture struct {
	store    *PostgresStore
	account  string
	identity PlanScheduleCommandIdentity
	day      time.Time
	mapping  []PlanScheduleMapping
}

type editBuilder func(*PlanEditorSnapshot) *PlanScheduleMutation

func newPersistFixture(ctx context.Context, t *testing.T) *persistFixture {
	t.Helper()
	store := setupRampStepStore(ctx, t)
	account := &CloudAccount{Name: "editor target", Provider: "aws", ExternalID: uuid.NewString()}
	require.NoError(t, store.CreateCloudAccount(ctx, account))
	return &persistFixture{store: store, account: account.ID, day: time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 10),
		identity: PlanScheduleCommandIdentity{CommandID: uuid.NewString(), PlanID: uuid.NewString(),
			Actor: PlanScheduleActor{Kind: PlanScheduleAdminActor}, RequestHash: "sha256:" + strings.Repeat("a", 64),
			ProposedAccountIDs: []string{account.ID}, ProposedProviders: []string{"aws"}}}
}

func (f *persistFixture) create() *PlanScheduleMutation {
	include, exclude := []string(nil), []string{}
	plan := &PurchasePlan{ID: f.identity.PlanID, Name: "atomic editor plan", Enabled: true, NotificationDaysBefore: 1,
		Services: map[string]ServiceConfig{"aws/ec2": {Provider: "aws", Service: "ec2", Enabled: true, Term: 1,
			Payment: "no-upfront", Coverage: 80, ExcludeEngines: []string{}}},
		RampSchedule: RampSchedule{Type: "weekly", PercentPerStep: 100.0 / 3, StepIntervalDays: 7, TotalSteps: 3,
			StartDate: f.day, DatesManaged: true, AllocationMode: RampAllocationEqualCount}}
	roots := make([]PlanScheduleRootChange, 3)
	for i := range roots {
		date := f.day.AddDate(0, 0, i*7)
		roots[i] = PlanScheduleRootChange{StepNumber: i + 1, ScheduledDate: &date}
	}
	return &PlanScheduleMutation{Plan: plan, AccountIDs: []string{f.account}, Roots: roots,
		Intent: PlanScheduleIntent{StepCount: new(3), AccountIDs: &f.identity.ProposedAccountIDs,
			Services: map[string]PlanScheduleServicePatch{"aws/ec2": {Provider: new("aws"), Service: new("ec2"), Enabled: new(true),
				Term: new(1), Payment: new("no-upfront"), Coverage: new(80.0), IncludeEngines: &include, ExcludeEngines: &exclude}},
			Ramp: PlanScheduleRampPatch{Type: new("weekly"), StepIntervalDays: new(7), StartDate: &f.day}},
		Scope: PlanScheduleScope{AccountIDs: []string{f.account}}}
}

// edit patches the service (coverage, plus term when term > 0) and replaces the root of each named step one day later.
func (f *persistFixture) edit(term int, steps ...int) editBuilder {
	return func(before *PlanEditorSnapshot) *PlanScheduleMutation {
		plan := *before.Plan
		service := before.Plan.Services["aws/ec2"]
		service.Coverage = 90
		patch := PlanScheduleServicePatch{Coverage: new(90.0)}
		if term > 0 {
			service.Term, patch.Term = term, &term
		}
		plan.Services = map[string]ServiceConfig{"aws/ec2": service}
		roots := make([]PlanScheduleRootChange, 0, len(steps))
		for _, step := range steps {
			date := f.day.AddDate(0, 0, 7*(step-1)+1)
			old := before.Executions[slices.IndexFunc(before.Executions, func(e PurchaseExecution) bool { return e.StepNumber == step })].ExecutionID
			roots = append(roots, PlanScheduleRootChange{OldExecutionID: &old, StepNumber: step, ScheduledDate: &date})
		}
		return &PlanScheduleMutation{Plan: &plan, AccountIDs: []string{f.account}, Roots: roots, Scope: PlanScheduleScope{AccountIDs: []string{f.account}},
			Intent: PlanScheduleIntent{Services: map[string]PlanScheduleServicePatch{"aws/ec2": patch}}}
	}
}

// run persists and receipts one mutation (a Create when edit is nil) in one transaction, then fails it with fault when set.
func (f *persistFixture) run(ctx context.Context, t *testing.T, edit editBuilder, fault error) (*PlanScheduleCommandResult, error) {
	t.Helper()
	var result *PlanScheduleCommandResult
	err := f.store.WithTx(ctx, func(tx pgx.Tx) error {
		var before *PlanEditorSnapshot
		mutation := f.create()
		if edit != nil {
			var err error
			before, err = f.store.readPlanEditorSnapshotTx(ctx, tx, f.identity.PlanID)
			require.NoError(t, err)
			mutation = edit(before)
			revision, err := before.Revision()
			require.NoError(t, err)
			f.identity.ExpectedRevision = &revision
		}
		var err error
		f.mapping, err = f.store.persistPlanScheduleMutationTx(ctx, tx, before, capturePlanScheduleWindows(before), mutation, nil, time.Now())
		if err != nil {
			return err
		}
		after, err := f.store.readPlanEditorSnapshotTx(ctx, tx, f.identity.PlanID)
		require.NoError(t, err)
		body, err := json.Marshal(map[string]any{"plan_id": after.Plan.ID, "mapping": f.mapping})
		require.NoError(t, err)
		if result, err = insertPlanScheduleReceiptTx(ctx, tx, f.identity, after, mutation.Scope, body); err != nil {
			return err
		}
		return fault
	})
	return result, err
}

func (f *persistFixture) counts(ctx context.Context, t *testing.T) []int {
	t.Helper()
	var plans, accounts, executions, receipts int
	require.NoError(t, f.store.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM purchase_plans WHERE id=$1),
		(SELECT count(*) FROM plan_accounts WHERE plan_id=$1), (SELECT count(*) FROM purchase_executions WHERE plan_id=$1),
		(SELECT count(*) FROM plan_schedule_command_receipts WHERE command_id=$2)`, f.identity.PlanID, f.identity.CommandID).
		Scan(&plans, &accounts, &executions, &receipts))
	return []int{plans, accounts, executions, receipts}
}

// planRow is the stored plan state the Edit tests assert.
type planRow struct {
	ledger, unknown, baseline string
	coverage, term, step      int
	next                      time.Time
	canceled                  int
}

func (f *persistFixture) row(ctx context.Context, t *testing.T) (row planRow) {
	t.Helper()
	require.NoError(t, f.store.db.QueryRow(ctx, `SELECT ramp_ledger_state, COALESCE((services->'aws/ec2'->'future_precision')::text,''),
		COALESCE(ramp_baseline::text,''), (services->'aws/ec2'->>'coverage')::int, (services->'aws/ec2'->>'term')::int,
		(ramp_schedule->>'current_step')::int, next_execution_date,
		(SELECT count(*) FROM purchase_executions WHERE plan_id=$1 AND status='canceled') FROM purchase_plans WHERE id=$1`, f.identity.PlanID).
		Scan(&row.ledger, &row.unknown, &row.baseline, &row.coverage, &row.term, &row.step, &row.next, &row.canceled))
	return row
}

func (f *persistFixture) created(ctx context.Context, t *testing.T) {
	t.Helper()
	_, err := f.run(ctx, t, nil, nil)
	require.NoError(t, err)
	_, err = f.store.db.Exec(ctx, `UPDATE purchase_plans SET services=jsonb_set(services,'{aws/ec2,future_precision}','9007199254740993') WHERE id=$1`, f.identity.PlanID)
	require.NoError(t, err)
	f.identity.CommandID = uuid.NewString()
}

func TestPlanSchedulePersistCreateRollsBackWithReceiptAndWritesRawIntent(t *testing.T) {
	ctx := context.Background()
	f := newPersistFixture(ctx, t)
	fault := errors.New("fault after persist and receipt")
	_, err := f.run(ctx, t, nil, fault)
	require.ErrorIs(t, err, fault)
	require.Equal(t, []int{0, 0, 0, 0}, f.counts(ctx, t))

	result, err := f.run(ctx, t, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 201, result.Status)
	require.Equal(t, []int{1, 1, 3, 1}, f.counts(ctx, t))
	var include, exclude string
	var created time.Time
	require.NoError(t, f.store.db.QueryRow(ctx, `SELECT (services->'aws/ec2'->'include_engines')::text, (services->'aws/ec2'->'exclude_engines')::text,
		created_at FROM purchase_plans WHERE id=$1`, f.identity.PlanID).Scan(&include, &exclude, &created))
	require.Equal(t, []string{"null", "[]"}, []string{include, exclude})
	require.WithinDuration(t, time.Now(), created, time.Minute)
	row := f.row(ctx, t)
	require.Equal(t, "new", row.ledger)
	require.True(t, f.day.Equal(row.next))
}

func TestPlanScheduleEditWithRootsMovesNextDateAndRollsBackWithReceipt(t *testing.T) {
	ctx := context.Background()
	f := newPersistFixture(ctx, t)
	f.created(ctx, t)
	fault := errors.New("fault after edit receipt")
	_, err := f.run(ctx, t, f.edit(0, 1), fault)
	require.ErrorIs(t, err, fault)
	row := f.row(ctx, t)
	require.Equal(t, []int{80, 0}, []int{row.coverage, row.canceled})
	require.True(t, f.day.Equal(row.next))
	require.Equal(t, 0, f.counts(ctx, t)[3])

	result, err := f.run(ctx, t, f.edit(0, 1), nil)
	require.NoError(t, err)
	require.Equal(t, 200, result.Status)
	row = f.row(ctx, t)
	require.Equal(t, []int{90, 1}, []int{row.coverage, row.canceled})
	require.Equal(t, "9007199254740993", row.unknown)
	require.Equal(t, "new", row.ledger)
	require.True(t, f.day.AddDate(0, 0, 1).Equal(row.next))
	require.Len(t, f.mapping, 1)
	require.Equal(t, 1, f.mapping[0].StepNumber)
	require.True(t, f.day.AddDate(0, 0, 1).Equal(*f.mapping[0].ScheduledDate))
	require.NotNil(t, f.mapping[0].OldExecutionID)
	require.NotNil(t, f.mapping[0].NewExecutionID)
	require.NotEqual(t, *f.mapping[0].OldExecutionID, *f.mapping[0].NewExecutionID)
}

func TestPlanScheduleUnstartedShapeEditKeepsTheRampLedgerNew(t *testing.T) {
	ctx := context.Background()
	f := newPersistFixture(ctx, t)
	f.created(ctx, t)
	_, err := f.run(ctx, t, f.edit(3, 1, 2, 3), nil)
	require.NoError(t, err)
	row := f.row(ctx, t)
	require.Equal(t, 3, row.term)
	require.Equal(t, "new", row.ledger)
	require.Len(t, f.mapping, 3)
}

func TestPlanScheduleProgressedEditLeavesTheTrustedLedgerAlone(t *testing.T) {
	ctx := context.Background()
	f := newPersistFixture(ctx, t)
	f.created(ctx, t)
	key := strings.Join([]string{"aws", f.account, "ec2", "us-east-1", "m5.large", "1", "no-upfront", "", "", "", ""}, "\x1f")
	baseline, err := json.Marshal(PlanRampBaseline{key: {Count: 100}})
	require.NoError(t, err)
	_, err = f.store.db.Exec(ctx, `UPDATE purchase_plans SET ramp_baseline=$2::jsonb, ramp_ledger_state='trusted' WHERE id=$1`, f.identity.PlanID, baseline)
	require.NoError(t, err)
	_, err = f.store.db.Exec(ctx, `UPDATE purchase_plans SET ramp_schedule=jsonb_set(ramp_schedule,'{current_step}','1') WHERE id=$1`, f.identity.PlanID)
	require.NoError(t, err)
	before := f.row(ctx, t)
	require.Equal(t, "trusted", before.ledger)
	require.NotEmpty(t, before.baseline)
	_, err = f.run(ctx, t, f.edit(0, 2), nil)
	require.NoError(t, err)
	after := f.row(ctx, t)
	require.Equal(t, 90, after.coverage)
	require.Equal(t, []any{before.ledger, before.baseline, before.step}, []any{after.ledger, after.baseline, after.step})
	require.True(t, f.day.AddDate(0, 0, 8).Equal(after.next))
}

func TestPlanScheduleEditRefusesAnAccountSetItDidNotIntend(t *testing.T) {
	ctx := context.Background()
	f := newPersistFixture(ctx, t)
	f.created(ctx, t)
	_, err := f.run(ctx, t, func(before *PlanEditorSnapshot) *PlanScheduleMutation {
		mutation := f.edit(0)(before)
		mutation.AccountIDs = append(mutation.AccountIDs, uuid.NewString())
		return mutation
	}, nil)
	require.ErrorIs(t, err, ErrPurchasePlanConflict)
	require.Equal(t, 0, f.counts(ctx, t)[3])
}

func TestPlanScheduleMetadataOnlyEditWithNoRootsKeepsTheScheduleAndLedger(t *testing.T) {
	ctx := context.Background()
	f := newPersistFixture(ctx, t)
	f.created(ctx, t)
	kept := f.day.AddDate(0, 0, 3) // differs from the first root, so a recompute would be visible
	_, err := f.store.db.Exec(ctx, `UPDATE purchase_plans SET next_execution_date=$2 WHERE id=$1`, f.identity.PlanID, kept)
	require.NoError(t, err)
	_, err = f.run(ctx, t, func(before *PlanEditorSnapshot) *PlanScheduleMutation {
		plan := *before.Plan
		plan.Name, plan.NotificationDaysBefore = "renamed editor plan", 3
		return &PlanScheduleMutation{Plan: &plan, AccountIDs: []string{f.account}, Scope: PlanScheduleScope{AccountIDs: []string{f.account}}}
	}, nil)
	require.NoError(t, err)
	var name string
	var notice int
	require.NoError(t, f.store.db.QueryRow(ctx, `SELECT name, notification_days_before FROM purchase_plans WHERE id=$1`, f.identity.PlanID).Scan(&name, &notice))
	require.Equal(t, []any{"renamed editor plan", 3}, []any{name, notice})
	row := f.row(ctx, t)
	require.Equal(t, []any{"new", 0}, []any{row.ledger, row.canceled})
	require.True(t, kept.Equal(row.next))
	require.Empty(t, f.mapping)
	require.Equal(t, []int{1, 1, 3, 1}, f.counts(ctx, t))
}
