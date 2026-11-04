//go:build integration
// +build integration

package config

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const lineageTestHorizon = 24 * time.Hour

var lineageTuple = RedshiftTuple{Region: "us-east-1", NodeType: "ra3.xlplus", Term: 1, Payment: "all-upfront", Count: 2}

// seedLineageRow inserts an execution holding one selected Redshift rec that
// matches lineageTuple, in the given lineage family, status and stamp age.
func seedLineageRow(ctx context.Context, t *testing.T, store *PostgresStore, family, status string, stampAge *time.Duration) *PurchaseExecution {
	t.Helper()
	exec := newNoSubmissionExec(ctx, t, store, status)
	exec.Recommendations = []RecommendationRecord{{
		Provider: "aws", Service: "redshift", Region: lineageTuple.Region, ResourceType: lineageTuple.NodeType,
		Term: lineageTuple.Term, Payment: lineageTuple.Payment, Count: lineageTuple.Count, Selected: true,
	}}
	require.NoError(t, store.SavePurchaseExecution(ctx, exec))
	var stamp *time.Time
	if stampAge != nil {
		ts := time.Now().Add(-*stampAge)
		stamp = &ts
	}
	_, err := store.db.Exec(ctx, `UPDATE purchase_executions SET idempotency_key = $2, first_dispatch_at = $3 WHERE execution_id = $1`,
		exec.ExecutionID, family, stamp)
	require.NoError(t, err)
	return exec
}

func findLineageConflicts(ctx context.Context, t *testing.T, store *PostgresStore, self, family string) []RedshiftLineageConflict {
	t.Helper()
	var got []RedshiftLineageConflict
	require.NoError(t, store.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		got, err = store.FindOpenRedshiftLineageConflictsTx(ctx, tx, self, family, []RedshiftTuple{lineageTuple}, nil, lineageTestHorizon)
		return err
	}))
	return got
}

func ptrDur(d time.Duration) *time.Duration { return &d }

func conflictIDs(cs []RedshiftLineageConflict) []string {
	ids := make([]string, 0, len(cs))
	for _, c := range cs {
		ids = append(ids, c.ExecutionID)
	}
	return ids
}

func TestFindOpenRedshiftLineageConflicts(t *testing.T) {
	ctx := context.Background()
	store := setupLadderStore(ctx, t)
	recent, old := ptrDur(time.Hour), ptrDur(48*time.Hour)
	// Rows of one subtest must not leak into the next: they all share a tuple.
	reset := func(t *testing.T) {
		t.Helper()
		_, err := store.db.Exec(ctx, `DELETE FROM purchase_executions`)
		require.NoError(t, err)
	}

	t.Run("same_family_running_other_row_conflicts", func(t *testing.T) {
		reset(t)
		self := seedLineageRow(ctx, t, store, "fam-a", "running", recent)
		other := seedLineageRow(ctx, t, store, "fam-a:acct", "running", recent)
		got := findLineageConflicts(ctx, t, store, self.ExecutionID, "fam-a")
		require.Len(t, got, 1)
		assert.Equal(t, other.ExecutionID, got[0].ExecutionID)
		assert.True(t, got[0].SameFamily)
		assert.Equal(t, "running", got[0].Status)
		require.NotNil(t, got[0].FirstDispatchAt)
		require.Len(t, got[0].Recommendations, 1)
	})
	t.Run("same_family_self_excluded", func(t *testing.T) {
		reset(t)
		self := seedLineageRow(ctx, t, store, "fam-b", "running", recent)
		assert.Empty(t, findLineageConflicts(ctx, t, store, self.ExecutionID, "fam-b"))
	})
	t.Run("same_family_non_running_excluded", func(t *testing.T) {
		reset(t)
		self := seedLineageRow(ctx, t, store, "fam-c", "running", recent)
		for _, st := range []string{"failed", "pending", "approved"} {
			seedLineageRow(ctx, t, store, "fam-c:"+st, st, recent)
		}
		assert.Empty(t, findLineageConflicts(ctx, t, store, self.ExecutionID, "fam-c"))
	})
	t.Run("other_family_running_any_stamp_age_conflicts", func(t *testing.T) {
		reset(t)
		self := seedLineageRow(ctx, t, store, "fam-d", "running", recent)
		other := seedLineageRow(ctx, t, store, "fam-d-other", "running", old)
		got := findLineageConflicts(ctx, t, store, self.ExecutionID, "fam-d")
		require.Len(t, got, 1)
		assert.Equal(t, []string{other.ExecutionID}, conflictIDs(got))
		assert.False(t, got[0].SameFamily)
	})
	t.Run("other_family_pending_with_stamp_conflicts", func(t *testing.T) {
		reset(t)
		self := seedLineageRow(ctx, t, store, "fam-e", "running", recent)
		other := seedLineageRow(ctx, t, store, "fam-e-other", "pending", recent)
		assert.Equal(t, []string{other.ExecutionID}, conflictIDs(findLineageConflicts(ctx, t, store, self.ExecutionID, "fam-e")))
	})
	t.Run("paused_conflicts", func(t *testing.T) {
		reset(t)
		self := seedLineageRow(ctx, t, store, "fam-f", "running", recent)
		other := seedLineageRow(ctx, t, store, "fam-f-other", "paused", recent)
		assert.Equal(t, []string{other.ExecutionID}, conflictIDs(findLineageConflicts(ctx, t, store, self.ExecutionID, "fam-f")))
	})
	t.Run("approved_and_scheduled_conflict", func(t *testing.T) {
		reset(t)
		self := seedLineageRow(ctx, t, store, "fam-g", "running", recent)
		a := seedLineageRow(ctx, t, store, "fam-g-a", "approved", recent)
		s := seedLineageRow(ctx, t, store, "fam-g-s", "scheduled", recent)
		n := seedLineageRow(ctx, t, store, "fam-g-n", "notified", recent)
		assert.ElementsMatch(t, []string{a.ExecutionID, s.ExecutionID, n.ExecutionID},
			conflictIDs(findLineageConflicts(ctx, t, store, self.ExecutionID, "fam-g")))
	})
	t.Run("stamp_older_than_H_does_not_conflict_when_not_running", func(t *testing.T) {
		reset(t)
		self := seedLineageRow(ctx, t, store, "fam-h", "running", recent)
		seedLineageRow(ctx, t, store, "fam-h-other", "pending", old)
		assert.Empty(t, findLineageConflicts(ctx, t, store, self.ExecutionID, "fam-h"))
	})
	t.Run("row_without_stamp_ignored", func(t *testing.T) {
		reset(t)
		self := seedLineageRow(ctx, t, store, "fam-i", "running", recent)
		seedLineageRow(ctx, t, store, "fam-i-other", "pending", nil)
		assert.Empty(t, findLineageConflicts(ctx, t, store, self.ExecutionID, "fam-i"))
	})
	t.Run("UnstampedRunningRowIsNotAConflict", func(t *testing.T) {
		reset(t)
		self := seedLineageRow(ctx, t, store, "fam-j", "running", recent)
		seedLineageRow(ctx, t, store, "fam-j-other", "running", nil)
		assert.Empty(t, findLineageConflicts(ctx, t, store, self.ExecutionID, "fam-j"))
	})
	t.Run("row_account_null_falls_back_to_rec_account", func(t *testing.T) {
		reset(t)
		a1, a2 := "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
		self := seedLineageRow(ctx, t, store, "fam-k", "running", recent)
		other := seedLineageRow(ctx, t, store, "fam-k-other", "running", recent)
		_, err := store.db.Exec(ctx, `UPDATE purchase_executions SET recommendations = jsonb_set(recommendations, '{0,cloud_account_id}', to_jsonb($2::text)) WHERE execution_id = $1`, other.ExecutionID, a1)
		require.NoError(t, err)
		find := func(acct string) []RedshiftLineageConflict {
			tp := lineageTuple
			tp.CloudAccountID = &acct
			var got []RedshiftLineageConflict
			require.NoError(t, store.WithTx(ctx, func(tx pgx.Tx) error {
				var e error
				got, e = store.FindOpenRedshiftLineageConflictsTx(ctx, tx, self.ExecutionID, "fam-k", []RedshiftTuple{tp}, nil, lineageTestHorizon)
				return e
			}))
			return got
		}
		assert.Empty(t, find(a2))
		assert.Equal(t, []string{other.ExecutionID}, conflictIDs(find(a1)))
	})
	t.Run("null_recommendations_json_does_not_error", func(t *testing.T) {
		reset(t)
		self := seedLineageRow(ctx, t, store, "fam-l", "running", recent)
		other := seedLineageRow(ctx, t, store, "fam-l-other", "running", recent)
		_, err := store.db.Exec(ctx, `UPDATE purchase_executions SET recommendations = 'null'::jsonb WHERE execution_id = $1`, other.ExecutionID)
		require.NoError(t, err)
		assert.Empty(t, findLineageConflicts(ctx, t, store, self.ExecutionID, "fam-l"))
	})
	t.Run("empty_family_is_rejected", func(t *testing.T) {
		reset(t)
		require.NoError(t, store.WithTx(ctx, func(tx pgx.Tx) error {
			_, err := store.FindOpenRedshiftLineageConflictsTx(ctx, tx, "x", "", []RedshiftTuple{lineageTuple}, nil, lineageTestHorizon)
			assert.Error(t, err)
			return nil
		}))
	})
}
