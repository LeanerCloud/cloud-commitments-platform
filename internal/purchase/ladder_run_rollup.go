package purchase

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

// ladderRunRollupStatus maps a run's tranche status counts to the status the
// run should take: completed when every tranche completed, failed when every
// tranche is settled and at least one did not complete (failed, canceled, or an
// unknown status), and ok=false while any tranche is still scheduled or fired
// (the run stays planned and its scheduled tranches stay amendable).
func ladderRunRollupStatus(counts map[ladder.TrancheStatus]int) (to ladder.RunStatus, ok bool) {
	total := 0
	for _, n := range counts {
		total += n
	}
	if total == 0 || counts[ladder.TrancheStatusScheduled] > 0 || counts[ladder.TrancheStatusFired] > 0 {
		return "", false
	}
	if counts[ladder.TrancheStatusCompleted] == total {
		return ladder.RunStatusCompleted, true
	}
	return ladder.RunStatusFailed, true
}

// rollUpLadderRun moves a planned run to completed or failed once all its
// tranches are settled. The from-set is planned only, so a canceled, expired or
// already rolled-up run is never touched, and of two racing settles of the last
// tranches exactly one CAS wins. It runs after the tranche transaction
// commits, so the last committer always sees every tranche settled.
//
// Known gaps: a rollup that errors is not retried by the next sweep (only
// fired tranches are visited), and three other paths settle a tranche without
// rolling up its run: the overdue sweep in PlanAndSaveLadderRun, the
// notice-send-failure settle in ladder_fire_settle.go, and the operator resolve
// in store_postgres_ladder_resolve.go. None is a one-line call here (different
// store interfaces, and the config store cannot import purchase). The run
// status is informational: netting and amendment read tranche statuses.
func rollUpLadderRun(ctx context.Context, store LadderReconcileStore, runID string) error {
	counts, err := store.CountLadderRunTrancheStatuses(ctx, runID)
	if err != nil {
		return fmt.Errorf("roll up ladder_run %s: %w", runID, err)
	}
	to, ok := ladderRunRollupStatus(counts)
	if !ok {
		return nil
	}
	if _, err := store.TransitionLadderRunStatus(ctx, runID, []ladder.RunStatus{ladder.RunStatusPlanned}, to); err != nil {
		return fmt.Errorf("roll up ladder_run %s to %s: %w", runID, to, err)
	}
	return nil
}
