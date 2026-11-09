//go:build integration

package purchase

// Real PostgreSQL for the execute-time ladder gates added in platform#70 S8c1:
// a ladder execution is fired through fireOneDue against the real store and
// the counting fake provider (labeled fixture; nothing reaches a cloud API).

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ladderFireRow seeds a ladder config, run and tranche of the given layer, plus
// a scheduled execution keyed by the tranche id, with the ladder switches on.
func ladderFireRow(ctx context.Context, t *testing.T, f *rampStepFixture, layer ladder.LayerType, stamped bool) *config.PurchaseExecution {
	t.Helper()
	f.repairAccounts(ctx, t)
	global, err := f.store.GetGlobalConfig(ctx)
	require.NoError(t, err)
	global.LadderingEnabled, global.LadderExecutionEnabled = true, true
	global.RequireDifferentApprover, global.OfferingClass = false, ""
	require.NoError(t, f.store.SaveGlobalConfig(ctx, global))

	cfg, err := f.store.UpsertLadderConfig(ctx, &config.LadderConfigDB{
		CloudAccountID: f.accounts[0].ID, Provider: "aws", Enabled: true, Mode: "auto_approve", Cadence: "daily",
		TargetCoverage: 80, BufferFraction: 0.1, BaselinePercentile: 5, LookbackDays: 30,
		BufferUtilizationThreshold: 50, MaxActionsPerRun: 5, RampSchedule: []byte(`{"steps":[{"after_days":0,"fraction":1.0}]}`)})
	require.NoError(t, err)
	run, err := f.store.SaveLadderRun(ctx, &config.LadderRunDB{ConfigID: &cfg.ID, StartedAt: time.Now().UTC(), Status: ladder.RunStatusExecuting})
	require.NoError(t, err)
	basis, rate := config.LadderAmountCommitment, "1"
	tr := config.LadderTrancheDB{ID: uuid.New().String(), ConfigID: &cfg.ID, RunID: &run.ID, LayerType: layer,
		Term: ladder.Term1Year, PaymentOption: ladder.PaymentNoUpfront, Status: ladder.TrancheStatusScheduled,
		AmountUSDHr: 5, ScheduledDate: time.Now().Add(-time.Hour), AmountBasis: &basis, CommitmentRate: &rate}
	require.NoError(t, f.store.SaveLadderTranches(ctx, []config.LadderTrancheDB{tr}))

	row := &config.PurchaseExecution{
		PlanID: f.plan.ID, ExecutionID: uuid.New().String(), Status: "scheduled", StepNumber: 3, IdempotencyKey: tr.ID,
		ScheduledDate: time.Now().Add(-time.Minute), CloudAccountID: &f.accounts[0].ID,
		Recommendations: []config.RecommendationRecord{withAccount(rampStepRecommendation()[0], f.accounts[0].ID)},
	}
	if stamped {
		now := time.Now().Add(-time.Hour)
		row.NotificationSent = &now
	}
	require.NoError(t, f.store.SavePurchaseExecution(ctx, row))
	return row
}

func TestLadderFire_RefusesUnannouncedFourEyesAndNonConvertible(t *testing.T) {
	ctx := context.Background()
	f := newRampStepFixture(ctx, t)
	f.email.On("SendPurchaseFailedNotification", mock.Anything, mock.Anything).Return(nil).Maybe()
	cases := []struct {
		name    string
		layer   ladder.LayerType
		stamped bool
		mutate  func(*config.GlobalConfig)
		want    string
	}{
		{"notification_sent NULL", ladder.LayerComputeSP, false, func(*config.GlobalConfig) {}, ladderNoticeNotSentNote},
		{"4-eyes on", ladder.LayerComputeSP, true, func(g *config.GlobalConfig) { g.RequireDifferentApprover = true }, ladderNeedsSecondApproverNote},
		{"RI layer, class switched to standard", ladder.LayerConvertibleRI, true, func(g *config.GlobalConfig) { g.OfferingClass = "standard" }, ladderRINeedsConvertibleNote},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := ladderFireRow(ctx, t, f, tc.layer, tc.stamped)
			global, err := f.store.GetGlobalConfig(ctx)
			require.NoError(t, err)
			tc.mutate(global)
			require.NoError(t, f.store.SaveGlobalConfig(ctx, global))

			before := f.purchaseCount()
			assert.Equal(t, fireErrored, f.manager.fireOneDue(ctx, rowStatusCopy(row)))
			assert.Equal(t, before, f.purchaseCount(), "a refused ladder execution must reach no provider")
			got := rowStatus(ctx, t, f, row.ExecutionID)
			assert.Equal(t, "failed", got.Status)
			assert.True(t, got.NoSubmission, "the refusal must stamp no_submission")
			assert.Contains(t, got.Error, tc.want)
		})
	}

	t.Run("stamped, 4-eyes off, convertible RI still buys", func(t *testing.T) {
		row := ladderFireRow(ctx, t, f, ladder.LayerConvertibleRI, true)
		global, err := f.store.GetGlobalConfig(ctx)
		require.NoError(t, err)
		global.RequireDifferentApprover, global.OfferingClass = false, "convertible"
		require.NoError(t, f.store.SaveGlobalConfig(ctx, global))
		before := f.purchaseCount()
		f.manager.fireOneDue(ctx, rowStatusCopy(row))
		assert.Equal(t, 1, f.purchaseCount()-before)
	})
}
