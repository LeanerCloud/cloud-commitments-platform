package purchase

import (
	"context"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/execution"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func aggregateOne(t *testing.T, rec config.RecommendationRecord, out recPurchaseOutcome) config.RecommendationRecord {
	t.Helper()
	exec := &config.PurchaseExecution{ExecutionID: "exec-1", Recommendations: []config.RecommendationRecord{rec}}
	m := &Manager{}
	m.aggregatePurchaseOutcomes(context.Background(), exec, &config.PurchasePlan{}, "111111111111",
		types.OfferingClassTypeStandard, []execution.Result[recPurchaseOutcome]{{Value: out}})
	return exec.Recommendations[0]
}

// A failed purchase that still carries a commitment ID (the provider bought a
// node it could not record, go#107) keeps the ID, and stays Purchased=false.
func TestAggregatePurchaseOutcomes_FailedWithCommitmentIDKeepsPurchaseID(t *testing.T) {
	got := aggregateOne(t, config.RecommendationRecord{Service: "redshift", ResourceType: "ra3.xlplus"},
		recPurchaseOutcome{
			purchase: common.PurchaseResult{CommitmentID: "rn-new"},
			err:      errors.New("purchase failed: redshift reserved node rn-new was bought but not recorded"),
		})
	assert.Equal(t, "rn-new", got.PurchaseID)
	assert.False(t, got.Purchased)
	assert.NotEmpty(t, got.Error)
}

// A Retry successor copies the predecessor's rec verbatim; when it fails
// without an ID the predecessor's ID is still true (the node exists).
func TestAggregatePurchaseOutcomes_RetrySuccessorFailingWithoutIDKeepsPredecessorID(t *testing.T) {
	got := aggregateOne(t, config.RecommendationRecord{Service: "redshift", PurchaseID: "rn-pred"},
		recPurchaseOutcome{err: errors.New("purchase failed: boom")})
	assert.Equal(t, "rn-pred", got.PurchaseID)
	assert.False(t, got.Purchased)
}

func TestAggregatePurchaseOutcomes_FailedWithoutIDLeavesPurchaseIDEmpty(t *testing.T) {
	got := aggregateOne(t, config.RecommendationRecord{Service: "redshift"},
		recPurchaseOutcome{err: errors.New("purchase failed: boom")})
	require.Empty(t, got.PurchaseID)
	assert.False(t, got.Purchased)
}
