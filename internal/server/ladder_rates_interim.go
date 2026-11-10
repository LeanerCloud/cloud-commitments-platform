package server

import (
	"context"
	"fmt"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

// ladderRates fetches the sizing rates at the plan pair. The global pair and
// layer states are not used yet: the next change adds the lower-of-two
// conversion base and replaces this file.
func ladderRates(
	ctx context.Context,
	capability pkgladder.LadderCapability,
	scope pkgladder.Scope,
	plan, _ ladderTerms,
	_ map[pkgladder.LayerType]pkgladder.LayerState,
) (sizing, conversionBase map[pkgladder.LayerType]float64, err error) {
	rater, ok := capability.(pkgladder.CommitmentRateCapability)
	if !ok {
		return nil, nil, fmt.Errorf("capability %T does not provide commitment rates", capability)
	}
	sizing, err = rater.GetCommitmentRates(ctx, scope, plan.term, plan.payment)
	if err != nil {
		return nil, nil, fmt.Errorf("GetCommitmentRates: %w", err)
	}
	return sizing, sizing, nil
}
