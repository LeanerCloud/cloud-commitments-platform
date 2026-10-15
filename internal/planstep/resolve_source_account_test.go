package planstep

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/require"
)

func TestResolveRefusesSourceAccountAmbiguity(t *testing.T) {
	t.Parallel()
	for _, source := range []*string{new("source-b"), nil} {
		first := azureRec(10, 100)
		first.SourceAccountID = new("source-a")
		second := first
		second.ID, second.SourceAccountID, second.UpfrontCost = "source-b", source, 200
		result, err := resolveStep1(t, fakeSource{shown: []config.RecommendationRecord{first}},
			fakeStore{rows: []config.RecommendationRecord{first, second}}, azurePlan(100, 100), accts)
		require.ErrorContains(t, err, "more than one row for identity")
		require.Empty(t, result)
	}
}
