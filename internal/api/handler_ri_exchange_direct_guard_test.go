package api

import (
	"context"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #1829: a direct execute whose source RI is held by another exchange is
// refused with a 409 before the submit claim and before any AWS call.
func TestExecuteExchange_DirectGuard_RefusesWith409BeforeClaimAndAWS(t *testing.T) {
	store := newDirectExchangeAuditStore()
	store.saveErr = fmt.Errorf("%w: source RI(s) ri-0a1b2c3d are held by a direct exchange that may have reached AWS; an operator must verify the outcome at AWS and settle the record", config.ErrRIExchangeSourceInFlight)
	h := awsAuditHandler(t, store, func(context.Context, exchange.ExchangeExecuteRequest) (string, *exchange.ExchangeQuoteSummary, error) {
		t.Fatal("AWS must not be called for a refused direct execute")
		return "", nil, nil
	})

	_, err := h.executeExchange(auditRequest(context.Background(), awsAuditBody))
	ce, ok := IsClientError(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, 409, ce.code)
	assert.Contains(t, ce.message, "ri-0a1b2c3d")
	// The handler passes the store's text through, so the no-blocker-id property
	// belongs to the store; it is asserted there (config guard integration tests).
	assert.Equal(t, []string{"save"}, store.recordedSteps(), "no claim, no execute, no finalize")
	assert.Empty(t, store.claimedKeys())
}

// Any other save failure still refuses the exchange, but as a server error.
func TestExecuteExchange_DirectGuard_OtherSaveErrorsStay5xx(t *testing.T) {
	store := newDirectExchangeAuditStore()
	store.saveErr = fmt.Errorf("connection refused")
	h := awsAuditHandler(t, store, func(context.Context, exchange.ExchangeExecuteRequest) (string, *exchange.ExchangeQuoteSummary, error) {
		t.Fatal("AWS must not be called when the audit row cannot be written")
		return "", nil, nil
	})

	_, err := h.executeExchange(auditRequest(context.Background(), awsAuditBody))
	require.Error(t, err)
	if ce, ok := IsClientError(err); ok {
		assert.GreaterOrEqual(t, ce.code, 500)
	}
	assert.Equal(t, []string{"save"}, store.recordedSteps())
}
