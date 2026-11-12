package config

import (
	"math/big"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

// LadderInFlightExposure retains the original monetary provenance for one observation.
type LadderInFlightExposure struct {
	Layer          ladder.LayerType
	Amount         string
	AmountBasis    LadderAmountBasis
	CommitmentRate *string
}

type LadderInFlight []LadderInFlightExposure

func (inFlight LadderInFlight) onDemandTotal() (float64, error) {
	total := new(big.Rat)
	for i := range inFlight {
		e := &inFlight[i]
		od, err := nettingOnDemand(e.Amount, &LadderTrancheDB{AmountBasis: &e.AmountBasis, CommitmentRate: e.CommitmentRate})
		if err != nil {
			return 0, err
		}
		total.Add(total, od)
	}
	return nettingFloat(total)
}

// ApplyToAllocationInput adapts validated observations to the currently pinned SDK.
func (inFlight LadderInFlight) ApplyToAllocationInput(in *ladder.AllocationInput) error {
	total, err := inFlight.onDemandTotal()
	if err != nil {
		return err
	}
	in.InFlightUSDPerHour = &total
	return nil
}
