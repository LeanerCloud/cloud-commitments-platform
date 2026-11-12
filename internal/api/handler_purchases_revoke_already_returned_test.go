package api

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Issue #1584: after the sweep releases an uncertain Return's flag, a new
// revoke can get Azure's "already returned" answer to its only send. That
// proves the reservation is returned, so the purchase is marked revoked instead
// of cleared and left showing active. Stub Azure clients and a mock store.

func TestCallAzureReturn_AlreadyReturned_MarksRevoked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	r := armReservationRecord()
	mockStore.On("MarkRevocationReturnPosted", mock.Anything, r.PurchaseID).Return(nil).Once()
	mockStore.On("MarkPurchaseRevoked", mock.Anything, r.PurchaseID, mock.AnythingOfType("time.Time"),
		"direct-api", "", mock.Anything, mock.Anything).Return(nil).Once()

	calcClient := &stubCalcRefundClientWithAmount{amount: 10.0, currency: "USD", sessID: "s-already"}
	returnClient := &stubReturnClient{err: &azcore.ResponseError{StatusCode: 409, ErrorCode: azureAlreadyReturnedCode}}
	h := &Handler{config: mockStore}

	result, err := h.callAzureReturn(ctx, calcClient, returnClient, r, "order-abc", "res-xyz", confirmedQuote(10, "USD"))
	require.NoError(t, err)
	rr, ok := result.(*revokePurchaseResult)
	require.True(t, ok, "got %T", result)
	assert.Equal(t, "revoked", rr.Status)
	mockStore.AssertNotCalled(t, "ClearRevocationInFlight")
	assert.Equal(t, 1, returnClient.calls)
}

// Only the exact code on a definite refusal is read as "already returned":
// another code, or the same code after a retry (attempts > 1 is not a
// definite refusal), keeps its old handling and never marks the purchase.
func TestCallAzureReturn_AlreadyReturnedLookalikes_DoNotMarkRevoked(t *testing.T) {
	t.Parallel()
	cases := map[string]*azcore.ResponseError{
		"other 4xx code":  {StatusCode: 409, ErrorCode: "ReservationNotFound"},
		"no code":         {StatusCode: 409},
		"code on a 5xx":   {StatusCode: 500, ErrorCode: azureAlreadyReturnedCode},
		"prefix of code":  {StatusCode: 400, ErrorCode: azureAlreadyReturnedCode + "Extra"},
		"lower-case code": {StatusCode: 400, ErrorCode: "reservationalreadyreturned"},
	}
	for name, respErr := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mockStore := new(MockConfigStore)
			mockStore.On("ClearRevocationInFlight", mock.Anything, mock.Anything).Return(nil).Maybe()
			r := armReservationRecord()
			h := &Handler{config: mockStore}
			calcClient := &stubCalcRefundClientWithAmount{amount: 10.0, currency: "USD", sessID: "s-look"}

			result, _ := h.callAzureReturn(context.Background(), calcClient, &stubReturnClient{err: respErr}, r, "order-abc", "res-xyz", confirmedQuote(10, "USD"))
			_, revoked := result.(*revokePurchaseResult)
			assert.False(t, revoked)
			mockStore.AssertNotCalled(t, "MarkPurchaseRevoked")
			mockStore.AssertNotCalled(t, "MarkRevocationReturnPosted")
		})
	}
}

func TestIsAzureAlreadyReturned_RequiresSingleSend(t *testing.T) {
	t.Parallel()
	err := &azcore.ResponseError{StatusCode: 409, ErrorCode: azureAlreadyReturnedCode}
	assert.True(t, isAzureAlreadyReturned(err, 1))
	assert.True(t, isAzureAlreadyReturned(err, 0))
	assert.False(t, isAzureAlreadyReturned(err, 2), "after a retry the answer may be to our own accepted try; that stays uncertain")
	assert.False(t, isAzureAlreadyReturned(context.Canceled, 1))
}
