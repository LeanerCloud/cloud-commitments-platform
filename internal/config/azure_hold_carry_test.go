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
	stored := map[string]bool{"a": true, "b": true, "c": true}
	ambient := map[string]bool{ambientAccountKey: true}
	for name, tc := range map[string]struct {
		prev     *RecommendationsCollectionPartial
		collects []SuccessfulCollect
		partial  *RecommendationsCollectionPartial
		stored   map[string]bool
		want     []string
		wantHold bool
	}{
		"no previous hold":                   {nil, nil, nil, stored, nil, false},
		"nothing re-priced":                  {holdOf("a", "b"), nil, nil, stored, []string{"a", "b"}, true},
		"one re-priced":                      {holdOf("a", "b"), azureCollect(&a), nil, stored, []string{"b"}, true},
		"both re-priced":                     {holdOf("a", "b"), append(azureCollect(&a), azureCollect(&b)...), nil, stored, nil, false},
		"other provider ignored":             {holdOf("a"), []SuccessfulCollect{{Provider: "gcp", CloudAccountID: &a}}, nil, stored, []string{"a"}, true},
		"new hold is merged":                 {holdOf("a", "b"), nil, holdOf("c"), stored, []string{"a", "b", "c"}, true},
		"re-held account dedupes":            {holdOf("a", "b"), nil, holdOf("a"), stored, []string{"a", "b"}, true},
		"account without rows drops":         {holdOf("a", "b"), nil, nil, map[string]bool{"b": true}, []string{"b"}, true},
		"all rows gone drops":                {holdOf("a"), nil, nil, map[string]bool{"c": true}, nil, false},
		"ambient carries while rows exist":   {holdOf(), nil, nil, ambient, nil, true},
		"ambient survives account rows too":  {holdOf(), azureCollect(&c), nil, map[string]bool{ambientAccountKey: true, "c": true}, nil, true},
		"ambient re-priced":                  {holdOf(), azureCollect(nil), nil, ambient, nil, false},
		"ambient drops with no ambient rows": {holdOf(), nil, nil, stored, nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			got := carryAzureHold(tc.prev, tc.partial, tc.collects, tc.stored)
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
