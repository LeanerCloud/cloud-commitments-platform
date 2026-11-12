package api

import (
	"context"
	"testing"

	azurecompute "github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/compute"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// POST /api/ri-exchange/azure-instances/offerings must hand the Azure client the
// sources and targets the caller named: each source keeps the quantity the caller
// asked to exchange (not the full quantity of the owned reservation) with the
// ownership details read from the listing, and each target keeps its sku, location,
// term and quantity with the billing scope derived from the subscription. The other
// offerings tests stub CalculateExchange with mock.Anything.
func TestGetAzureCompatibleOfferings_PassesTheRequestedSourcesAndTargetsToAzure(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", ctx, "tok").Return(&Session{UserID: "user-1"}, nil)
	mockAuth.On("HasPermissionAPI", ctx, "user-1", "view", "purchases").Return(true, nil)
	allowAnyAccountScope(mockAuth)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })

	opsClient := new(mockAzureExchangeOpsClient)
	opsClient.On("ListExchangeableReservations", mock.Anything).Return([]azurecompute.ExchangeableReservation{
		{ReservationID: "res-1", BillingScopeID: "/subscriptions/sub-1", Region: "eastus", Quantity: 10},
		{ReservationID: "res-2", BillingScopeID: "/subscriptions/sub-1", Region: "westeurope", Quantity: 20},
	}, nil)
	var gotSources []azurecompute.ExchangeableReservation
	var gotTargets []azurecompute.ExchangeTarget
	opsClient.On("CalculateExchange", ctx, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		gotSources = args.Get(1).([]azurecompute.ExchangeableReservation)
		gotTargets = args.Get(2).([]azurecompute.ExchangeTarget)
	}).Return(&azurecompute.ExchangePreview{SessionID: "sess-1"}, []azurecompute.CompatibleOffering{}, nil)
	t.Cleanup(func() { opsClient.AssertExpectations(t) })

	h := &Handler{auth: mockAuth, azureExchangeFactory: func(_ string) azureExchangeClient { return opsClient }}
	_, err := h.getAzureCompatibleOfferings(ctx, &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer tok"},
		Body: `{
			"subscription_id": "sub-1",
			"sources": [{"reservation_id": "RES-2", "quantity": 7}, {"reservation_id": "res-1", "quantity": 4}],
			"targets": [{"sku": "Standard_D8s_v3", "location": "westeurope", "term": "P3Y", "quantity": 2},
				{"sku": "Standard_E4s_v3", "location": "eastus", "term": "P1Y", "quantity": 5}]
		}`,
	})
	require.NoError(t, err)

	term3, err := azureReservationTermFromString("P3Y")
	require.NoError(t, err)
	term1, err := azureReservationTermFromString("P1Y")
	require.NoError(t, err)
	assert.Equal(t, []azurecompute.ExchangeableReservation{
		{ReservationID: "res-2", BillingScopeID: "/subscriptions/sub-1", Region: "westeurope", Quantity: 7},
		{ReservationID: "res-1", BillingScopeID: "/subscriptions/sub-1", Region: "eastus", Quantity: 4},
	}, gotSources, "sources keep the request's order and quantities and the listing's details, matched case-insensitively")
	assert.Equal(t, []azurecompute.ExchangeTarget{
		{SKU: "Standard_D8s_v3", Location: "westeurope", Term: term3, Quantity: 2, BillingScopeID: "/subscriptions/sub-1"},
		{SKU: "Standard_E4s_v3", Location: "eastus", Term: term1, Quantity: 5, BillingScopeID: "/subscriptions/sub-1"},
	}, gotTargets, "targets keep their order, sku, location, term and quantity, with the scope derived from the subscription")
}

// The execute endpoint re-quotes the exchange itself before committing, and that
// re-quote must carry the same sources and targets as the request: a source with
// the requested quantity and the listing's details, a target with its sku,
// location, term, quantity and the scope derived from the subscription. The
// expectation is registered before the audit fixture's mock.Anything one, which
// would otherwise match first.
func TestExecuteAzureExchange_ReQuotesTheRequestedSourcesAndTargets(t *testing.T) {
	term3, err := azureReservationTermFromString("P3Y")
	require.NoError(t, err)
	wantSources := []azurecompute.ExchangeableReservation{
		{ReservationID: "res-1", BillingScopeID: "/subscriptions/sub-1", Region: "eastus", Quantity: 4},
	}
	wantTargets := []azurecompute.ExchangeTarget{
		{SKU: "Standard_D8s_v3", Location: "eastus", Term: term3, Quantity: 2, BillingScopeID: "/subscriptions/sub-1"},
	}
	store := newDirectExchangeAuditStore()
	opsClient := new(mockAzureExchangeOpsClient)
	opsClient.On("CalculateExchange", mock.Anything, wantSources, wantTargets).Return(
		&azurecompute.ExchangePreview{SessionID: "sess-quoted", NetPayable: toPtr(-12.5), NetPayableCurrency: "USD"},
		[]azurecompute.CompatibleOffering{}, nil,
	).Once()
	opsClient.On("ExecuteExchange", mock.Anything, "sess-quoted").Return(&azurecompute.ExchangeResult{
		SessionID: "sess-quoted", NetPayable: toPtr(-12.5), NetPayableCurrency: "USD", Status: "Succeeded",
	}, nil).Once()
	h := azureAuditHandler(t, store, opsClient)

	_, err = h.executeAzureExchange(auditRequest(context.Background(), `{
		"subscription_id": "sub-1",
		"sources": [{"reservation_id": "RES-1", "quantity": 4}],
		"targets": [{"sku": "Standard_D8s_v3", "location": "eastus", "term": "P3Y", "quantity": 2}],
		"max_payment_due": "100.00",
		"currency": "USD"
	}`))

	require.NoError(t, err)
}
