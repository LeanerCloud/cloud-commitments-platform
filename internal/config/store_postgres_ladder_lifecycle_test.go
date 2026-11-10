package config

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var allLadderTrancheStatuses = []ladder.TrancheStatus{
	ladder.TrancheStatusScheduled, ladder.TrancheStatusFired, ladder.TrancheStatusCompleted,
	ladder.TrancheStatusCanceled, ladder.TrancheStatusFailed,
}

// Pure unit test (no database): the edge table is the whole state machine.
func TestLadderTrancheEdges_OnlyDocumentedTransitionsAreLegal(t *testing.T) {
	legal := map[[2]ladder.TrancheStatus]bool{
		{ladder.TrancheStatusScheduled, ladder.TrancheStatusFired}:    true,
		{ladder.TrancheStatusScheduled, ladder.TrancheStatusCanceled}: true,
		{ladder.TrancheStatusFired, ladder.TrancheStatusCompleted}:    true,
		{ladder.TrancheStatusFired, ladder.TrancheStatusFailed}:       true,
	}
	for _, from := range allLadderTrancheStatuses {
		for _, to := range allLadderTrancheStatuses {
			err := validateLadderTrancheEdge(from, to)
			if legal[[2]ladder.TrancheStatus{from, to}] {
				assert.NoError(t, err, "%s -> %s", from, to)
			} else {
				assert.Error(t, err, "%s -> %s must be illegal", from, to)
			}
		}
	}
}

// Illegal edges and the claim-only edge fail before any SQL runs, so a nil DB
// proves no query was attempted.
func TestTransitionLadderTranche_RejectsIllegalEdgesWithoutQuerying(t *testing.T) {
	s := &PostgresStore{}
	for _, from := range allLadderTrancheStatuses {
		for _, to := range allLadderTrancheStatuses {
			if validateLadderTrancheEdge(from, to) == nil && (from != ladder.TrancheStatusScheduled || to != ladder.TrancheStatusFired) {
				continue
			}
			applied, err := s.TransitionLadderTrancheTx(context.Background(), nil, "id", from, to, "test")
			assert.Error(t, err, "%s -> %s", from, to)
			assert.False(t, applied)
		}
	}
}

func TestLadderGraceCutoff_IsExactComplementOfNettingBound(t *testing.T) {
	asOf := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, asOf.Add(-24*time.Hour), ladderGraceCutoff(asOf))
}

// A failed or canceled transition without a reason is rejected before any SQL.
func TestTransitionLadderTranche_RequiresReasonForFailedAndCanceled(t *testing.T) {
	s := &PostgresStore{}
	for _, c := range []struct{ from, to ladder.TrancheStatus }{
		{ladder.TrancheStatusFired, ladder.TrancheStatusFailed},
		{ladder.TrancheStatusScheduled, ladder.TrancheStatusCanceled},
	} {
		applied, err := s.TransitionLadderTrancheTx(context.Background(), nil, "id", c.from, c.to, "")
		assert.ErrorContains(t, err, "needs a status reason", "%s -> %s", c.from, c.to)
		assert.False(t, applied)
	}
}

func TestLadderClaimFilter_Validate(t *testing.T) {
	for _, c := range []struct {
		name    string
		layers  []ladder.LayerType
		wantErr bool
	}{
		{"nil", nil, true},
		{"empty", []ladder.LayerType{}, true},
		{"unknown", []ladder.LayerType{"compute_sp"}, true},
		{"empty string", []ladder.LayerType{""}, true},
		{"one valid one unknown", []ladder.LayerType{ladder.LayerComputeSP, "x"}, true},
		{"valid one", []ladder.LayerType{ladder.LayerComputeSP}, false},
		{"valid many", []ladder.LayerType{ladder.LayerComputeSP, ladder.LayerEC2InstanceSP}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := LadderClaimFilter{Layers: c.layers}.validate()
			if c.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
	assert.ErrorContains(t, LadderClaimFilter{}.validate(), "layer")
}

// Invalid input fails before the config lock and any SQL, so a nil DB proves
// nothing was attempted and the callback never runs.
func TestClaimDueLadderTranches_ValidatesBeforeSQL(t *testing.T) {
	s := &PostgresStore{}
	create := func(context.Context, pgx.Tx, LadderTrancheDB) (string, error) {
		t.Fatal("create must not run")
		return "", nil
	}
	valid := LadderClaimFilter{Layers: []ladder.LayerType{ladder.LayerComputeSP}, TrancheIDs: []string{"id"}}
	asOf := time.Now()

	_, err := s.ClaimDueLadderTranches(context.Background(), "cfg", asOf, 0, valid, create)
	require.ErrorContains(t, err, "limit must be positive")
	for _, layers := range [][]ladder.LayerType{nil, {}, {"compute_sp"}} {
		claimed, err := s.ClaimDueLadderTranches(context.Background(), "cfg", asOf, 1,
			LadderClaimFilter{Layers: layers, TrancheIDs: valid.TrancheIDs}, create)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "layer")
		assert.Nil(t, claimed)
	}
}

// The wire strings are stored in ladder_tranches and matched by the claim, so
// a rename in the provider module must fail here instead of orphaning rows.
func TestLadderPlanningTerms_WireStrings(t *testing.T) {
	assert.Equal(t, "1yr", string(LadderTerm))
	assert.Equal(t, "no-upfront", string(LadderPayment))
}

// The claim's run-start window and the overdue sweep share this value, so a
// change must be a deliberate edit of both semantics.
func TestLadderClaimPolicy_GracePin(t *testing.T) {
	assert.Equal(t, 24*time.Hour, LadderFireGrace)
}
