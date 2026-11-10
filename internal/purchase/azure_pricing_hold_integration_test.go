//go:build integration

package purchase

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func azureHoldRec(id, acct, resourceType string, upfront float64) config.RecommendationRecord {
	return config.RecommendationRecord{
		ID: id, CloudAccountID: &acct, Provider: "azure", Service: "vm", Region: "eastus",
		ResourceType: resourceType, Savings: 10, Count: 1, Term: 12, Payment: "upfront", UpfrontCost: upfront,
	}
}

// TestAzurePricingHoldSurvivingRow drives the real marker and rows: an expired
// hold whose eviction skipped a locked row leaves a row the gate must refuse.
func TestAzurePricingHoldSurvivingRow(t *testing.T) {
	ctx := context.Background()
	db := testhelpers.RequireMigratedDatabase(ctx, t, rampMigrationsPath()).DB
	store := config.NewPostgresStore(db)
	acct := uuid.NewString()
	require.NoError(t, store.CreateCloudAccount(ctx, &config.CloudAccount{
		ID: acct, Name: "hold-gate", Provider: "azure", ExternalID: "sub-hold-gate", Enabled: true}))
	base := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, store.UpsertRecommendations(ctx, base.Add(-48*time.Hour),
		[]config.RecommendationRecord{azureHoldRec("r1", acct, "D2", 100), azureHoldRec("r2", acct, "D4", 100)}, nil))
	seed, err := json.Marshal(&config.RecommendationsCollectionPartial{Providers: []config.PartialProviderSweep{{
		Provider: "azure", IncompleteAccounts: 1, HeldAccountIDs: []string{acct},
		HeldSince: map[string]time.Time{acct: base.Add(-config.PricingHoldMaxAge)},
		Reasons:   map[string]int{config.PartialReasonPricingUnavailable: 1},
	}}})
	require.NoError(t, err)
	_, err = db.Exec(ctx, `UPDATE recommendations_state SET last_collection_partial = $1::jsonb WHERE id = 1`, seed)
	require.NoError(t, err)
	hold := &config.RecommendationsCollectionPartial{Providers: []config.PartialProviderSweep{{
		Provider: "azure", IncompleteAccounts: 1, HeldAccountIDs: []string{acct},
		Reasons: map[string]int{config.PartialReasonPricingUnavailable: 1},
	}}}

	// Another tx locks one stale row, so the eviction skips it.
	lock, err := db.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Rollback(context.Background()) })
	var lockedID string
	require.NoError(t, lock.QueryRow(ctx, `SELECT id::text FROM recommendations WHERE provider = 'azure' AND account_key::text = $1 ORDER BY id LIMIT 1 FOR UPDATE`, acct).Scan(&lockedID))
	require.NoError(t, store.UpsertRecommendationsWithPartial(ctx, base, nil, nil, hold))

	survivors, err := store.ListStoredRecommendations(ctx, config.RecommendationFilter{Provider: "azure"})
	require.NoError(t, err)
	require.Len(t, survivors, 1)
	survivor := survivors[0]

	mgr := &Manager{config: store}
	gate := func(rec config.RecommendationRecord) error {
		exec := armedExecution("azure", "compute", 0, "approved")
		exec.Recommendations = []config.RecommendationRecord{rec}
		return mgr.staleAzurePricingRefusal(ctx, exec)
	}
	require.ErrorIs(t, gate(survivor), ErrStaleAzurePricing, "the surviving row of an expired hold must not be purchasable")

	// Disabling the account keeps the marker while the survivor exists.
	_, err = db.Exec(ctx, `UPDATE cloud_accounts SET enabled = false WHERE id = $1`, acct)
	require.NoError(t, err)
	require.NoError(t, store.UpsertRecommendationsWithPartial(ctx, base.Add(30*time.Second), nil, nil, nil))
	require.ErrorIs(t, gate(survivor), ErrStaleAzurePricing, "a disabled account's survivor stays refused")

	require.NoError(t, lock.Rollback(ctx))
	require.NoError(t, store.UpsertRecommendationsWithPartial(ctx, base.Add(time.Minute), nil, nil, hold))
	require.ErrorIs(t, gate(survivor), ErrStaleAzurePricing, "the evicted row no longer matches")

	// A successful collect for the account re-prices it: its new row is purchasable.
	require.NoError(t, store.UpsertRecommendationsWithPartial(ctx, base.Add(2*time.Minute),
		[]config.RecommendationRecord{azureHoldRec("r3", acct, "D2", 150)}, []config.SuccessfulCollect{{Provider: "azure", CloudAccountID: &acct}}, nil))
	fresh, err := store.ListStoredRecommendations(ctx, config.RecommendationFilter{Provider: "azure"})
	require.NoError(t, err)
	require.Len(t, fresh, 1)
	require.NoError(t, gate(fresh[0]))
}
