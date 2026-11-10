package config

import (
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var holdNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func holdOf(ids ...string) *RecommendationsCollectionPartial {
	return &RecommendationsCollectionPartial{Providers: []PartialProviderSweep{{
		Provider: "azure", HeldAccountIDs: ids, Reasons: map[string]int{PartialReasonPricingUnavailable: 1},
	}}}
}

func azureCollect(id *string) []SuccessfulCollect {
	return []SuccessfulCollect{{Provider: "azure", CloudAccountID: id}}
}

func TestCarryAzureHold(t *testing.T) {
	a, b, c := "a", "b", "c"
	stored := map[string]bool{"a": true, "b": true, "c": true}
	ambient := map[string]bool{ambientAccountKey: true}
	for name, tc := range map[string]struct {
		prev     *RecommendationsCollectionPartial
		collects []SuccessfulCollect
		partial  *RecommendationsCollectionPartial
		stored   map[string]bool
		want     []string
		wantHold bool
	}{
		"no previous hold":                   {nil, nil, nil, stored, nil, false},
		"nothing re-priced":                  {holdOf("a", "b"), nil, nil, stored, []string{"a", "b"}, true},
		"one re-priced":                      {holdOf("a", "b"), azureCollect(&a), nil, stored, []string{"b"}, true},
		"both re-priced":                     {holdOf("a", "b"), append(azureCollect(&a), azureCollect(&b)...), nil, stored, nil, false},
		"other provider ignored":             {holdOf("a"), []SuccessfulCollect{{Provider: "gcp", CloudAccountID: &a}}, nil, stored, []string{"a"}, true},
		"new hold is merged":                 {holdOf("a", "b"), nil, holdOf("c"), stored, []string{"a", "b", "c"}, true},
		"re-held account dedupes":            {holdOf("a", "b"), nil, holdOf("a"), stored, []string{"a", "b"}, true},
		"account without rows drops":         {holdOf("a", "b"), nil, nil, map[string]bool{"b": true}, []string{"b"}, true},
		"all rows gone drops":                {holdOf("a"), nil, nil, map[string]bool{"c": true}, nil, false},
		"ambient carries while rows exist":   {holdOf(), nil, nil, ambient, []string{ambientAccountKey}, true},
		"ambient survives account rows too":  {holdOf(), azureCollect(&c), nil, map[string]bool{ambientAccountKey: true, "c": true}, []string{ambientAccountKey}, true},
		"ambient re-priced":                  {holdOf(), azureCollect(nil), nil, ambient, nil, false},
		"ambient drops with no ambient rows": {holdOf(), nil, nil, stored, nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			got, _ := carryAzureHold(tc.prev, tc.partial, tc.collects, tc.stored, nil, holdNow)
			if !tc.wantHold {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Len(t, got.Providers, 1)
			assert.ElementsMatch(t, tc.want, got.Providers[0].HeldAccountIDs)
			assert.Positive(t, got.Providers[0].Reasons[PartialReasonPricingUnavailable])
		})
	}
}

// An ambient hold merged with a named account's hold must stay an ambient hold
// after the named account re-prices: the stale ambient rows are still there.
func TestCarryAzureHold_AmbientSurvivesNamedReprice(t *testing.T) {
	c := "c"
	stored := map[string]bool{ambientAccountKey: true, "c": true}

	// Cycle 1: the ambient hold carries while this cycle holds account c.
	merged, _ := carryAzureHold(holdOf(), holdOf("c"), nil, stored, nil, holdNow)
	require.NotNil(t, merged)

	// Cycle 2: c re-prices, the ambient identity does not.
	got, _ := carryAzureHold(merged, nil, azureCollect(&c), stored, nil, holdNow)
	require.NotNil(t, got, "ambient rows still exist, so the hold must stay")
	assert.Contains(t, got.Providers[0].HeldAccountIDs, ambientAccountKey)

	// Cycle 3: the ambient identity re-prices, which ends the hold.
	end, _ := carryAzureHold(got, nil, azureCollect(nil), stored, nil, holdNow)
	assert.Nil(t, end)
}

// An ambient pricing outage in this cycle plus a carried named hold keeps both.
func TestCarryAzureHold_ThisCycleAmbientKeptWithCarriedNamed(t *testing.T) {
	stored := map[string]bool{ambientAccountKey: true, "a": true}
	got, _ := carryAzureHold(holdOf("a"), holdOf(), nil, stored, nil, holdNow)
	require.NotNil(t, got)
	assert.ElementsMatch(t, []string{"a", ambientAccountKey}, got.Providers[0].HeldAccountIDs)
}

// Carried accounts add to the incomplete count instead of being absorbed into a
// larger count that came from other failures this cycle.
func TestCarryAzureHold_IncompleteAccountsCountsEachCarriedAccount(t *testing.T) {
	stored := map[string]bool{"a": true, "b": true, "x": true, ambientAccountKey: true}
	for name, tc := range map[string]struct {
		prev, partial *RecommendationsCollectionPartial
		want          int
	}{
		"carried on top of other failures": {holdOf("a", "b"), withIncomplete(holdOf("x"), 3), 5},
		"own ambient outage counted once":  {holdOf("a"), withIncomplete(holdOf(), 1), 2},
		"nothing this cycle":               {holdOf("a", "b"), nil, 2},
		"ambient carried":                  {holdOf(), nil, 1},
	} {
		t.Run(name, func(t *testing.T) {
			got, _ := carryAzureHold(tc.prev, tc.partial, nil, stored, nil, holdNow)
			require.NotNil(t, got)
			assert.Equal(t, tc.want, got.Providers[0].IncompleteAccounts)
		})
	}
}

func withIncomplete(p *RecommendationsCollectionPartial, n int) *RecommendationsCollectionPartial {
	p.Providers[0].IncompleteAccounts = n
	return p
}

// Helpers for the hold-age bound (issue #1675).

func setOf(ids ...string) map[string]bool {
	m := map[string]bool{}
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// holdAt is a stored hold whose accounts have the given start times.
func holdAt(since map[string]time.Time) *RecommendationsCollectionPartial {
	ids := slices.Sorted(maps.Keys(since))
	p := holdOf(ids...)
	p.Providers[0].HeldSince = since
	p.Providers[0].IncompleteAccounts = len(ids)
	return p
}

// withExpired adds an expired-hold record (ids and the matching reason count).
func withExpired(p *RecommendationsCollectionPartial, ids ...string) *RecommendationsCollectionPartial {
	p.Providers[0].ExpiredHoldAccountIDs = ids
	p.Providers[0].Reasons[PartialReasonPricingHoldExpired] = len(ids)
	return p
}

func expiredOnly(ids ...string) *RecommendationsCollectionPartial {
	p := holdOf()
	delete(p.Providers[0].Reasons, PartialReasonPricingUnavailable)
	return withExpired(p, ids...)
}

func azureOf(t *testing.T, p *RecommendationsCollectionPartial) PartialProviderSweep {
	t.Helper()
	require.NotNil(t, p)
	require.Len(t, p.Providers, 1)
	return p.Providers[0]
}

func TestCarryAzureHold_SetsHeldSinceOnFirstHold(t *testing.T) {
	got, evict := carryAzureHold(nil, holdOf("a"), nil, setOf("a"), nil, holdNow)
	sweep := azureOf(t, got)
	assert.Equal(t, map[string]time.Time{"a": holdNow}, sweep.HeldSince)
	assert.Equal(t, []string{"a"}, sweep.HeldAccountIDs)
	assert.Empty(t, evict)
}

func TestCarryAzureHold_KeepsHeldSinceAcrossCycles(t *testing.T) {
	start := holdNow.Add(-24 * time.Hour)
	got, evict := carryAzureHold(holdAt(map[string]time.Time{"a": start}), holdOf("b"), nil, setOf("a", "b"), nil, holdNow)
	sweep := azureOf(t, got)
	assert.Equal(t, map[string]time.Time{"a": start, "b": holdNow}, sweep.HeldSince)
	assert.Empty(t, evict)
}

func TestCarryAzureHold_ExpiresAtMaxAge(t *testing.T) {
	start := holdNow
	prev := func() *RecommendationsCollectionPartial {
		return holdAt(map[string]time.Time{"a": start, "b": start.Add(time.Hour)})
	}
	stored := setOf("a", "b")

	got, evict := carryAzureHold(prev(), nil, nil, stored, setOf("a", "b"), start.Add(PricingHoldMaxAge-time.Nanosecond))
	assert.Equal(t, []string{"a", "b"}, azureOf(t, got).HeldAccountIDs)
	assert.Empty(t, evict)

	got, evict = carryAzureHold(prev(), nil, nil, stored, setOf("a", "b"), start.Add(PricingHoldMaxAge))
	sweep := azureOf(t, got)
	assert.Equal(t, []string{"b"}, sweep.HeldAccountIDs, "only the aged account leaves the hold")
	assert.Equal(t, map[string]time.Time{"b": start.Add(time.Hour)}, sweep.HeldSince)
	assert.Equal(t, []string{"a"}, sweep.ExpiredHoldAccountIDs)
	assert.Equal(t, []string{"a"}, evict)
	assert.Equal(t, 1, sweep.IncompleteAccounts)
	assert.Equal(t, 1, sweep.Reasons[PartialReasonPricingHoldExpired])
	assert.Positive(t, sweep.Reasons[PartialReasonPricingUnavailable])

	// The last held account expiring ends the hold, so purchases stop being refused.
	got, evict = carryAzureHold(holdAt(map[string]time.Time{"a": start}), nil, nil, stored, setOf("a"), start.Add(PricingHoldMaxAge))
	sweep = azureOf(t, got)
	assert.Empty(t, sweep.HeldAccountIDs)
	assert.Nil(t, previousAzureHold(got))
	assert.Zero(t, sweep.IncompleteAccounts)
	assert.Equal(t, []string{"a"}, evict)
}

func TestCarryAzureHold_RepriceResetsHeldSince(t *testing.T) {
	a := "a"
	prev := holdAt(map[string]time.Time{"a": holdNow, "b": holdNow})
	got, _ := carryAzureHold(prev, nil, azureCollect(&a), setOf("a", "b"), nil, holdNow.Add(30*time.Hour))
	assert.Equal(t, map[string]time.Time{"b": holdNow}, azureOf(t, got).HeldSince)

	later := holdNow.Add(40 * time.Hour)
	got, evict := carryAzureHold(got, holdOf("a"), nil, setOf("a", "b"), setOf("a", "b"), later)
	sweep := azureOf(t, got)
	assert.Equal(t, []string{"a"}, sweep.HeldAccountIDs, "a is held again with a fresh start")
	assert.Equal(t, later, sweep.HeldSince["a"])
	assert.Equal(t, []string{"b"}, evict)
}

func TestCarryAzureHold_LegacyHoldWithoutSinceNotExpired(t *testing.T) {
	got, evict := carryAzureHold(holdOf("a"), nil, nil, setOf("a"), nil, holdNow.Add(1000*time.Hour))
	sweep := azureOf(t, got)
	assert.Equal(t, []string{"a"}, sweep.HeldAccountIDs)
	assert.Equal(t, holdNow.Add(1000*time.Hour), sweep.HeldSince["a"])
	assert.Empty(t, evict)
}

func TestCarryAzureHold_SkippedAzureCycleKeepsAgeButChecksBound(t *testing.T) {
	prev := func() *RecommendationsCollectionPartial { return holdAt(map[string]time.Time{"a": holdNow}) }
	got, evict := carryAzureHold(prev(), nil, nil, setOf("a"), setOf("a"), holdNow.Add(10*time.Hour))
	assert.Equal(t, holdNow, azureOf(t, got).HeldSince["a"])
	assert.Empty(t, evict)

	got, evict = carryAzureHold(prev(), nil, nil, setOf("a"), setOf("a"), holdNow.Add(37*time.Hour))
	assert.Equal(t, []string{"a"}, azureOf(t, got).ExpiredHoldAccountIDs)
	assert.Equal(t, []string{"a"}, evict)
}

func TestCarryAzureHold_AmbientExpires(t *testing.T) {
	stored := setOf(ambientAccountKey)
	t.Run("carried from a legacy ambient hold", func(t *testing.T) {
		got, _ := carryAzureHold(holdOf(), nil, nil, stored, nil, holdNow)
		assert.Equal(t, holdNow, azureOf(t, got).HeldSince[ambientAccountKey])
		got, evict := carryAzureHold(got, nil, nil, stored, nil, holdNow.Add(PricingHoldMaxAge))
		sweep := azureOf(t, got)
		assert.Equal(t, []string{ambientAccountKey}, sweep.ExpiredHoldAccountIDs)
		assert.Equal(t, []string{ambientAccountKey}, evict)
		assert.Nil(t, previousAzureHold(got))
	})
	t.Run("this cycle's own ambient outage", func(t *testing.T) {
		got, _ := carryAzureHold(nil, withIncomplete(holdOf(), 1), nil, stored, nil, holdNow)
		sweep := azureOf(t, got)
		assert.Empty(t, sweep.HeldAccountIDs, "the own ambient hold keeps its empty id list")
		assert.Equal(t, holdNow, sweep.HeldSince[ambientAccountKey])

		got, evict := carryAzureHold(got, withIncomplete(holdOf(), 1), nil, stored, nil, holdNow.Add(PricingHoldMaxAge))
		sweep = azureOf(t, got)
		assert.Equal(t, []string{ambientAccountKey}, evict)
		assert.Nil(t, previousAzureHold(got))
		assert.Zero(t, sweep.IncompleteAccounts)
		assert.Empty(t, sweep.HeldSince)
	})
}

func TestCarryAzureHold_NoStoredRowsNotAged(t *testing.T) {
	got, evict := carryAzureHold(holdAt(map[string]time.Time{"a": holdNow}), nil, nil, setOf("c"), nil, holdNow.Add(100*time.Hour))
	assert.Nil(t, got, "no stored rows ends the hold without expiring it")
	assert.Empty(t, evict)
}

func TestCarryAzureHold_EachAccountOnceWhenInCarriedAndThisCycle(t *testing.T) {
	stored := setOf("a")
	prev := func() *RecommendationsCollectionPartial { return holdAt(map[string]time.Time{"a": holdNow}) }

	got, evict := carryAzureHold(prev(), withIncomplete(holdOf("a"), 1), nil, stored, setOf("a"), holdNow.Add(10*time.Hour))
	sweep := azureOf(t, got)
	assert.Equal(t, []string{"a"}, sweep.HeldAccountIDs)
	assert.Equal(t, 1, sweep.IncompleteAccounts)
	assert.Empty(t, evict)

	got, evict = carryAzureHold(prev(), withIncomplete(holdOf("a"), 1), nil, stored, setOf("a"), holdNow.Add(PricingHoldMaxAge))
	sweep = azureOf(t, got)
	assert.Equal(t, []string{"a"}, evict, "evicted once, not once per source")
	assert.Equal(t, []string{"a"}, sweep.ExpiredHoldAccountIDs)
	assert.Zero(t, sweep.IncompleteAccounts)
}

func TestCarryAzureHold_ExpiredAccountNotReagedNorReheld(t *testing.T) {
	stored, active := setOf("a", "b"), setOf("a", "b")
	prev := expiredOnly("a")

	got, evict := carryAzureHold(prev, withIncomplete(holdOf("a", "b"), 2), nil, stored, active, holdNow)
	sweep := azureOf(t, got)
	assert.Equal(t, []string{"b"}, sweep.HeldAccountIDs, "a is not held again")
	assert.Equal(t, map[string]time.Time{"b": holdNow}, sweep.HeldSince)
	assert.Equal(t, []string{"a"}, sweep.ExpiredHoldAccountIDs)
	assert.Equal(t, []string{"a"}, evict)
	assert.Equal(t, 1, sweep.IncompleteAccounts)

	// Long after the bound it still is not re-armed.
	got, evict = carryAzureHold(got, withIncomplete(holdOf("a"), 1), nil, stored, active, holdNow.Add(500*time.Hour))
	sweep = azureOf(t, got)
	assert.Empty(t, sweep.HeldAccountIDs)
	assert.Nil(t, previousAzureHold(got), "no refusal flapping for the expired account")
	assert.Equal(t, []string{"a", "b"}, sweep.ExpiredHoldAccountIDs, "b aged out meanwhile")
	assert.ElementsMatch(t, []string{"a", "b"}, evict)
}

func TestCarryAzureHold_OlderCollectedAtNeverMovesHeldSinceBackwards(t *testing.T) {
	newer := holdNow.Add(10 * time.Hour)
	got, evict := carryAzureHold(holdAt(map[string]time.Time{"a": newer}), holdOf("a"), nil, setOf("a"), nil, holdNow)
	sweep := azureOf(t, got)
	assert.Equal(t, newer, sweep.HeldSince["a"])
	assert.Equal(t, []string{"a"}, sweep.HeldAccountIDs, "a negative age expires nothing")
	assert.Empty(t, evict)
}

func TestCarryAzureHold_ExpiredIdsCarriedAcrossSkippedAzureCycle(t *testing.T) {
	gcp := "a"
	got, evict := carryAzureHold(expiredOnly("a"), nil, []SuccessfulCollect{{Provider: "gcp", CloudAccountID: &gcp}}, setOf("a"), setOf("a"), holdNow)
	sweep := azureOf(t, got)
	assert.Equal(t, []string{"a"}, sweep.ExpiredHoldAccountIDs)
	assert.Equal(t, 1, sweep.Reasons[PartialReasonPricingHoldExpired])
	assert.Equal(t, []string{"a"}, evict, "its stored row is evicted although the account is not held this cycle")
}

// An expired marker outlives account state while a row it protects exists, so
// the purchase gate keeps refusing a survivor of a failed eviction.
func TestCarryAzureHold_KeptExpiredWhileRowsRemain(t *testing.T) {
	a := "a"
	cases := []struct {
		name         string
		prev         *RecommendationsCollectionPartial
		collects     []SuccessfulCollect
		stored       map[string]bool
		active       map[string]bool
		wantExpired  []string
		wantEvict    []string
		wantNoMarker bool
	}{
		{name: "disabled account with a stored row stays expired", prev: expiredOnly("a"), stored: setOf("a"), wantExpired: []string{"a"}, wantEvict: []string{"a"}},
		{name: "disabled account without rows ends the marker", prev: expiredOnly("a"), wantNoMarker: true},
		{name: "active account without rows stays expired", prev: expiredOnly("a"), active: setOf("a"), wantExpired: []string{"a"}},
		{name: "re-priced account drops out despite its old rows", prev: expiredOnly("a"), collects: azureCollect(&a), stored: setOf("a"), active: setOf("a"), wantNoMarker: true},
		{name: "ambient key stays expired without rows or accounts", prev: expiredOnly(ambientAccountKey), wantExpired: []string{ambientAccountKey}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, evict := carryAzureHold(tc.prev, nil, tc.collects, tc.stored, tc.active, holdNow)
			assert.Equal(t, tc.wantEvict, evict)
			if tc.wantNoMarker {
				assert.Nil(t, got)
				return
			}
			sweep := azureOf(t, got)
			assert.Equal(t, tc.wantExpired, sweep.ExpiredHoldAccountIDs)
			assert.Equal(t, len(tc.wantExpired), sweep.Reasons[PartialReasonPricingHoldExpired])
		})
	}
}

func TestCarryAzureHold_ExpiredIdsClearedOnReprice(t *testing.T) {
	a := "a"
	got, _ := carryAzureHold(expiredOnly("a", "b"), nil, azureCollect(&a), setOf(), setOf("a", "b"), holdNow)
	sweep := azureOf(t, got)
	assert.Equal(t, []string{"b"}, sweep.ExpiredHoldAccountIDs)
	assert.Equal(t, 1, sweep.Reasons[PartialReasonPricingHoldExpired])

	got, _ = carryAzureHold(expiredOnly(ambientAccountKey), nil, azureCollect(nil), setOf(), nil, holdNow)
	assert.Nil(t, got, "an ambient re-price clears the ambient expiry")
}

func TestCarryAzureHold_ExpiredIdsDroppedForDisabledOrDeletedAccount(t *testing.T) {
	got, _ := carryAzureHold(expiredOnly("a", "b", ambientAccountKey), nil, nil, setOf(), setOf("a"), holdNow)
	assert.Equal(t, []string{ambientAccountKey, "a"}, azureOf(t, got).ExpiredHoldAccountIDs, "ambient is always active")

	got, _ = carryAzureHold(expiredOnly("a", "b"), nil, nil, setOf(), setOf(), holdNow)
	assert.Nil(t, got, "no active account keeps the record")
}

func TestCarryAzureHold_ExpiredIdsKeepReasonCountInSync(t *testing.T) {
	a := "a"
	prev := withExpired(holdAt(map[string]time.Time{"c": holdNow}), "a", "b")
	got, _ := carryAzureHold(prev, nil, azureCollect(&a), setOf("c"), setOf("a", "b", "c"), holdNow.Add(PricingHoldMaxAge))
	sweep := azureOf(t, got)
	assert.Equal(t, []string{"b", "c"}, sweep.ExpiredHoldAccountIDs)
	assert.Equal(t, len(sweep.ExpiredHoldAccountIDs), sweep.Reasons[PartialReasonPricingHoldExpired])

	// At zero the reason key is removed.
	got, _ = carryAzureHold(withExpired(holdAt(map[string]time.Time{"c": holdNow}), "a"), nil, azureCollect(&a), setOf("c"), setOf("a", "c"), holdNow)
	sweep = azureOf(t, got)
	assert.Empty(t, sweep.ExpiredHoldAccountIDs)
	assert.NotContains(t, sweep.Reasons, PartialReasonPricingHoldExpired)
}

func TestCarryAzureHold_ExpiredHeldAccountKeysIncludedEveryCycle(t *testing.T) {
	stored, active := setOf("a", "c"), setOf("a", "c")
	prev := withExpired(holdAt(map[string]time.Time{"c": holdNow}), "a")
	now := holdNow.Add(PricingHoldMaxAge)
	got, evict := carryAzureHold(prev, holdOf("a"), nil, stored, active, now)
	assert.ElementsMatch(t, []string{"a", "c"}, evict, "the previously expired a and the newly expired c")

	got, evict = carryAzureHold(got, holdOf("a"), nil, stored, active, now.Add(24*time.Hour))
	assert.ElementsMatch(t, []string{"a", "c"}, evict, "both stay evicted while their rows are stored")
	assert.Equal(t, []string{"a", "c"}, azureOf(t, got).ExpiredHoldAccountIDs)
}
