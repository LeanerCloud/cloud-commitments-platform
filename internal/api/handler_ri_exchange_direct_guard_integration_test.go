//go:build integration

package api

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const guardAcct = "210987654321"

// #1829 against a real database: the issue's repro and its reverse.

func directGuardHandler(t *testing.T, store *config.PostgresStore, executor awsExecutor) (*Handler, *int) {
	t.Helper()
	calls := new(int)
	return &Handler{
		auth:                   auditSessionAuth(t),
		config:                 store,
		reshapeAccountResolver: func(context.Context) (string, error) { return auditCloudAccount, nil },
		executeExchangeFn: func(ctx context.Context, req exchange.ExchangeExecuteRequest) (string, *exchange.ExchangeQuoteSummary, error) {
			*calls++
			return executor(ctx, req)
		},
	}, calls
}

func countRows(ctx context.Context, t *testing.T, c *testhelpers.PostgresContainer, table string) int {
	t.Helper()
	var n int
	require.NoError(t, c.DB.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n))
	return n
}

// Repro: a processing history row holds ri-0a1b2c3d, so the direct execute is
// refused with a 409 and leaves no audit row, no claim and no AWS call.
func TestExecuteExchange_DirectGuard_Repro(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RequireMigratedDatabase(ctx, t, getMigrationsPath())
	store := config.NewPostgresStore(container.DB)
	require.NoError(t, store.SaveRIExchangeRecord(ctx, &config.RIExchangeRecord{
		AccountID: guardAcct, Region: "eu-central-1", SourceRIIDs: []string{"ri-0a1b2c3d"}, SourceInstanceType: "m5.large",
		SourceCount: 1, TargetOfferingID: "o", TargetInstanceType: "m6i.large", TargetCount: 1, PaymentDue: "10.00", Status: "processing", Mode: "auto",
	}))
	h, calls := directGuardHandler(t, store, func(context.Context, exchange.ExchangeExecuteRequest) (string, *exchange.ExchangeQuoteSummary, error) {
		return "riex-1", nil, nil
	})

	_, err := h.executeExchange(auditRequest(ctx, awsAuditBody))
	ce, ok := IsClientError(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, 409, ce.code)
	assert.Contains(t, ce.message, "ri-0a1b2c3d")
	assert.Zero(t, *calls, "AWS must not be called")
	assert.Zero(t, countRows(ctx, t, container, "ri_exchange_direct_executions"))
	assert.Zero(t, countRows(ctx, t, container, "ri_exchange_idempotency"))
}

// Reverse: a direct execute whose accept response is lost leaves an unknown
// row, which then holds its RIs against AUTO and approval reservations.
func TestExecuteExchange_DirectGuard_Reverse(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RequireMigratedDatabase(ctx, t, getMigrationsPath())
	store := config.NewPostgresStore(container.DB)
	h, calls := directGuardHandler(t, store, func(context.Context, exchange.ExchangeExecuteRequest) (string, *exchange.ExchangeQuoteSummary, error) {
		return "", nil, common.ErrOutcomeUnknown
	})

	_, err := h.executeExchange(auditRequest(ctx, awsAuditBody))
	ce, ok := IsClientError(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, 502, ce.code)
	assert.Equal(t, 1, *calls)
	var status string
	require.NoError(t, container.DB.QueryRow(ctx, "SELECT status FROM ri_exchange_direct_executions").Scan(&status))
	assert.Equal(t, config.RIExchangeDirectStatusUnknown, status)

	record := func(ri string) *config.RIExchangeRecord {
		return &config.RIExchangeRecord{AccountID: guardAcct, Region: "eu-central-1", SourceRIIDs: []string{ri}, SourceInstanceType: "m5.large",
			SourceCount: 1, TargetOfferingID: "o", TargetInstanceType: "m6i.large", TargetCount: 1, PaymentDue: "10.00", Status: "processing", Mode: "auto"}
	}
	_, err = store.ReserveRIExchange(ctx, record("ri-0a1b2c3d"), "1000", "1000")
	require.ErrorIs(t, err, config.ErrRIExchangeSourceInFlight, "AUTO reservation of the held RI")
	_, err = store.ReserveRIExchange(ctx, record("ri-other"), "1000", "1000")
	require.NoError(t, err, "a different RI still reserves")

	approving := record("ri-0a1b2c3d")
	require.NoError(t, store.SaveRIExchangeRecord(ctx, approving))
	_, err = store.ReserveApprovedRIExchange(ctx, approving.ID, "5000", "1000")
	require.ErrorIs(t, err, config.ErrRIExchangeSourceInFlight, "approval of the held RI")
}
