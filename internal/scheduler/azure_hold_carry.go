package scheduler

import (
	"context"
	"fmt"
	"slices"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// carryForwardAzureHold keeps the Azure pricing hold alive for as long as the
// held rows are. UpsertRecommendations resets last_collection_partial on every
// cycle, but an account's Advisor rows stay at their old price until a later
// cycle re-prices them, so the previous hold is merged into this cycle's state:
// accounts re-priced now (collected with no outage skips, hence in collects)
// drop out, newly held ones are added, the rest carry over. That covers
// another outage, an outright Azure failure and a run that skips Azure.
//
// A hold with no account IDs (an unresolved ambient identity) carries until an
// ambient Azure collection succeeds. An error reading the previous state is
// returned: the caller must not upsert, because that would reset the marker.
func (s *Scheduler) carryForwardAzureHold(ctx context.Context, partial *config.RecommendationsCollectionPartial, collects []config.SuccessfulCollect) (*config.RecommendationsCollectionPartial, error) {
	fresh, err := s.config.GetRecommendationsFreshness(ctx)
	if err != nil {
		return nil, fmt.Errorf("read previous pricing hold: %w", err)
	}
	if fresh == nil || fresh.LastCollectionPartial == nil {
		return partial, nil
	}
	prev := previousAzureHold(fresh.LastCollectionPartial)
	if prev == nil {
		return partial, nil
	}
	carried, unresolved := stillHeld(prev, collects)
	if len(carried) == 0 && !unresolved {
		return partial, nil
	}
	return mergeCarriedHold(partial, carried), nil
}

// mergeCarriedHold adds the carried account IDs and the pricing_unavailable
// reason to partial's Azure entry, creating the entry when this cycle had none.
func mergeCarriedHold(partial *config.RecommendationsCollectionPartial, carried []string) *config.RecommendationsCollectionPartial {
	if partial == nil {
		partial = &config.RecommendationsCollectionPartial{}
	}
	idx := slices.IndexFunc(partial.Providers, func(p config.PartialProviderSweep) bool { return p.Provider == "azure" })
	if idx < 0 {
		partial.Providers = append(partial.Providers, config.PartialProviderSweep{Provider: "azure", Reasons: map[string]int{}})
		idx = len(partial.Providers) - 1
	}
	sweep := &partial.Providers[idx]
	if sweep.Reasons == nil {
		sweep.Reasons = map[string]int{}
	}
	if sweep.Reasons[config.PartialReasonPricingUnavailable] == 0 {
		sweep.Reasons[config.PartialReasonPricingUnavailable] = 1
	}
	held := slices.Concat(sweep.HeldAccountIDs, carried)
	slices.Sort(held)
	sweep.HeldAccountIDs = slices.Compact(held)
	sweep.IncompleteAccounts = max(sweep.IncompleteAccounts, len(sweep.HeldAccountIDs))
	return partial
}

// previousAzureHold returns the Azure entry that records a pricing hold, or nil.
func previousAzureHold(partial *config.RecommendationsCollectionPartial) *config.PartialProviderSweep {
	for i := range partial.Providers {
		sweep := &partial.Providers[i]
		if sweep.Provider == "azure" && sweep.Reasons[config.PartialReasonPricingUnavailable] > 0 {
			return sweep
		}
	}
	return nil
}

// stillHeld returns the previously held accounts this cycle did not re-price,
// and whether an account-less (ambient) hold is still unresolved.
func stillHeld(prev *config.PartialProviderSweep, collects []config.SuccessfulCollect) (carried []string, unresolved bool) {
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
		if !repriced[id] {
			carried = append(carried, id)
		}
	}
	return carried, len(prev.HeldAccountIDs) == 0 && !ambientRepriced
}
