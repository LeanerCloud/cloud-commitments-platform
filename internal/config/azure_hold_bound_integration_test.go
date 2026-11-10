//go:build integration
// +build integration

package config_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type boundEnv struct {
	ctx   context.Context
	db    *database.Connection
	store *config.PostgresStore
	base  time.Time
}

func newBoundEnv(t *testing.T) boundEnv {
	t.Helper()
	ctx := context.Background()
	db := testhelpers.RequireMigratedDatabase(ctx, t, getMigrationsPath()).DB
	return boundEnv{ctx: ctx, db: db, store: config.NewPostgresStore(db), base: time.Now().UTC().Truncate(time.Second)}
}

func (e boundEnv) account(t *testing.T, ext string) string {
	t.Helper()
	id := uuid.NewString()
	seedRecommendationCloudAccount(e.ctx, t, e.store, id, "azure", ext)
	return id
}

// seedHold writes a stored hold on key (uuid.Nil for the ambient identity)
// that began at since, bypassing the carry.
func (e boundEnv) seedHold(t *testing.T, key string, since time.Time) {
	t.Helper()
	sweep := config.PartialProviderSweep{
		Provider: "azure", IncompleteAccounts: 1, HeldAccountIDs: []string{key},
		HeldSince: map[string]time.Time{key: since},
		Reasons:   map[string]int{config.PartialReasonPricingUnavailable: 1},
	}
	if key == uuid.Nil.String() {
		sweep.HeldAccountIDs = nil
	}
	raw, err := json.Marshal(&config.RecommendationsCollectionPartial{Providers: []config.PartialProviderSweep{sweep}})
	require.NoError(t, err)
	_, err = e.db.Exec(e.ctx, `UPDATE recommendations_state SET last_collection_partial = $1::jsonb WHERE id = 1`, raw)
	require.NoError(t, err)
}

func holdPartial(keys ...string) *config.RecommendationsCollectionPartial {
	return &config.RecommendationsCollectionPartial{Providers: []config.PartialProviderSweep{{
		Provider: "azure", IncompleteAccounts: len(keys), HeldAccountIDs: keys,
		Reasons: map[string]int{config.PartialReasonPricingUnavailable: 1},
	}}}
}

func (e boundEnv) rowCount(t *testing.T, key string) int {
	t.Helper()
	var n int
	require.NoError(t, e.db.QueryRow(e.ctx, `SELECT count(*) FROM recommendations WHERE provider = 'azure' AND account_key::text = $1`, key).Scan(&n))
	return n
}

func (e boundEnv) azureSweep(t *testing.T) config.PartialProviderSweep {
	t.Helper()
	fresh, err := e.store.GetRecommendationsFreshness(e.ctx)
	require.NoError(t, err)
	require.NotNil(t, fresh.LastCollectionPartial)
	for _, p := range fresh.LastCollectionPartial.Providers {
		if p.Provider == "azure" {
			return p
		}
	}
	t.Fatal("no azure sweep in the marker")
	return config.PartialProviderSweep{}
}

func (e boundEnv) requireExpiredOnly(t *testing.T, acct string) {
	t.Helper()
	sweep := e.azureSweep(t)
	require.Empty(t, sweep.HeldAccountIDs)
	require.Equal(t, []string{acct}, sweep.ExpiredHoldAccountIDs)
	require.Equal(t, 1, sweep.Reasons[config.PartialReasonPricingHoldExpired])
	require.Zero(t, sweep.Reasons[config.PartialReasonPricingUnavailable])
}

func TestUpsertRecommendationsWithPartial_EvictsHeldRowsPastBound(t *testing.T) {
	e := newBoundEnv(t)
	acct := e.account(t, "sub-bound-evict")
	require.NoError(t, e.store.UpsertRecommendations(e.ctx, e.base.Add(-48*time.Hour),
		[]config.RecommendationRecord{azureAccountRec("r1", acct, "D2"), azureAccountRec("r2", acct, "D4")}, nil))
	e.seedHold(t, acct, e.base.Add(-config.PricingHoldMaxAge))

	require.NoError(t, e.store.UpsertRecommendationsWithPartial(e.ctx, e.base, nil, nil, holdPartial(acct)))

	require.Zero(t, e.rowCount(t, acct), "stale rows of the expired account must be deleted")
	sweep := e.azureSweep(t)
	require.Equal(t, []string{acct}, sweep.ExpiredHoldAccountIDs)
	require.NotContains(t, sweep.HeldAccountIDs, acct)
	require.Equal(t, 1, sweep.Reasons[config.PartialReasonPricingHoldExpired])
}

func TestUpsertRecommendationsWithPartial_KeepsHeldRowsBeforeBound(t *testing.T) {
	e := newBoundEnv(t)
	acct := e.account(t, "sub-bound-keep")
	require.NoError(t, e.store.UpsertRecommendations(e.ctx, e.base.Add(-48*time.Hour),
		[]config.RecommendationRecord{azureAccountRec("r1", acct, "D2")}, nil))
	e.seedHold(t, acct, e.base.Add(-config.PricingHoldMaxAge+time.Minute))

	require.NoError(t, e.store.UpsertRecommendationsWithPartial(e.ctx, e.base, nil, nil, holdPartial(acct)))

	require.Equal(t, 1, e.rowCount(t, acct))
	sweep := e.azureSweep(t)
	require.Equal(t, []string{acct}, sweep.HeldAccountIDs)
	require.Empty(t, sweep.ExpiredHoldAccountIDs)
}

func TestUpsertRecommendationsWithPartial_KeepsRowsRepricedThisCycle(t *testing.T) {
	e := newBoundEnv(t)
	acct := e.account(t, "sub-bound-fresh")
	require.NoError(t, e.store.UpsertRecommendations(e.ctx, e.base.Add(-48*time.Hour),
		[]config.RecommendationRecord{azureAccountRec("stale", acct, "D2")}, nil))
	e.seedHold(t, acct, e.base.Add(-config.PricingHoldMaxAge))

	require.NoError(t, e.store.UpsertRecommendationsWithPartial(e.ctx, e.base,
		[]config.RecommendationRecord{azureAccountRec("fresh", acct, "D4")}, nil, holdPartial(acct)))

	require.Equal(t, 1, e.rowCount(t, acct), "only the row collected this cycle survives the collected_at guard")
}

func TestUpsertRecommendationsWithPartial_AmbientRowsEvictedByNilKey(t *testing.T) {
	e := newBoundEnv(t)
	require.NoError(t, e.store.UpsertRecommendations(e.ctx, e.base.Add(-48*time.Hour),
		[]config.RecommendationRecord{ambientRec("amb", "azure", "D2")}, nil))
	e.seedHold(t, uuid.Nil.String(), e.base.Add(-config.PricingHoldMaxAge))

	ambient := &config.RecommendationsCollectionPartial{Providers: []config.PartialProviderSweep{{
		Provider: "azure", IncompleteAccounts: 1, Reasons: map[string]int{config.PartialReasonPricingUnavailable: 1},
	}}}
	require.NoError(t, e.store.UpsertRecommendationsWithPartial(e.ctx, e.base, nil, nil, ambient))

	require.Zero(t, e.rowCount(t, uuid.Nil.String()))
	require.Equal(t, []string{uuid.Nil.String()}, e.azureSweep(t).ExpiredHoldAccountIDs)
}

// With no previous hold, this cycle's own hold must start its clock; the old
// early return in carryHoldInTx skipped the carry and left held_since unset.
func TestUpsertRecommendationsWithPartial_StampsFirstHoldThisCycle(t *testing.T) {
	e := newBoundEnv(t)
	acct := e.account(t, "sub-bound-first")
	require.NoError(t, e.store.UpsertRecommendations(e.ctx, e.base.Add(-time.Hour),
		[]config.RecommendationRecord{azureAccountRec("r1", acct, "D2")}, nil))

	require.NoError(t, e.store.UpsertRecommendationsWithPartial(e.ctx, e.base, nil, nil, holdPartial(acct)))

	sweep := e.azureSweep(t)
	require.True(t, e.base.Equal(sweep.HeldSince[acct]), "held_since = %v, want %v", sweep.HeldSince[acct], e.base)
}

func TestUpsertRecommendationsWithPartial_ExpiredAccountKeepsBeingEvictedEachCycle(t *testing.T) {
	e := newBoundEnv(t)
	acct := e.account(t, "sub-bound-repeat")
	require.NoError(t, e.store.UpsertRecommendations(e.ctx, e.base.Add(-48*time.Hour),
		[]config.RecommendationRecord{azureAccountRec("r1", acct, "D2")}, nil))
	e.seedHold(t, acct, e.base.Add(-config.PricingHoldMaxAge))
	require.NoError(t, e.store.UpsertRecommendationsWithPartial(e.ctx, e.base, nil, nil, holdPartial(acct)))
	require.Zero(t, e.rowCount(t, acct))

	// A stale row re-appears (an older-stamped writer), and the outage continues.
	require.NoError(t, e.store.UpsertRecommendations(e.ctx, e.base.Add(-time.Hour),
		[]config.RecommendationRecord{azureAccountRec("r2", acct, "D4")}, nil))
	require.NoError(t, e.store.UpsertRecommendationsWithPartial(e.ctx, e.base.Add(24*time.Hour), nil, nil, holdPartial(acct)))

	require.Zero(t, e.rowCount(t, acct), "the already expired account is evicted again")
	sweep := e.azureSweep(t)
	require.Equal(t, []string{acct}, sweep.ExpiredHoldAccountIDs)
	require.NotContains(t, sweep.HeldAccountIDs, acct, "an expired account is not re-held")
}

func TestUpsertRecommendationsWithPartial_OverlappingUpsertsSerializeAndAgeOnce(t *testing.T) {
	e := newBoundEnv(t)
	expired := e.account(t, "sub-bound-race-x")
	young := e.account(t, "sub-bound-race-y")
	require.NoError(t, e.store.UpsertRecommendations(e.ctx, e.base.Add(-48*time.Hour), []config.RecommendationRecord{
		azureAccountRec("rx", expired, "D2"), azureAccountRec("ry", young, "D2"),
	}, nil))
	youngSince := e.base.Add(-time.Hour)
	raw, err := json.Marshal(&config.RecommendationsCollectionPartial{Providers: []config.PartialProviderSweep{{
		Provider: "azure", IncompleteAccounts: 2, HeldAccountIDs: []string{expired, young},
		HeldSince: map[string]time.Time{expired: e.base.Add(-config.PricingHoldMaxAge), young: youngSince},
		Reasons:   map[string]int{config.PartialReasonPricingUnavailable: 1},
	}}})
	require.NoError(t, err)
	_, err = e.db.Exec(e.ctx, `UPDATE recommendations_state SET last_collection_partial = $1::jsonb WHERE id = 1`, raw)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recs := []config.RecommendationRecord{
				azureAccountRec("ry-"+uuid.NewString(), young, "D4"), azureAccountRec("rx-"+uuid.NewString(), expired, "D8"),
			}
			errs <- e.store.UpsertRecommendationsWithPartial(ctx, e.base.Add(time.Duration(i)*time.Second), recs, nil, holdPartial(expired, young))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	sweep := e.azureSweep(t)
	require.Equal(t, []string{expired}, sweep.ExpiredHoldAccountIDs)
	require.Equal(t, []string{young}, sweep.HeldAccountIDs)
	require.True(t, youngSince.Equal(sweep.HeldSince[young]), "held_since moved: %v", sweep.HeldSince[young])
}

func TestUpsertRecommendationsWithPartial_ExpiredEvictionSkipsRowsLockedByAnotherTx(t *testing.T) {
	e := newBoundEnv(t)
	acct := e.account(t, "sub-bound-skip")
	require.NoError(t, e.store.UpsertRecommendations(e.ctx, e.base.Add(-48*time.Hour),
		[]config.RecommendationRecord{azureAccountRec("r1", acct, "D2"), azureAccountRec("r2", acct, "D4")}, nil))
	e.seedHold(t, acct, e.base.Add(-config.PricingHoldMaxAge))

	// Another tx holds one stale row's lock, like a collection mid-write.
	hold, err := e.db.Begin(e.ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = hold.Rollback(context.Background()) })
	var lockedID string
	require.NoError(t, hold.QueryRow(e.ctx, `SELECT id::text FROM recommendations WHERE provider = 'azure' AND account_key::text = $1 ORDER BY id LIMIT 1 FOR UPDATE`, acct).Scan(&lockedID))

	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	require.NoError(t, e.store.UpsertRecommendationsWithPartial(ctx, e.base, nil, nil, holdPartial(acct)), "the eviction must not wait on a locked row")
	require.Equal(t, 1, e.rowCount(t, acct), "only the unlocked stale row is deleted this cycle")
	// The purchase gate relies on this exact marker: expired, nothing held, no pricing_unavailable.
	e.requireExpiredOnly(t, acct)

	require.NoError(t, hold.Rollback(e.ctx))
	require.NoError(t, e.store.UpsertRecommendationsWithPartial(e.ctx, e.base.Add(time.Minute), nil, nil, holdPartial(acct)))
	require.Zero(t, e.rowCount(t, acct), "the next cycle deletes the row that was locked")
	e.requireExpiredOnly(t, acct)
}

// Disabling the account must not end the marker while a survivor row exists: the
// next cycle reads the stored rows before it evicts them, so the marker ends one
// cycle after the row is deleted.
func TestUpsertRecommendationsWithPartial_SurvivorThenDisableKeepsMarkerUntilDeleted(t *testing.T) {
	e := newBoundEnv(t)
	acct := e.account(t, "sub-bound-disable")
	require.NoError(t, e.store.UpsertRecommendations(e.ctx, e.base.Add(-48*time.Hour),
		[]config.RecommendationRecord{azureAccountRec("r1", acct, "D2"), azureAccountRec("r2", acct, "D4")}, nil))
	e.seedHold(t, acct, e.base.Add(-config.PricingHoldMaxAge))
	hold, err := e.db.Begin(e.ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = hold.Rollback(context.Background()) })
	_, err = hold.Exec(e.ctx, `SELECT id FROM recommendations WHERE provider = 'azure' AND account_key::text = $1 ORDER BY id LIMIT 1 FOR UPDATE`, acct)
	require.NoError(t, err)
	require.NoError(t, e.store.UpsertRecommendationsWithPartial(e.ctx, e.base, nil, nil, holdPartial(acct)))
	require.Equal(t, 1, e.rowCount(t, acct))

	_, err = e.db.Exec(e.ctx, `UPDATE cloud_accounts SET enabled = false WHERE id = $1`, acct)
	require.NoError(t, err)
	require.NoError(t, hold.Rollback(e.ctx))

	// Cycle 2 reads the stored rows before it evicts: the marker stays, the row goes.
	require.NoError(t, e.store.UpsertRecommendationsWithPartial(e.ctx, e.base.Add(time.Minute), nil, nil, nil))
	require.Zero(t, e.rowCount(t, acct), "an expired account's surviving row is evicted although nothing is held")
	e.requireExpiredOnly(t, acct)

	// Cycle 3 finds no rows and no enabled account: the marker ends.
	require.NoError(t, e.store.UpsertRecommendationsWithPartial(e.ctx, e.base.Add(2*time.Minute), nil, nil, nil))
	fresh, err := e.store.GetRecommendationsFreshness(e.ctx)
	require.NoError(t, err)
	require.Nil(t, fresh.LastCollectionPartial)
}

// JSONB rejects invalid text, so a corrupt marker is a valid value of the wrong
// shape. Collection writes must fail loudly instead of reading it as "no hold".
func (e boundEnv) seedCorruptMarker(t *testing.T) {
	t.Helper()
	_, err := e.db.Exec(e.ctx, `UPDATE recommendations_state SET last_collection_partial = '[]'::jsonb WHERE id = 1`)
	require.NoError(t, err)
}

func TestUpsertRecommendationsWithPartial_CorruptMarkerFailsTheWriteWithRepairHint(t *testing.T) {
	e := newBoundEnv(t)
	acct := e.account(t, "sub-corrupt-write")
	e.seedCorruptMarker(t)

	err := e.store.UpsertRecommendationsWithPartial(e.ctx, e.base, []config.RecommendationRecord{azureAccountRec("r1", acct, "D2")},
		[]config.SuccessfulCollect{{Provider: "azure", CloudAccountID: &acct}}, nil)

	require.Error(t, err)
	require.Contains(t, err.Error(), "UPDATE recommendations_state SET last_collection_partial = NULL WHERE id = 1")
	require.Zero(t, e.rowCount(t, acct), "the failed write must roll back")
}

func TestGetRecommendationsFreshness_CorruptMarkerIsReportedNotFatal(t *testing.T) {
	e := newBoundEnv(t)
	e.seedCorruptMarker(t)

	fresh, err := e.store.GetRecommendationsFreshness(e.ctx)

	require.NoError(t, err, "the scheduler and dashboard depend on this read")
	require.Nil(t, fresh.LastCollectionPartial)
	require.True(t, fresh.LastCollectionPartialUnreadable)
}
