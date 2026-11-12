package purchase

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
)

func TestPurchaseCallError_CurrencyRefusalIsRefusedBeforeSubmission(t *testing.T) {
	err := purchaseCallError(context.Background(), fmt.Errorf("azure: %w", common.ErrCurrencyNotAccepted))

	assert.True(t, refusedBeforeSubmission(err))
	assert.ErrorIs(t, err, common.ErrCurrencyNotAccepted)
	assert.Contains(t, err.Error(), "billing currency")
}

func TestPurchaseCallError_CurrencyRefusalWithOutcomeUnknownStaysUnknown(t *testing.T) {
	in := fmt.Errorf("%w: attempt 2/3 failed: %w", common.ErrOutcomeUnknown, common.ErrCurrencyNotAccepted)
	err := purchaseCallError(context.Background(), in)

	assert.False(t, refusedBeforeSubmission(err))
	assert.ErrorIs(t, err, common.ErrOutcomeUnknown)
}

func TestPurchaseCallError_PlainErrorIsNotRefused(t *testing.T) {
	err := purchaseCallError(context.Background(), errors.New("boom"))

	assert.False(t, refusedBeforeSubmission(err))
	assert.NotErrorIs(t, err, common.ErrOutcomeUnknown)
}

func TestPurchaseCallError_CancelledContextWinsOverCurrencyRefusal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := purchaseCallError(ctx, fmt.Errorf("azure: %w", common.ErrCurrencyNotAccepted))

	assert.ErrorIs(t, err, common.ErrOutcomeUnknown)
	assert.False(t, refusedBeforeSubmission(err))
}
