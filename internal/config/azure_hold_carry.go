package config

import (
	"slices"
)

// carryAzureHold keeps the Azure pricing hold alive for as long as the held
// rows are. Every upsert replaces last_collection_partial, but an account's
// Advisor rows stay at their old price until a later cycle re-prices them, so
// the previous hold (prev, read under the state row lock) is merged into this
// cycle's partial: accounts re-priced now (in collects) drop out, newly held
// ones are added, the rest carry over. That covers another outage, an outright
// Azure failure and a run that skips Azure.
//
// A held account that is no longer an enabled registered Azure account (enabled
// holds its IDs) is dropped, since it will never be collected again. A hold
// with no account IDs (an unresolved ambient identity) carries until an
// ambient Azure collection succeeds or registered Azure accounts exist, after
// which the ambient identity is no longer collected.
func carryAzureHold(prev, partial *RecommendationsCollectionPartial, collects []SuccessfulCollect, enabled map[string]bool) *RecommendationsCollectionPartial {
	held := previousAzureHold(prev)
	if held == nil {
		return partial
	}
	carried, unresolved := stillHeld(held, collects, enabled)
	if len(carried) == 0 && !unresolved {
		return partial
	}
	return mergeCarriedHold(partial, carried)
}

// mergeCarriedHold adds the carried account IDs and the pricing_unavailable
// reason to partial's Azure entry, creating the entry when this cycle had none.
func mergeCarriedHold(partial *RecommendationsCollectionPartial, carried []string) *RecommendationsCollectionPartial {
	if partial == nil {
		partial = &RecommendationsCollectionPartial{}
	}
	idx := slices.IndexFunc(partial.Providers, func(p PartialProviderSweep) bool { return p.Provider == "azure" })
	if idx < 0 {
		partial.Providers = append(partial.Providers, PartialProviderSweep{Provider: "azure", Reasons: map[string]int{}})
		idx = len(partial.Providers) - 1
	}
	sweep := &partial.Providers[idx]
	if sweep.Reasons == nil {
		sweep.Reasons = map[string]int{}
	}
	if sweep.Reasons[PartialReasonPricingUnavailable] == 0 {
		sweep.Reasons[PartialReasonPricingUnavailable] = 1
	}
	held := slices.Concat(sweep.HeldAccountIDs, carried)
	slices.Sort(held)
	sweep.HeldAccountIDs = slices.Compact(held)
	sweep.IncompleteAccounts = max(sweep.IncompleteAccounts, len(sweep.HeldAccountIDs))
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

// stillHeld returns the previously held accounts this cycle did not re-price,
// that are still enabled, and whether an account-less (ambient) hold is still
// unresolved.
func stillHeld(prev *PartialProviderSweep, collects []SuccessfulCollect, enabled map[string]bool) (carried []string, unresolved bool) {
	repriced := map[string]bool{}
	ambientRepriced := false
	for _, sc := range collects {
		switch {
		case sc.Provider != "azure":
		case sc.CloudAccountID == nil:
			ambientRepriced = true
		default:
			repriced[*sc.CloudAccountID] = true
		}
	}
	for _, id := range prev.HeldAccountIDs {
		if !repriced[id] && enabled[id] {
			carried = append(carried, id)
		}
	}
	return carried, len(prev.HeldAccountIDs) == 0 && !ambientRepriced && len(enabled) == 0
}
