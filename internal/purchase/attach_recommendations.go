package purchase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
)

// AttachResolvedRecommendations persists the recommendations a plan step
// resolved onto its still-empty pending/notified execution, together with the
// suppressions that stop Opportunities (and the next step's resolution) from
// offering the same recs again. Both writes happen in one transaction: a crash
// leaves the row empty with no suppressions, or populated with them.
//
// Returns false (and writes nothing) when another resolver attached first or
// the row left pending/notified; the caller re-reads the row instead of
// overwriting it. On success exec is updated in place.
func (m *Manager) AttachResolvedRecommendations(ctx context.Context, exec *config.PurchaseExecution, recs []config.RecommendationRecord) (bool, error) {
	if len(recs) == 0 {
		return false, errors.New("refusing to attach an empty recommendation set")
	}
	var upfront, savings float64
	for i := range recs {
		upfront += recs[i].UpfrontCost
		savings += recs[i].Savings
	}
	globalCfg, err := m.config.GetGlobalConfig(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to read grace periods for suppressions: %w", err)
	}
	sups := config.BuildSuppressions(recs, exec.ExecutionID, globalCfg, time.Now())

	attached := false
	err = m.config.WithTx(ctx, func(tx pgx.Tx) error {
		ok, setErr := m.config.SetExecutionRecommendationsIfEmptyTx(ctx, tx, exec.ExecutionID, recs, upfront, savings)
		if setErr != nil || !ok {
			return setErr
		}
		for i := range sups {
			if supErr := m.config.CreateSuppressionTx(ctx, tx, &sups[i]); supErr != nil {
				return fmt.Errorf("failed to record suppressions for execution %s: %w", exec.ExecutionID, supErr)
			}
		}
		attached = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if attached {
		exec.Recommendations, exec.TotalUpfrontCost, exec.EstimatedSavings = recs, upfront, savings
	}
	return attached, nil
}
