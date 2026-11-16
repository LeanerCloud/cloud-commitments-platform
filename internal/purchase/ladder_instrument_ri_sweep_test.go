package purchase

import (
	"context"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRIPricer struct {
	details *common.OfferingDetails
	err     error
	recs    []common.Recommendation
	classes []string
}

func (p *fakeRIPricer) GetOfferingDetailsForClass(_ context.Context, rec common.Recommendation, class string) (*common.OfferingDetails, error) {
	p.recs, p.classes = append(p.recs, rec), append(p.classes, class)
	return p.details, p.err
}

func riOffering() *common.OfferingDetails {
	return &common.OfferingDetails{ResourceType: "m5.large", Currency: "USD", RecurringCost: 0.1, Term: "1yr", PaymentOption: "No Upfront"}
}

func riDueRow(id string, amount float64) config.LadderDueRow {
	r := dueRow(id, amount, true)
	r.Tranche.LayerType = ladder.LayerConvertibleRI
	return r
}

func riSweep(p *fakeRIPricer, factoryErr error) (*fakeFireStore, LadderFireResult, error) {
	f := newFireStore(fireCfg("c1"))
	f.rows["c1"] = []config.LadderDueRow{riDueRow("R1", 1.0), dueRow("T1", 1.0, true)}
	rec := storedRI("m5.large", 50)
	rec.Region = "us-east-1"
	f.stored = []config.RecommendationRecord{rec}
	d := fireDepsFor(f)
	d.RIPricer = func(context.Context, string, string) (RIOfferingPricer, error) { return p, factoryErr }
	res, err := RunLadderFire(context.Background(), d)
	return f, res, err
}

func TestSweep_RIPricesTheOfferingBeforeTheClaimAndBuysTheFlooredCount(t *testing.T) {
	p := &fakeRIPricer{details: riOffering()}
	f, res, err := riSweep(p, nil)
	require.NoError(t, err)
	assert.Equal(t, LadderFireResult{Configs: 1, Due: 2, Claimed: 2, Announced: 2}, res)
	assert.Equal(t, []string{"convertible"}, p.classes)
	require.Len(t, p.recs, 1)
	assert.Equal(t, common.Term1Year, p.recs[0].Term)
	assert.Equal(t, common.PaymentNoUpfront, p.recs[0].PaymentOption)
	assert.Equal(t, "us-east-1", p.recs[0].Region)
	assert.Equal(t, "m5.large", p.recs[0].ResourceType)
	assert.Equal(t, config.RecommendationFilter{Provider: "aws", Service: "ec2", Region: "us-east-1", AccountIDs: []string{"acct-a"}}, f.storedFilters[0])
	require.Len(t, f.saved, 2)
	require.Len(t, f.saved[0].Recommendations, 1)
	assert.Equal(t, 10, f.saved[0].Recommendations[0].Count)
}

func TestSweep_RIUnpricedTrancheIsUnresolvedAndTheGoodTrancheStillClaims(t *testing.T) {
	up, wrongShape, eur, threeYear, partial := riOffering(), riOffering(), riOffering(), riOffering(), riOffering()
	up.UpfrontCost, wrongShape.ResourceType, eur.Currency = 5, "c5.large", "EUR"
	threeYear.Term, partial.PaymentOption = "3yr", "Partial Upfront"
	for name, tc := range map[string]struct {
		p          *fakeRIPricer
		factoryErr error
	}{
		"lookup error":  {p: &fakeRIPricer{err: errors.New("no offering")}},
		"nil details":   {p: &fakeRIPricer{}},
		"upfront fee":   {p: &fakeRIPricer{details: up}},
		"wrong shape":   {p: &fakeRIPricer{details: wrongShape}},
		"not USD":       {p: &fakeRIPricer{details: eur}},
		"3y offering":   {p: &fakeRIPricer{details: threeYear}},
		"partial offer": {p: &fakeRIPricer{details: partial}},
		"factory error": {p: &fakeRIPricer{details: riOffering()}, factoryErr: errors.New("no creds")},
	} {
		f, res, err := riSweep(tc.p, tc.factoryErr)
		require.NoError(t, err, name)
		assert.Equal(t, LadderFireResult{Configs: 1, Due: 2, Unresolved: 1, Claimed: 1, Announced: 1}, res, name)
		assert.Equal(t, []string{"T1"}, f.filter.TrancheIDs, name)
	}
}

func TestSweep_RIWithoutAPricerOrWithAStoreErrorClaimsNoRITranche(t *testing.T) {
	f := newFireStore(fireCfg("c1"))
	f.rows["c1"] = []config.LadderDueRow{riDueRow("R1", 1.0)}
	f.stored = []config.RecommendationRecord{storedRI("m5.large", 50)}
	res, err := runFire(f)
	require.NoError(t, err)
	assert.Equal(t, LadderFireResult{Configs: 1, Due: 1, Unresolved: 1}, res)
	assert.Zero(t, f.claimCalls)

	f.storedErr = errors.New("db down")
	res, err = runFire(f)
	require.Error(t, err)
	assert.Equal(t, 1, res.Errored)
	assert.Zero(t, f.claimCalls)
}

func TestSweep_RIOtherAccountsRecsAreNotUsed(t *testing.T) {
	p := &fakeRIPricer{details: riOffering()}
	f := newFireStore(fireCfg("c1"))
	f.rows["c1"] = []config.LadderDueRow{riDueRow("R1", 1.0)}
	rec := storedRI("m5.large", 50)
	other := "acct-b"
	rec.Region, rec.CloudAccountID = "us-east-1", &other
	f.stored = []config.RecommendationRecord{rec}
	d := fireDepsFor(f)
	d.RIPricer = func(context.Context, string, string) (RIOfferingPricer, error) { return p, nil }
	res, err := RunLadderFire(context.Background(), d)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Unresolved)
	assert.Empty(t, p.recs, "no offering is priced for another account's rec")
	assert.Zero(t, f.claimCalls)
}

func TestSweep_ComputeOnlyConfigsDoNotPriceRIs(t *testing.T) {
	p := &fakeRIPricer{details: riOffering()}
	f := newFireStore(fireCfg("c1"))
	d := fireDepsFor(f)
	d.RIPricer = func(context.Context, string, string) (RIOfferingPricer, error) { return p, nil }
	_, err := RunLadderFire(context.Background(), d)
	require.NoError(t, err)
	assert.Empty(t, p.recs)
	assert.Empty(t, f.storedFilters)
}

func TestLadderInstrument_RIThroughPrefetchVisibility(t *testing.T) {
	in := riInput(1.0, map[string]float64{"m5.large": 0.1}, storedRI("m5.large", 50))
	recs, err := NewLadderInstrumentRegistry().Build(in)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	recs[0].PurchaseID = "ri-1"
	v := &visFake{}
	v.addTranche("t1", "acct-a", "ri-1", ladder.LayerConvertibleRI)
	v.execs["exec-t1"].Recommendations = recs
	r := &riFake{byAcct: map[string][]ec2types.ReservedInstances{"acct-a": {activeRI("ri-1")}}}
	vis := PrefetchLadderVisibility(context.Background(), v, v.factory, r.factory, v.fired)
	ok, err := vis.CommitmentVisible(context.Background(), config.LadderTrancheDB{}, "exec-t1")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, []string{"us-west-2"}, r.factoryRgns)
	assert.Equal(t, []string{"ri-1"}, r.gotIDs)
}
