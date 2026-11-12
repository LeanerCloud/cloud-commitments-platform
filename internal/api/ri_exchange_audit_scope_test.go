package api

import (
	"context"
	"testing"

	azurecompute "github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/compute"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// A target that spells out its billing scope is recorded as sent in the audit
// row's targets, so the operator's own statement of the scope is on the record.
// The scope must match the one derived from the request's subscription; the audit
// test for the default body has no scope in its target.
func TestExecuteAzureExchange_AuditRecordsASpelledOutBillingScope(t *testing.T) {
	store := newDirectExchangeAuditStore()
	opsClient := new(mockAzureExchangeOpsClient)
	opsClient.On("ExecuteExchange", mock.Anything, "sess-quoted").Return(&azurecompute.ExchangeResult{
		SessionID: "sess-quoted", NetPayable: toPtr(-12.5), NetPayableCurrency: "USD", Status: "Succeeded",
	}, nil).Once()
	h := azureAuditHandler(t, store, opsClient)

	body := `{
		"subscription_id": "sub-1",
		"sources": [{"reservation_id": "res-1", "quantity": 4}],
		"targets": [{"sku": "Standard_D8s_v3", "location": "eastus", "term": "P3Y", "quantity": 2, "billing_scope_id": "/subscriptions/sub-1"}],
		"max_payment_due": "100.00",
		"currency": "USD"
	}`
	_, err := h.executeAzureExchange(auditRequest(context.Background(), body))
	require.NoError(t, err)

	row, _, _ := store.onlyRow(t)
	assert.JSONEq(t, `[{"sku":"Standard_D8s_v3","location":"eastus","term":"P3Y","quantity":2,"billing_scope_id":"/subscriptions/sub-1"}]`, string(row.Targets))
}
