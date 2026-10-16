//go:build integration

package config

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
)

// commandRun is one ExecutePlanScheduleCommand call whose callbacks count their invocations.
type commandRun struct {
	builds, authorizes int
	fail               error
	edit               editBuilder
	onProject          func(*PlanEditorSnapshot) error
}

func (c *commandRun) exec(ctx context.Context, f *persistFixture, store *PostgresStore) (*PlanScheduleCommandResult, error) {
	build := func(_ context.Context, before *PlanEditorSnapshot, _ []PlanEditorAccount, _ time.Time) (*PlanScheduleMutation, error) {
		c.builds++
		if c.fail != nil {
			return nil, c.fail
		}
		if c.edit == nil {
			return f.create(), nil
		}
		return c.edit(before), nil
	}
	authorize := func(context.Context, PlanScheduleScope, []PlanEditorAccount) error { c.authorizes++; return nil }
	project := func(_ context.Context, after *PlanEditorSnapshot, mapping []PlanScheduleMapping, _ json.RawMessage) (json.RawMessage, error) {
		if c.onProject != nil {
			if err := c.onProject(after); err != nil {
				return nil, err
			}
		}
		return json.Marshal(map[string]any{"plan_id": after.Plan.ID, "updated_at": after.Plan.UpdatedAt, "mapping": mapping})
	}
	return store.ExecutePlanScheduleCommand(ctx, f.identity, build, authorize, project)
}

// committedRevision is the revision of the plan as stored now.
func (f *persistFixture) committedRevision(ctx context.Context, t *testing.T) string {
	t.Helper()
	var revision string
	require.NoError(t, f.store.WithTx(ctx, func(tx pgx.Tx) error {
		snapshot, err := f.store.readPlanEditorSnapshotTx(ctx, tx, f.identity.PlanID)
		if err == nil {
			revision, err = snapshot.Revision()
		}
		return err
	}))
	return revision
}

func TestExecutePlanScheduleCommandCreatesAndReplaysWithoutBuilding(t *testing.T) {
	ctx := context.Background()
	f := newPersistFixture(ctx, t)
	failed := &commandRun{fail: errors.New("build refused")}
	_, err := failed.exec(ctx, f, f.store)
	require.ErrorIs(t, err, failed.fail)
	require.Equal(t, []int{0, 0, 0, 0}, f.counts(ctx, t))
	first := &commandRun{}
	created, err := first.exec(ctx, f, f.store)
	require.NoError(t, err)
	require.Equal(t, 201, created.Status)
	require.False(t, created.Replayed)
	require.Equal(t, []int{1, 1, 3, 1}, f.counts(ctx, t))
	replay := &commandRun{}
	again, err := replay.exec(ctx, f, f.store)
	require.NoError(t, err)
	require.Equal(t, 201, again.Status)
	require.True(t, again.Replayed)
	require.JSONEq(t, string(created.Body), string(again.Body))
	require.Equal(t, []int{0, 1}, []int{replay.builds, replay.authorizes})
	for name, mutate := range map[string]func(*PlanScheduleCommandIdentity){
		"request hash": func(i *PlanScheduleCommandIdentity) { i.RequestHash = "sha256:" + strings.Repeat("b", 64) },
		"principal": func(i *PlanScheduleCommandIdentity) {
			i.Actor = PlanScheduleActor{Kind: PlanScheduleSessionActor, UserID: new(uuid.NewString())}
		},
		"plan": func(i *PlanScheduleCommandIdentity) { i.PlanID = uuid.NewString() },
	} {
		other := *f
		mutate(&other.identity)
		conflict := &commandRun{}
		_, err = conflict.exec(ctx, &other, f.store)
		require.ErrorIs(t, err, ErrPurchasePlanConflict, name)
		require.Equal(t, 0, conflict.builds, name)
	}
}

func TestExecutePlanScheduleCommandEditUsesTheStoredRowAndChecksTheRevisionBeforeBuild(t *testing.T) {
	ctx := context.Background()
	f := newPersistFixture(ctx, t)
	f.created(ctx, t)
	revision := f.committedRevision(ctx, t)
	stale := "sha256:" + strings.Repeat("0", 64)
	f.identity.ExpectedRevision = &stale
	refused := &commandRun{edit: f.edit(0, 1)}
	_, err := refused.exec(ctx, f, f.store)
	require.ErrorIs(t, err, ErrPurchasePlanConflict)
	require.Equal(t, 0, refused.builds)

	f.identity.ExpectedRevision = &revision
	projectedAt := time.Time{}
	run := &commandRun{edit: f.edit(0, 1), onProject: func(after *PlanEditorSnapshot) error { projectedAt = after.Plan.UpdatedAt; return nil }}
	result, err := run.exec(ctx, f, f.store)
	require.NoError(t, err)
	require.Equal(t, 200, result.Status)
	require.Equal(t, 1, run.builds)
	var stored time.Time
	require.NoError(t, f.store.db.QueryRow(ctx, `SELECT updated_at FROM purchase_plans WHERE id=$1`, f.identity.PlanID).Scan(&stored))
	require.True(t, stored.Equal(projectedAt), "projected %v, stored %v", projectedAt, stored)
	var receipt string
	require.NoError(t, f.store.db.QueryRow(ctx, `SELECT result_revision FROM plan_schedule_command_receipts WHERE command_id=$1`, f.identity.CommandID).Scan(&receipt))
	require.Equal(t, f.committedRevision(ctx, t), receipt)
}

func TestExecutePlanScheduleCommandReplaysAReceiptThatAContenderCommittedFirst(t *testing.T) {
	ctx := context.Background()
	f := newPersistFixture(ctx, t)
	f.created(ctx, t)
	revision := f.committedRevision(ctx, t)
	f.identity.ExpectedRevision = &revision
	first := true
	run := &commandRun{edit: f.edit(0, 1), onProject: func(after *PlanEditorSnapshot) error {
		if !first {
			return nil
		}
		first = false
		// Another connection commits this command's receipt after the receipt read: the insert must hit 23505.
		return f.store.WithTx(ctx, func(tx pgx.Tx) error {
			_, err := insertPlanScheduleReceiptTx(ctx, tx, f.identity, after, PlanScheduleScope{AccountIDs: []string{f.account}}, json.RawMessage(`{"winner":true}`))
			return err
		})
	}}
	result, err := run.exec(ctx, f, f.store)
	require.NoError(t, err)
	require.True(t, result.Replayed)
	require.JSONEq(t, `{"winner":true}`, string(result.Body))
	require.Equal(t, 1, run.builds)
	row := f.row(ctx, t)
	require.Equal(t, []int{80, 0}, []int{row.coverage, row.canceled}, "the losing attempt rolled back")
}

func TestPlanScheduleCommandSlotsMatchThePoolMinusOne(t *testing.T) {
	ctx := context.Background()
	f := newPersistFixture(ctx, t)
	maxConns := f.store.db.(*database.Connection).Pool().Config().MaxConns
	require.Equal(t, int(maxConns)-1, cap(f.store.commandSlots))
}

// blockingCommand holds the store's only slot inside build until release is closed.
func blockingCommand(ctx context.Context, t *testing.T, f *persistFixture, store *PostgresStore) (release func(), done <-chan error) {
	held, gate, result := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := store.ExecutePlanScheduleCommand(ctx, f.identity, func(context.Context, *PlanEditorSnapshot, []PlanEditorAccount, time.Time) (*PlanScheduleMutation, error) {
			close(held)
			<-gate
			return nil, errors.New("released")
		}, nil, nil)
		result <- err
	}()
	<-held
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release) // a failed assertion must not leave the holder blocking cleanup
	return release, result
}

func TestExecutePlanScheduleCommandWaitsForASlotAndRunsOnceItIsFree(t *testing.T) {
	ctx := context.Background()
	f := newPersistFixture(ctx, t)
	store := &PostgresStore{db: f.store.db, commandSlots: make(chan struct{}, 1), commandWait: time.Minute}
	release, firstDone := blockingCommand(ctx, t, f, store)

	second := &commandRun{}
	type outcome struct {
		result *PlanScheduleCommandResult
		err    error
	}
	secondDone := make(chan outcome, 1)
	go func() {
		result, err := second.exec(ctx, f, store)
		secondDone <- outcome{result, err}
	}()
	select {
	case got := <-secondDone:
		t.Fatalf("the second command must wait for the slot, returned %v", got.err)
	case <-time.After(300 * time.Millisecond):
	}
	require.Equal(t, 0, second.builds)
	release()
	require.EqualError(t, <-firstDone, "released")
	got := <-secondDone
	require.NoError(t, got.err)
	require.Equal(t, 201, got.result.Status)
}

func TestExecutePlanScheduleCommandReturnsBusyWhenTheSlotStaysTaken(t *testing.T) {
	ctx := context.Background()
	f := newPersistFixture(ctx, t)
	store := &PostgresStore{db: f.store.db, commandSlots: make(chan struct{}, 1), commandWait: 100 * time.Millisecond}
	release, firstDone := blockingCommand(ctx, t, f, store)
	other := &commandRun{}
	_, err := other.exec(ctx, f, store)
	require.ErrorIs(t, err, ErrPlanScheduleBusy)
	require.Equal(t, 0, other.builds)

	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	store.commandWait = time.Minute
	_, err = other.exec(short, f, store)
	require.ErrorIs(t, err, ErrPlanScheduleBusy, "a caller deadline sooner than the wait also ends in busy")
	release()
	require.EqualError(t, <-firstDone, "released")
}
