//go:build integration
// +build integration

package config

import (
	"context"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const attemptToken = "tok-0123456789abcdef"

func attemptRec(exec string, state common.AttemptState, id, offering string) common.AttemptRecord {
	return common.AttemptRecord{IdempotencyToken: attemptToken, ExecutionID: exec, State: state, ReservationID: id, OfferingID: offering}
}

// TestRecordAttempt_TransitionTable pins the 16 cases (4 sources x 4 targets)
// of go's validTransition: sources none, sending, outcome_unknown, purchased
// (rn-1); targets sending, outcome_unknown, purchased rn-1, purchased rn-2.
func TestRecordAttempt_TransitionTable(t *testing.T) {
	ctx := context.Background()
	store := setupLadderStore(ctx, t)

	// The unique purchased-reservation index spans executions, so every case
	// gets its own ids: "rn-1-<exec>" and "rn-2-<exec>".
	sources := map[string]func(exec string) []common.AttemptRecord{
		"none": func(string) []common.AttemptRecord { return nil },
		"sending": func(e string) []common.AttemptRecord {
			return []common.AttemptRecord{attemptRec(e, common.AttemptSending, "", "")}
		},
		"outcome_unknown": func(e string) []common.AttemptRecord {
			return []common.AttemptRecord{attemptRec(e, common.AttemptSending, "", ""), attemptRec(e, common.AttemptOutcomeUnknown, "", "")}
		},
		"purchased": func(e string) []common.AttemptRecord {
			return []common.AttemptRecord{attemptRec(e, common.AttemptPurchased, "rn-1-"+e, "")}
		},
	}
	targets := []struct {
		name string
		rec  func(exec string) common.AttemptRecord
		// allowed is keyed by source name.
		allowed map[string]bool
	}{
		{"sending", func(e string) common.AttemptRecord { return attemptRec(e, common.AttemptSending, "", "") },
			map[string]bool{"none": true}},
		{"outcome_unknown", func(e string) common.AttemptRecord { return attemptRec(e, common.AttemptOutcomeUnknown, "", "") },
			map[string]bool{"sending": true}},
		{"purchased_same_id", func(e string) common.AttemptRecord { return attemptRec(e, common.AttemptPurchased, "rn-1-"+e, "") },
			map[string]bool{"none": true, "sending": true, "outcome_unknown": true, "purchased": true}},
		{"purchased_other_id", func(e string) common.AttemptRecord { return attemptRec(e, common.AttemptPurchased, "rn-2-"+e, "") },
			map[string]bool{"none": true, "sending": true, "outcome_unknown": true}},
	}

	n := 0
	for srcName, setup := range sources {
		for _, tgt := range targets {
			n++
			exec := fmt.Sprintf("exec-%d", n)
			t.Run(srcName+"_to_"+tgt.name, func(t *testing.T) {
				for _, s := range setup(exec) {
					require.NoError(t, store.RecordAttempt(ctx, s))
				}
				err := store.RecordAttempt(ctx, tgt.rec(exec))
				if tgt.allowed[srcName] {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			})
		}
	}
	assert.Equal(t, 16, n)
}

// execCounter counts statements sent to the database, so a test can tell a
// record rejected by Validate from one rejected by a table CHECK.
type execCounter struct {
	dbConn
	execs int
}

func (c *execCounter) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	c.execs++
	return c.dbConn.Exec(ctx, sql, args...)
}

func TestRecordAttempt_InvalidRecordRejectedBeforeUpsert(t *testing.T) {
	ctx := context.Background()
	store := setupLadderStore(ctx, t)
	counter := &execCounter{dbConn: store.db}
	store.db = counter

	require.Error(t, store.RecordAttempt(ctx, attemptRec("exec-1", common.AttemptPurchased, "", "")), "purchased without an ID")
	require.Error(t, store.RecordAttempt(ctx, attemptRec("exec-1", common.AttemptSending, "rn-1", "")), "ID on a non-purchased record")
	require.Error(t, store.RecordAttempt(ctx, attemptRec("exec-1", common.AttemptState("bogus"), "", "")), "unknown state")
	require.Error(t, store.RecordAttempt(ctx, attemptRec("", common.AttemptSending, "", "")), "empty execution")

	assert.Zero(t, counter.execs, "no invalid record reached the database")
	got, err := store.PriorAttempts(ctx, attemptToken)
	require.NoError(t, err)
	assert.Empty(t, got, "no invalid record reached the table")
}

func TestPriorAttempts_AcrossExecutions(t *testing.T) {
	ctx := context.Background()
	store := setupLadderStore(ctx, t)

	got, err := store.PriorAttempts(ctx, attemptToken)
	require.NoError(t, err)
	assert.Empty(t, got, "no rows is not an error")

	require.NoError(t, store.RecordAttempt(ctx, attemptRec("exec-a", common.AttemptSending, "", "off-1")))
	require.NoError(t, store.RecordAttempt(ctx, attemptRec("exec-b", common.AttemptPurchased, "rn-9", "off-1")))
	other := attemptRec("exec-c", common.AttemptSending, "", "")
	other.IdempotencyToken = "other-token-0123456"
	require.NoError(t, store.RecordAttempt(ctx, other))

	got, err = store.PriorAttempts(ctx, attemptToken)
	require.NoError(t, err)
	assert.ElementsMatch(t, []common.AttemptRecord{
		attemptRec("exec-a", common.AttemptSending, "", "off-1"),
		attemptRec("exec-b", common.AttemptPurchased, "rn-9", "off-1"),
	}, got)
}

func TestRecordAttempt_OfferingIDWriteOnce(t *testing.T) {
	ctx := context.Background()
	store := setupLadderStore(ctx, t)

	require.NoError(t, store.RecordAttempt(ctx, attemptRec("exec-1", common.AttemptSending, "", "off-first")))
	require.NoError(t, store.RecordAttempt(ctx, attemptRec("exec-1", common.AttemptPurchased, "rn-1", "off-second")))
	got, err := store.PriorAttempts(ctx, attemptToken)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "off-first", got[0].OfferingID, "first offering survives a later write")

	// A row first written without an offering takes the first one offered.
	require.NoError(t, store.RecordAttempt(ctx, attemptRec("exec-2", common.AttemptSending, "", "")))
	require.NoError(t, store.RecordAttempt(ctx, attemptRec("exec-2", common.AttemptOutcomeUnknown, "", "off-late")))
	got, err = store.PriorAttempts(ctx, attemptToken)
	require.NoError(t, err)
	for _, r := range got {
		if r.ExecutionID == "exec-2" {
			assert.Equal(t, "off-late", r.OfferingID)
		}
	}
}

func TestClaimedByOthers(t *testing.T) {
	ctx := context.Background()
	store := setupLadderStore(ctx, t)
	const otherToken = "other-token-0123456"

	require.NoError(t, store.RecordAttempt(ctx, attemptRec("exec-own", common.AttemptPurchased, "rn-own", "")))
	other := common.AttemptRecord{IdempotencyToken: otherToken, ExecutionID: "exec-o1", State: common.AttemptPurchased, ReservationID: "rn-other"}
	require.NoError(t, store.RecordAttempt(ctx, other))
	sending := common.AttemptRecord{IdempotencyToken: otherToken, ExecutionID: "exec-o2", State: common.AttemptSending}
	require.NoError(t, store.RecordAttempt(ctx, sending))

	got, err := store.ClaimedByOthers(ctx, attemptToken, []string{"rn-own", "rn-other", "rn-unknown"})
	require.NoError(t, err)
	assert.Equal(t, []string{"rn-other"}, got, "only another token's purchased id is claimed")

	got, err = store.ClaimedByOthers(ctx, otherToken, []string{"rn-own", "rn-other"})
	require.NoError(t, err)
	assert.Equal(t, []string{"rn-own"}, got, "the view is symmetric per token")
}

// A query failure must surface as a wrapped error with a nil result, never as
// "nothing claimed" (which would let a caller proceed to buy).
func TestClaimedByOthers_QueryErrorReturnsError(t *testing.T) {
	ctx := context.Background()
	store := setupLadderStore(ctx, t)
	counter := &queryCounter{dbConn: store.db}
	store.db = counter

	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()

	got, err := store.ClaimedByOthers(canceledCtx, attemptToken, []string{"rn-1"})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(), "list claimed reservations", "wrapped by the query-error branch")
	assert.Nil(t, got)
	assert.Equal(t, 1, counter.queries, "the query ran, so the empty-ids early return was not taken")
}

func TestClaimedByOthers_EmptyIDsNoQuery(t *testing.T) {
	ctx := context.Background()
	store := setupLadderStore(ctx, t)
	counter := &queryCounter{dbConn: store.db}
	store.db = counter

	got, err := store.ClaimedByOthers(ctx, attemptToken, nil)
	require.NoError(t, err)
	assert.Nil(t, got)
	got, err = store.ClaimedByOthers(ctx, attemptToken, []string{})
	require.NoError(t, err)
	assert.Nil(t, got)
	assert.Zero(t, counter.queries, "no ids means no query")
}

type queryCounter struct {
	dbConn
	queries int
}

func (c *queryCounter) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.queries++
	return c.dbConn.Query(ctx, sql, args...)
}

// The unique purchased-reservation index (migration 000162) makes the second
// purchased write of one reservation fail, whatever the token.
func TestRecordAttempt_PurchasedReservationUniqueAcrossTokens(t *testing.T) {
	ctx := context.Background()
	store := setupLadderStore(ctx, t)

	require.NoError(t, store.RecordAttempt(ctx, attemptRec("exec-1", common.AttemptPurchased, "rn-1", "")))
	second := common.AttemptRecord{IdempotencyToken: "other-token-0123456", ExecutionID: "exec-2", State: common.AttemptPurchased, ReservationID: "rn-1"}
	err := store.RecordAttempt(ctx, second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already recorded as purchased")
	assert.NotContains(t, err.Error(), "SQLSTATE", "no raw pg detail")
	assert.NotContains(t, err.Error(), second.IdempotencyToken, "token stays masked")

	// The same row re-recorded with the same id stays allowed.
	require.NoError(t, store.RecordAttempt(ctx, attemptRec("exec-1", common.AttemptPurchased, "rn-1", "")))
}
