package purchase

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	sptypes "github.com/aws/aws-sdk-go-v2/service/savingsplans/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// builtComputeSPFired returns a fired tranche whose execution holds the
// records the registry built, with the commitment ID the Manager would stamp.
func builtComputeSPFired(t *testing.T, f *visFake, selected bool) config.LadderTrancheDB {
	t.Helper()
	recs, err := NewLadderInstrumentRegistry().Build(LadderBuildInput{
		Tranche:        spTranche(0.123456, ladder.Term1Year),
		CloudAccountID: "acct-a", Region: "us-west-2",
	})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	recs[0].PurchaseID = "sp-1"
	recs[0].Selected = selected
	execID := "exec-built"
	f.execs = map[string]*config.PurchaseExecution{
		execID: {ExecutionID: execID, Status: config.StatusCompleted, Recommendations: recs},
	}
	return config.LadderTrancheDB{ID: "tr-1", ExecutionID: &execID, LayerType: ladder.LayerComputeSP, Status: ladder.TrancheStatusFired}
}

func TestLadderInstrument_ComputeSPThroughPrefetchVisibility(t *testing.T) {
	type call struct{ account, region string }
	run := func(t *testing.T, f *visFake, selected bool) (prefetchedVisibility, []call) {
		fired := builtComputeSPFired(t, f, selected)
		var calls []call
		factory := func(_ context.Context, account, region string) (SPDescriber, error) {
			calls = append(calls, call{account, region})
			return f, nil
		}
		return PrefetchLadderVisibility(context.Background(), f, factory, []config.LadderTrancheDB{fired}), calls
	}

	t.Run("active plan is visible in the built account and region", func(t *testing.T) {
		f := &visFake{plans: []sptypes.SavingsPlan{sp("sp-1", sptypes.SavingsPlanTypeCompute, sptypes.SavingsPlanStateActive, "")}}
		vis, calls := run(t, f, true)
		assert.Equal(t, []call{{"acct-a", "us-west-2"}}, calls)
		ok, err := vis.CommitmentVisible(context.Background(), config.LadderTrancheDB{}, "exec-built")
		require.NoError(t, err)
		assert.True(t, ok)
	})
	t.Run("queued plan is not visible", func(t *testing.T) {
		f := &visFake{plans: []sptypes.SavingsPlan{sp("sp-1", sptypes.SavingsPlanTypeCompute, sptypes.SavingsPlanStateQueued, "")}}
		vis, _ := run(t, f, true)
		ok, err := vis.CommitmentVisible(context.Background(), config.LadderTrancheDB{}, "exec-built")
		require.NoError(t, err)
		assert.False(t, ok)
	})
	t.Run("unselected record yields an error", func(t *testing.T) {
		f := &visFake{plans: []sptypes.SavingsPlan{sp("sp-1", sptypes.SavingsPlanTypeCompute, sptypes.SavingsPlanStateActive, "")}}
		vis, calls := run(t, f, false)
		assert.Empty(t, calls)
		ok, err := vis.CommitmentVisible(context.Background(), config.LadderTrancheDB{}, "exec-built")
		require.Error(t, err)
		assert.False(t, ok)
	})
}

func TestLadderInstrument_ComputeSPRecordReachesServiceClient(t *testing.T) {
	for _, tc := range []struct {
		name   string
		term   ladder.Term
		amount float64
		want   string
		hourly float64
	}{
		{"1yr", ladder.Term1Year, 0.123456, "1yr", 0.12345},
		{"3yr", ladder.Term3Year, 1000000, "3yr", 1000000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recs, err := NewLadderInstrumentRegistry().Build(LadderBuildInput{
				Tranche:        spTranche(tc.amount, tc.term),
				CloudAccountID: "acct-a", Region: "us-west-2",
			})
			require.NoError(t, err)
			require.Len(t, recs, 1)

			mockFactory := new(MockProviderFactory)
			mockProviderInst := new(MockProvider)
			mockServiceClient := new(MockServiceClient)
			assertMocksOnCleanup(t, mockFactory, mockProviderInst, mockServiceClient)
			mockFactory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(mockProviderInst, nil)
			mockProviderInst.On("GetServiceClient", mock.Anything, common.ServiceSavingsPlansCompute, "us-west-2").Return(mockServiceClient, nil)
			var got common.Recommendation
			mockServiceClient.On("PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything).
				Run(func(args mock.Arguments) { got = args.Get(1).(common.Recommendation) }).
				Return(common.PurchaseResult{Success: true, CommitmentID: "sp-1"}, nil)
			m := &Manager{providerFactory: mockFactory}
			opts := common.PurchaseOptions{Source: common.PurchaseSourceWeb, ExecutionID: "exec-1", RequestedAt: time.Now()}

			_, err = m.executeSinglePurchase(context.Background(), recs[0], nil, opts, nil)

			require.NoError(t, err)
			assert.Equal(t, common.ProviderAWS, got.Provider)
			assert.Equal(t, common.ServiceSavingsPlansCompute, got.Service)
			assert.Equal(t, "us-west-2", got.Region)
			assert.Equal(t, tc.want, got.Term)
			assert.Equal(t, "no-upfront", got.PaymentOption)
			assert.Equal(t, 1, got.Count)
			details, ok := got.Details.(*common.SavingsPlanDetails)
			require.True(t, ok, "details are %T", got.Details)
			assert.Equal(t, common.SavingsPlanDetails{PlanType: "Compute", HourlyCommitment: tc.hourly}, *details)
		})
	}
}
