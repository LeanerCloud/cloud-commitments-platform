package config

import (
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
)

// ambientAccountKey is the account_key of rows with no registered account.
var ambientAccountKey = uuid.Nil.String()

// carryAzureHold keeps the Azure pricing hold alive for as long as the held
// rows are. Every upsert replaces last_collection_partial, but an account's
// Advisor rows stay at their old price until a later cycle re-prices them, so
// the previous hold (prev, read under the state row lock) is merged into this
// cycle's partial: accounts re-priced now (in collects) drop out, newly held
// ones are added, the rest carry over. That covers another outage, an outright
// Azure failure and a run that skips Azure.
//
// A hold lasts only while stored Azure rows it protects exist: stored holds the
// account_key of every provider=azure row, read in the same transaction. A
// deleted account's rows cascade and pruned rows disappear, which ends the
// hold; a disabled account keeps its rows, so it stays held. A hold with no
// account IDs (an unresolved ambient identity) covers the ambient rows, keyed
// by the nil UUID, and carries until an ambient Azure collection succeeds or
// none remain. Once the hold also names accounts, the ambient part is recorded
// as the nil UUID in the held IDs, so it outlives the named accounts' re-pricing.
//
// A hold also ends by age: an account held for PricingHoldMaxAge or longer is
// removed from the hold, listed in ExpiredHoldAccountIDs and returned in evict
// so the store deletes its stale rows (the scheduler would otherwise withhold it
// again every cycle). HeldSince carries when each hold began; a hold without a
// recorded start (written before the field existed) starts now. Expired ids are
// carried until the account re-prices or is not in active (the enabled Azure
// accounts; the ambient key is always active), are never re-aged or re-held, and
// are evicted each cycle they are still held. now is the collection time.
func carryAzureHold(prev, partial *RecommendationsCollectionPartial, collects []SuccessfulCollect, stored, active map[string]bool, now time.Time) (merged *RecommendationsCollectionPartial, evict []string) {
	prevSweep := azureSweep(prev)
	repriced := repricedKeys(collects)
	expiredPrev := keptExpired(prevSweep, repriced, active)
	carried, unresolved := carriedFromPrev(prev, repriced, stored)
	if len(carried) == 0 && !unresolved && len(expiredPrev) == 0 && !holdsPricing(azureSweep(partial)) {
		return partial, nil
	}
	carriedSet := carriedKeySet(carried, unresolved)
	toMerge := slices.DeleteFunc(slices.Sorted(maps.Keys(carriedSet)), func(id string) bool { return expiredPrev[id] })
	if len(toMerge) > 0 {
		partial = mergeCarriedHold(partial, toMerge)
	}
	if azureSweep(partial) == nil && len(expiredPrev) == 0 {
		return partial, nil
	}
	partial, sweep := ensureAzureSweep(partial)
	aged := classifyHeld(sweep, holdAging{prior: prevSweep.heldSince(), carried: carriedSet, expiredPrev: expiredPrev, now: now})
	applyAging(sweep, aged)
	setExpired(sweep, expiredPrev, aged.expired)
	return partial, aged.evict
}

// carriedFromPrev returns the previously held accounts not re-priced this cycle
// that still have stored rows, and whether an ambient hold is unresolved.
func carriedFromPrev(prev *RecommendationsCollectionPartial, repriced, stored map[string]bool) (carried []string, unresolved bool) {
	held := previousAzureHold(prev)
	if held == nil {
		return nil, false
	}
	return stillHeld(held, repriced, stored)
}

// carriedKeySet is carried plus the ambient key when the ambient hold is unresolved.
func carriedKeySet(carried []string, unresolved bool) map[string]bool {
	set := map[string]bool{}
	for _, id := range carried {
		set[id] = true
	}
	if unresolved {
		set[ambientAccountKey] = true
	}
	return set
}

// holdsPricing reports whether sweep records a pricing hold this cycle.
func holdsPricing(sweep *PartialProviderSweep) bool {
	return sweep != nil && (len(sweep.HeldAccountIDs) > 0 || sweep.Reasons[PartialReasonPricingUnavailable] > 0)
}

func (p *PartialProviderSweep) heldSince() map[string]time.Time {
	if p == nil {
		return nil
	}
	return p.HeldSince
}

// holdAging is the context for deciding each held account's age.
type holdAging struct {
	prior       map[string]time.Time
	carried     map[string]bool
	expiredPrev map[string]bool
	now         time.Time
}

// since is when id's hold started: the stored start for a hold carried from the
// previous cycle, this cycle's time otherwise. It never moves backwards, so an
// older-stamped collection that commits last cannot restart the clock.
func (a holdAging) since(id string) time.Time {
	if t, ok := a.prior[id]; ok && a.carried[id] {
		return t
	}
	return a.now
}

type agedHold struct {
	kept         []string
	since        map[string]time.Time
	expired      []string
	evict        []string
	ambientEmpty bool
}

// classifyHeld sorts the sweep's held accounts into kept, newly expired and
// previously expired (stripped). A negative age never expires.
func classifyHeld(sweep *PartialProviderSweep, a holdAging) agedHold {
	out := agedHold{since: map[string]time.Time{}}
	keys := sweep.HeldAccountIDs
	if len(keys) == 0 && sweep.Reasons[PartialReasonPricingUnavailable] > 0 {
		keys = []string{ambientAccountKey}
		out.ambientEmpty = true
	}
	for _, id := range keys {
		since := a.since(id)
		switch {
		case a.expiredPrev[id]:
			out.evict = append(out.evict, id)
		case a.now.Sub(since) >= PricingHoldMaxAge:
			out.expired = append(out.expired, id)
			out.evict = append(out.evict, id)
		default:
			out.kept = append(out.kept, id)
			out.since[id] = since
		}
	}
	return out
}

// applyAging writes the kept holds and their start times back into sweep and
// fixes the incomplete count and pricing reason for the accounts removed.
func applyAging(sweep *PartialProviderSweep, aged agedHold) {
	sweep.IncompleteAccounts = max(0, sweep.IncompleteAccounts-len(aged.evict))
	if len(aged.since) == 0 {
		sweep.HeldSince = nil
	} else {
		sweep.HeldSince = aged.since
	}
	if aged.ambientEmpty {
		// An ambient-only hold keeps an empty id list; its start lives under the nil key.
		if len(aged.kept) == 0 {
			delete(sweep.Reasons, PartialReasonPricingUnavailable)
		}
		return
	}
	sweep.HeldAccountIDs = aged.kept
	if len(aged.kept) == 0 {
		delete(sweep.Reasons, PartialReasonPricingUnavailable)
	}
}

// setExpired records the previously expired and newly expired ids on sweep and
// keeps the reason count equal to their number.
func setExpired(sweep *PartialProviderSweep, prevExpired map[string]bool, newly []string) {
	ids := slices.Concat(slices.Collect(maps.Keys(prevExpired)), newly)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 0 {
		sweep.ExpiredHoldAccountIDs = nil
		delete(sweep.Reasons, PartialReasonPricingHoldExpired)
		return
	}
	sweep.ExpiredHoldAccountIDs = ids
	if sweep.Reasons == nil {
		sweep.Reasons = map[string]int{}
	}
	sweep.Reasons[PartialReasonPricingHoldExpired] = len(ids)
}

// keptExpired returns the previously expired accounts that have not re-priced
// and are still enabled Azure accounts (the ambient key is always active).
func keptExpired(prev *PartialProviderSweep, repriced, active map[string]bool) map[string]bool {
	kept := map[string]bool{}
	if prev == nil {
		return kept
	}
	for _, id := range prev.ExpiredHoldAccountIDs {
		if !repriced[id] && (id == ambientAccountKey || active[id]) {
			kept[id] = true
		}
	}
	return kept
}

// azureSweep returns the Azure entry of partial, or nil.
func azureSweep(partial *RecommendationsCollectionPartial) *PartialProviderSweep {
	if partial == nil {
		return nil
	}
	i := slices.IndexFunc(partial.Providers, func(p PartialProviderSweep) bool { return p.Provider == "azure" })
	if i < 0 {
		return nil
	}
	return &partial.Providers[i]
}

// ensureAzureSweep returns partial and its Azure entry, creating either.
func ensureAzureSweep(partial *RecommendationsCollectionPartial) (*RecommendationsCollectionPartial, *PartialProviderSweep) {
	if partial == nil {
		partial = &RecommendationsCollectionPartial{}
	}
	sweep := azureSweep(partial)
	if sweep == nil {
		partial.Providers = append(partial.Providers, PartialProviderSweep{Provider: "azure", Reasons: map[string]int{}})
		sweep = &partial.Providers[len(partial.Providers)-1]
	}
	if sweep.Reasons == nil {
		sweep.Reasons = map[string]int{}
	}
	return partial, sweep
}

// mergeCarriedHold adds the carried account IDs and the pricing_unavailable
// reason to partial's Azure entry, creating the entry when this cycle had none.
// An entry that already records the pricing reason with no account IDs is this
// cycle's ambient outage; it gains the nil UUID so it survives a named hold.
func mergeCarriedHold(partial *RecommendationsCollectionPartial, carried []string) *RecommendationsCollectionPartial {
	partial, sweep := ensureAzureSweep(partial)
	ownAmbient := false
	if sweep.Reasons[PartialReasonPricingUnavailable] == 0 {
		sweep.Reasons[PartialReasonPricingUnavailable] = 1
	} else if len(sweep.HeldAccountIDs) == 0 {
		carried = append(carried, ambientAccountKey)
		ownAmbient = true
	}
	before := len(sweep.HeldAccountIDs)
	held := slices.Concat(sweep.HeldAccountIDs, carried)
	slices.Sort(held)
	sweep.HeldAccountIDs = slices.Compact(held)
	// Each newly held account adds to the incomplete count; this cycle's own
	// ambient outage was already counted.
	sweep.IncompleteAccounts += len(sweep.HeldAccountIDs) - before
	if ownAmbient {
		sweep.IncompleteAccounts--
	}
	return partial
}

// previousAzureHold returns the Azure entry that records a pricing hold, or nil.
func previousAzureHold(partial *RecommendationsCollectionPartial) *PartialProviderSweep {
	if partial == nil {
		return nil
	}
	for i := range partial.Providers {
		sweep := &partial.Providers[i]
		if sweep.Provider == "azure" && sweep.Reasons[PartialReasonPricingUnavailable] > 0 {
			return sweep
		}
	}
	return nil
}

// stillHeld returns the previously held accounts this cycle did not re-price
// that still have stored Azure rows, and whether an account-less (ambient) hold is still
// unresolved.
func stillHeld(prev *PartialProviderSweep, repriced, stored map[string]bool) (carried []string, unresolved bool) {
	for _, id := range prev.HeldAccountIDs {
		if !repriced[id] && stored[id] {
			carried = append(carried, id)
		}
	}
	return carried, len(prev.HeldAccountIDs) == 0 && !repriced[ambientAccountKey] && stored[ambientAccountKey]
}

// repricedKeys returns the Azure account keys collected successfully this cycle.
func repricedKeys(collects []SuccessfulCollect) map[string]bool {
	repriced := map[string]bool{}
	for _, sc := range collects {
		switch {
		case sc.Provider != "azure":
		case sc.CloudAccountID == nil:
			repriced[ambientAccountKey] = true
		default:
			repriced[*sc.CloudAccountID] = true
		}
	}
	return repriced
}
