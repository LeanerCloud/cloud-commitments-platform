//go:build integration

package config

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
)

// racingStore returns a store on its own pool of at most maxConns connections
// to the fixture's database, so a test can prove the command needs no more
// connections than it holds.
func racingStore(ctx context.Context, t *testing.T, f *rampFixture, maxConns int32) *PostgresStore {
	t.Helper()
	conn, ok := f.store.db.(*database.Connection)
	require.True(t, ok, "fixture store must sit on a database.Connection")
	cfg := conn.Pool().Config().Copy()
	cfg.MaxConns = maxConns
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return &PostgresStore{db: pool}
}

// raceTwo runs both commands at once from a barrier and returns their errors.
func raceTwo(ctx context.Context, store *PostgresStore, a, b PlanScheduleCommand) [2]error {
	var errs [2]error
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, cmd := range []PlanScheduleCommand{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = store.ApplyPlanScheduleCommand(ctx, cmd)
		}()
	}
	close(start)
	wg.Wait()
	return errs
}

func requireOneWinner(t *testing.T, errs [2]error) (winner int) {
	t.Helper()
	winner = -1
	for i, err := range errs {
		if err == nil {
			require.Equal(t, -1, winner, "both commands succeeded")
			winner = i
			continue
		}
		require.ErrorIs(t, err, ErrPurchasePlanConflict, "the loser must see a clean conflict")
	}
	require.NotEqual(t, -1, winner, "no command succeeded: %v", errs)
	return winner
}

func TestApplyPlanScheduleCommandRaceOneWinnerOneConflict(t *testing.T) {
	for name, maxConns := range map[string]int32{"default pool": 10, "pool of two": 2} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f, old, plan := managedRootFixture(ctx, t, "pending")
			store := racingStore(ctx, t, f, maxConns)
			base := time.Now().UTC().Truncate(24 * time.Hour)
			dates := [2]time.Time{base.AddDate(0, 0, 20), base.AddDate(0, 0, 27)}
			names := [2]string{"client-a", "client-b"}
			var cmds [2]PlanScheduleCommand
			for i := range cmds {
				cmds[i] = rootCommand(plan, dates[i])
				cmds[i].Name = &names[i]
			}

			winner := requireOneWinner(t, raceTwo(ctx, store, cmds[0], cmds[1]))

			got, err := f.store.GetPurchasePlan(ctx, f.planID)
			require.NoError(t, err)
			require.Equal(t, names[winner], got.Name)
			require.True(t, dates[winner].Equal(*got.NextExecutionDate))
			require.Equal(t, 1, receiptCount(ctx, t, f.store), "the loser leaves no receipt")
			var open []PurchaseExecution
			for _, r := range stepRows(ctx, t, f) {
				if r.Status == StatusCanceled {
					require.Equal(t, old.ExecutionID, r.ExecutionID)
					continue
				}
				open = append(open, r)
			}
			require.Len(t, open, 1, "exactly one root stays open")
			require.True(t, dates[winner].Equal(open[0].ScheduledDate))
		})
	}
}
