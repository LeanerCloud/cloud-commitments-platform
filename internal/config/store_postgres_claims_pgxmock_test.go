package config

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/pashagolub/pgxmock/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const claimTok = "tok-0123456789abcdef"

func anyArgs(n int) []any {
	args := make([]any, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

var (
	claimHandle = common.Claim{Token: claimTok, Holder: "h1", Generation: 3, Phase: common.ClaimClaimed}
	acquireCols = []string{"generation", "phase"}
	inspectCols = []string{"phase", "order_id", "lease_left", "settle_left"}
)

// The fencing rules live in SQL, so the shape is pinned: every statement that
// changes a held row matches the handle's (holder, generation), the deadline and
// the settle window come from the database clock, and the takeover is one
// conditional upsert.
func TestClaimSQL_Shape(t *testing.T) {
	t.Parallel()
	for name, sql := range map[string]string{
		"renew": renewClaimSQL, "advance": advanceClaimSQL, "complete": completeClaimSQL, "drop": dropClaimSQL,
	} {
		assert.Contains(t, sql, "holder = $2 AND generation = $3", name)
	}
	assert.Contains(t, advanceClaimSQL, "phase IN ('claimed', 'sending')")
	assert.Contains(t, dropClaimSQL, "AND phase = 'claimed'\n    RETURNING", "delete is store-side conditional on phase claimed")
	assert.Equal(t, 1, strings.Count(acquireClaimSQL, "INSERT INTO purchase_claims"), "single statement")
	assert.Contains(t, acquireClaimSQL, "ON CONFLICT (idempotency_token) DO UPDATE")
	assert.Contains(t, acquireClaimSQL, "generation = c.generation + 1")
	assert.Contains(t, acquireClaimSQL, "c.phase <> 'completed'")
	assert.Contains(t, acquireClaimSQL, "c.expires_at <= now()")
	assert.Contains(t, acquireClaimSQL, "c.phase = 'claimed' OR c.last_renewed_at + make_interval(secs => $4::float8) <= now()")
	assert.NotContains(t, acquireClaimSQL, "phase = EXCLUDED", "a takeover keeps the phase")
	for name, sql := range map[string]string{"acquire": acquireClaimSQL, "renew": renewClaimSQL, "inspect": inspectClaimSQL} {
		assert.NotContains(t, sql, "$5", name)
	}
}

func TestAcquireClaim_Takes(t *testing.T) {
	t.Parallel()
	pool := newMock(t)
	defer pool.Close()
	pool.ExpectQuery("INSERT INTO purchase_claims").
		WithArgs(claimTok, "h1", 120.0, 600.0).
		WillReturnRows(pgxmock.NewRows(acquireCols).AddRow(int64(2), "sending"))
	c, err := storeWith(pool).AcquireClaim(context.Background(), claimTok, "h1", 2*time.Minute, 10*time.Minute)
	require.NoError(t, err)
	assert.Equal(t, common.Claim{Token: claimTok, Holder: "h1", Generation: 2, Phase: common.ClaimSending}, c)
	require.NoError(t, pool.ExpectationsWereMet(), "no transaction and no second statement")
}

func TestAcquireClaim_Refusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		wantErr   error
		name      string
		phase     string
		orderID   string
		wantClaim common.Claim
		leaseLeft float64
		settle    float64
	}{
		{name: "live lease", phase: "claimed", leaseLeft: 30, wantErr: common.ErrClaimHeld},
		{name: "live sending lease", phase: "sending", leaseLeft: 30, wantErr: common.ErrClaimHeld},
		{name: "settle pending", phase: "sending", settle: 400, wantErr: common.ErrClaimSettlePending},
		{name: "completed", phase: "completed", orderID: "order-1",
			wantClaim: common.Claim{Token: claimTok, Phase: common.ClaimCompleted, OrderID: "order-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pool := newMock(t)
			defer pool.Close()
			pool.ExpectQuery("INSERT INTO purchase_claims").WithArgs(anyArgs(4)...).WillReturnRows(pgxmock.NewRows(acquireCols))
			pool.ExpectQuery("FROM purchase_claims WHERE idempotency_token").WithArgs(claimTok, 600.0).
				WillReturnRows(pgxmock.NewRows(inspectCols).AddRow(tc.phase, tc.orderID, tc.leaseLeft, tc.settle))
			c, err := storeWith(pool).AcquireClaim(context.Background(), claimTok, "h2", 2*time.Minute, 10*time.Minute)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Equal(t, common.Claim{}, c)
				assert.Equal(t, errors.Is(tc.wantErr, common.ErrClaimSettlePending), errors.Is(err, common.ErrOutcomeUnknown))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantClaim, c)
		})
	}
}

func TestAcquireClaim_RetriesWhenRowVanishes(t *testing.T) {
	t.Parallel()
	pool := newMock(t)
	defer pool.Close()
	for i := 0; i < 2; i++ {
		pool.ExpectQuery("INSERT INTO purchase_claims").WithArgs(anyArgs(4)...).WillReturnRows(pgxmock.NewRows(acquireCols))
		pool.ExpectQuery("FROM purchase_claims WHERE idempotency_token").WithArgs(anyArgs(2)...).WillReturnRows(pgxmock.NewRows(inspectCols))
	}
	pool.ExpectQuery("INSERT INTO purchase_claims").WithArgs(anyArgs(4)...).WillReturnRows(pgxmock.NewRows(acquireCols).AddRow(int64(1), "claimed"))
	c, err := storeWith(pool).AcquireClaim(context.Background(), claimTok, "h1", time.Minute, time.Minute)
	require.NoError(t, err)
	assert.EqualValues(t, 1, c.Generation)
	require.NoError(t, pool.ExpectationsWereMet())
}

func TestAcquireClaim_FailsLoud(t *testing.T) {
	t.Parallel()
	boom := errors.New("connection reset")
	pool := newMock(t)
	defer pool.Close()
	pool.ExpectQuery("INSERT INTO purchase_claims").WithArgs(anyArgs(4)...).WillReturnError(boom)
	_, err := storeWith(pool).AcquireClaim(context.Background(), claimTok, "h1", time.Minute, time.Minute)
	require.ErrorIs(t, err, boom)
	assert.NotErrorIs(t, err, common.ErrClaimHeld)

	pool2 := newMock(t)
	defer pool2.Close()
	pool2.ExpectQuery("INSERT INTO purchase_claims").WithArgs(anyArgs(4)...).WillReturnRows(pgxmock.NewRows(acquireCols))
	pool2.ExpectQuery("FROM purchase_claims WHERE idempotency_token").WithArgs(anyArgs(2)...).WillReturnError(boom)
	_, err = storeWith(pool2).AcquireClaim(context.Background(), claimTok, "h1", time.Minute, time.Minute)
	require.ErrorIs(t, err, boom, "an unreadable row is never reported as held or free")

	// Validation runs before any statement (the mock has no expectations).
	pool3 := newMock(t)
	defer pool3.Close()
	_, err = storeWith(pool3).AcquireClaim(context.Background(), "", "h1", time.Minute, time.Minute)
	require.Error(t, err)
	_, err = storeWith(pool3).AcquireClaim(context.Background(), claimTok, "h1", 0, time.Minute)
	require.Error(t, err)
}

// A handle whose (holder, generation) no longer matches updates no row: Renew,
// Advance and a completing Release report ErrClaimLost, and the statement carries
// the handle's own generation so a successor's row cannot match.
func TestOwnedWrites_StaleHandleIsLost(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, tc := range map[string]struct {
		do     func(s *PostgresStore) error
		expect func(p pgxmock.PgxPoolIface) *pgxmock.ExpectedExec
	}{
		"renew": {
			do: func(s *PostgresStore) error { return s.RenewClaim(ctx, claimHandle, 2*time.Minute) },
			expect: func(p pgxmock.PgxPoolIface) *pgxmock.ExpectedExec {
				return p.ExpectExec("UPDATE purchase_claims").WithArgs(claimTok, "h1", int64(3), 120.0)
			},
		},
		"advance": {
			do: func(s *PostgresStore) error { return s.AdvanceClaim(ctx, claimHandle, common.ClaimSending) },
			expect: func(p pgxmock.PgxPoolIface) *pgxmock.ExpectedExec {
				return p.ExpectExec("SET phase = 'sending'").WithArgs(claimTok, "h1", int64(3))
			},
		},
		"complete": {
			do: func(s *PostgresStore) error {
				return s.ReleaseClaim(ctx, claimHandle, common.ClaimOutcome{OrderID: "order-9"})
			},
			expect: func(p pgxmock.PgxPoolIface) *pgxmock.ExpectedExec {
				return p.ExpectExec("SET phase = 'completed'").WithArgs(claimTok, "h1", int64(3), "order-9")
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			pool := newMock(t)
			defer pool.Close()
			tc.expect(pool).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			require.ErrorIs(t, tc.do(storeWith(pool)), common.ErrClaimLost)
			require.NoError(t, pool.ExpectationsWereMet())

			pool2 := newMock(t)
			defer pool2.Close()
			tc.expect(pool2).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			require.NoError(t, tc.do(storeWith(pool2)))

			pool3 := newMock(t)
			defer pool3.Close()
			boom := errors.New("timeout")
			tc.expect(pool3).WillReturnError(boom)
			err := tc.do(storeWith(pool3))
			require.ErrorIs(t, err, boom)
			assert.NotErrorIs(t, err, common.ErrClaimLost, "an unknown state is not a lost claim")
		})
	}
}

func TestAdvanceClaim_OnlySending(t *testing.T) {
	t.Parallel()
	pool := newMock(t)
	defer pool.Close()
	for _, p := range []common.ClaimPhase{common.ClaimClaimed, common.ClaimCompleted} {
		require.Error(t, storeWith(pool).AdvanceClaim(context.Background(), claimHandle, p))
	}
	require.NoError(t, pool.ExpectationsWereMet(), "no statement for a refused phase")
}

func TestReleaseClaim_NothingBought(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name            string
		deleted, sendng bool
		wantLost        bool
	}{
		{name: "claimed row deleted", deleted: true},
		{name: "sending row kept", sendng: true},
		{name: "stale handle", wantLost: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pool := newMock(t)
			defer pool.Close()
			pool.ExpectQuery("DELETE FROM purchase_claims").WithArgs(claimTok, "h1", int64(3)).
				WillReturnRows(pgxmock.NewRows([]string{"deleted", "sending"}).AddRow(tc.deleted, tc.sendng))
			err := storeWith(pool).ReleaseClaim(context.Background(), claimHandle, common.ClaimOutcome{})
			if tc.wantLost {
				require.ErrorIs(t, err, common.ErrClaimLost)
				return
			}
			require.NoError(t, err)
		})
	}
}

// A lease that runs out between the refused upsert and the diagnostic read is
// retried, not reported as held or as a settle wait (which would claim an unknown
// outcome for a row that never reached sending).
func TestAcquireClaim_RetriesWhenRowBecomesTakeable(t *testing.T) {
	t.Parallel()
	for name, row := range map[string][]any{
		"claimed lease ran out": {"claimed", "", 0.0, 0.0},
		"settle window ran out": {"sending", "", 0.0, 0.0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			pool := newMock(t)
			defer pool.Close()
			pool.ExpectQuery("INSERT INTO purchase_claims").WithArgs(anyArgs(4)...).WillReturnRows(pgxmock.NewRows(acquireCols))
			pool.ExpectQuery("FROM purchase_claims WHERE idempotency_token").WithArgs(anyArgs(2)...).
				WillReturnRows(pgxmock.NewRows(inspectCols).AddRow(row...))
			pool.ExpectQuery("INSERT INTO purchase_claims").WithArgs(anyArgs(4)...).
				WillReturnRows(pgxmock.NewRows(acquireCols).AddRow(int64(2), "claimed"))
			c, err := storeWith(pool).AcquireClaim(context.Background(), claimTok, "h1", time.Minute, time.Minute)
			require.NoError(t, err)
			assert.EqualValues(t, 2, c.Generation)
			require.NoError(t, pool.ExpectationsWereMet())
		})
	}
}

func TestAcquireClaim_GivesUpWhenRowKeepsChanging(t *testing.T) {
	t.Parallel()
	pool := newMock(t)
	defer pool.Close()
	for i := 0; i < maxAcquireAttempts; i++ {
		pool.ExpectQuery("INSERT INTO purchase_claims").WithArgs(anyArgs(4)...).WillReturnRows(pgxmock.NewRows(acquireCols))
		pool.ExpectQuery("FROM purchase_claims WHERE idempotency_token").WithArgs(anyArgs(2)...).WillReturnRows(pgxmock.NewRows(inspectCols))
	}
	_, err := storeWith(pool).AcquireClaim(context.Background(), claimTok, "h1", time.Minute, time.Minute)
	require.Error(t, err)
	assert.NotErrorIs(t, err, common.ErrClaimHeld)
	require.NoError(t, pool.ExpectationsWereMet())
}
