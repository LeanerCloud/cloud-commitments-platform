package purchase

import (
	"os"
	"regexp"
	"sort"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spSlugForm is the closed set of Savings Plans spellings (#7): the umbrella and
// each per-plan-type slug, hyphenated or dash-free, nothing else. A third form
// such as "savings_plans" must fail here, not silently miss rows.
var spSlugForm = regexp.MustCompile(`^savings-?plans(-(compute|ec2instance|sagemaker|database))?$`)

func TestSavingsPlansSlugsAreOnlyTheTwoSpellings(t *testing.T) {
	for key := range savingsPlansSlugs {
		assert.Regexp(t, spSlugForm, key, "savingsPlansSlugs holds a third Savings Plans spelling %q", key)
	}
	assert.Equal(t, "savingsplans", string(common.ServiceSavingsPlansAll))
}

// TestFrontendSavingsPlansSlugsMatchBackend reads the frontend's canonical plan
// service ids and requires its per-plan-type Savings Plans slugs to be exactly
// the library's, each one accepted by the purchase manager.
func TestFrontendSavingsPlansSlugsMatchBackend(t *testing.T) {
	src, err := os.ReadFile("../../frontend/src/lib/plan-service-ids.ts")
	require.NoError(t, err)
	matches := regexp.MustCompile(`['"](savings-?plans-[a-z0-9]+)['"]`).FindAllStringSubmatch(string(src), -1)
	planTypes := common.SavingsPlansPlanTypes()
	got := make([]string, 0, len(matches))
	for _, m := range matches {
		got = append(got, m[1])
	}
	want := make([]string, 0, len(planTypes))
	for _, pt := range planTypes {
		want = append(want, string(pt))
	}
	sort.Strings(got)
	sort.Strings(want)
	assert.Equal(t, want, got, "frontend plan-service-ids.ts Savings Plans slugs drifted from common.SavingsPlansPlanTypes()")
}
