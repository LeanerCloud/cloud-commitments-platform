package purchase

import (
	"context"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// A provider's currency refusal is a plain purchase failure whose reason names
// the cause (go#640); the sentinel stays in the chain so callers can match it.
func TestExecuteSinglePurchase_CurrencyRefusalSurfacesInFailureReason(t *testing.T) {
	refusal := fmt.Errorf("calculatePrice bills in EUR but the expected currency is USD; refusing to purchase: %w", common.ErrCurrencyNotAccepted)
	sc := new(MockServiceClient)
	sc.On("PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything).
		Return(common.PurchaseResult{}, refusal)

	_, err := managerServing(sc).executeSinglePurchase(context.Background(), zonalRecRecord(t, "us-east-1b"), nil, common.PurchaseOptions{ExecutionID: "exec-cur"})

	require.Error(t, err)
	assert.ErrorIs(t, err, common.ErrCurrencyNotAccepted)
	assert.Contains(t, err.Error(), "bills in EUR")
	assert.Contains(t, err.Error(), "currency not accepted")
}
