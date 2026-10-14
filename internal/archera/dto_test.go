package archera

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rat(t *testing.T, s string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	require.True(t, ok)
	return r
}

func TestDecimal(t *testing.T) {
	for in, want := range map[string]string{
		"0": "0", "110": "110", "-5.5": "-5.5", "104.5": "104.5", "0.3": "0.3",
		"1e100":     "1" + strings.Repeat("0", 100),
		"1e-100":    "0." + strings.Repeat("0", 99) + "1",
		"-1.5e-100": "-0." + strings.Repeat("0", 99) + "15",
		"1/8":       "0.125", "7/25": "0.28",
	} {
		got, err := Decimal(rat(t, in))
		require.NoError(t, err, in)
		require.NotNil(t, got)
		assert.Equal(t, want, *got, in)
	}
}

func TestDecimal_NilIsUnknownNotZero(t *testing.T) {
	got, err := Decimal(nil)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestDecimal_NonTerminatingFailsLoud(t *testing.T) {
	for _, in := range []string{"1/3", "2/7", "1/6"} {
		got, err := Decimal(rat(t, in))
		assert.ErrorIs(t, err, ErrUnrepresentable, in)
		assert.Nil(t, got)
	}
}

func TestCleanString(t *testing.T) {
	assert.Equal(t, "abc", CleanString("a\x00b\u200bc\u2028\n\t"))
	assert.Equal(t, "plain text", CleanString("plain text"))
	long := strings.Repeat("a", 300)
	assert.Len(t, CleanString(long), 256)
	// 3-byte runes: 256/3 = 85 runes = 255 bytes, never a split rune.
	got := CleanString(strings.Repeat("€", 100))
	assert.Len(t, got, 255)
	assert.True(t, strings.HasPrefix(got, "€"))
}

func offer(t *testing.T, provider common.ProviderType, typ string) insurance.OfferEntry {
	name := "Guaranteed\x00 EC2"
	return insurance.OfferEntry{
		OfferID: "o\u200b1", CommitmentType: typ, Provider: provider, IsCurrent: true,
		GuaranteedDisplayName: &name, LeaseMenuItemID: &name,
		DiscountRate: rat(t, "0.3"),
		Monthly:      insurance.Financials{CommitmentCostTotal: rat(t, "110"), Premium: rat(t, "10")},
		UpfrontCost:  rat(t, "1200"),
	}
}

func sample(t *testing.T) *insurance.Comparison {
	return &insurance.Comparison{
		OrgID: "ORG-SECRETISH-ID", PlanID: "plan-1",
		Current: insurance.Totals{Monthly: insurance.Financials{CommitmentCostTotal: rat(t, "110")}, UpfrontCost: rat(t, "1200")},
		Hypotheticals: []insurance.Hypothetical{{
			PaymentOption: insurance.PaymentNoUpfront, DeltaMonthlyNetSavings: rat(t, "7.25"),
			LineItems: []insurance.HypotheticalLineItem{{LineItemID: "li-1", Reason: insurance.TermReasonNoAlternative}},
		}},
		Rows:      []insurance.ComparisonRow{{LineItemID: "li-1", Current: offer(t, common.ProviderAWS, "aws/AmazonEC2"), Candidates: []insurance.OfferEntry{offer(t, common.ProviderGCP, "gcp/other")}}},
		FetchedAt: time.Date(2026, 10, 9, 12, 0, 0, 0, time.FixedZone("x", 3600)),
	}
}

func TestBuildComparison_ShapeAndHonesty(t *testing.T) {
	dto, err := BuildComparison(sample(t))
	require.NoError(t, err)
	raw, err := json.Marshal(dto)
	require.NoError(t, err)
	out := string(raw)

	assert.NotContains(t, out, "ORG-SECRETISH-ID", "org ID must not be returned")
	assert.Equal(t, "2026-10-09T11:00:00Z", dto.FetchedAt)
	assert.True(t, dto.PremiumIncluded)
	assert.Nil(t, dto.Currency)
	assert.Contains(t, out, `"currency":null`)
	assert.Equal(t, currencyNote, dto.CurrencyNote)
	assert.Contains(t, out, `"monthly_730h_gross_savings":null`, "unknown money stays null, never 0")
	assert.Contains(t, out, `"monthly_730h_commitment_cost_total":"110"`)
	assert.Contains(t, out, `"upfront_one_time_cost":"1200"`)
	assert.Equal(t, common.ArcheraNonGatingDisclosure, dto.NonGatingDisclosure)
	assert.Equal(t, common.ArcheraSponsorshipDisclosure, dto.SponsorshipDisclosure)
	assert.Equal(t, "no_alternative", dto.Hypotheticals[0].LineItems[0].Reason)
	assert.Equal(t, "7.25", *dto.Hypotheticals[0].DeltaVsCurrent.MonthlyNetSavings)

	cur := dto.Rows[0].Current
	assert.Equal(t, "supported", cur.ProductSupport.Status)
	assert.NotEmpty(t, cur.ProductSupport.Source)
	assert.NotEmpty(t, cur.ProductSupport.Evidence)
	assert.Equal(t, "unknown", cur.ProductSupport.UnderwritingAllowed)
	assert.Equal(t, "unknown", cur.ProductSupport.CustomerEligibility)
	assert.True(t, cur.LeaseAttached)
	assert.Equal(t, "o1", cur.OfferID, "control and format characters are stripped")
	require.NotNil(t, cur.ArcheraOfferName)
	assert.Equal(t, "Guaranteed EC2", *cur.ArcheraOfferName)
	assert.Contains(t, cur.ArcheraOfferNameNote, "not a guarantee")

	cand := dto.Rows[0].Candidates[0]
	assert.Equal(t, "unknown", cand.ProductSupport.Status, "a product outside the documented rows is unknown")
	assert.Empty(t, cand.ProductSupport.Source)
	assert.Empty(t, cand.ProductSupport.Evidence)
}

func TestBuildComparison_NoOfferNameWhenNil(t *testing.T) {
	c := sample(t)
	c.Rows[0].Current.GuaranteedDisplayName = nil
	c.Rows[0].Current.LeaseMenuItemID = nil
	dto, err := BuildComparison(c)
	require.NoError(t, err)
	raw, _ := json.Marshal(dto.Rows[0].Current)
	assert.NotContains(t, string(raw), "archera_offer_name")
	assert.False(t, dto.Rows[0].Current.LeaseAttached)
}

func TestBuildComparison_UnrepresentableMoneyFailsLoud(t *testing.T) {
	c := sample(t)
	c.Current.UpfrontCost = rat(t, "1/3")
	dto, err := BuildComparison(c)
	assert.ErrorIs(t, err, ErrUnrepresentable)
	assert.Nil(t, dto)
}

// The pkg contract has no division and the DTO must never convert to float:
// a value that needs more than float64 precision survives exactly.
func TestBuildComparison_ExactBeyondFloat64(t *testing.T) {
	c := sample(t)
	c.Current.UpfrontCost = rat(t, "12345678901234567890.123456789")
	dto, err := BuildComparison(c)
	require.NoError(t, err)
	assert.Equal(t, "12345678901234567890.123456789", *dto.Current.UpfrontCost)
}

func TestBuildComparison_CapsEveryVendorString(t *testing.T) {
	c := sample(t)
	long := strings.Repeat("x", 400)
	e := &c.Rows[0].Current
	e.OfferID, e.CommitmentType, e.GuaranteedDisplayName, e.Region, e.LeaseMenuItemID = long, long, &long, &long, &long
	c.Rows[0].LineItemID = long
	c.Hypotheticals[0].LineItems[0].LineItemID = long
	c.Hypotheticals[0].LineItems[0].ActualCommitmentType = &long
	dto, err := BuildComparison(c)
	require.NoError(t, err)
	cur := dto.Rows[0].Current
	for name, v := range map[string]string{
		"offer": cur.OfferID, "type": cur.CommitmentType, "name": *cur.ArcheraOfferName, "region": *cur.Region,
		"lease": *cur.LeaseMenuItemID, "row": dto.Rows[0].LineItemID,
		"li": dto.Hypotheticals[0].LineItems[0].LineItemID, "actual": *dto.Hypotheticals[0].LineItems[0].ActualCommitmentType,
	} {
		assert.LessOrEqual(t, len(v), 256, name)
	}
}

func TestBuildComparison_CleansPlanID(t *testing.T) {
	c := sample(t)
	c.PlanID = "plan\u200b-\x00" + strings.Repeat("p", 400)
	dto, err := BuildComparison(c)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(dto.PlanID, "plan-p"))
	assert.Len(t, dto.PlanID, 256)
}
