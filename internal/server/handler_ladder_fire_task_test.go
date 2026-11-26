package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fireTaskStore is the fire fake with switchable gates and an RI tranche, so
// the dispatched task can be pushed through each refusal and the RI pricing.
type fireTaskStore struct {
	global func(*config.GlobalConfig)
	cfg    func(*config.LadderConfigDB)
	ceil   *int64
	fireFakeStore
	listed int
	ri     bool
}

func (f *fireTaskStore) GetGlobalConfig(ctx context.Context) (*config.GlobalConfig, error) {
	g, _ := f.fireFakeStore.GetGlobalConfig(ctx)
	if f.global != nil {
		f.global(g)
	}
	return g, nil
}

func (f *fireTaskStore) GetLadderConfigs(ctx context.Context) ([]config.LadderConfigDB, error) {
	cfgs, _ := f.fireFakeStore.GetLadderConfigs(ctx)
	if f.cfg != nil {
		f.cfg(&cfgs[0])
	}
	return cfgs, nil
}

func (f *fireTaskStore) GetLadderSweepCeiling(ctx context.Context, id string) (*int64, error) {
	if f.ceil != nil {
		return f.ceil, nil
	}
	return f.fireFakeStore.GetLadderSweepCeiling(ctx, id)
}

func (f *fireTaskStore) ListDueLadderTranches(ctx context.Context, id string, at time.Time, n int, l []ladder.LayerType) ([]config.LadderDueRow, error) {
	f.listed++
	rows, err := f.fireFakeStore.ListDueLadderTranches(ctx, id, at, n, l)
	if f.ri {
		rows[0].Tranche.LayerType = ladder.LayerConvertibleRI
		rows[0].Tranche.AmountUSDHr = 1.0
	}
	return rows, err
}

func (f *fireTaskStore) ListStoredRecommendations(context.Context, config.RecommendationFilter) ([]config.RecommendationRecord, error) {
	if !f.ri {
		return nil, nil
	}
	d, _ := common.MarshalServiceDetails(&common.ComputeDetails{InstanceType: "m5.large", Platform: "Linux/UNIX", Tenancy: "default", Scope: "regional"})
	acct, monthly := "acct", 50.0
	return []config.RecommendationRecord{{
		Provider: "aws", Service: "ec2", Region: "us-east-1", ResourceType: "m5.large", Details: d,
		Count: 7, Term: 1, Payment: "no-upfront", MonthlyCost: &monthly, CloudAccountID: &acct,
	}}, nil
}

type fakeOfferingPricer struct{ calls int }

func (p *fakeOfferingPricer) GetOfferingDetailsForClass(context.Context, common.Recommendation, string) (*common.OfferingDetails, error) {
	p.calls++
	return &common.OfferingDetails{ResourceType: "m5.large", Currency: "USD", RecurringCost: 0.1, Term: "1yr", PaymentOption: "No Upfront"}, nil
}

type pricerPurchase struct {
	PurchaseManagerInterface
	pricer *fakeOfferingPricer
	err    error
}

func (p pricerPurchase) LadderRIPricer(context.Context, string, string) (purchase.RIOfferingPricer, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.pricer, nil
}

func runFireTask(t *testing.T, store *fireTaskStore, pm PurchaseManagerInterface) purchase.LadderFireResult {
	t.Helper()
	app := fireApp(store, fakeSender{}, "https://d.example.com")
	app.Purchase = pm
	res, err := app.runScheduledTask(context.Background(), TaskLadderFire, ScheduledTaskParams{})
	require.NoError(t, err)
	out, ok := res.(purchase.LadderFireResult)
	require.True(t, ok, "ladder_fire returns its sweep result, got %T", res)
	return out
}

func TestLadderFireTask_IsRoutableAndUnknownTypesStillError(t *testing.T) {
	t.Parallel()
	taskType, _, err := ParseScheduledEvent([]byte(`{"action":"ladder_fire"}`))
	require.NoError(t, err)
	assert.Equal(t, TaskLadderFire, taskType)
	assert.Contains(t, ScheduledTaskNames(), "ladder_fire")

	_, _, err = ParseScheduledEvent([]byte(`{"action":"ladder_fire_now"}`))
	require.Error(t, err)
	_, err = (&Application{}).runScheduledTask(context.Background(), ScheduledTaskType("nope"), ScheduledTaskParams{})
	require.Error(t, err)
}

func TestLadderFireTask_DispatchReachesTheSweep(t *testing.T) {
	t.Parallel()
	store := &fireTaskStore{}
	res := runFireTask(t, store, nil)
	assert.Equal(t, 1, res.Configs)
	assert.Equal(t, 1, store.claims, "all gates open: the sweep reaches the claim")
}

func TestLadderFireTask_SafetyGatesRefuseBeforeAnyListOrClaim(t *testing.T) {
	t.Parallel()
	zero := int64(0)
	cases := map[string]*fireTaskStore{
		"laddering_enabled off":         {global: func(g *config.GlobalConfig) { g.LadderingEnabled = false }},
		"ladder_execution_enabled off":  {global: func(g *config.GlobalConfig) { g.LadderExecutionEnabled = false }},
		"require_different_approver on": {global: func(g *config.GlobalConfig) { g.RequireDifferentApprover = true }},
		"config disabled":               {cfg: func(c *config.LadderConfigDB) { c.Enabled = false }},
		"config not auto_approve":       {cfg: func(c *config.LadderConfigDB) { c.Mode = "approval" }},
		"no per-run cap":                {cfg: func(c *config.LadderConfigDB) { c.MaxHourlyCommitPerRun = nil }},
		"non-AWS config":                {cfg: func(c *config.LadderConfigDB) { c.Provider = "azure" }},
		"no sweep ceiling":              {ceil: &zero},
	}
	for name, store := range cases {
		t.Run(name, func(t *testing.T) {
			res := runFireTask(t, store, nil)
			assert.Equal(t, 1, res.Skipped)
			assert.Zero(t, store.listed, "a refused config lists no tranches")
			assert.Zero(t, store.claims)
		})
	}
}

func TestLadderFireTask_CeilingHoldsBackAnOversizedTranche(t *testing.T) {
	t.Parallel()
	tiny := int64(1)
	store := &fireTaskStore{ceil: &tiny}
	res := runFireTask(t, store, nil)
	assert.Zero(t, res.Claimed)
	assert.Equal(t, 1, res.Trimmed, "a tranche above the sweep ceiling is held back, not claimed")
}

func TestLadderFireTask_RIPricerFactoryDecidesRIResolution(t *testing.T) {
	t.Parallel()
	pricer := &fakeOfferingPricer{}
	with := &fireTaskStore{ri: true}
	res := runFireTask(t, with, pricerPurchase{pricer: pricer})
	assert.Zero(t, res.Unresolved)
	assert.Equal(t, 1, pricer.calls, "the RI offering is priced through the manager's factory")
	assert.Equal(t, 1, with.claims)

	for name, pm := range map[string]PurchaseManagerInterface{
		"no pricer source":   nil,
		"factory build fail": pricerPurchase{err: errors.New("no creds")},
	} {
		t.Run(name, func(t *testing.T) {
			without := &fireTaskStore{ri: true}
			res := runFireTask(t, without, pm)
			assert.Equal(t, 1, res.Unresolved, "fail closed: the RI tranche stays unresolved")
			assert.Zero(t, without.claims)
		})
	}
}
