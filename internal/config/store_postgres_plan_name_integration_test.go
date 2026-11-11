//go:build integration

package config

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

const planNameLockLike = "%pg_advisory_xact_lock%"

func seenAlways([]CloudAccount) bool { return true }

func seenNever([]CloudAccount) bool { return false }

func seenIfAccount(id string) func([]CloudAccount) bool {
	return func(accounts []CloudAccount) bool {
		for i := range accounts {
			if accounts[i].ID == id {
				return true
			}
		}
		return false
	}
}

func planNamed(name string) *PurchasePlan {
	return &PurchasePlan{Name: name, Enabled: true, Services: map[string]ServiceConfig{}}
}

func newPlanNameAccount(ctx context.Context, t *testing.T, store *PostgresStore, provider string) *CloudAccount {
	t.Helper()
	account := &CloudAccount{Name: "plan name " + uuid.NewString()[:8], Provider: provider, ExternalID: uuid.NewString()}
	require.NoError(t, store.CreateCloudAccount(ctx, account))
	return account
}

func countPlansNamed(ctx context.Context, t *testing.T, store *PostgresStore, key string) int {
	t.Helper()
	var n int
	require.NoError(t, store.db.QueryRow(ctx, `SELECT count(*) FROM purchase_plans WHERE lower(btrim(name)) = lower(btrim($1))`, key).Scan(&n))
	return n
}

// holdPlanNameLock opens a transaction that owns the name lock for key, which
// stands in for a create that is mid-flight. The key is computed in SQL so the
// test does not depend on the database collation.
func holdPlanNameLock(ctx context.Context, t *testing.T, store *PostgresStore, key string) pgx.Tx {
	t.Helper()
	tx, err := store.db.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('`+planNameLockPrefix+`' || lower(btrim($1::text)), 0))`, key)
	require.NoError(t, err)
	return tx
}

func requireNameTaken(t *testing.T, err error, id, name string) {
	t.Helper()
	require.ErrorIs(t, err, ErrPlanNameTaken)
	var taken *PlanNameTakenError
	require.True(t, errors.As(err, &taken))
	require.Equal(t, id, taken.ExistingID)
	require.Equal(t, name, taken.ExistingName)
}

func TestCreatePurchasePlanWithAccounts_RefusesVisibleDuplicate(t *testing.T) {
	ctx := context.Background()
	store := setupRampStepStore(ctx, t)
	account := newPlanNameAccount(ctx, t, store, "aws")
	first := planNamed("Weekly EC2")
	require.NoError(t, store.CreatePurchasePlanWithAccounts(ctx, first, []string{account.ID}, seenAlways))

	err := store.CreatePurchasePlanWithAccounts(ctx, planNamed(" weekly ec2 "), []string{account.ID}, seenAlways)
	requireNameTaken(t, err, first.ID, "Weekly EC2")
	require.Equal(t, 1, countPlansNamed(ctx, t, store, "weekly ec2"))

	attached, err := store.GetPlanAccounts(ctx, first.ID)
	require.NoError(t, err)
	require.Len(t, attached, 1, "the first plan and its account commit together")

	require.NoError(t, store.CreatePurchasePlanWithAccounts(ctx, planNamed("Another name"), []string{account.ID}, seenAlways))
}

func TestCreatePurchasePlanWithAccounts_InvisibleDuplicateIsInserted(t *testing.T) {
	ctx := context.Background()
	store := setupRampStepStore(ctx, t)
	account := newPlanNameAccount(ctx, t, store, "aws")
	require.NoError(t, store.CreatePurchasePlanWithAccounts(ctx, planNamed("Shared name"), []string{account.ID}, seenNever))
	require.NoError(t, store.CreatePurchasePlanWithAccounts(ctx, planNamed("shared name"), []string{account.ID}, seenNever))
	require.Equal(t, 2, countPlansNamed(ctx, t, store, "Shared name"))
}

func TestCreatePurchasePlanWithAccounts_UnicodeNames(t *testing.T) {
	ctx := context.Background()
	store := setupRampStepStore(ctx, t)
	account := newPlanNameAccount(ctx, t, store, "aws")
	exact := planNamed("Économie exacte")
	require.NoError(t, store.CreatePurchasePlanWithAccounts(ctx, exact, []string{account.ID}, seenAlways))
	err := store.CreatePurchasePlanWithAccounts(ctx, planNamed("Économie exacte"), []string{account.ID}, seenAlways)
	requireNameTaken(t, err, exact.ID, "Économie exacte")

	// The test database is en_US.utf8 like production, so Unicode case folds.
	folded := planNamed("Économie pliée")
	require.NoError(t, store.CreatePurchasePlanWithAccounts(ctx, folded, []string{account.ID}, seenAlways))
	err = store.CreatePurchasePlanWithAccounts(ctx, planNamed("économie pliée"), []string{account.ID}, seenAlways)
	requireNameTaken(t, err, folded.ID, "Économie pliée")
}

// btrim alone strips only spaces; a name that differs by a trailing tab or newline is the same name.
func TestCreatePurchasePlanWithAccounts_TabAndNewlineAreWhitespace(t *testing.T) {
	ctx := context.Background()
	store := setupRampStepStore(ctx, t)
	account := newPlanNameAccount(ctx, t, store, "aws")
	existing := planNamed("Tabbed plan")
	require.NoError(t, store.CreatePurchasePlanWithAccounts(ctx, existing, []string{account.ID}, seenAlways))
	for _, variant := range []string{"Tabbed plan\t", "\nTabbed plan\r\n"} {
		err := store.CreatePurchasePlanWithAccounts(ctx, planNamed(variant), []string{account.ID}, seenAlways)
		requireNameTaken(t, err, existing.ID, "Tabbed plan")
	}

	// The stored name is what listPlansNamedTx matches, so a padded stored name needs the same trim.
	padded := planNamed("Padded plan\t\n")
	require.NoError(t, store.CreatePurchasePlanWithAccounts(ctx, padded, []string{account.ID}, seenAlways))
	err := store.CreatePurchasePlanWithAccounts(ctx, planNamed("Padded plan"), []string{account.ID}, seenAlways)
	requireNameTaken(t, err, padded.ID, "Padded plan\t\n")
}

func TestCreatePurchasePlanWithAccounts_QueuesOnNormalisedNameLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := setupRampStepStore(ctx, t)
	account := newPlanNameAccount(ctx, t, store, "aws")
	holder := holdPlanNameLock(ctx, t, store, "weekly ec2")

	done := make(chan error, 1)
	go func() {
		done <- store.CreatePurchasePlanWithAccounts(ctx, planNamed(" Weekly EC2"), []string{account.ID}, seenAlways)
	}()
	testhelpers.WaitForLockWait(ctx, t, store.db, planNameLockLike)

	existing := planNamed("weekly ec2")
	require.NoError(t, store.createPurchasePlan(ctx, holder, existing))
	require.NoError(t, store.setPlanAccountsTx(ctx, holder, existing.ID, []string{account.ID}))
	require.NoError(t, holder.Commit(ctx))

	requireNameTaken(t, testhelpers.Await(t, done), existing.ID, "weekly ec2")
	require.Equal(t, 1, countPlansNamed(ctx, t, store, "weekly ec2"))
}

func TestCreatePurchasePlanWithAccounts_ScopedDoubleCreate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := setupRampStepStore(ctx, t)
	accountA := newPlanNameAccount(ctx, t, store, "aws")
	accountB := newPlanNameAccount(ctx, t, store, "aws")

	run := func(t *testing.T, holderAccount *CloudAccount, wantTaken bool) {
		holder := holdPlanNameLock(ctx, t, store, " x ")
		done := make(chan error, 1)
		go func() {
			done <- store.CreatePurchasePlanWithAccounts(ctx, planNamed(" x "), []string{accountA.ID}, seenIfAccount(accountA.ID))
		}()
		testhelpers.WaitForLockWait(ctx, t, store.db, planNameLockLike)
		existing := planNamed("X")
		require.NoError(t, store.createPurchasePlan(ctx, holder, existing))
		require.NoError(t, store.setPlanAccountsTx(ctx, holder, existing.ID, []string{holderAccount.ID}))
		require.NoError(t, holder.Commit(ctx))

		err := testhelpers.Await(t, done)
		if wantTaken {
			requireNameTaken(t, err, existing.ID, "X")
			return
		}
		require.NoError(t, err, "a plan outside the caller's scope must not block the create")
	}

	t.Run("overlapping account is refused", func(t *testing.T) { run(t, accountA, true) })
	t.Run("disjoint account is inserted", func(t *testing.T) {
		_, err := store.db.Exec(ctx, `DELETE FROM purchase_plans WHERE lower(btrim(name)) = 'x'`)
		require.NoError(t, err)
		run(t, accountB, false)
		require.Equal(t, 2, countPlansNamed(ctx, t, store, "x"))
	})
}

func TestCreatePurchasePlanWithAccounts_AccountErrorsLeaveNoPlan(t *testing.T) {
	ctx := context.Background()
	store := setupRampStepStore(ctx, t)
	azure := newPlanNameAccount(ctx, t, store, "azure")

	services := map[string]ServiceConfig{"aws/ec2": {Provider: "aws", Service: "ec2", Enabled: true, Term: 1}}
	plan := planNamed("Mismatch")
	plan.Services = services
	err := store.CreatePurchasePlanWithAccounts(ctx, plan, []string{azure.ID}, seenNever)
	require.ErrorIs(t, err, ErrPlanProviderMismatch)
	require.Contains(t, err.Error(), "plan provider mismatch: account")
	require.Zero(t, countPlansNamed(ctx, t, store, "Mismatch"))

	unknown := planNamed("Unknown account")
	unknown.Services = services
	err = store.CreatePurchasePlanWithAccounts(ctx, unknown, []string{uuid.NewString()}, seenNever)
	require.ErrorIs(t, err, ErrNotFound)
	require.Zero(t, countPlansNamed(ctx, t, store, "Unknown account"))
}
