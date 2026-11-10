package server

import (
	"context"
	"fmt"
	"maps"
	"slices"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

// ladderRates returns the sizing rates, fetched at the plan pair, and the
// conversion base for existing coverage: the lower of the plan-pair and
// global-pair rate per layer. Observed rates still override the base later
// (ladderConversionRates). When the pairs are equal there is one fetch and
// the base is the sizing map. Both fetches run before any write, and a failure
// of either fails the run.
func ladderRates(
	ctx context.Context,
	capability pkgladder.LadderCapability,
	scope pkgladder.Scope,
	plan, global ladderTerms,
	states map[pkgladder.LayerType]pkgladder.LayerState,
) (sizing, conversionBase map[pkgladder.LayerType]float64, err error) {
	rater, ok := capability.(pkgladder.CommitmentRateCapability)
	if !ok {
		return nil, nil, fmt.Errorf("capability %T does not provide commitment rates", capability)
	}
	sizing, err = rater.GetCommitmentRates(ctx, scope, plan.term, plan.payment)
	if err != nil {
		return nil, nil, fmt.Errorf("GetCommitmentRates: %w", err)
	}
	if global == plan {
		return sizing, sizing, nil
	}
	ref, err := rater.GetCommitmentRates(ctx, scope, global.term, global.payment)
	if err != nil {
		return nil, nil, fmt.Errorf("GetCommitmentRates at global default %s %s: %w", global.term, global.payment, err)
	}
	base, err := lowerOfTwoRates(sizing, ref, heldLayers(states))
	if err != nil {
		return nil, nil, err
	}
	return sizing, base, nil
}

// lowerOfTwoRates returns, per layer present in both maps, the lower rate:
// od = commitment / r, so the lower rate credits more coverage and the plan
// under-buys. A layer missing from either map is absent, which holds the run
// only if the layer holds commitment. Rates of held layers are validated here
// because the minimum would otherwise hide a bad one; layers without holdings
// are never converted, so a bad rate there must not fail the run.
func lowerOfTwoRates(
	sizing, ref map[pkgladder.LayerType]float64,
	held map[pkgladder.LayerType]bool,
) (map[pkgladder.LayerType]float64, error) {
	out := make(map[pkgladder.LayerType]float64, len(sizing))
	for _, layer := range slices.Sorted(maps.Keys(sizing)) {
		r, ok := ref[layer]
		if !ok {
			continue
		}
		if held[layer] {
			if err := bothUsable(layer, sizing[layer], r); err != nil {
				return nil, err
			}
		}
		out[layer] = min(sizing[layer], r)
	}
	return out, nil
}

func bothUsable(layer pkgladder.LayerType, sizing, ref float64) error {
	if _, err := commitmentRateRat(sizing); err != nil {
		return fmt.Errorf("layer %s plan-pair rate: %w", layer, err)
	}
	if _, err := commitmentRateRat(ref); err != nil {
		return fmt.Errorf("layer %s global-pair rate: %w", layer, err)
	}
	return nil
}

// heldLayers is the set of layers with existing or expiring commitment.
func heldLayers(states map[pkgladder.LayerType]pkgladder.LayerState) map[pkgladder.LayerType]bool {
	held := make(map[pkgladder.LayerType]bool, len(states))
	for layer, st := range states {
		if holdsCommitment(st) {
			held[layer] = true
		}
	}
	return held
}
