package purchase

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLadderInstrument_FamilyBoundBuysOneRecordForTheWholeTranche(t *testing.T) {
	// Stored recs naming three other families and the tranche family must not
	// split or multiply the amount: one record, floored tranche amount.
	in := ec2Input(1.234567, storedEC2SP("c5", 3), storedEC2SP("r5", 1), storedEC2SP("m5", 1))
	in.Tranche.Family = "m5"
	assert.Equal(t, []ec2Part{{"m5", 1.23456}}, buildEC2(t, in))

	in = ec2Input(0.5, storedEC2SP("c5", 3), storedEC2SP("r5", 1))
	in.Tranche.Family = "m6i"
	assert.Equal(t, []ec2Part{{"m6i", 0.5}}, buildEC2(t, in), "stored recs naming only other families")

	in = ec2Input(0.5)
	in.Tranche.Family = "m6i"
	assert.Equal(t, []ec2Part{{"m6i", 0.5}}, buildEC2(t, in), "no stored recs is not errNoEC2SPFamily")
}

func TestLadderInstrument_FamilyBoundNeverExceedsTheTrancheOverManyRecFamilies(t *testing.T) {
	in := ec2Input(2.0, storedEC2SP("a", 1), storedEC2SP("b", 5), storedEC2SP("c", 2), storedEC2SP("d", 9))
	in.Tranche.Family = "a"
	parts := buildEC2(t, in)
	require.Len(t, parts, 1)
	assert.Equal(t, ec2Part{"a", 2.0}, parts[0])
}

func TestLadderInstrument_FamilyOnOtherLayersAndMalformedFamiliesFailLoud(t *testing.T) {
	reg := NewLadderInstrumentRegistry()
	for _, layer := range []ladder.LayerType{ladder.LayerComputeSP, ladder.LayerConvertibleRI} {
		in := ec2Input(1.0, storedEC2SP("m5", 1))
		in.Tranche.LayerType = layer
		in.Tranche.Family = "m5"
		_, err := reg.Build(in)
		require.Error(t, err, layer)
		assert.Contains(t, err.Error(), "only valid on layer")
	}
	for _, bad := range []string{" m5", "m5 ", "m 5", "m5\n", "m5\t", "m5\x00", " "} {
		in := ec2Input(1.0, storedEC2SP("m5", 1))
		in.Tranche.Family = bad
		_, err := reg.Build(in)
		require.Error(t, err, "family %q", bad)
		assert.Contains(t, err.Error(), "whitespace or control")
	}
}

func TestLadderInstrument_FamilyBoundBelowMinimumIsAnError(t *testing.T) {
	in := ec2Input(0.0005, storedEC2SP("m5", 1), storedEC2SP("c5", 1))
	in.Tranche.Family = "m5"
	_, err := NewLadderInstrumentRegistry().Build(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hourly commitment must be between")
}

func TestLadderInstrument_EmptyFamilyStillSplitsByWeight(t *testing.T) {
	in := ec2Input(1.0, storedEC2SP("m5", 3), storedEC2SP("c5", 1))
	assert.Equal(t, []ec2Part{{"c5", 0.25}, {"m5", 0.75}}, buildEC2(t, in))
}
