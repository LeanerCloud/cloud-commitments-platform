package api

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// executeExchangeCaptured runs the handler with body and returns the request it
// handed to the exchange library, the real callee of POST /api/ri-exchange/execute.
func executeExchangeCaptured(t *testing.T, body string) exchange.ExchangeExecuteRequest {
	t.Helper()
	var got exchange.ExchangeExecuteRequest
	h := awsAuditHandler(t, newDirectExchangeAuditStore(), func(_ context.Context, req exchange.ExchangeExecuteRequest) (string, *exchange.ExchangeQuoteSummary, error) {
		got = req
		return "riex-0f1e2d3c", &exchange.ExchangeQuoteSummary{PaymentDueUSDStr: "40.250000", PaymentDueRaw: "40.25", CurrencyCode: "USD"}, nil
	})
	_, err := h.executeExchange(auditRequest(context.Background(), body))
	require.NoError(t, err)
	return got
}

// Every field of ExchangeExecuteRequestBody must reach the library request:
// a dropped field would change what AWS exchanges or the cap it is bound by.
func TestExecuteExchange_EveryBodyFieldReachesTheLibraryRequest(t *testing.T) {
	t.Parallel()
	t.Run("targets array", func(t *testing.T) {
		got := executeExchangeCaptured(t, awsAuditBody)

		assert.Equal(t, "eu-central-1", got.Region)
		assert.Equal(t, []string{"ri-0a1b2c3d", "ri-4e5f6a7b"}, got.ReservedIDs)
		assert.Equal(t, []exchange.TargetConfig{
			{OfferingID: auditOfferingA, Count: 2},
			{OfferingID: auditOfferingB, Count: 1},
		}, got.Targets)
		require.NotNil(t, got.MaxPaymentDueUSD)
		assert.Equal(t, "500.00", got.MaxPaymentDueUSD.FloatString(2))
		assert.Empty(t, got.TargetOfferingID)
		assert.Zero(t, got.TargetCount)
	})

	t.Run("legacy singleton target", func(t *testing.T) {
		got := executeExchangeCaptured(t, `{
			"ri_ids": ["ri-0a1b2c3d"],
			"target_offering_id": "`+auditOfferingA+`",
			"target_count": 3,
			"max_payment_due_usd": "75.50",
			"region": "us-west-2"
		}`)

		assert.Equal(t, "us-west-2", got.Region)
		assert.Equal(t, []string{"ri-0a1b2c3d"}, got.ReservedIDs)
		assert.Nil(t, got.Targets, "the legacy fields are forwarded as-is, not folded into targets")
		assert.Equal(t, auditOfferingA, got.TargetOfferingID)
		assert.Equal(t, int32(3), got.TargetCount)
		require.NotNil(t, got.MaxPaymentDueUSD)
		assert.Equal(t, "75.50", got.MaxPaymentDueUSD.FloatString(2))
	})
}

// quoteCaptured runs getExchangeQuote with body and returns the request it handed
// to the exchange library.
func quoteCaptured(t *testing.T, body string) exchange.ExchangeQuoteRequest {
	t.Helper()
	var got exchange.ExchangeQuoteRequest
	h := &Handler{
		auth: &mockAuthForExchange{},
		exchangeQuoteFn: func(_ context.Context, req exchange.ExchangeQuoteRequest) (*exchange.ExchangeQuoteSummary, error) {
			got = req
			return &exchange.ExchangeQuoteSummary{IsValidExchange: true}, nil
		},
	}
	h.awsCfgOnce.Do(func() { h.awsCfg = aws.Config{Region: "us-east-1"} })
	_, err := h.getExchangeQuote(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
		Body:    body,
	})
	require.NoError(t, err)
	return got
}

// Every field of ExchangeQuoteRequestBody must reach the library request, so the
// quote prices the exchange the caller asked for.
func TestGetExchangeQuote_EveryBodyFieldReachesTheLibraryRequest(t *testing.T) {
	t.Parallel()
	t.Run("targets array", func(t *testing.T) {
		got := quoteCaptured(t, `{
			"ri_ids": ["ri-0a1b2c3d", "ri-4e5f6a7b"],
			"targets": [{"offering_id": "`+auditOfferingA+`", "count": 2}, {"offering_id": "`+auditOfferingB+`", "count": 1}],
			"region": "eu-central-1"
		}`)

		assert.Equal(t, "eu-central-1", got.Region)
		assert.Equal(t, []string{"ri-0a1b2c3d", "ri-4e5f6a7b"}, got.ReservedIDs)
		assert.Equal(t, []exchange.TargetConfig{
			{OfferingID: auditOfferingA, Count: 2},
			{OfferingID: auditOfferingB, Count: 1},
		}, got.Targets)
		assert.Empty(t, got.TargetOfferingID)
		assert.Zero(t, got.TargetCount)
	})

	t.Run("legacy singleton target", func(t *testing.T) {
		got := quoteCaptured(t, `{
			"ri_ids": ["ri-0a1b2c3d"],
			"target_offering_id": "`+auditOfferingA+`",
			"target_count": 3
		}`)

		assert.Equal(t, []string{"ri-0a1b2c3d"}, got.ReservedIDs)
		assert.Nil(t, got.Targets, "the legacy fields are forwarded as-is, not folded into targets")
		assert.Equal(t, auditOfferingA, got.TargetOfferingID)
		assert.Equal(t, int32(3), got.TargetCount)
	})
}
