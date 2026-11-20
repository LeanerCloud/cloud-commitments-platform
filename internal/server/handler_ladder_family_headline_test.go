package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgcommon "github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
)

// storedFamilyRecs are the stored EC2 SP recommendations naming BOTH families.
// A family-bound tranche must ignore them for amounts and family choice.
func storedFamilyRecs(t *testing.T, families ...string) []config.RecommendationRecord {
	t.Helper()
	acct := "acct-a"
	recs := make([]config.RecommendationRecord, 0, len(families))
	for _, f := range families {
		details, err := pkgcommon.MarshalServiceDetails(&pkgcommon.SavingsPlanDetails{
			PlanType: "EC2Instance", InstanceFamily: f, Region: "us-east-1", HourlyCommitment: 1,
		})
		require.NoError(t, err)
		recs = append(recs, config.RecommendationRecord{
			Provider: "aws", Service: string(pkgcommon.ServiceSavingsPlansEC2Instance),
			Term: 1, Payment: "no-upfront", Details: details, CloudAccountID: &acct,
		})
	}
	return recs
}

func inFlightFromRows(rows []config.LadderTrancheDB) []config.LadderInFlightRow {
	out := make([]config.LadderInFlightRow, len(rows))
	for i, r := range rows {
		out[i] = config.LadderInFlightRow{Layer: r.LayerType, Family: r.Family, CommitmentUSDPerHour: r.AmountUSDHr, Rate: 1}
	}
	return out
}

// HEADLINE (#1964): a two-family baseline is planned, persisted, bought and
// netted per family. The purchase side buys exactly one record per family with
// that tranche's own amount (never the tranche amount in every stored-rec
// family), and the next run nets each family on its own headroom.
func TestLadderTwoFamilies_PlanPersistBuyAndNetPerFamily(t *testing.T) {
	byFamily := map[string]float64{"m5": 6, "c5": 2}
	first := runFamilyLadder(t, byFamily, nil)
	require.Len(t, first.savedTranches, 2)

	var trancheTotal float64
	reg := purchase.NewLadderInstrumentRegistry()
	bought := map[string]float64{}
	for i := range first.savedTranches {
		row := first.savedTranches[i]
		trancheTotal += row.AmountUSDHr
		recs, err := reg.Build(purchase.LadderBuildInput{
			CloudAccountID: "acct-a", Region: "us-east-1", Tranche: row,
			StoredRecs: storedFamilyRecs(t, "m5", "c5"),
		})
		require.NoError(t, err)
		require.Len(t, recs, 1, "family %s: one record, not one per stored-rec family", row.Family)
		d, err := pkgcommon.DecodeServiceDetailsFor(recs[0].Service, recs[0].Details)
		require.NoError(t, err)
		sp := d.(*pkgcommon.SavingsPlanDetails)
		assert.Equal(t, row.Family, sp.InstanceFamily)
		assert.InDelta(t, row.AmountUSDHr, sp.HourlyCommitment, 1e-5)
		assert.LessOrEqual(t, sp.HourlyCommitment, row.AmountUSDHr)
		bought[sp.InstanceFamily] += sp.HourlyCommitment
	}
	require.Len(t, bought, 2)
	assert.InDelta(t, 4.8, bought["m5"], 1e-5)
	assert.InDelta(t, 1.6, bought["c5"], 1e-5)
	assert.InDelta(t, trancheTotal, bought["m5"]+bought["c5"], 1e-5, "total bought == sum of tranche amounts")

	// Second run: both families are covered in flight, so nothing more is planned.
	second := runFamilyLadder(t, byFamily, inFlightFromRows(first.savedTranches))
	assert.Empty(t, second.savedTranches, "both families are netted by their own in-flight rows")

	// Only m5 in flight: m5 is netted on its own headroom (6 - 4.8 = 1.2) and c5
	// keeps its full 2.0, so the 1.6 gap splits 1.2:2.0. A cross-family netting
	// would zero c5 or hand m5 the whole gap.
	var m5Only []config.LadderTrancheDB
	for _, r := range first.savedTranches {
		if r.Family == "m5" {
			m5Only = append(m5Only, r)
		}
	}
	third := runFamilyLadder(t, byFamily, inFlightFromRows(m5Only))
	got := trancheAmountsByFamily(third.savedTranches)
	assert.InDelta(t, 0.6, got["m5"], 1e-6)
	assert.InDelta(t, 1.0, got["c5"], 1e-6)
	assert.InDelta(t, 1.6, got["m5"]+got["c5"], 1e-6)

	// Pin anchor: the layer is the family-bound EC2 Instance SP.
	for _, r := range third.savedTranches {
		assert.Equal(t, pkgladder.LayerEC2InstanceSP, r.LayerType)
	}
}
