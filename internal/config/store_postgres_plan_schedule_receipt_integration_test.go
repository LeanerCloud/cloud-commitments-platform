//go:build integration

package config

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type receiptFixture struct {
	*rampFixture
	identity PlanScheduleCommandIdentity
	original json.RawMessage
}

// newReceiptFixture stores one receipt (status 200) for a fully populated principal.
func newReceiptFixture(ctx context.Context, t *testing.T) *receiptFixture {
	t.Helper()
	f := &receiptFixture{rampFixture: newRampFixture(ctx, t, 1, 0, 3)}
	revision, user, key := "sha256:"+strings.Repeat("c", 64), uuid.NewString(), uuid.NewString()
	f.identity = PlanScheduleCommandIdentity{CommandID: uuid.NewString(), PlanID: f.planID, ExpectedRevision: &revision,
		Actor: PlanScheduleActor{Kind: PlanScheduleUserKeyActor, UserID: &user, KeyID: &key}, RequestHash: "sha256:" + strings.Repeat("a", 64)}
	f.original = f.insert(ctx, t, f.identity)
	return f
}

// insert stores a receipt for identity and returns its stored body.
func (f *receiptFixture) insert(ctx context.Context, t *testing.T, identity PlanScheduleCommandIdentity) (body json.RawMessage) {
	t.Helper()
	require.NoError(t, f.store.WithTx(ctx, func(tx pgx.Tx) error {
		after, err := f.store.readPlanEditorSnapshotTx(ctx, tx, f.planID)
		require.NoError(t, err)
		result, err := insertPlanScheduleReceiptTx(ctx, tx, identity, after, PlanScheduleScope{AccountIDs: f.accounts}, json.RawMessage(`{"stored":true}`))
		body = result.Body
		return err
	}))
	return body
}

func (f *receiptFixture) read(ctx context.Context, identity PlanScheduleCommandIdentity, authorize AuthorizePlanScheduleReplay) (replay *PlanScheduleCommandResult, err error) {
	err = f.store.WithTx(ctx, func(tx pgx.Tx) error {
		replay, err = readPlanScheduleReceiptTx(ctx, tx, identity, authorize)
		return err
	})
	return replay, err
}

func TestPlanScheduleReceiptReplaysOnlyAnExactIdentityMatch(t *testing.T) {
	ctx := context.Background()
	f := newReceiptFixture(ctx, t)
	var accountIDs []string
	allow := func(_ context.Context, scope PlanScheduleScope, accounts []PlanEditorAccount) error {
		accountIDs = scope.AccountIDs
		require.Len(t, accounts, 1)
		return nil
	}
	replay, err := f.read(ctx, f.identity, allow)
	require.NoError(t, err)
	require.Equal(t, 200, replay.Status)
	require.True(t, replay.Replayed)
	require.JSONEq(t, string(f.original), string(replay.Body))
	require.Equal(t, f.accounts, accountIDs)

	other, otherRevision := uuid.NewString(), "sha256:"+strings.Repeat("d", 64)
	for name, change := range map[string]func(*PlanScheduleCommandIdentity){
		"request hash":  func(i *PlanScheduleCommandIdentity) { i.RequestHash = "sha256:" + strings.Repeat("b", 64) },
		"actor user":    func(i *PlanScheduleCommandIdentity) { i.Actor.UserID = &other },
		"actor key":     func(i *PlanScheduleCommandIdentity) { i.Actor.KeyID = &other },
		"actor kind":    func(i *PlanScheduleCommandIdentity) { i.Actor.Kind = PlanScheduleSessionActor },
		"plan":          func(i *PlanScheduleCommandIdentity) { i.PlanID = other },
		"base revision": func(i *PlanScheduleCommandIdentity) { i.ExpectedRevision = &otherRevision },
		"absent base":   func(i *PlanScheduleCommandIdentity) { i.ExpectedRevision = nil },
	} {
		mismatch := f.identity
		change(&mismatch)
		replay, err = f.read(ctx, mismatch, allow)
		require.ErrorIs(t, err, ErrPurchasePlanConflict, name)
		require.Nil(t, replay, name)
	}
	denied := errors.New("replay authority denied")
	replay, err = f.read(ctx, f.identity, func(context.Context, PlanScheduleScope, []PlanEditorAccount) error { return denied })
	require.ErrorIs(t, err, denied)
	require.Nil(t, replay)
	unknown := f.identity
	unknown.CommandID = uuid.NewString()
	replay, err = f.read(ctx, unknown, allow)
	require.True(t, replay == nil && err == nil)
}

func TestPlanScheduleReceiptIsImmutableAndRetentionKeepsTheWindow(t *testing.T) {
	ctx := context.Background()
	f := newReceiptFixture(ctx, t)
	_, err := f.store.db.Exec(ctx, `UPDATE plan_schedule_command_receipts SET status_code=201 WHERE command_id=$1`, f.identity.CommandID)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23514", pgErr.Code)

	var inside, outside string
	for id, age := range map[*string]string{&inside: "29 days", &outside: "31 days"} {
		*id = uuid.NewString()
		_, err = f.store.db.Exec(ctx, `INSERT INTO plan_schedule_command_receipts (command_id,plan_id,actor_kind,request_hash,result_revision,status_code,result_json,scope_evidence,created_at)
			VALUES ($1,$2,'admin-api-key',$3,$3,200,'{}','{}',now()-$4::interval)`, *id, f.planID, f.identity.RequestHash, age)
		require.NoError(t, err)
	}
	_, err = f.store.CleanupOldExecutions(ctx, 30)
	require.NoError(t, err)
	var kept []string
	rows, err := f.store.db.Query(ctx, `SELECT command_id::text FROM plan_schedule_command_receipts`)
	require.NoError(t, err)
	kept, err = pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	require.ElementsMatch(t, []string{f.identity.CommandID, inside}, kept)

	tag, err := f.store.db.Exec(ctx, `DELETE FROM plan_schedule_command_receipts WHERE command_id=$1`, f.identity.CommandID)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected())
}

func TestPlanScheduleReceiptReplaysRealPrincipalShapesWithTheirOriginalStatus(t *testing.T) {
	ctx := context.Background()
	f := newReceiptFixture(ctx, t)
	user := uuid.NewString()
	allow := func(context.Context, PlanScheduleScope, []PlanEditorAccount) error { return nil }
	for name, tc := range map[string]struct {
		actor  PlanScheduleActor
		create bool
		status int
	}{
		"session edit":   {PlanScheduleActor{Kind: PlanScheduleSessionActor, UserID: &user}, false, 200},
		"admin create":   {PlanScheduleActor{Kind: PlanScheduleAdminActor}, true, 201},
		"session create": {PlanScheduleActor{Kind: PlanScheduleSessionActor, UserID: &user}, true, 201},
	} {
		identity := f.identity
		identity.CommandID, identity.Actor = uuid.NewString(), tc.actor
		if tc.create {
			identity.ExpectedRevision = nil
		}
		f.insert(ctx, t, identity)
		replay, err := f.read(ctx, identity, allow)
		require.NoError(t, err, name)
		require.Equal(t, tc.status, replay.Status, name)
		require.True(t, replay.Replayed, name)
	}
}

func TestPlanScheduleReceiptRejectsMalformedCommittedBody(t *testing.T) {
	ctx := context.Background()
	f := newReceiptFixture(ctx, t)
	for _, body := range []string{`[1]`, `null`, `not json`} {
		identity := f.identity
		identity.CommandID = uuid.NewString()
		err := f.store.WithTx(ctx, func(tx pgx.Tx) error {
			after, err := f.store.readPlanEditorSnapshotTx(ctx, tx, f.planID)
			require.NoError(t, err)
			_, err = insertPlanScheduleReceiptTx(ctx, tx, identity, after, PlanScheduleScope{}, json.RawMessage(body))
			return err
		})
		require.ErrorContains(t, err, "invalid committed command response", body)
		var stored int
		require.NoError(t, f.store.db.QueryRow(ctx, `SELECT count(*) FROM plan_schedule_command_receipts WHERE command_id=$1`, identity.CommandID).Scan(&stored))
		require.Zero(t, stored, body)
	}
}
