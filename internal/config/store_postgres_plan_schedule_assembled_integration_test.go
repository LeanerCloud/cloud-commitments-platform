//go:build integration

package config

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
)

func TestExecutePlanScheduleCommandIsOneTransactionAndBornNew(t *testing.T) {
	ctx := context.Background()
	f := newPersistFixture(ctx, t)
	failed := &commandRun{onProject: func(*PlanEditorSnapshot) error { return errors.New("project failed") }}
	_, err := failed.exec(ctx, f, f.store)
	require.EqualError(t, err, "project failed")
	require.Equal(t, []int{0, 0, 0, 0}, f.counts(ctx, t))
	_, err = (&commandRun{}).exec(ctx, f, f.store)
	require.NoError(t, err)
	require.Equal(t, "new", f.row(ctx, t).ledger)

	f.identity.CommandID = uuid.NewString()
	revision := f.committedRevision(ctx, t)
	f.identity.ExpectedRevision = &revision
	failed = &commandRun{edit: f.edit(0, 1), onProject: failed.onProject}
	_, err = failed.exec(ctx, f, f.store)
	require.EqualError(t, err, "project failed")
	row := f.row(ctx, t)
	require.Equal(t, []int{80, 0, 0}, []int{row.coverage, row.canceled, f.counts(ctx, t)[3]})
}

func TestExecutePlanScheduleCommandReplaysAfterPlanDeletionButFailsClosedWithoutAnAccount(t *testing.T) {
	ctx := context.Background()
	f := newPersistFixture(ctx, t)
	created, err := (&commandRun{}).exec(ctx, f, f.store)
	require.NoError(t, err)
	_, err = f.store.db.Exec(ctx, `DELETE FROM purchase_plans WHERE id=$1`, f.identity.PlanID)
	require.NoError(t, err)

	replay := &commandRun{}
	again, err := replay.exec(ctx, f, f.store)
	require.NoError(t, err)
	require.True(t, again.Replayed)
	require.JSONEq(t, string(created.Body), string(again.Body))
	require.Equal(t, 0, replay.builds)

	_, err = f.store.db.Exec(ctx, `DELETE FROM cloud_accounts WHERE id=$1`, f.account)
	require.NoError(t, err)
	denied := &commandRun{}
	leaked, err := denied.exec(ctx, f, f.store)
	require.ErrorIs(t, err, ErrPlanEditorAttribution)
	require.Nil(t, leaked)
	require.Equal(t, []int{0, 0}, []int{denied.builds, denied.authorizes})
}

func TestExecutePlanScheduleCommandTermEditReplacesEveryRootAndKeepsTheLedgerNew(t *testing.T) {
	ctx := context.Background()
	f := newPersistFixture(ctx, t)
	f.created(ctx, t)
	revision := f.committedRevision(ctx, t)
	f.identity.ExpectedRevision = &revision
	edit := &commandRun{edit: f.edit(3, 1, 2, 3)}
	result, err := edit.exec(ctx, f, f.store)
	require.NoError(t, err)
	require.Equal(t, 200, result.Status)
	row := f.row(ctx, t)
	require.Equal(t, []any{3, "new", 3}, []any{row.term, row.ledger, row.canceled})
	var mapping struct{ Mapping []PlanScheduleMapping }
	require.NoError(t, json.Unmarshal(result.Body, &mapping))
	require.Len(t, mapping.Mapping, 3)
	for i, m := range mapping.Mapping {
		require.Equal(t, i+1, m.StepNumber)
		require.NotEqual(t, *m.OldExecutionID, *m.NewExecutionID)
		require.True(t, f.day.AddDate(0, 0, 7*i+1).Equal(*m.ScheduledDate))
	}
}

func TestExecutePlanScheduleCommandEditAfterProgressLeavesLedgerBaselineAndStep(t *testing.T) {
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
	require.Equal(t, "trusted", before.ledger, "the fixture must start trusted")
	revision := f.committedRevision(ctx, t)
	f.identity.ExpectedRevision = &revision
	_, err = (&commandRun{edit: f.edit(0, 2)}).exec(ctx, f, f.store)
	require.NoError(t, err)
	after := f.row(ctx, t)
	require.Equal(t, []any{"trusted", before.baseline, 1, 90}, []any{after.ledger, after.baseline, after.step, after.coverage})
}

func TestExecutePlanScheduleCommandClassifiesDatabaseErrorsAndKeepsSettingsLocal(t *testing.T) {
	ctx := context.Background()
	f := newPersistFixture(ctx, t)
	for _, c := range []struct {
		name, code, constraint string
		builds                 int
		conflict               bool
	}{
		{"unrelated unique violation is returned raw", "23505", "other_pkey", 1, false},
		{"receipt contender retries, then fails closed", "23505", "plan_schedule_command_receipts_pkey", 3, true},
		{"serialization failure retries, then fails closed", "40001", "", 3, true},
		{"lock timeout is a sanitized conflict", "55P03", "", 1, true},
	} {
		pgErr := &pgconn.PgError{Code: c.code, ConstraintName: c.constraint}
		run := &commandRun{fail: pgErr}
		_, err := run.exec(ctx, f, f.store)
		require.Equal(t, c.builds, run.builds, c.name)
		require.Equal(t, c.conflict, errors.Is(err, ErrPurchasePlanConflict), c.name)
		if !c.conflict {
			require.Same(t, pgErr, err, c.name)
		}
	}
	_, err := (&commandRun{}).exec(ctx, f, f.store)
	require.NoError(t, err)
	pool := f.store.db.(*database.Connection).Pool()
	for idle := int(pool.Stat().IdleConns()); idle > 0; idle-- {
		conn, err := pool.Acquire(ctx)
		require.NoError(t, err)
		defer conn.Release()
		for _, setting := range []string{"lock_timeout", "statement_timeout"} {
			var value string
			require.NoError(t, conn.QueryRow(ctx, `SHOW `+setting).Scan(&value))
			require.Equal(t, "0", value, "%s leaked onto a pooled connection", setting)
		}
	}
}

func TestPlanScheduleCommandsNeedAPoolOfAtLeastTwoConnections(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RequirePostgresContainer(ctx, t)
	t.Cleanup(func() { container.Cleanup(context.Background()) })
	for maxConns, wantSlots := range map[int]int{1: 0, 2: 1} {
		config := *container.Config
		config.MaxConnections, config.MinConnections = maxConns, 0
		db, err := database.NewConnection(ctx, &config, nil)
		require.NoError(t, err)
		t.Cleanup(db.Close)
		store := NewPostgresStore(db)
		require.Equal(t, wantSlots, cap(store.commandSlots))
		if wantSlots == 0 {
			identity := PlanScheduleCommandIdentity{CommandID: uuid.NewString(), PlanID: uuid.NewString(), Actor: PlanScheduleActor{Kind: PlanScheduleAdminActor},
				RequestHash: "sha256:" + strings.Repeat("a", 64), ProposedAccountIDs: []string{uuid.NewString()}}
			_, err = store.ExecutePlanScheduleCommand(ctx, identity, nil, nil, nil)
			require.ErrorIs(t, err, ErrPlanSchedulePoolTooSmall)
		}
	}
}

func TestExecutePlanScheduleCommandFreesTheSlotAfterSuccessErrorAndPanic(t *testing.T) {
	ctx := context.Background()
	f := newPersistFixture(ctx, t)
	store := &PostgresStore{db: f.store.db, commandSlots: make(chan struct{}, 1)}
	for _, mode := range []string{"build panic", "build error", "project panic", "project error", "success"} {
		build := func(context.Context, *PlanEditorSnapshot, []PlanEditorAccount, time.Time) (*PlanScheduleMutation, error) {
			switch mode {
			case "build panic":
				panic("boom")
			case "build error":
				return nil, errors.New("build failed")
			}
			return f.create(), nil
		}
		project := func(context.Context, *PlanEditorSnapshot, []PlanScheduleMapping, json.RawMessage) (json.RawMessage, error) {
			switch mode {
			case "project panic":
				panic("boom")
			case "project error":
				return nil, errors.New("project failed")
			}
			return json.RawMessage(`{}`), nil
		}
		run := func() error {
			_, err := store.ExecutePlanScheduleCommand(ctx, f.identity, build, nil, project)
			return err
		}
		if mode == "build panic" || mode == "project panic" {
			require.Panics(t, func() { _ = run() }, mode)
		} else {
			require.Equal(t, mode != "success", run() != nil, mode)
		}
		require.Empty(t, store.commandSlots, mode)
	}
}
