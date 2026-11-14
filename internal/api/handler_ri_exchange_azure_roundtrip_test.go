package api

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	azurecompute "github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/compute"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// Round-trip tests for issue #1907 (pairs E3 and E4): every Azure exchange
// body field reaches the CalculateExchange / ExecuteExchange client call.

const azureRoundTripSources = `[
	{"reservation_id": "RES-1", "quantity": 4},
	{"reservation_id": "res-2", "quantity": 1}
]`

// The second target omits billing_scope_id (always derived), the first spells
// it out in a different case than the derived scope.
const azureRoundTripTargets = `[
	{"sku": "Standard_D8s_v3", "location": "eastus", "term": "P3Y", "quantity": 2, "billing_scope_id": "/SUBSCRIPTIONS/sub-1"},
	{"sku": "Standard_E4s_v3", "location": "westeurope", "term": "P1Y", "quantity": 7}
]`

func azureRoundTripOwned() []azurecompute.ExchangeableReservation {
	return []azurecompute.ExchangeableReservation{
		{ReservationOrderID: "order-1", ReservationID: "res-1", BillingScopeID: "/subscriptions/sub-1", Region: "eastus", Quantity: 10},
		{ReservationOrderID: "order-2", ReservationID: "res-2", BillingScopeID: "/subscriptions/sub-1", Region: "westeurope", Quantity: 3},
	}
}

// azureRoundTripWantSources: server-listed metadata with the requested
// quantities (4 and 1, not the listed 10 and 3).
func azureRoundTripWantSources() []azurecompute.ExchangeableReservation {
	owned := azureRoundTripOwned()
	owned[0].Quantity, owned[1].Quantity = 4, 1
	return owned
}

func azureRoundTripWantTargets() []azurecompute.ExchangeTarget {
	return []azurecompute.ExchangeTarget{
		{SKU: "Standard_D8s_v3", Location: "eastus", Term: armreservations.ReservationTermP3Y, Quantity: 2, BillingScopeID: "/subscriptions/sub-1"},
		{SKU: "Standard_E4s_v3", Location: "westeurope", Term: armreservations.ReservationTermP1Y, Quantity: 7, BillingScopeID: "/subscriptions/sub-1"},
	}
}

func TestGetAzureCompatibleOfferings_RoundTripEveryField(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", ctx, "tok").Return(&Session{UserID: "user-1"}, nil)
	mockAuth.On("HasPermissionAPI", ctx, "user-1", "view", "purchases").Return(true, nil)
	allowAnyAccountScope(mockAuth)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })

	var gotSub string
	opsClient := new(mockAzureExchangeOpsClient)
	opsClient.On("ListExchangeableReservations", mock.Anything).Return(azureRoundTripOwned(), nil)
	opsClient.On("CalculateExchange", ctx, azureRoundTripWantSources(), azureRoundTripWantTargets()).
		Return(&azurecompute.ExchangePreview{SessionID: "sess-1"}, []azurecompute.CompatibleOffering{}, nil).Once()
	t.Cleanup(func() { opsClient.AssertExpectations(t) })

	h := &Handler{
		auth: mockAuth,
		azureExchangeFactory: func(subscriptionID string) azureExchangeClient {
			gotSub = subscriptionID
			return opsClient
		},
	}
	_, err := h.getAzureCompatibleOfferings(ctx, &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer tok"},
		Body:    `{"subscription_id":"sub-1","sources":` + azureRoundTripSources + `,"targets":` + azureRoundTripTargets + `}`,
	})
	require.NoError(t, err)
	assert.Equal(t, "sub-1", gotSub, "subscription_id must select the client")
}

func TestExecuteAzureExchange_RoundTripEveryField(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newDirectExchangeAuditStore()
	store.GetCloudAccountByExternalIDFn = func(_ context.Context, _, externalID string) (*config.CloudAccount, error) {
		assert.Equal(t, "sub-1", externalID)
		return &config.CloudAccount{ID: auditCloudAccount}, nil
	}
	var gotSub string
	opsClient := new(mockAzureExchangeOpsClient)
	opsClient.On("ListExchangeableReservations", mock.Anything).Return(azureRoundTripOwned(), nil)
	// The server re-quotes with the caller's sources and targets, then
	// executes the session that fresh quote returned.
	opsClient.On("CalculateExchange", mock.Anything, azureRoundTripWantSources(), azureRoundTripWantTargets()).
		Return(&azurecompute.ExchangePreview{SessionID: "sess-quoted", NetPayable: toPtr(50.0), NetPayableCurrency: "USD"},
			[]azurecompute.CompatibleOffering{}, nil).Once()
	opsClient.On("ExecuteExchange", mock.Anything, "sess-quoted").
		Return(&azurecompute.ExchangeResult{SessionID: "sess-quoted", NetPayable: toPtr(50.0), NetPayableCurrency: "USD", Status: "Succeeded"}, nil).Once()
	t.Cleanup(func() { opsClient.AssertExpectations(t) })
	h := &Handler{
		auth:   auditSessionAuth(t),
		config: store,
		azureExchangeFactory: func(subscriptionID string) azureExchangeClient {
			gotSub = subscriptionID
			return opsClient
		},
	}

	body := `{"subscription_id":"sub-1","sources":` + azureRoundTripSources + `,"targets":` + azureRoundTripTargets +
		`,"max_payment_due":"60.00","currency":"USD"}`
	_, err := h.executeAzureExchange(auditRequest(ctx, body))
	require.NoError(t, err)
	assert.Equal(t, "sub-1", gotSub, "subscription_id must select the client")

	// max_payment_due and currency reach the audit row as the cap.
	row, _, _ := store.onlyRow(t)
	assert.Equal(t, "60", row.MaxPaymentDue)
	assert.Equal(t, "USD", row.MaxPaymentCurrency)
}
