package purchase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func heldFreshness(heldIDs ...string) *config.RecommendationsFreshness {
	return &config.RecommendationsFreshness{LastCollectionPartial: &config.RecommendationsCollectionPartial{
		Providers: []config.PartialProviderSweep{{
			Provider: "azure", IncompleteAccounts: len(heldIDs), HeldAccountIDs: heldIDs,
			Reasons: map[string]int{config.PartialReasonPricingUnavailable: 1},
		}},
	}}
}

// expiredFreshness is the marker after a hold expired while the outage goes on:
// expired is evicted and no longer held, held accounts are still held.
func expiredFreshness(expired string, held ...string) *config.RecommendationsFreshness {
	return &config.RecommendationsFreshness{LastCollectionPartial: &config.RecommendationsCollectionPartial{
		Providers: []config.PartialProviderSweep{{
			Provider: "azure", IncompleteAccounts: len(held), HeldAccountIDs: held, ExpiredHoldAccountIDs: []string{expired},
			Reasons: map[string]int{config.PartialReasonPricingUnavailable: 1, config.PartialReasonPricingHoldExpired: 1},
		}},
	}}
}

// expiredOnlyFreshness is the marker the cycle after a hold expired leaves: the
// ids are expired and nothing is held, so pricing_unavailable is absent.
func expiredOnlyFreshness(expired ...string) *config.RecommendationsFreshness {
	return &config.RecommendationsFreshness{LastCollectionPartial: &config.RecommendationsCollectionPartial{
		Providers: []config.PartialProviderSweep{{
			Provider: "azure", ExpiredHoldAccountIDs: expired,
			Reasons: map[string]int{config.PartialReasonPricingHoldExpired: len(expired)},
		}},
	}}
}

// An Advisor row held through a pricing outage still matches the approved
// execution, because both carry the same old price. The collection state is
// what tells the purchase path the price may be days old.
func TestAzurePricingHold(t *testing.T) {
	acct := "11111111-1111-1111-1111-111111111111"
	other := "22222222-2222-2222-2222-222222222222"
	cases := []struct {
		name      string
		execAcct  *string
		fresh     *config.RecommendationsFreshness
		freshErr  error
		wantStale bool
	}{
		{"held account refused", &acct, heldFreshness(acct), nil, true},
		{"hold cleared by a good cycle", &acct, &config.RecommendationsFreshness{}, nil, false},
		{"unheld account unaffected", &other, heldFreshness(acct), nil, false},
		{"unreadable hold state refuses", &acct, nil, errors.New("database unavailable"), true},
		{"missing collection state refuses", &acct, nil, nil, true},
		// The reason gate: only a pricing_unavailable sweep holds rows. A sweep that
		// is partial for another reason (here throttling) must not refuse a purchase.
		{"partial sweep without the pricing reason does not refuse", &acct, &config.RecommendationsFreshness{LastCollectionPartial: &config.RecommendationsCollectionPartial{
			Providers: []config.PartialProviderSweep{{Provider: "azure", HeldAccountIDs: []string{acct}, Reasons: map[string]int{config.PartialReasonThrottled: 1}}},
		}}, nil, false},
		// A stored row of an expired-hold account is one eviction failed to delete
		// (it was locked by another tx) or one that predates the hold: its price is
		// older than the hold limit, so the gate refuses it with or without an outage.
		{"expired-hold account with a surviving row refused", &acct, expiredOnlyFreshness(acct), nil, true},
		{"expired-hold account refused while the outage continues", &acct, expiredFreshness(acct, other), nil, true},
		{"expired other account does not refuse an unrelated account", &acct, expiredOnlyFreshness(other), nil, false},
		{"ambient row refused when the ambient key expired", nil, expiredOnlyFreshness(uuid.Nil.String()), nil, true},
		{"ambient row not refused when only a named account expired", nil, expiredOnlyFreshness(acct), nil, false},
		{"held account refused next to an expired one", &acct, expiredFreshness(other, acct), nil, true},
		{"unresolved ambient rec refused during an outage", nil, heldFreshness(), nil, true},
		// Fail-closed by design: a rec without an account cannot be matched to the
		// hold list, so any Azure hold refuses it.
		{"accountless rec refused while other accounts are held", nil, heldFreshness(acct), nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := config.RecommendationRecord{ID: "azure-held", Provider: "azure", Service: "compute", Region: "westeurope",
				ResourceType: "Standard_D2s_v3", Count: 2, Term: 3, UpfrontCost: 200, Selected: true, CloudAccountID: tc.execAcct}
			exec := armedExecution("azure", "compute", 0, "approved")
			exec.Recommendations = []config.RecommendationRecord{base}
			mgr, store, recorder := armedHarness(t, common.ServiceCompute)
			store.On("ListStoredRecommendations", mock.Anything, config.RecommendationFilter{Provider: "azure", RequireAzurePricingMigration: true}).
				Return([]config.RecommendationRecord{base}, nil).Maybe()
			store.On("GetRecommendationsFreshness", mock.Anything).Return(tc.fresh, tc.freshErr).Maybe()

			err := mgr.executeAndFinalize(context.Background(), exec)

			if tc.wantStale {
				require.ErrorIs(t, err, ErrStaleAzurePricing)
				assert.Zero(t, recorder.count())
				assert.Equal(t, "failed", exec.Status)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 1, recorder.count())
		})
	}
}

// orderStore models a collection commit landing right after the gate's first
// read: the first read gets the pre-commit state, anything after it the new one.
type orderStore struct {
	*MockConfigStore
	committed bool
	calls     []string
	oldRow    config.RecommendationRecord
	newRow    config.RecommendationRecord
}

func (s *orderStore) ListStoredRecommendations(context.Context, config.RecommendationFilter) ([]config.RecommendationRecord, error) {
	s.calls = append(s.calls, "rows")
	row := s.oldRow
	if s.committed {
		row = s.newRow
	}
	s.committed = true
	return []config.RecommendationRecord{row}, nil
}

func (s *orderStore) GetRecommendationsFreshness(context.Context) (*config.RecommendationsFreshness, error) {
	s.calls = append(s.calls, "freshness")
	fresh := expiredOnlyFreshness(*s.oldRow.CloudAccountID)
	if s.committed {
		fresh = &config.RecommendationsFreshness{}
	}
	s.committed = true
	return fresh, nil
}

// A re-pricing collection commits marker and rows together. Reading the rows
// before the marker could pair the old row with the cleared marker.
func TestAzurePricingReadsFreshnessBeforeRows(t *testing.T) {
	acct := "11111111-1111-1111-1111-111111111111"
	oldRow := config.RecommendationRecord{ID: "azure-old", Provider: "azure", Service: "compute", Region: "westeurope",
		ResourceType: "Standard_D2s_v3", Count: 2, Term: 3, UpfrontCost: 200, CloudAccountID: &acct}
	newRow := oldRow
	newRow.UpfrontCost = 300
	store := &orderStore{MockConfigStore: new(MockConfigStore), oldRow: oldRow, newRow: newRow}
	exec := armedExecution("azure", "compute", 0, "approved")
	exec.Recommendations = []config.RecommendationRecord{oldRow}

	err := (&Manager{config: store}).staleAzurePricingRefusal(context.Background(), exec)

	require.ErrorIs(t, err, ErrStaleAzurePricing)
	assert.Equal(t, []string{"freshness", "rows"}, store.calls)
}

func TestAzureHoldRefusal(t *testing.T) {
	acct := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	other := "22222222-2222-2222-2222-222222222222"
	row := func(id string, account *string) config.RecommendationRecord {
		return config.RecommendationRecord{ID: id, Provider: "azure", CloudAccountID: account}
	}
	upper := strings.ToUpper(acct)
	require.NotEqual(t, acct, upper, "the upper-case cases must differ from the lower-case marker id")
	nonAzure := expiredOnlyFreshness(acct)
	nonAzure.LastCollectionPartial.Providers[0].Provider = "aws"
	cases := []struct {
		name      string
		fresh     *config.RecommendationsFreshness
		exec      *string
		stored    *string
		wantStale bool
	}{
		{"fresh collect without a partial", &config.RecommendationsFreshness{}, &acct, &acct, false},
		{"held just inside the limit", heldFreshness(acct), &acct, &acct, true},
		{"expired at the limit with a surviving row", expiredOnlyFreshness(acct), &acct, &acct, true},
		{"re-priced account passes", &config.RecommendationsFreshness{LastCollectionPartial: &config.RecommendationsCollectionPartial{}}, &acct, &acct, false},
		{"non-azure sweep with an expired id", nonAzure, &acct, &acct, false},
		{"stored row account decides, exec rec has none", expiredOnlyFreshness(acct), nil, &acct, true},
		{"stored row account decides, exec rec on another", expiredOnlyFreshness(acct), &other, &acct, true},
		{"upper-case payload account id still matches the expired key", expiredOnlyFreshness(acct), &acct, &upper, true},
		{"upper-case payload account id still matches the held key", heldFreshness(acct), &upper, &upper, true},
		{"exec rec on the expired account, stored row elsewhere", expiredOnlyFreshness(acct), &acct, &other, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			azure := []config.RecommendationRecord{row("azure-1", tc.exec)}
			err := azureHoldRefusal(tc.fresh, azure, []config.RecommendationRecord{row("azure-1", tc.stored)})
			if !tc.wantStale {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrStaleAzurePricing)
		})
	}

	err := azureHoldRefusal(expiredOnlyFreshness(acct), nil, []config.RecommendationRecord{row("azure-1", &acct)})
	require.ErrorContains(t, err, config.PricingHoldMaxAge.String())
}

// An unreadable marker hides any hold, so an Azure execution cannot be confirmed
// current; other providers never read it.
func TestAzurePricingUnreadableMarker(t *testing.T) {
	acct := "11111111-1111-1111-1111-111111111111"
	cases := []struct {
		name      string
		provider  string
		fresh     *config.RecommendationsFreshness
		wantStale bool
	}{
		{"unreadable marker refuses an Azure execution", "azure", &config.RecommendationsFreshness{LastCollectionPartialUnreadable: true}, true},
		{"readable empty marker passes", "azure", &config.RecommendationsFreshness{}, false},
		{"valid marker holding another account passes", "azure", heldFreshness("22222222-2222-2222-2222-222222222222"), false},
		{"nil freshness still fails closed", "azure", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := config.RecommendationRecord{ID: "azure-1", Provider: "azure", Service: "compute", Region: "westeurope",
				ResourceType: "Standard_D2s_v3", Count: 2, Term: 3, UpfrontCost: 200, Selected: true, CloudAccountID: &acct}
			exec := armedExecution("azure", "compute", 0, "approved")
			exec.Recommendations = []config.RecommendationRecord{base}
			mgr, store, recorder := armedHarness(t, common.ServiceCompute)
			store.On("ListStoredRecommendations", mock.Anything, config.RecommendationFilter{Provider: "azure", RequireAzurePricingMigration: true}).
				Return([]config.RecommendationRecord{base}, nil).Maybe()
			store.On("GetRecommendationsFreshness", mock.Anything).Return(tc.fresh, nil).Maybe()

			err := mgr.executeAndFinalize(context.Background(), exec)

			if tc.wantStale {
				require.ErrorIs(t, err, ErrStaleAzurePricing)
				assert.Zero(t, recorder.count())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 1, recorder.count())
		})
	}
}

func TestAzurePricingUnreadableMarkerIgnoredForOtherProviders(t *testing.T) {
	exec := armedExecution("aws", "ec2", 0, "approved")
	mgr, store, _ := armedHarness(t, common.ServiceEC2)
	store.On("GetRecommendationsFreshness", mock.Anything).Return(&config.RecommendationsFreshness{LastCollectionPartialUnreadable: true}, nil).Maybe()

	require.NoError(t, mgr.staleAzurePricingRefusal(context.Background(), exec))
}
