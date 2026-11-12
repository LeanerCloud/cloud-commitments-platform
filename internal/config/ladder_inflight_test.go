package config

import (
	"math"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/stretchr/testify/require"
)

func TestLadderInFlight_CompleteTotalAndProvenance(t *testing.T) {
	t.Parallel()
	rate := "0.500"
	rows := LadderInFlight{
		{Layer: ladder.LayerEC2InstanceSP, Amount: "0.10", AmountBasis: LadderAmountCommitment, CommitmentRate: &rate},
		{Layer: ladder.LayerConvertibleRI, Amount: "0.10", AmountBasis: LadderAmountOnDemand},
	}
	input := &ladder.AllocationInput{}
	require.NoError(t, rows.ApplyToAllocationInput(input))
	require.Equal(t, math.Nextafter(0.3, math.Inf(1)), *input.InFlightUSDPerHour)
	require.Equal(t, "0.10", rows[0].Amount)
	require.Equal(t, "0.500", *rows[0].CommitmentRate)
	tenths := make(LadderInFlight, 10)
	for i := range tenths {
		tenths[i] = LadderInFlightExposure{Amount: "0.1", AmountBasis: LadderAmountOnDemand}
	}
	require.NoError(t, tenths.ApplyToAllocationInput(input))
	require.Equal(t, 1.0, *input.InFlightUSDPerHour)
	for _, empty := range []LadderInFlight{{}, {{Amount: "0", AmountBasis: LadderAmountOnDemand}}} {
		require.NoError(t, empty.ApplyToAllocationInput(input))
		require.Zero(t, *input.InFlightUSDPerHour)
	}
}

func TestLadderInFlight_InvalidNeverReplacesInput(t *testing.T) {
	t.Parallel()
	bad, tiny := "bad", "1e-400"
	for _, row := range []LadderInFlightExposure{
		{Amount: "0"},
		{Amount: "1", AmountBasis: LadderAmountCommitment},
		{Amount: "1", AmountBasis: LadderAmountCommitment, CommitmentRate: &bad},
		{Amount: "-1", AmountBasis: LadderAmountOnDemand},
		{Amount: "NaN", AmountBasis: LadderAmountOnDemand},
		{Amount: "1", AmountBasis: LadderAmountCommitment, CommitmentRate: &tiny},
	} {
		previous := 123.0
		input := &ladder.AllocationInput{InFlightUSDPerHour: &previous}
		require.Error(t, (LadderInFlight{row}).ApplyToAllocationInput(input))
		require.Same(t, &previous, input.InFlightUSDPerHour)
	}
}
