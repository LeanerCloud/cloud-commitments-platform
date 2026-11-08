package purchase

import (
	"context"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// unprovenPredecessorStore is a strict store whose only answer is "predecessor
// not proven unsent"; any other call panics.
func unprovenPredecessorStore(t *testing.T) *MockConfigStore {
	t.Helper()
	store := new(MockConfigStore)
	store.On("PredecessorProvenUnsent", mock.Anything, mock.Anything).Return(false, nil).Maybe()
	return store
}

func armedRefusalText(t *testing.T, m *Manager, exec *config.PurchaseExecution) string {
	t.Helper()
	err := m.armedRedriveRefusal(context.Background(), exec)
	require.Error(t, err)
	return err.Error()
}

// runArmedRetry claims and executes a pending retry successor and returns the
// provider call count and the last saved row.
func runArmedRetry(t *testing.T, exec *config.PurchaseExecution, svcType common.ServiceType, proof predecessorProof) (int, config.PurchaseExecution, *MockConfigStore) {
	t.Helper()
	mgr, store, rec := armedHarnessWithProof(t, svcType, proof)
	var last config.PurchaseExecution
	store.SavePurchaseExecutionFn = func(_ context.Context, e *config.PurchaseExecution) error {
		last = *e
		return nil
	}
	if exec.Recommendations[0].Provider == "azure" {
		// The stale-pricing check runs before every Azure dispatch; serve the row's own price.
		exec.Recommendations[0].ID = "current-azure"
		store.On("ListStoredRecommendations", mock.Anything, config.RecommendationFilter{Provider: "azure", RequireAzurePricingMigration: true}).
			Return(exec.Recommendations, nil).Maybe()
	}
	expectClaim(store, exec, []string{"approved", "pending", "notified"})
	_, _ = mgr.claimAndExecuteWithin(context.Background(), exec, 0)
	return rec.count(), last, store
}

func TestArmedRetryOfProvenUnsentFirstAttemptExecutes(t *testing.T) {
	cases := []struct {
		name, provider, service string
		svcType                 common.ServiceType
	}{
		{"aws ec2", "aws", "ec2", common.ServiceEC2},
		{"azure savingsplans", "azure", "savingsplans", common.ServiceSavingsPlansAll},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := armedExecution(tc.provider, tc.service, 1, "notified")
			calls, last, _ := runArmedRetry(t, exec, tc.svcType, predecessorProof{proven: true})
			assert.Equal(t, 1, calls, "a retry of a proven-unsent first attempt buys exactly once")
			assert.NotContains(t, last.Error, "refusing to execute")
		})
	}
}

func TestArmedRetryRefusedWithoutProof(t *testing.T) {
	cases := []struct {
		name, provider, service string
		svcType                 common.ServiceType
		attempt                 int
		proven                  bool
		lookupErr               error
		wantText                string
		wantLookups             int
	}{
		{"ec2 unproven", "aws", "ec2", common.ServiceEC2, 1, false, nil, "Check EC2 Reserved Instances", 1},
		{"azure sp unproven", "azure", "savingsplans", common.ServiceSavingsPlansAll, 1, false, nil, "second savings plan", 1},
		{"ec2 attempt 2 even if proven", "aws", "ec2", common.ServiceEC2, 2, true, nil, "Check EC2 Reserved Instances", 0},
		{"azure sp attempt 2 even if proven", "azure", "savingsplans", common.ServiceSavingsPlansAll, 2, true, nil, "second savings plan", 0},
		{"ec2 lookup error", "aws", "ec2", common.ServiceEC2, 1, true, errors.New("db down"), "start a new purchase", 1},
		{"azure sp lookup error", "azure", "savingsplans", common.ServiceSavingsPlansAll, 1, true, errors.New("db down"), "start a new purchase", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := armedExecution(tc.provider, tc.service, tc.attempt, "notified")
			calls, last, store := runArmedRetry(t, exec, tc.svcType, predecessorProof{proven: tc.proven, err: tc.lookupErr})
			assert.Equal(t, 0, calls, "a refused retry must make zero provider calls")
			assert.Equal(t, "failed", last.Status)
			assert.False(t, last.NoSubmission, "an armed refusal must not stamp no_submission")
			assert.Contains(t, last.Error, tc.wantText)
			assert.NotContains(t, last.Error, "db down", "the stored error must not carry the DB error")
			if tc.wantLookups == 0 {
				store.AssertNotCalled(t, "PredecessorProvenUnsent", mock.Anything, mock.Anything)
			}
		})
	}
}

// Attempt-0 rows and retries with no refusal never consult the store.
func TestArmedRedriveRefusalDoesNotLookUpUnlessNeeded(t *testing.T) {
	cases := []struct {
		name, provider, service string
		svcType                 common.ServiceType
		attempt                 int
	}{
		{"ec2 attempt 0", "aws", "ec2", common.ServiceEC2, 0},
		{"azure sp attempt 0", "azure", "savingsplans", common.ServiceSavingsPlansAll, 0},
		{"rds attempt 1", "aws", "rds", common.ServiceRDS, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := armedExecution(tc.provider, tc.service, tc.attempt, "notified")
			calls, _, store := runArmedRetry(t, exec, tc.svcType, predecessorProof{})
			assert.Equal(t, 1, calls)
			store.AssertNotCalled(t, "PredecessorProvenUnsent", mock.Anything, mock.Anything)
		})
	}
}
