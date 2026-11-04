//go:build integration

package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	azureprovider "github.com/LeanerCloud/cloud-commitments-go/providers/azure"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Skip reasons as the go#663 Advisor pricing records them (see pricingOutageReasons).
var advisorOutageSkips = []string{
	`advisor: azure advisor: advisor compute reservation price for "Standard_D2s_v3" in "westeurope" (1y): get: retail prices fetch failed: EOF`,
	`advisor: price lookup unavailable: azure advisor: advisor compute reservation price for "Standard_D2s_v3" in "westeurope" (1y): get: retail prices fetch failed: EOF`,
}

// collectAzureAmbientCycle runs one ambient Azure collection against store
// and persists it the way a scheduled cycle does.
func collectAzureAmbientCycle(t *testing.T, store config.StoreInterface, recs []common.Recommendation, sweepErr error) []config.RecommendationRecord {
	t.Helper()
	t.Setenv("AZURE_SUBSCRIPTION_ID", "sub-a")
	ctx := context.Background()

	recClient := new(MockRecommendationsClient)
	recClient.On("GetAllRecommendations", mock.Anything).Return(recs, sweepErr).Once()
	prov := new(MockProvider)
	prov.On("GetRecommendationsClient", mock.Anything).Return(recClient, nil).Once()
	factory := new(MockProviderFactory)
	factory.On("CreateAndValidateProvider", mock.Anything, "azure", mock.Anything).Return(prov, nil).Once()
	s := &Scheduler{config: store, providerFactory: factory}

	got, ids, _, err := s.collectAzureRecommendations(ctx, &config.GlobalConfig{})
	require.NoError(t, err)
	s.persistCollection(ctx, got, expandSuccessfulCollects("azure", ids), nil, nil)
	return got
}

func storedAzureIDs(t *testing.T, store config.StoreInterface) []string {
	t.Helper()
	rows, err := store.ListStoredRecommendations(context.Background(), config.RecommendationFilter{})
	require.NoError(t, err)
	ids := []string{}
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func seedAdvisorRow(t *testing.T, store config.StoreInterface) {
	t.Helper()
	require.NoError(t, store.UpsertRecommendations(context.Background(), time.Now().Add(-time.Hour), []config.RecommendationRecord{
		{ID: "prev-advisor-row", Provider: "azure", Service: "compute", Region: "westeurope", ResourceType: "Standard_D2s_v3",
			Savings: 40, Count: 1, Term: 12, Payment: "upfront"},
	}, nil))
}

// An Advisor pricing outage skips every row; the previous cycle's rows must
// survive it, a later good cycle replaces them, and a genuine empty cycle
// still evicts.
func TestAzureAdvisorPricingOutage_HoldsEviction(t *testing.T) {
	ctx := context.Background()
	newStore := func(t *testing.T) config.StoreInterface {
		container := testhelpers.RequireMigratedDatabase(ctx, t, "../database/postgres/migrations")
		store := config.NewPostgresStore(container.DB)
		seedAdvisorRow(t, store)
		return store
	}

	t.Run("outage keeps prior rows", func(t *testing.T) {
		store := newStore(t)
		collectAzureAmbientCycle(t, store, nil, &azureprovider.PartialResultError{Skipped: advisorOutageSkips})
		require.Contains(t, storedAzureIDs(t, store), "prev-advisor-row")
	})

	t.Run("later good cycle replaces them", func(t *testing.T) {
		store := newStore(t)
		collectAzureAmbientCycle(t, store, nil, &azureprovider.PartialResultError{Skipped: advisorOutageSkips})
		got := collectAzureAmbientCycle(t, store, []common.Recommendation{partialSweepRec()}, nil)
		require.NotEmpty(t, got)
		ids := storedAzureIDs(t, store)
		require.NotContains(t, ids, "prev-advisor-row")
		require.Len(t, ids, len(got))
	})

	t.Run("genuine empty cycle still evicts", func(t *testing.T) {
		store := newStore(t)
		collectAzureAmbientCycle(t, store, nil, nil)
		require.NotContains(t, storedAzureIDs(t, store), "prev-advisor-row")
	})

	t.Run("unrelated skip reasons still evict", func(t *testing.T) {
		store := newStore(t)
		collectAzureAmbientCycle(t, store, nil, &azureprovider.PartialResultError{Skipped: []string{"advisor: row is in EUR, not USD"}})
		require.NotContains(t, storedAzureIDs(t, store), "prev-advisor-row")
	})
}
