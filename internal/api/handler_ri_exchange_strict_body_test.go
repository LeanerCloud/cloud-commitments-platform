package api

import (
	"context"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The money-path bodies refuse a key they do not have, so a mistyped optional
// field fails loudly instead of being ignored: the frontend sends exactly the
// documented keys for each (checked against the request types and the call sites).
// The purchase execute body is the exception and stays permissive, see below.

func withUnexpectedKey(body string) string {
	i := strings.LastIndex(body, "}")
	return body[:i] + `,"unexpected_key":1` + body[i:]
}

func requireUnknownKeyRefusal(t *testing.T, err error) {
	t.Helper()
	ce, ok := IsClientError(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, 400, ce.code)
	assert.Contains(t, ce.message, `unknown field "unexpected_key"`)
}

func TestDecodeStrictBody(t *testing.T) {
	type body struct {
		Name string `json:"name"`
	}
	t.Run("accepts the known keys", func(t *testing.T) {
		var b body
		require.NoError(t, decodeStrictBody(`{"name":"x"}`, &b))
		assert.Equal(t, "x", b.Name)
	})
	t.Run("names an unknown key", func(t *testing.T) {
		var b body
		requireUnknownKeyRefusal(t, decodeStrictBody(`{"name":"x","unexpected_key":1}`, &b))
	})
	for name, raw := range map[string]string{
		"malformed":        `{invalid json}`,
		"two documents":    `{"name":"x"}{"name":"y"}`,
		"trailing garbage": `{"name":"x"} nope`,
		"empty":            ``,
	} {
		t.Run(name+" is a plain invalid body", func(t *testing.T) {
			var b body
			err := decodeStrictBody(raw, &b)
			ce, ok := IsClientError(err)
			require.True(t, ok, "got %v", err)
			assert.Equal(t, 400, ce.code)
			assert.Equal(t, "invalid request body", ce.message)
		})
	}
}

func TestUpdateRIExchangeConfig_RefusesAnUnknownKey(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })
	mockAuth.On("ValidateSession", ctx, "admin-token").Return(&Session{UserID: "uid"}, nil)
	mockAuth.grantAdmin()
	h := &Handler{auth: mockAuth}

	_, err := h.updateRIExchangeConfig(ctx, &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"Authorization": "Bearer admin-token"},
		Body:    `{"mode":"manual","utilization_threshold":50,"lookback_days":7,"unexpected_key":1}`,
	})

	requireUnknownKeyRefusal(t, err)
}

func TestGetExchangeQuote_RefusesAnUnknownKey(t *testing.T) {
	h := &Handler{auth: &mockAuthForExchange{}}

	_, err := h.getExchangeQuote(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer test-token"},
		Body:    `{"ri_ids":["ri-123"],"target_offering_id":"4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91","target_count":1,"unexpected_key":1}`,
	})

	requireUnknownKeyRefusal(t, err)
}

func TestExecuteExchange_RefusesAnUnknownKeyBeforeAnyWrite(t *testing.T) {
	store := newDirectExchangeAuditStore()
	h := awsAuditHandler(t, store, func(context.Context, exchange.ExchangeExecuteRequest) (string, *exchange.ExchangeQuoteSummary, error) {
		t.Fatal("AWS must not be called for a refused body")
		return "", nil, nil
	})

	_, err := h.executeExchange(auditRequest(context.Background(), withUnexpectedKey(awsAuditBody)))

	requireUnknownKeyRefusal(t, err)
	assert.Empty(t, store.recordedSteps(), "a refused body must not save an audit row")
}

func TestGetAzureCompatibleOfferings_RefusesAnUnknownKey(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", ctx, "tok").Return(&Session{UserID: "user-1"}, nil)
	mockAuth.On("HasPermissionAPI", ctx, "user-1", "view", "purchases").Return(true, nil)
	allowAnyAccountScope(mockAuth)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })
	h := &Handler{auth: mockAuth}

	_, err := h.getAzureCompatibleOfferings(ctx, &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"authorization": "Bearer tok"},
		Body:    withUnexpectedKey(validAzureOfferingsBody),
	})

	requireUnknownKeyRefusal(t, err)
}

func TestExecuteAzureExchange_RefusesAnUnknownKeyBeforeAnyWrite(t *testing.T) {
	store := newDirectExchangeAuditStore()
	h := azureAuditHandler(t, store, new(mockAzureExchangeOpsClient))

	_, err := h.executeAzureExchange(auditRequest(context.Background(), withUnexpectedKey(azureAuditBody)))

	requireUnknownKeyRefusal(t, err)
	assert.Empty(t, store.recordedSteps(), "a refused body must not save an audit row")
}

// The purchase execute body stays permissive: the UI spreads whole server-provided
// recommendation rows into it and the handler re-prices them from the stored set,
// taking only count, recommended_count and selected from the request, so an unknown
// key cannot change what is bought.
func TestParseExecutePurchaseBody_IgnoresUnknownKeys(t *testing.T) {
	req, err := parseExecutePurchaseBody(`{"recommendations":[{"provider":"aws","service":"ec2","count":1,"unexpected_row_key":1}],"unexpected_key":1}`)

	require.NoError(t, err)
	require.Len(t, req.Recommendations, 1)
	assert.Equal(t, "aws", req.Recommendations[0].Provider)
}
