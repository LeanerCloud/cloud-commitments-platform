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

func lockedState(ctx context.Context, f *rampFixture, identity PlanScheduleCommandIdentity, probe func(*PlanEditorSnapshot, []PlanEditorAccount)) error {
	return f.store.WithTx(ctx, func(tx pgx.Tx) error {
		snapshot, accounts, err := f.store.lockPlanScheduleStateTx(ctx, tx, identity)
		if err == nil && probe != nil {
			probe(snapshot, accounts)
		}
		return err
	})
}

func notAvailable(t *testing.T, f *rampFixture, query, id string) {
	_, err := f.store.db.Exec(context.Background(), query, id)
	require.ErrorContains(t, err, "55P03", query)
}

func TestLockPlanScheduleStateHoldsEveryLockAndReturnsTheAccountUnion(t *testing.T) {
	ctx := context.Background()
	f := newRampFixture(ctx, t, 2, 0, 3)
	other := &CloudAccount{Name: "proposed", Provider: "gcp", ExternalID: uuid.NewString()}
	require.NoError(t, f.store.CreateCloudAccount(ctx, other))
	root := f.row(ctx, t, 1, nil, "pending")
	_, err := f.store.db.Exec(ctx, `UPDATE purchase_plans SET services='{"ibm/vm":{"provider":"oracle","service":"vm"}}' WHERE id=$1`, f.planID)
	require.NoError(t, err)
	_, err = f.store.db.Exec(ctx, `UPDATE cloud_accounts SET provider='azure' WHERE id=$1`, f.accounts[1])
	require.NoError(t, err)
	identity := PlanScheduleCommandIdentity{PlanID: f.planID, ProposedAccountIDs: []string{other.ID, f.accounts[0]}}
	require.NoError(t, lockedState(ctx, f, identity, func(snapshot *PlanEditorSnapshot, accounts []PlanEditorAccount) {
		require.Equal(t, f.planID, snapshot.Plan.ID)
		require.ElementsMatch(t, append([]string{other.ID}, f.accounts...), []string{accounts[0].ID, accounts[1].ID, accounts[2].ID})
		notAvailable(t, f, `SELECT 1 FROM purchase_plans WHERE id=$1 FOR KEY SHARE NOWAIT`, f.planID)
		notAvailable(t, f, `SELECT 1 FROM purchase_executions WHERE execution_id=$1 FOR KEY SHARE NOWAIT`, root.ExecutionID)
		notAvailable(t, f, `SELECT 1 FROM cloud_accounts WHERE id=$1 FOR UPDATE NOWAIT`, other.ID)
		var free bool
		_, err = f.store.db.Exec(ctx, `SELECT 1 FROM cloud_accounts WHERE id=$1 FOR SHARE NOWAIT`, other.ID)
		require.NoError(t, err, "FOR SHARE must not block readers")
		for _, key := range []string{"cudly:plan-schedule:plan:" + f.planID, planStepProviderLockPrefix + "aws", planStepProviderLockPrefix + "gcp", planStepProviderLockPrefix + "azure", planStepProviderLockPrefix + "ibm", planStepProviderLockPrefix + "oracle"} {
			require.NoError(t, f.store.db.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, key).Scan(&free))
			require.False(t, free, key)
		}
	}))
}

func TestLockPlanScheduleStateAbsentPlanAndAccounts(t *testing.T) {
	ctx := context.Background()
	f := newRampFixture(ctx, t, 1, 0, 3)
	identity := PlanScheduleCommandIdentity{PlanID: uuid.NewString(), ProposedAccountIDs: f.accounts}
	require.NoError(t, lockedState(ctx, f, identity, func(snapshot *PlanEditorSnapshot, accounts []PlanEditorAccount) {
		require.Nil(t, snapshot)
		require.Equal(t, f.accounts[0], accounts[0].ID)
		notAvailable(t, f, `SELECT 1 FROM cloud_accounts WHERE id=$1 FOR UPDATE NOWAIT`, f.accounts[0])
	}))
	identity.ExpectedRevision = new("rev")
	require.ErrorIs(t, lockedState(ctx, f, identity, nil), ErrPurchasePlanConflict)
	identity.ExpectedRevision, identity.ProposedAccountIDs = nil, []string{uuid.NewString()}
	require.ErrorIs(t, lockedState(ctx, f, identity, nil), ErrPlanEditorAttribution)
}

func TestLockPlanScheduleStateRefusesRoutingThatChangedWhileWaitingForThePlanLock(t *testing.T) {
	ctx := context.Background()
	f := newRampFixture(ctx, t, 1, 0, 3)
	contender, err := f.store.db.Begin(ctx)
	require.NoError(t, err)
	_, err = contender.Exec(ctx, `SELECT 1 FROM purchase_plans WHERE id=$1 FOR UPDATE`, f.planID)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- lockedState(ctx, f, PlanScheduleCommandIdentity{PlanID: f.planID}, nil) }()
	require.Eventually(t, func() bool {
		var waiting int
		require.NoError(t, f.store.db.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%FROM purchase_plans WHERE id=$1 FOR UPDATE%'`).Scan(&waiting))
		return waiting == 1
	}, 10*time.Second, 50*time.Millisecond)
	_, err = contender.Exec(ctx, `UPDATE purchase_plans SET services='{"gcp/compute":{"provider":"gcp","service":"compute"}}' WHERE id=$1`, f.planID)
	require.NoError(t, err)
	require.NoError(t, contender.Commit(ctx))
	require.ErrorIs(t, <-done, ErrPurchasePlanConflict, "providers discovered after the wait were never locked")
}

func TestLockPlanScheduleStateLocksAProviderThatOnlyTheProposalNames(t *testing.T) {
	ctx := context.Background()
	f := newRampFixture(ctx, t, 1, 0, 3)
	identity := PlanScheduleCommandIdentity{PlanID: f.planID, ProposedProviders: []string{"oracle"}}
	require.NoError(t, lockedState(ctx, f, identity, func(*PlanEditorSnapshot, []PlanEditorAccount) {
		var free bool
		require.NoError(t, f.store.db.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, planStepProviderLockPrefix+"oracle").Scan(&free))
		require.False(t, free)
	}))
}
