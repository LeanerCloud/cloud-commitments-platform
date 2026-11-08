//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One AWS RI keeps the second execute's idempotency key apart from the first
// execute's, so the refusal and the later success come from the in-flight
// guard alone.
const directResolveSecondBody = `{
	"ri_ids": ["ri-0a1b2c3d"],
	"targets": [{"offering_id": "` + auditOfferingA + `", "count": 1}],
	"max_payment_due_usd": "500.00",
	"region": "eu-central-1"
}`

func directResolveHistoryRecord(ri string) *config.RIExchangeRecord {
	return &config.RIExchangeRecord{AccountID: guardAcct, Region: "eu-central-1", SourceRIIDs: []string{ri}, SourceInstanceType: "m5.large",
		SourceCount: 1, TargetOfferingID: "o", TargetInstanceType: "m6i.large", TargetCount: 1, PaymentDue: "10.00", Status: "processing", Mode: "auto"}
}

func listDirectRows(t *testing.T, h *Handler) riExchangeDirectListResponse {
	t.Helper()
	res, err := h.listRIExchangeDirectExecutions(auditListRequest())
	require.NoError(t, err)
	// Round-trip through JSON, as a client sees it.
	raw, err := json.Marshal(res)
	require.NoError(t, err)
	var out riExchangeDirectListResponse
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func auditListRequest() (context.Context, *events.LambdaFunctionURLRequest) {
	return context.Background(), &events.LambdaFunctionURLRequest{Headers: map[string]string{"authorization": "Bearer tok"}}
}

// #1836 end to end against a real database: a lost accept leaves an unknown
// row, the guard refuses the same RI, the operator lists and resolves the row
// through the HTTP handlers, and every path then proceeds.
func TestDirectExchangeResolve_EndToEnd(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RequireMigratedDatabase(ctx, t, getMigrationsPath())
	store := config.NewPostgresStore(container.DB)
	lostAccept := true
	h, calls := directGuardHandler(t, store, func(context.Context, exchange.ExchangeExecuteRequest) (string, *exchange.ExchangeQuoteSummary, error) {
		if lostAccept {
			return "", nil, common.ErrOutcomeUnknown
		}
		return "riex-1", &exchange.ExchangeQuoteSummary{PaymentDueUSDStr: "40.250000", PaymentDueRaw: "40.25", CurrencyCode: "USD"}, nil
	})

	// 1. The accept outcome is lost.
	_, err := h.executeExchange(auditRequest(ctx, awsAuditBody))
	ce, ok := IsClientError(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, 502, ce.code)
	require.Equal(t, 1, *calls)

	// 2. The same RI is refused, with no further AWS call.
	_, err = h.executeExchange(auditRequest(ctx, directResolveSecondBody))
	ce, ok = IsClientError(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, 409, ce.code)
	assert.Equal(t, 1, *calls, "a refused execute must not call AWS")

	// 3. The list shows the row, with no raw error.
	rows := listDirectRows(t, h).Executions
	require.Len(t, rows, 1)
	row := rows[0]
	assert.Equal(t, config.RIExchangeDirectStatusUnknown, row.Status)
	assert.Equal(t, []string{"ri-0a1b2c3d", "ri-4e5f6a7b"}, row.SourceIDs)

	// 4. Resolve through the handler. The row's times are set relative to the
	// database clock, and the operator's check is one minute and a bit after
	// completion, read back from the database so no Go clock is involved.
	_, err = container.DB.Exec(ctx, `UPDATE ri_exchange_direct_executions
		SET created_at = now() - interval '2 hours', completed_at = now() - interval '1 hour' WHERE id = $1`, row.ID)
	require.NoError(t, err)
	var checkedAt time.Time
	require.NoError(t, container.DB.QueryRow(ctx,
		`SELECT completed_at + interval '2 minutes' FROM ri_exchange_direct_executions WHERE id = $1`, row.ID).Scan(&checkedAt))
	body := fmt.Sprintf(`{"outcome":"failed","expected_status":"unknown","reason":"checked at AWS, no exchange happened",
		"acknowledge_unknown_outcome":true,"cloud_check":{"checked_region":"eu-central-1","checked_at":%q}}`, checkedAt.UTC().Format(time.RFC3339))
	res, err := h.resolveRIExchangeDirectExecution(ctx, &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer tok"}, Body: body}, row.ID)
	require.NoError(t, err)
	resolved, ok := res.(riExchangeDirectResolveResponse)
	require.True(t, ok)
	assert.Equal(t, config.RIExchangeDirectStatusFailed, resolved.ResolvedStatus)
	assert.Equal(t, config.RIExchangeDirectStatusUnknown, resolved.Status, "the original status is kept")
	assert.False(t, resolved.Replayed)

	// 5. The list no longer shows the row.
	assert.Empty(t, listDirectRows(t, h).Executions)

	// 6. A direct execute of the RI proceeds, and so do AUTO and approve
	// reservations of the formerly held RIs.
	lostAccept = false
	_, err = h.executeExchange(auditRequest(ctx, directResolveSecondBody))
	require.NoError(t, err, "a direct execute of a resolved RI proceeds")
	assert.Equal(t, 2, *calls)

	_, err = store.ReserveRIExchange(ctx, directResolveHistoryRecord("ri-0a1b2c3d"), "1000", "1000")
	require.NoError(t, err, "AUTO reservation of a resolved RI")
	approving := directResolveHistoryRecord("ri-4e5f6a7b")
	require.NoError(t, store.SaveRIExchangeRecord(ctx, approving))
	_, err = store.ReserveApprovedRIExchange(ctx, approving.ID, "5000", "1000")
	require.NoError(t, err, "approval of a resolved RI")
}
