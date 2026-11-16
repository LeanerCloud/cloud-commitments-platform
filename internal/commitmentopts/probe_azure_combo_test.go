package commitmentopts

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/billingbenefits/armbillingbenefits"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAzureSPProber_EmptyBenefitsSlice(t *testing.T) {
	// An empty benefits slice confirms nothing, so no combo is recorded as
	// sold: the probe requires positive confirmation (audit finding A05-012).
	p := newFakeProber(func(_ armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
		return armbillingbenefits.RPClientValidatePurchaseResponse{}, nil
	})

	got, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
	require.Error(t, err, "nothing confirmed means no snapshot")
	assert.Empty(t, got)
}

func TestAzureSPProber_NilValidFlag(t *testing.T) {
	// A benefit with a nil Valid pointer confirms nothing, so the combo is
	// not recorded as sold (audit finding A05-012).
	p := newFakeProber(func(_ armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
		return armbillingbenefits.RPClientValidatePurchaseResponse{
			SavingsPlanValidateResponse: armbillingbenefits.SavingsPlanValidateResponse{
				Benefits: []*armbillingbenefits.SavingsPlanValidResponseProperty{
					{Valid: nil},
				},
			},
		}, nil
	})

	got, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
	require.Error(t, err, "nothing confirmed means no snapshot")
	assert.Empty(t, got)
}

func TestAzureSPProber_ConfirmationRule(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name     string
		benefits []*armbillingbenefits.SavingsPlanValidResponseProperty
		want     int
	}{
		{"nil flag beside a confirmed benefit", []*armbillingbenefits.SavingsPlanValidResponseProperty{nil, {Valid: nil}, {Valid: &yes}}, 6},
		{"any invalid benefit vetoes", []*armbillingbenefits.SavingsPlanValidResponseProperty{{Valid: &yes}, {Valid: &no}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newFakeProber(func(armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
				return armbillingbenefits.RPClientValidatePurchaseResponse{
					SavingsPlanValidateResponse: armbillingbenefits.SavingsPlanValidateResponse{Benefits: tc.benefits},
				}, nil
			})
			got, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
			if tc.want == 0 {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Len(t, got, tc.want)
		})
	}
}

// ---------------------------------------------------------------------------
// AzureSPProber.ProbeAzure — error handling
// ---------------------------------------------------------------------------

func TestAzureSPProber_APIError_Propagates(t *testing.T) {
	// Any non-validation error from ValidatePurchase must bubble up and
	// abort the probe — we cannot distinguish "not offered" from
	// "auth failed" without the response.
	boom := errors.New("network failure")
	p := newFakeProber(func(_ armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
		return armbillingbenefits.RPClientValidatePurchaseResponse{}, boom
	})

	_, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
}

func TestAzureSPProber_APIError_FirstComboFails(t *testing.T) {
	// If the first combo call fails, the prober must return an error
	// immediately and not silently continue with the remaining combos.
	calls := 0
	boom := errors.New("auth denied")
	p := newFakeProber(func(_ armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
		calls++
		return armbillingbenefits.RPClientValidatePurchaseResponse{}, boom
	})

	_, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
	require.Error(t, err)
	assert.Equal(t, 1, calls, "prober must stop after first error")
}

func TestAzureSPProber_ClientBuildError(t *testing.T) {
	// If NewClient itself fails (e.g. invalid credential format), the
	// error must propagate before any ValidatePurchase call is made.
	buildErr := errors.New("bad credential")
	p := &AzureSPProber{
		NewClient: func(_ azcore.TokenCredential) (AzureSPValidateAPI, error) {
			return nil, buildErr
		},
	}

	_, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
	require.Error(t, err)
	assert.ErrorIs(t, err, buildErr)
}

// ---------------------------------------------------------------------------
// Combo content and deduplication
// ---------------------------------------------------------------------------

func TestAzureSPProber_ComboFields(t *testing.T) {
	// Verify that the returned Combos carry the correct provider, service,
	// termYears, and payment strings.
	p := newFakeProber(func(_ armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
		return validTrue(), nil
	})

	got, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
	require.NoError(t, err)

	sort.Slice(got, func(i, j int) bool {
		if got[i].TermYears != got[j].TermYears {
			return got[i].TermYears < got[j].TermYears
		}
		return got[i].Payment < got[j].Payment
	})

	want := []Combo{
		{Provider: "azure", Service: "savingsplans", TermYears: 1, Payment: "monthly"},
		{Provider: "azure", Service: "savingsplans", TermYears: 1, Payment: "upfront"},
		{Provider: "azure", Service: "savingsplans", TermYears: 3, Payment: "monthly"},
		{Provider: "azure", Service: "savingsplans", TermYears: 3, Payment: "upfront"},
		{Provider: "azure", Service: "savingsplans", TermYears: 5, Payment: "monthly"},
		{Provider: "azure", Service: "savingsplans", TermYears: 5, Payment: "upfront"},
	}
	assert.Equal(t, want, got)
}

// respError builds the error the Azure SDK returns for an HTTP status.
func respError(status int) error {
	return &azcore.ResponseError{StatusCode: status}
}

func TestAzureSPProber_4xxDropsOnlyThatCombo(t *testing.T) {
	// Azure answering 422 for the 5-year terms means it does not sell them:
	// the other combos are kept and the probe succeeds.
	p := newFakeProber(func(body armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
		if *body.Benefits[0].Properties.Term == armbillingbenefits.TermP5Y {
			return armbillingbenefits.RPClientValidatePurchaseResponse{}, respError(http.StatusUnprocessableEntity)
		}
		return validTrue(), nil
	})
	got, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
	require.NoError(t, err)
	assert.Len(t, got, 4)
	for _, c := range got {
		assert.NotEqual(t, 5, c.TermYears)
	}
}

func TestAzureSPProber_NonRejectionErrorsAbort(t *testing.T) {
	// Identity, timeout, throttling and server errors say nothing about the
	// combo, so they abort the probe and nothing is returned.
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			p := newFakeProber(func(armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
				return armbillingbenefits.RPClientValidatePurchaseResponse{}, respError(status)
			})
			got, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
			require.Error(t, err)
			assert.Nil(t, got)
		})
	}
}

func TestAzureSPProber_ContextErrorAborts(t *testing.T) {
	p := newFakeProber(func(armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
		return armbillingbenefits.RPClientValidatePurchaseResponse{}, context.DeadlineExceeded
	})
	_, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestIsComboRejection_WrappedError(t *testing.T) {
	assert.True(t, isComboRejection(errors.Join(errors.New("x"), respError(http.StatusBadRequest))))
	assert.False(t, isComboRejection(errors.New("plain")))
}

func TestAzureSPProber_AllRejectedIsAnError(t *testing.T) {
	// A wrong account answers 400 or 404 for every combo. That must not become
	// an empty, permanently cached snapshot.
	for _, status := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusConflict} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			p := newFakeProber(func(armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
				return armbillingbenefits.RPClientValidatePurchaseResponse{}, respError(status)
			})
			got, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
			require.Error(t, err)
			assert.Nil(t, got)
		})
	}
}

func rejectTerm(term armbillingbenefits.Term, status int) func(armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
	return func(body armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
		if *body.Benefits[0].Properties.Term == term {
			return armbillingbenefits.RPClientValidatePurchaseResponse{}, respError(status)
		}
		return validTrue(), nil
	}
}

func TestAzureSPProber_4xxOnSoldTermAborts(t *testing.T) {
	// Azure really sells 1-year and 3-year plans, so a 4xx on one of them is a
	// transient or wrong-scope answer: dropping it would persist a snapshot
	// missing a sold combo for good. The probe aborts instead.
	for _, term := range []armbillingbenefits.Term{armbillingbenefits.TermP1Y, armbillingbenefits.TermP3Y} {
		for _, status := range []int{http.StatusBadRequest, http.StatusConflict, http.StatusUnprocessableEntity} {
			t.Run(string(term)+"/"+http.StatusText(status), func(t *testing.T) {
				p := newFakeProber(rejectTerm(term, status))
				got, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
				require.Error(t, err)
				assert.Nil(t, got)
			})
		}
	}
}

func TestAzureSPProber_4xxOnFiveYearDropsOnlyFiveYear(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusConflict, http.StatusUnprocessableEntity} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			p := newFakeProber(rejectTerm(armbillingbenefits.TermP5Y, status))
			got, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
			require.NoError(t, err)
			require.Len(t, got, 4)
			for _, c := range got {
				assert.NotEqual(t, 5, c.TermYears)
			}
		})
	}
}

func TestAzureSPProber_MixedFiveYearRejectedSoldTermRejected(t *testing.T) {
	// A droppable 5-year rejection does not excuse a 3-year one.
	p := newFakeProber(func(body armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
		switch *body.Benefits[0].Properties.Term {
		case armbillingbenefits.TermP5Y:
			return armbillingbenefits.RPClientValidatePurchaseResponse{}, respError(http.StatusBadRequest)
		case armbillingbenefits.TermP3Y:
			return armbillingbenefits.RPClientValidatePurchaseResponse{}, respError(http.StatusUnprocessableEntity)
		}
		return validTrue(), nil
	})
	got, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
	require.Error(t, err)
	assert.Nil(t, got)
}

func TestAzureSPProber_404OnOneComboAborts(t *testing.T) {
	p := newFakeProber(func(body armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
		if *body.Benefits[0].Properties.Term == armbillingbenefits.TermP5Y {
			return armbillingbenefits.RPClientValidatePurchaseResponse{}, respError(http.StatusNotFound)
		}
		return validTrue(), nil
	})
	got, err := p.ProbeAzure(context.Background(), nil, testAzureSub, "USD")
	require.Error(t, err)
	assert.Nil(t, got)
}
