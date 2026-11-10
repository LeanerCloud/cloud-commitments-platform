//go:build integration

package api

// Real PostgreSQL and the real retry handler for platform#1930: a fully refused
// fan-out leaves a failed root and failed account rows that are all proven
// unsent, so the per-row gate lets every one of them through. Retrying both
// kinds would buy the same accounts twice on providers with no duplicate guard.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type retryFamilyFixture struct {
	*createConcurrencyFixture
	handler *Handler
	userID  string
}

func newRetryFamilyFixture(t *testing.T) *retryFamilyFixture {
	t.Helper()
	ctx := context.Background()
	base := newCreateConcurrencyFixture(ctx, t)
	userID := base.newUser(ctx, t, "retrier")
	auth := new(MockAuthService)
	auth.On("ValidateSession", mock.Anything, "admin-token").Return(&Session{UserID: userID, Email: "retrier@example.com"}, nil).Maybe()
	auth.grantAdminPurchaser()
	return &retryFamilyFixture{createConcurrencyFixture: base, handler: &Handler{config: base.store, auth: auth}, userID: userID}
}

func (f *createConcurrencyFixture) newUser(ctx context.Context, t *testing.T, name string) string {
	t.Helper()
	id := uuid.NewString()
	_, err := f.pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, salt, active, group_ids, created_at, updated_at)
		SELECT $1, $2, 'x', 'y', true, ARRAY[g.id], now(), now()
		  FROM groups g ORDER BY g.name LIMIT 1`, id, name+"-"+id+"@example.com")
	require.NoError(t, err)
	return id
}

// fanOut is one refused fan-out: root R plus account rows A and B.
type fanOut struct {
	root, a, b *config.PurchaseExecution
}

// familyRecs returns one recommendation. The count differs per call and the
// account is part of the retry's submit fingerprint, so one test's retries do
// not collapse onto each other in the submit dedupe window.
func familyRecs(provider, service string, account *string) []config.RecommendationRecord {
	return []config.RecommendationRecord{{
		ID: "rec-1", Provider: provider, Service: service, Region: "us-east-1", ResourceType: "m5.large", CloudAccountID: account,
		Count: int(familyAccountSeq.Add(1)), Term: 1, Payment: "no-upfront", UpfrontCost: 1000, Selected: true,
	}}
}

func (f *retryFamilyFixture) saveRow(ctx context.Context, t *testing.T, key string, account *string, recs []config.RecommendationRecord) *config.PurchaseExecution {
	t.Helper()
	row := &config.PurchaseExecution{
		ExecutionID: uuid.NewString(), PlanID: f.planID, IdempotencyKey: key, CloudAccountID: account,
		Status: config.StatusFailed, NoSubmission: true, StepNumber: 1, ScheduledDate: time.Now().Add(-time.Hour),
		Recommendations: recs, Source: common.PurchaseSourceWeb, CapacityPercent: 100, CreatedByUserID: &f.userID,
		Error: "all accounts refused",
	}
	require.NoError(t, f.store.SavePurchaseExecution(ctx, row))
	return row
}

var familyAccountSeq atomic.Int64

func (f *retryFamilyFixture) newFanOut(ctx context.Context, t *testing.T, provider, service string) fanOut {
	t.Helper()
	key := uuid.NewString()
	out := fanOut{root: f.saveRow(ctx, t, key, nil, familyRecs(provider, service, nil))}
	for _, dst := range []**config.PurchaseExecution{&out.a, &out.b} {
		acct := &config.CloudAccount{Name: "fam-acct-" + uuid.NewString(), Provider: provider, ExternalID: fmt.Sprintf("%012d", familyAccountSeq.Add(1)), Enabled: true}
		require.NoError(t, f.store.CreateCloudAccount(ctx, acct))
		*dst = f.saveRow(ctx, t, key+":"+acct.ID, &acct.ID, familyRecs(provider, service, &acct.ID))
	}
	return out
}

func (f *retryFamilyFixture) retry(ctx context.Context, row *config.PurchaseExecution) error {
	_, err := f.handler.retryPurchase(ctx, adminRetryRequest(), row.ExecutionID)
	return err
}

func adminRetryRequest() *events.LambdaFunctionURLRequest {
	return &events.LambdaFunctionURLRequest{Headers: map[string]string{"Authorization": "Bearer admin-token"}}
}

func requireFamilyClaimed(t *testing.T, err error) {
	t.Helper()
	ce, ok := IsClientError(err)
	require.True(t, ok, "want a client error, got %v", err)
	assert.Equal(t, 409, ce.code)
	assert.Equal(t, true, ce.details["retry_family_claimed"], ce.message)
	assert.Contains(t, ce.message, "belongs to a fan-out")
}

func (f *retryFamilyFixture) successorCount(ctx context.Context, t *testing.T, key string) int {
	t.Helper()
	var n int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM purchase_executions
		WHERE retry_attempt_n = 1 AND split_part(idempotency_key, ':', 1) = $1`, key).Scan(&n))
	return n
}

// The refused-fan-out premise: with no guard, every row of it passes the gate.
func TestRetryFamily_RootThenAccountRow(t *testing.T) {
	for _, tc := range []struct{ name, provider, service string }{
		{"ec2", "aws", "ec2"},
		{"azure savings plans", "azure", "savingsplans"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newRetryFamilyFixture(t)
			fo := f.newFanOut(ctx, t, tc.provider, tc.service)

			require.NoError(t, f.retry(ctx, fo.root))
			requireFamilyClaimed(t, f.retry(ctx, fo.a))
			requireFamilyClaimed(t, f.retry(ctx, fo.b))
			assert.Equal(t, 1, f.successorCount(ctx, t, fo.root.IdempotencyKey), "only the root's successor exists")
		})
	}
}

func TestRetryFamily_AccountRowThenRoot(t *testing.T) {
	for _, tc := range []struct{ name, provider, service string }{
		{"ec2", "aws", "ec2"},
		{"azure savings plans", "azure", "savingsplans"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newRetryFamilyFixture(t)
			fo := f.newFanOut(ctx, t, tc.provider, tc.service)

			require.NoError(t, f.retry(ctx, fo.a))
			requireFamilyClaimed(t, f.retry(ctx, fo.root))
			require.NoError(t, f.retry(ctx, fo.b), "a same-kind sibling covers another account and stays allowed")
			assert.Equal(t, 2, f.successorCount(ctx, t, fo.root.IdempotencyKey))
		})
	}
}

// A dedupe-capable provider keeps the old behavior: both kinds retry.
func TestRetryFamily_NonLiftedProviderIsUnaffected(t *testing.T) {
	ctx := context.Background()
	f := newRetryFamilyFixture(t)
	fo := f.newFanOut(ctx, t, "aws", "rds")

	require.NoError(t, f.retry(ctx, fo.root))
	require.NoError(t, f.retry(ctx, fo.a))
}

// A partial root is not failed, so only refused account rows are retryable and
// they do not conflict with each other.
func TestRetryFamily_PartialRootLeavesAccountRowsRetryable(t *testing.T) {
	ctx := context.Background()
	f := newRetryFamilyFixture(t)
	fo := f.newFanOut(ctx, t, "aws", "ec2")
	_, err := f.pool.Exec(ctx, `UPDATE purchase_executions SET status = 'partially_completed', no_submission = false WHERE execution_id = $1`, fo.root.ExecutionID)
	require.NoError(t, err)

	require.NoError(t, f.retry(ctx, fo.a))
	require.NoError(t, f.retry(ctx, fo.b))
}

// A single-account failure (one key, no fan-out) and a plan-less one have no
// counterpart kind.
func TestRetryFamily_SingleAccountAndPlanlessRowsAreUnaffected(t *testing.T) {
	ctx := context.Background()
	f := newRetryFamilyFixture(t)
	acct := &config.CloudAccount{Name: "solo-" + uuid.NewString(), Provider: "aws", ExternalID: fmt.Sprintf("%012d", familyAccountSeq.Add(1)), Enabled: true}
	require.NoError(t, f.store.CreateCloudAccount(ctx, acct))
	solo := f.saveRow(ctx, t, uuid.NewString(), &acct.ID, familyRecs("aws", "ec2", &acct.ID))
	require.NoError(t, f.retry(ctx, solo))

	planless := f.saveRow(ctx, t, uuid.NewString(), nil, familyRecs("aws", "ec2", nil))
	_, err := f.pool.Exec(ctx, `UPDATE purchase_executions SET plan_id = NULL, step_number = 0 WHERE execution_id = $1`, planless.ExecutionID)
	require.NoError(t, err)
	require.NoError(t, f.retry(ctx, planless))
}

// The first retry claims the family; a successor that fails again is attempt 1
// and stays refused by the existing no-guard rule, and the old rows stay claimed.
func TestRetryFamily_SecondRetryAfterFailedFirstIsRefused(t *testing.T) {
	ctx := context.Background()
	f := newRetryFamilyFixture(t)
	fo := f.newFanOut(ctx, t, "aws", "ec2")
	require.NoError(t, f.retry(ctx, fo.root))

	var successorID string
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT retry_execution_id::text FROM purchase_executions WHERE execution_id = $1`, fo.root.ExecutionID).Scan(&successorID))
	_, err := f.pool.Exec(ctx, `UPDATE purchase_executions SET status = 'failed', no_submission = true, error = 'refused again' WHERE execution_id = $1`, successorID)
	require.NoError(t, err)

	ce, ok := IsClientError(f.retry(ctx, &config.PurchaseExecution{ExecutionID: successorID}))
	require.True(t, ok)
	assert.Equal(t, 409, ce.code)
	assert.Contains(t, ce.message, "cannot be retried safely")
	requireFamilyClaimed(t, f.retry(ctx, fo.a))
}

// familyBarrierStore holds each retry after its claim check until both have
// reached it or the other is parked on an advisory lock. With the family lock
// the second retry parks, so the first one commits its link before the second
// reads the claim; without it both read "unclaimed" and both link.
type familyBarrierStore struct {
	*config.PostgresStore
	pool    testhelpers.LockDB
	arrived atomic.Int32
}

func (s *familyBarrierStore) FamilyRetryClaimedTx(ctx context.Context, tx pgx.Tx, family, planID, selfID string, selfIsRoot bool) (bool, error) {
	claimed, err := s.PostgresStore.FamilyRetryClaimedTx(ctx, tx, family, planID, selfID, selfIsRoot)
	if err != nil {
		return false, err
	}
	s.arrived.Add(1)
	arrivedCount := func() int { return int(s.arrived.Load()) }
	if waitErr := testhelpers.AwaitArrivalsOrLockWaits(ctx, s.pool, 2, arrivedCount, "%pg_advisory_xact_lock%"); waitErr != nil {
		return false, waitErr
	}
	return claimed, nil
}

// Two retries by different users (so the per-creator submit lock does not
// already serialize them), one of the root and one of an account row, race:
// exactly one wins, the other is refused, and neither deadlocks.
func TestRetryFamily_ConcurrentRootAndAccountRowRetries(t *testing.T) {
	ctx := context.Background()
	f := newRetryFamilyFixture(t)
	otherUser := f.newUser(ctx, t, "second")
	barrier := &familyBarrierStore{PostgresStore: f.store, pool: f.pool}
	h := &Handler{config: barrier}

	for round := 0; round < 6; round++ {
		fo := f.newFanOut(ctx, t, "aws", "ec2")
		barrier.arrived.Store(0)
		rows := []*config.PurchaseExecution{fo.root, fo.a}
		sessions := []*Session{{UserID: f.userID}, {UserID: otherUser}}
		if round%2 == 1 {
			rows[0], rows[1] = rows[1], rows[0]
		}
		errs := make([]error, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range rows {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, _, errs[i] = h.persistRetryExecution(ctx, rows[i], sessions[i], 0, 0)
			}()
		}
		close(start)
		wg.Wait()

		wins := 0
		for _, err := range errs {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				require.NotEqual(t, "40P01", pgErr.Code, "round %d: deadlock", round)
			}
			if err == nil {
				wins++
				continue
			}
			requireFamilyClaimed(t, err)
		}
		require.Equal(t, 1, wins, "round %d: exactly one of the two kinds may be retried", round)
		require.Equal(t, 1, f.successorCount(ctx, t, fo.root.IdempotencyKey), "round %d", round)
	}
}
