package purchase

import (
	"context"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type runMove struct {
	id   string
	to   ladder.RunStatus
	from []ladder.RunStatus
}

// rollupStore serves run tranche counts and records the run transitions on
// top of pagingFake, whose own run methods are inert.
type rollupStore struct {
	*pagingFake
	runCounts  map[string]map[ladder.TrancheStatus]int
	rollupErr  error
	transitErr error
	runMoves   []runMove
}

func (f *pagingFake) CountLadderRunTrancheStatuses(context.Context, string) (map[ladder.TrancheStatus]int, error) {
	return nil, nil
}

func (f *pagingFake) TransitionLadderRunStatus(context.Context, string, []ladder.RunStatus, ladder.RunStatus) (*config.LadderRunDB, error) {
	return nil, nil
}

func (f *rollupStore) CountLadderRunTrancheStatuses(_ context.Context, runID string) (map[ladder.TrancheStatus]int, error) {
	return f.runCounts[runID], f.rollupErr
}

func (f *rollupStore) TransitionLadderRunStatus(_ context.Context, id string, from []ladder.RunStatus, to ladder.RunStatus) (*config.LadderRunDB, error) {
	f.runMoves = append(f.runMoves, runMove{id: id, from: from, to: to})
	return nil, f.transitErr
}

func (f *visFake) CountLadderRunTrancheStatuses(context.Context, string) (map[ladder.TrancheStatus]int, error) {
	return nil, nil
}

func (f *visFake) TransitionLadderRunStatus(context.Context, string, []ladder.RunStatus, ladder.RunStatus) (*config.LadderRunDB, error) {
	return nil, nil
}

func rollupFake(counts map[ladder.TrancheStatus]int) *rollupStore {
	run := "run-1"
	tr := pagingTranches("t1")
	tr[0].RunID = &run
	return &rollupStore{
		pagingFake: &pagingFake{tranches: tr, status: map[string]string{"t1": "failed"}},
		runCounts:  map[string]map[ladder.TrancheStatus]int{run: counts},
	}
}

func TestReconcile_RunRollup(t *testing.T) {
	const (
		sched = ladder.TrancheStatusScheduled
		fired = ladder.TrancheStatusFired
		done  = ladder.TrancheStatusCompleted
		fail  = ladder.TrancheStatusFailed
		canc  = ladder.TrancheStatusCanceled
	)
	cases := []struct {
		name   string
		counts map[ladder.TrancheStatus]int
		want   ladder.RunStatus // empty: no transition
	}{
		{"another tranche still scheduled", map[ladder.TrancheStatus]int{sched: 1, done: 1}, ""},
		{"another tranche still fired", map[ladder.TrancheStatus]int{fired: 1, done: 1}, ""},
		{"all completed", map[ladder.TrancheStatus]int{done: 2}, ladder.RunStatusCompleted},
		{"one failed", map[ladder.TrancheStatus]int{done: 1, fail: 1}, ladder.RunStatusFailed},
		{"one canceled", map[ladder.TrancheStatus]int{done: 1, canc: 1}, ladder.RunStatusFailed},
		{"legacy spelling", map[ladder.TrancheStatus]int{done: 1, "cancelled": 1}, ladder.RunStatusFailed}, //nolint:misspell // legacy stored status
		{"failed while another is open", map[ladder.TrancheStatus]int{fail: 1, sched: 1}, ""},
		{"no tranches", map[ladder.TrancheStatus]int{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := rollupFake(c.counts)
			_, err := ReconcileLadderTranches(context.Background(), f, stubVis{}, testNow)
			require.NoError(t, err)
			if c.want == "" {
				assert.Empty(t, f.runMoves)
				return
			}
			assert.Equal(t, []runMove{{id: "run-1", from: []ladder.RunStatus{ladder.RunStatusPlanned}, to: c.want}}, f.runMoves)
		})
	}
}

func TestReconcile_RunRollupSkippedWithoutRunOrMove(t *testing.T) {
	noRun := rollupFake(map[ladder.TrancheStatus]int{ladder.TrancheStatusCompleted: 1})
	noRun.tranches[0].RunID = nil
	lost := rollupFake(map[ladder.TrancheStatus]int{ladder.TrancheStatusCompleted: 1})
	lost.notMoved = map[string]bool{"t1": true}
	unresolved := rollupFake(map[ladder.TrancheStatus]int{ladder.TrancheStatusCompleted: 1})
	unresolved.status["t1"] = "running"
	for name, f := range map[string]*rollupStore{"no run id": noRun, "lost race": lost, "unresolved": unresolved} {
		_, err := ReconcileLadderTranches(context.Background(), f, stubVis{}, testNow)
		require.NoError(t, err, name)
		assert.Empty(t, f.runMoves, name)
	}
}

func TestReconcile_RunRollupErrorsAreCounted(t *testing.T) {
	counts := map[ladder.TrancheStatus]int{ladder.TrancheStatusCompleted: 1}
	count := rollupFake(counts)
	count.rollupErr = errors.New("count boom")
	transit := rollupFake(counts)
	transit.transitErr = errors.New("transition boom")
	for name, f := range map[string]*rollupStore{"count": count, "transition": transit} {
		res, err := ReconcileLadderTranches(context.Background(), f, stubVis{}, testNow)
		require.Error(t, err, name)
		assert.Equal(t, 1, res.Errored, name)
		assert.Equal(t, 1, res.Failed, name, "the tranche verdict stays recorded")
	}
}
