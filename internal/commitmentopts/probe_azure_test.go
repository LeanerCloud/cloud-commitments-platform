package commitmentopts

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/billingbenefits/armbillingbenefits"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAzureSub = "11111111-2222-3333-4444-555555555555"

// fakeAzureSPValidate is a test double for AzureSPValidateAPI. The fn
// field receives the full request body so tests can assert that the prober
// passes the expected term / billing plan fields.
type fakeAzureSPValidate struct {
	fn func(body armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error)
}

func (f *fakeAzureSPValidate) ValidatePurchase(
	_ context.Context,
	body armbillingbenefits.SavingsPlanPurchaseValidateRequest,
	_ *armbillingbenefits.RPClientValidatePurchaseOptions,
) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
	return f.fn(body)
}

// validTrue returns an RPClientValidatePurchaseResponse with a single
// benefit whose Valid flag is true.
func validTrue() armbillingbenefits.RPClientValidatePurchaseResponse {
	v := true
	return armbillingbenefits.RPClientValidatePurchaseResponse{
		SavingsPlanValidateResponse: armbillingbenefits.SavingsPlanValidateResponse{
			Benefits: []*armbillingbenefits.SavingsPlanValidResponseProperty{
				{Valid: &v},
			},
		},
	}
}

// validFalse returns a response indicating the offering is not available.
func validFalse(reason string) armbillingbenefits.RPClientValidatePurchaseResponse {
	v := false
	return armbillingbenefits.RPClientValidatePurchaseResponse{
		SavingsPlanValidateResponse: armbillingbenefits.SavingsPlanValidateResponse{
			Benefits: []*armbillingbenefits.SavingsPlanValidResponseProperty{
				{Valid: &v, Reason: &reason},
			},
		},
	}
}

// newFakeProber wires a fakeAzureSPValidate into an AzureSPProber so tests
// never touch the real Azure RP.
func newFakeProber(fn func(armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error)) *AzureSPProber {
	fake := &fakeAzureSPValidate{fn: fn}
	return &AzureSPProber{
		NewClient: func(_ azcore.TokenCredential) (AzureSPValidateAPI, error) {
			return fake, nil
		},
	}
}

// ---------------------------------------------------------------------------
// AzureSPProber.Service
// ---------------------------------------------------------------------------

func TestAzureSPProber_Service(t *testing.T) {
	p := &AzureSPProber{}
	assert.Equal(t, "savingsplans", p.Service())
}

// ---------------------------------------------------------------------------
// AzureSPProber.ProbeAzure — happy paths
// ---------------------------------------------------------------------------

func TestAzureSPProber_AllValid(t *testing.T) {
	// All 6 candidate combos accepted — all 6 Combos returned.
	p := newFakeProber(func(_ armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
		return validTrue(), nil
	})

	got, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
	require.NoError(t, err)
	assert.Len(t, got, 6)

	// All must carry provider=azure, service=savingsplans.
	for _, c := range got {
		assert.Equal(t, "azure", c.Provider)
		assert.Equal(t, "savingsplans", c.Service)
	}
}

func TestAzureSPProber_SomeValid(t *testing.T) {
	// Only the 1yr and 3yr combos are accepted; P5Y combos are rejected.
	// Simulates the live Azure state where P5Y SPs are not yet available.
	p := newFakeProber(func(body armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
		if len(body.Benefits) == 0 || body.Benefits[0] == nil {
			return validFalse("no benefits"), nil
		}
		props := body.Benefits[0].Properties
		if props == nil || props.Term == nil {
			return validFalse("no term"), nil
		}
		if *props.Term == armbillingbenefits.TermP5Y {
			return validFalse("P5Y not available"), nil
		}
		return validTrue(), nil
	})

	got, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
	require.NoError(t, err)
	// 2 terms x 2 payment plans = 4 combos (P5Y x2 dropped)
	assert.Len(t, got, 4)

	terms := make(map[int]int)
	for _, c := range got {
		terms[c.TermYears]++
	}
	assert.Equal(t, 2, terms[1], "expected 2 combos for 1yr")
	assert.Equal(t, 2, terms[3], "expected 2 combos for 3yr")
	assert.Equal(t, 0, terms[5], "expected no combos for 5yr")
}

func TestAzureSPProber_NoneValid(t *testing.T) {
	// Every combo answered Valid=false: nothing is sold, which is an error so
	// an empty snapshot is never persisted.
	p := newFakeProber(func(_ armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
		return validFalse("not available"), nil
	})

	got, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
	require.Error(t, err)
	assert.Empty(t, got)
}

func TestAzureSPProber_RequestContainsTerm(t *testing.T) {
	// The probe request body must pass the correct term to ValidatePurchase
	// so the API can evaluate it against the actual catalog.
	seenTerms := make(map[armbillingbenefits.Term]int)
	p := newFakeProber(func(body armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
		require.Len(t, body.Benefits, 1)
		require.NotNil(t, body.Benefits[0].Properties)
		require.NotNil(t, body.Benefits[0].Properties.Term)
		seenTerms[*body.Benefits[0].Properties.Term]++
		return validTrue(), nil
	})

	_, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
	require.NoError(t, err)

	assert.Equal(t, 2, seenTerms[armbillingbenefits.TermP1Y], "P1Y must appear twice (upfront + monthly)")
	assert.Equal(t, 2, seenTerms[armbillingbenefits.TermP3Y], "P3Y must appear twice")
	assert.Equal(t, 2, seenTerms[armbillingbenefits.TermP5Y], "P5Y must appear twice")
}

func TestAzureSPProber_RequestBillingPlan(t *testing.T) {
	// Upfront combos must omit BillingPlan (nil); monthly combos must
	// set BillingPlan = P1M. This drives the Azure API to evaluate the
	// correct payment schedule.
	type planKey struct {
		term armbillingbenefits.Term
		plan string // "nil" or "P1M"
	}
	seen := make(map[planKey]int)

	p := newFakeProber(func(body armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
		props := body.Benefits[0].Properties
		plan := "nil"
		if props.BillingPlan != nil {
			plan = string(*props.BillingPlan)
		}
		seen[planKey{*props.Term, plan}]++
		return validTrue(), nil
	})

	_, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
	require.NoError(t, err)

	for _, term := range []armbillingbenefits.Term{armbillingbenefits.TermP1Y, armbillingbenefits.TermP3Y, armbillingbenefits.TermP5Y} {
		assert.Equal(t, 1, seen[planKey{term, "nil"}], "upfront combo must have nil BillingPlan for %s", term)
		assert.Equal(t, 1, seen[planKey{term, "P1M"}], "monthly combo must have P1M BillingPlan for %s", term)
	}
}

// ---------------------------------------------------------------------------
// Billing currency (issue #814)
// ---------------------------------------------------------------------------

func TestAzureSPProber_RequestUsesCallerCurrency(t *testing.T) {
	// The probe must validate in the billing account's currency, not a
	// hardcoded USD. Lowercase input is normalized to upper case, matching
	// the cloud-commitments-go Azure provider.
	cases := []struct {
		in   string
		want string
	}{
		{"EUR", "EUR"},
		{"gbp", "GBP"},
		{"USD", "USD"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			var seen []string
			p := newFakeProber(func(body armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
				require.Len(t, body.Benefits, 1)
				require.NotNil(t, body.Benefits[0].Properties)
				require.NotNil(t, body.Benefits[0].Properties.Commitment)
				require.NotNil(t, body.Benefits[0].Properties.Commitment.CurrencyCode)
				seen = append(seen, *body.Benefits[0].Properties.Commitment.CurrencyCode)
				return validTrue(), nil
			})

			_, err := p.ProbeAzure(context.Background(), nil, testAzureSub, tc.in)
			require.NoError(t, err)

			require.Len(t, seen, len(azureCandidateCombos), "every candidate combo must be probed")
			for _, got := range seen {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestAzureSPProber_UnknownCurrency_FailsClosed(t *testing.T) {
	// With no resolvable billing currency the probe must refuse to run
	// rather than assume USD: no client is built and no request is sent.
	for _, in := range []string{"", "US", "EURO", "U$D", "12A", " usd"} {
		t.Run(in, func(t *testing.T) {
			clientBuilt := false
			validateCalls := 0
			p := &AzureSPProber{
				NewClient: func(_ azcore.TokenCredential) (AzureSPValidateAPI, error) {
					clientBuilt = true
					return &fakeAzureSPValidate{fn: func(_ armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
						validateCalls++
						return validTrue(), nil
					}}, nil
				},
			}

			got, err := p.ProbeAzure(context.Background(), nil, testAzureSub, in)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "currency")
			assert.Nil(t, got)
			assert.False(t, clientBuilt, "client must not be built without a valid currency")
			assert.Zero(t, validateCalls, "ValidatePurchase must not be called without a valid currency")
		})
	}
}

func TestAzureSPProber_RequestScopeAndSKU(t *testing.T) {
	// The request must carry the caller's subscription (no placeholder) and
	// the ARM SKU name Azure accepts, not the bare "Compute".
	calls := 0
	p := newFakeProber(func(body armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
		calls++
		b := body.Benefits[0]
		require.NotNil(t, b.SKU)
		assert.Equal(t, "Compute_Savings_Plan", *b.SKU.Name)
		assert.Equal(t, "/subscriptions/"+testAzureSub, *b.Properties.BillingScopeID)
		return validTrue(), nil
	})
	_, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
	require.NoError(t, err)
	assert.Equal(t, len(azureCandidateCombos), calls)
}

func TestAzureSPProber_EmptySubscription_FailsClosed(t *testing.T) {
	clientBuilt := false
	p := &AzureSPProber{NewClient: func(_ azcore.TokenCredential) (AzureSPValidateAPI, error) {
		clientBuilt = true
		return nil, nil
	}}
	got, err := p.ProbeAzure(context.Background(), nil, "", "USD")
	require.Error(t, err)
	assert.Nil(t, got)
	assert.False(t, clientBuilt)
}

// ---------------------------------------------------------------------------
// DefaultAzureProbers
// ---------------------------------------------------------------------------

func TestDefaultAzureProbers(t *testing.T) {
	probers := DefaultAzureProbers()
	require.Len(t, probers, 1)
	assert.Equal(t, "savingsplans", probers[0].Service())
}
