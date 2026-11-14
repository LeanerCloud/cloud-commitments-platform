package analytics

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
)

// TestIsSavingsPlanServiceAcceptsEverySpelling pins the Savings Plans slugs the
// purchase_history writers emit (#7): umbrella, hyphenated per-type, and the pre-#85 form.
func TestIsSavingsPlanServiceAcceptsEverySpelling(t *testing.T) {
	planTypes := common.SavingsPlansPlanTypes()
	slugs := make([]string, 0, len(planTypes)+2)
	slugs = append(slugs, "savingsplans", "savings-plans")
	for _, pt := range planTypes {
		slugs = append(slugs, string(pt))
	}
	for _, s := range slugs {
		assert.True(t, isSavingsPlanService(s), s)
		assert.Equal(t, string(common.CommitmentSavingsPlan), commitmentTypeFor("aws", s), s)
	}
	assert.False(t, isSavingsPlanService("savings_plans"))
	assert.False(t, isSavingsPlanService("compute"))
}
