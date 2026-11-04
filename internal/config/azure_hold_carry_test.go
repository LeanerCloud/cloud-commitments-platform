package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	enabled := map[string]bool{"a": true, "b": true, "c": true}
	for name, tc := range map[string]struct {
		prev     *RecommendationsCollectionPartial
		collects []SuccessfulCollect
		partial  *RecommendationsCollectionPartial
		enabled  map[string]bool
		want     []string
		wantHold bool
	}{
		"no previous hold":               {nil, nil, nil, enabled, nil, false},
		"nothing re-priced":              {holdOf("a", "b"), nil, nil, enabled, []string{"a", "b"}, true},
		"one re-priced":                  {holdOf("a", "b"), azureCollect(&a), nil, enabled, []string{"b"}, true},
		"both re-priced":                 {holdOf("a", "b"), append(azureCollect(&a), azureCollect(&b)...), nil, enabled, nil, false},
		"other provider ignored":         {holdOf("a"), []SuccessfulCollect{{Provider: "gcp", CloudAccountID: &a}}, nil, enabled, []string{"a"}, true},
		"new hold is merged":             {holdOf("a", "b"), nil, holdOf("c"), enabled, []string{"a", "b", "c"}, true},
		"re-held account dedupes":        {holdOf("a", "b"), nil, holdOf("a"), enabled, []string{"a", "b"}, true},
		"deleted account drops":          {holdOf("a", "b"), nil, nil, map[string]bool{"b": true}, []string{"b"}, true},
		"disabled account drops":         {holdOf("a"), nil, nil, map[string]bool{"c": true}, nil, false},
		"ambient carries":                {holdOf(), nil, nil, nil, nil, true},
		"ambient carries a fail":         {holdOf(), azureCollect(&c), nil, nil, nil, true},
		"ambient re-priced":              {holdOf(), azureCollect(nil), nil, nil, nil, false},
		"ambient clears once registered": {holdOf(), nil, nil, enabled, nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			got := carryAzureHold(tc.prev, tc.partial, tc.collects, tc.enabled)
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
