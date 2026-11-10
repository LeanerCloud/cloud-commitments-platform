//go:build integration

package config

// Real PostgreSQL tests for the fan-out retry family lock and claim query
// (platform#1930).

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retryFamilyRig is one fan-out: a failed root R (key K) and failed account
// rows A and B (keys K:<a>, K:<b>) of one plan.
type retryFamilyRig struct {
	store  *PostgresStore
	planID string
	family string
	root   *PurchaseExecution
	a, b   *PurchaseExecution
}

func newRetryFamilyRig(ctx context.Context, t *testing.T, store *PostgresStore) *retryFamilyRig {
	t.Helper()
	plan := &PurchasePlan{Name: "retry-family-" + uuid.NewString(), Enabled: true, Services: map[string]ServiceConfig{}}
	require.NoError(t, store.CreatePurchasePlan(ctx, plan))
	r := &retryFamilyRig{store: store, planID: plan.ID, family: uuid.NewString()}
	r.root = r.seed(ctx, t, r.family, nil)
	for i, dst := range []**PurchaseExecution{&r.a, &r.b} {
		acct := &CloudAccount{Name: fmt.Sprintf("acct-%s-%d", r.family, i), Provider: "aws", ExternalID: fmt.Sprintf("%012d", time.Now().UnixNano()%1e12+int64(i)), Enabled: true}
		require.NoError(t, store.CreateCloudAccount(ctx, acct))
		*dst = r.seed(ctx, t, r.family+":"+acct.ID, &acct.ID)
	}
	return r
}

func (r *retryFamilyRig) seed(ctx context.Context, t *testing.T, key string, account *string) *PurchaseExecution {
	t.Helper()
	row := &PurchaseExecution{
		ExecutionID: uuid.NewString(), PlanID: r.planID, IdempotencyKey: key, CloudAccountID: account,
		Status: StatusFailed, NoSubmission: true, ScheduledDate: time.Now(),
	}
	require.NoError(t, r.store.SavePurchaseExecution(ctx, row))
	return row
}

// retry links row to a fresh pending successor, as a committed retry would.
func (r *retryFamilyRig) retry(ctx context.Context, t *testing.T, row *PurchaseExecution) string {
	t.Helper()
	succ := &PurchaseExecution{
		ExecutionID: uuid.NewString(), PlanID: r.planID, IdempotencyKey: row.IdempotencyKey, CloudAccountID: row.CloudAccountID,
		Status: "pending", ScheduledDate: time.Now(), RetryAttemptN: 1,
	}
	require.NoError(t, r.store.WithTx(ctx, func(tx pgx.Tx) error {
		if err := r.store.SavePurchaseExecutionTx(ctx, tx, succ); err != nil {
			return err
		}
		linked, err := r.store.LinkRetryExecutionAtomic(ctx, tx, row.ExecutionID, succ.ExecutionID)
		require.True(t, linked)
		return err
	}))
	return succ.ExecutionID
}

func (r *retryFamilyRig) claimed(ctx context.Context, t *testing.T, row *PurchaseExecution) bool {
	t.Helper()
	var got bool
	require.NoError(t, r.store.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		got, err = r.store.FamilyRetryClaimedTx(ctx, tx, r.family, r.planID, row.ExecutionID, row.CloudAccountID == nil)
		return err
	}))
	return got
}

func TestFamilyRetryClaimed_OnlyTheCounterpartKindCounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)

	t.Run("nothing retried", func(t *testing.T) {
		r := newRetryFamilyRig(ctx, t, store)
		for _, row := range []*PurchaseExecution{r.root, r.a, r.b} {
			assert.False(t, r.claimed(ctx, t, row))
		}
	})
	t.Run("an account row retried claims the root but not its siblings", func(t *testing.T) {
		r := newRetryFamilyRig(ctx, t, store)
		r.retry(ctx, t, r.a)
		assert.True(t, r.claimed(ctx, t, r.root), "root sees the retried account row")
		assert.False(t, r.claimed(ctx, t, r.b), "same-kind sibling stays allowed")
		assert.False(t, r.claimed(ctx, t, r.a), "a row never conflicts with itself")
	})
	t.Run("the root retried claims every account row but not itself", func(t *testing.T) {
		r := newRetryFamilyRig(ctx, t, store)
		r.retry(ctx, t, r.root)
		assert.True(t, r.claimed(ctx, t, r.a))
		assert.True(t, r.claimed(ctx, t, r.b))
		assert.False(t, r.claimed(ctx, t, r.root))
	})
	t.Run("another family or plan is unaffected", func(t *testing.T) {
		r := newRetryFamilyRig(ctx, t, store)
		other := newRetryFamilyRig(ctx, t, store)
		r.retry(ctx, t, r.a)
		assert.False(t, other.claimed(ctx, t, other.root), "different key family")
		var got bool
		require.NoError(t, store.WithTx(ctx, func(tx pgx.Tx) error {
			var err error
			got, err = store.FamilyRetryClaimedTx(ctx, tx, r.family, other.planID, r.root.ExecutionID, true)
			return err
		}))
		assert.False(t, got, "same family text under a different plan")
	})
	t.Run("deleting the successor reopens the counterpart", func(t *testing.T) {
		r := newRetryFamilyRig(ctx, t, store)
		succ := r.retry(ctx, t, r.a)
		require.True(t, r.claimed(ctx, t, r.root))
		_, err := store.db.Exec(ctx, `DELETE FROM purchase_executions WHERE execution_id = $1`, succ)
		require.NoError(t, err)
		assert.False(t, r.claimed(ctx, t, r.root), "ON DELETE SET NULL clears the claim")
	})
}

// A legacy row without a key is its own family root: its account rows carry
// "<execution_id>:<account>".
func TestFamilyRetryClaimed_LegacyRootWithoutKeyUsesExecutionID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)
	r := newRetryFamilyRig(ctx, t, store)
	legacy := r.seed(ctx, t, "", nil)
	// SavePurchaseExecution mints a key, so blank it the way pre-key rows are.
	_, err := store.db.Exec(ctx, `UPDATE purchase_executions SET idempotency_key = '' WHERE execution_id = $1`, legacy.ExecutionID)
	require.NoError(t, err)
	acct := &CloudAccount{Name: "legacy-acct-" + uuid.NewString(), Provider: "aws", ExternalID: "999999999991", Enabled: true}
	require.NoError(t, store.CreateCloudAccount(ctx, acct))
	child := r.seed(ctx, t, legacy.ExecutionID+":"+acct.ID, &acct.ID)
	r.retry(ctx, t, child)

	var got bool
	require.NoError(t, store.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		got, err = store.FamilyRetryClaimedTx(ctx, tx, legacy.ExecutionID, r.planID, legacy.ExecutionID, true)
		return err
	}))
	assert.True(t, got)

	// The other direction: the keyless legacy root itself was retried.
	r.retry(ctx, t, legacy)
	other := r.seed(ctx, t, legacy.ExecutionID+":"+uuid.NewString(), &acct.ID)
	require.NoError(t, store.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		got, err = store.FamilyRetryClaimedTx(ctx, tx, legacy.ExecutionID, r.planID, other.ExecutionID, false)
		return err
	}))
	assert.True(t, got, "a keyless root is matched by its execution id")
}

func TestLockRetryFamily_RejectsEmptyFamily(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)
	err := store.WithTx(ctx, func(tx pgx.Tx) error { return store.LockRetryFamilyTx(ctx, tx, "") })
	require.ErrorContains(t, err, "empty family")
}

// A stuck holder of the family lock fails the waiter with 55P03 instead of
// queueing it forever, a different family is not blocked, and the timeout does
// not leak into later statements of a transaction that got the lock.
func TestLockRetryFamily_TimeoutIsPerFamilyAndDoesNotLeak(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store := setupLadderStore(ctx, t)
	family := uuid.NewString()

	old := ladderLockTimeout
	ladderLockTimeout = 300 * time.Millisecond
	defer func() { ladderLockTimeout = old }()

	holder, err := store.db.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback(ctx) }()
	require.NoError(t, store.LockRetryFamilyTx(ctx, holder, family))

	err = store.WithTx(ctx, func(tx pgx.Tx) error { return store.LockRetryFamilyTx(ctx, tx, family) })
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr, "a stuck holder must fail the waiter loudly")
	assert.Equal(t, "55P03", pgErr.Code)

	var timeout string
	require.NoError(t, store.WithTx(ctx, func(tx pgx.Tx) error {
		if lockErr := store.LockRetryFamilyTx(ctx, tx, uuid.NewString()); lockErr != nil {
			return lockErr
		}
		return tx.QueryRow(ctx, `SHOW lock_timeout`).Scan(&timeout)
	}), "another family must not wait")
	assert.Equal(t, "0", timeout, "the wait timeout must not leak into later statements")
}
