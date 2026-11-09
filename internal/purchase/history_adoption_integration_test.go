//go:build integration
// +build integration

package purchase

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// adoptedHistoryManager returns a manager on a real migrated store and a saver
// for one adopted history row.
func adoptedHistoryManager(ctx context.Context, t *testing.T) (*Manager, *config.PostgresStore) {
	t.Helper()
	store, _ := rampStepStore(ctx, t)
	return &Manager{config: store}, store
}

func saveAdoptedRow(ctx context.Context, t *testing.T, m *Manager, provider, service string, result common.PurchaseResult) {
	t.Helper()
	result.Success = true
	err := m.savePurchaseHistory(ctx,
		&config.PurchaseExecution{ExecutionID: "exec-" + result.CommitmentID}, &config.PurchasePlan{Name: "plan"},
		config.RecommendationRecord{Provider: provider, Service: service, ResourceType: "m5.large", Region: "us-east-1", Count: 1, Term: 1},
		result, "111111111111", ec2types.OfferingClassTypeConvertible)
	require.NoError(t, err)
}

func TestAdoptedOfferingClassPersistsInPostgres(t *testing.T) {
	ctx := context.Background()
	m, store := adoptedHistoryManager(ctx, t)

	saveAdoptedRow(ctx, t, m, "aws", "ec2",
		common.PurchaseResult{CommitmentID: "ri-std", ExistingCommitment: true, OfferingClass: "standard"})
	row, err := store.GetPurchaseHistoryByPurchaseID(ctx, "ri-std")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, "standard", row.OfferingClass)

	saveAdoptedRow(ctx, t, m, "aws", "ec2",
		common.PurchaseResult{CommitmentID: "ri-unknown", ExistingCommitment: true})
	row, err = store.GetPurchaseHistoryByPurchaseID(ctx, "ri-unknown")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Empty(t, row.OfferingClass, "an adopted RI with no reported class must read back NULL")

	require.NoError(t, store.StampOfferingClass(ctx, "ri-unknown", "standard"))
	row, err = store.GetPurchaseHistoryByPurchaseID(ctx, "ri-unknown")
	require.NoError(t, err)
	require.Equal(t, "standard", row.OfferingClass, "the lazy marketplace stamp must work on the NULL row")

	// First writer wins: a later save for the same purchase never rewrites the class.
	saveAdoptedRow(ctx, t, m, "aws", "ec2",
		common.PurchaseResult{CommitmentID: "ri-std", ExistingCommitment: true, OfferingClass: "convertible"})
	row, err = store.GetPurchaseHistoryByPurchaseID(ctx, "ri-std")
	require.NoError(t, err)
	require.Equal(t, "standard", row.OfferingClass)
}

func TestAdoptedGCPUpfrontPersistsAsZeroInPostgres(t *testing.T) {
	ctx := context.Background()
	m, store := adoptedHistoryManager(ctx, t)

	saveAdoptedRow(ctx, t, m, "gcp", "compute",
		common.PurchaseResult{CommitmentID: "cud-adopted", ExistingCommitment: true})
	saveAdoptedRow(ctx, t, m, "aws", "ec2",
		common.PurchaseResult{CommitmentID: "ri-adopted", ExistingCommitment: true})

	active, err := store.GetActivePurchaseHistory(ctx, time.Now(), nil, nil)
	require.NoError(t, err)
	upfront := map[string]*float64{}
	for _, r := range active {
		upfront[r.PurchaseID] = r.UpfrontCost
	}
	require.Contains(t, upfront, "cud-adopted")
	require.NotNil(t, upfront["cud-adopted"])
	require.Zero(t, *upfront["cud-adopted"])
	require.Contains(t, upfront, "ri-adopted")
	require.Nil(t, upfront["ri-adopted"], "an adopted AWS RI's original upfront stays unknown")
}
