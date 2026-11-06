package api

import (
	"context"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// [fail-first] #1050: a direct exchange whose accept response was lost may
// have happened at AWS. The caller gets a 502 naming its own source RIs and
// telling it to check before retrying; the audit row stays unknown, and no
// provider text reaches the response.
func TestExecuteExchange_OutcomeUnknownIs502NamingSourceRIs(t *testing.T) {
	store := newDirectExchangeAuditStore()
	h := awsAuditHandler(t, store, func(context.Context, exchange.ExchangeExecuteRequest) (string, *exchange.ExchangeQuoteSummary, error) {
		return "", nil, fmt.Errorf("%w: accept response lost: RequestError for %s", common.ErrOutcomeUnknown, outcomeUnknownLeak)
	})

	_, err := h.executeExchange(auditRequest(context.Background(), awsAuditBody))
	require.Error(t, err)
	ce, ok := IsClientError(err)
	require.True(t, ok, "the outcome is reported, not a generic 500")
	assert.Equal(t, 502, ce.code)
	assert.Contains(t, ce.message, "ri-0a1b2c3d, ri-4e5f6a7b")
	assert.Contains(t, ce.message, "check them at AWS before retrying")
	assert.NotContains(t, ce.message, outcomeUnknownLeak, "no provider text reaches the caller")

	_, outcome, finalized := store.onlyRow(t)
	require.True(t, finalized)
	assert.Equal(t, config.RIExchangeDirectStatusUnknown, outcome.Status, "a 5xx keeps the audit outcome unknown, never failed")
}
