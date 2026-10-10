package archera

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every numeric field in goldenFixture carries a distinct value whose hundreds
// digit names its block (1xx current totals, 2xx hypothetical, 3xx current
// offer, 4xx candidate) and whose units digit names the field, so a swapped,
// dropped or blanked mapping changes the golden JSON.
func goldenFixture(t *testing.T) *insurance.Comparison {
	t.Helper()
	fin := func(b int) insurance.Financials {
		return insurance.Financials{
			CommitmentCostTotal: rat(t, fmt.Sprint(b+1)), CloudProviderCost: rat(t, fmt.Sprint(b+2)),
			Premium: rat(t, fmt.Sprint(b+3)), GrossSavings: rat(t, fmt.Sprint(b+4)),
			NetSavings: rat(t, fmt.Sprint(b+5)), CoveredOnDemandCost: rat(t, fmt.Sprint(b+6)),
		}
	}
	str := func(s string) *string { return &s }
	pay := func(p insurance.PaymentOption) *insurance.PaymentOption { return &p }
	mkOffer := func(b int, id string, current bool, provider common.ProviderType, typ, region, term string, p insurance.PaymentOption) insurance.OfferEntry {
		return insurance.OfferEntry{
			OfferID: id, IsCurrent: current, Provider: provider, CommitmentType: typ,
			Region: str(region), ContractTerm: str(term), PaymentOption: pay(p),
			DiscountRate: rat(t, fmt.Sprintf("0.%d", b+8)), BreakevenDays: rat(t, fmt.Sprint(b+9)),
			Monthly: fin(b), UpfrontCost: rat(t, fmt.Sprint(b+7)),
			Delta: insurance.OfferDelta{
				MonthlyNetSavings: rat(t, fmt.Sprint(b+11)), UpfrontCost: rat(t, fmt.Sprint(b+12)),
				DiscountRate: rat(t, fmt.Sprintf("0.%d", b+13)), BreakevenDays: rat(t, fmt.Sprint(b+14)),
			},
		}
	}
	cur := mkOffer(300, "offer-cur", true, common.ProviderAWS, "aws/AmazonEC2", "us-east-1", "term-cur", insurance.PaymentNoUpfront)
	cur.LeaseMenuItemID, cur.GuaranteedDisplayName = str("lease-cur"), str("name-cur")
	cand := mkOffer(400, "offer-cand", false, common.ProviderGCP, "gcp/other", "eu-west-1", "term-cand", insurance.PaymentAllUpfront)
	return &insurance.Comparison{
		OrgID: "org-must-not-appear", PlanID: "plan-g",
		Current: insurance.Totals{Monthly: fin(100), UpfrontCost: rat(t, "107")},
		Hypotheticals: []insurance.Hypothetical{{
			ContractTerm: str("term-h"), PaymentOption: insurance.PaymentPartialUpfront,
			Totals:                     insurance.Totals{Monthly: fin(200), UpfrontCost: rat(t, "207")},
			DeltaMonthlyNetSavings:     rat(t, "211"),
			DeltaMonthlyCommitmentCost: rat(t, "212"),
			DeltaUpfrontCost:           rat(t, "213"),
			LineItems: []insurance.HypotheticalLineItem{{
				LineItemID: "li-h", Reason: insurance.TermReasonExactMatch, ActualTerm: str("at-h"),
				ActualPaymentOption: pay(insurance.PaymentAllUpfront), ActualCommitmentType: str("act-type-h"),
			}},
		}},
		Rows:      []insurance.ComparisonRow{{LineItemID: "row-1", Current: cur, Candidates: []insurance.OfferEntry{cand}}},
		FetchedAt: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC),
	}
}

func TestBuildComparison_GoldenJSON(t *testing.T) {
	dto, err := BuildComparison(goldenFixture(t))
	require.NoError(t, err)
	got, err := json.MarshalIndent(dto, "", "  ")
	require.NoError(t, err)
	if os.Getenv("ARCHERA_WRITE_GOLDEN") != "" {
		require.NoError(t, os.WriteFile("testdata/comparison.golden.json", append(got, '\n'), 0o600))
	}
	want, err := os.ReadFile("testdata/comparison.golden.json")
	require.NoError(t, err)
	assert.JSONEq(t, string(want), string(got))
}
