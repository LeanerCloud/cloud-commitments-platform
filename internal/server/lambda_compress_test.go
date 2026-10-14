package server

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/api"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func largeJSONResponse() *events.LambdaFunctionURLResponse {
	return &events.LambdaFunctionURLResponse{
		StatusCode: 200,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       `{"rows":"` + strings.Repeat("recommendation-", 8000) + `"}`,
	}
}

func gzipAccepting() *events.LambdaFunctionURLRequest {
	return &events.LambdaFunctionURLRequest{Headers: map[string]string{"accept-encoding": "gzip, deflate, br"}}
}

func TestCompressLambdaResponse_CompressesLargeJSON(t *testing.T) {
	resp := largeJSONResponse()
	original := resp.Body

	compressLambdaResponse(gzipAccepting(), resp)

	assert.True(t, resp.IsBase64Encoded)
	assert.Equal(t, "gzip", resp.Headers["Content-Encoding"])
	assert.Equal(t, "Accept-Encoding", resp.Headers["Vary"])
	raw, err := base64.StdEncoding.DecodeString(resp.Body)
	require.NoError(t, err)
	assert.Less(t, len(raw), len(original)/10, "repetitive JSON must shrink by far more than the base64 overhead")
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	require.NoError(t, err)
	got, err := io.ReadAll(zr)
	require.NoError(t, err)
	assert.Equal(t, original, string(got))
}

func TestCompressLambdaResponse_LeavesResponseAloneWhen(t *testing.T) {
	tests := []struct {
		name   string
		req    *events.LambdaFunctionURLRequest
		mutate func(*events.LambdaFunctionURLResponse)
	}{
		{"client does not accept gzip", &events.LambdaFunctionURLRequest{Headers: map[string]string{"accept-encoding": "br"}}, nil},
		{"no accept-encoding header", &events.LambdaFunctionURLRequest{}, nil},
		{"body below threshold", gzipAccepting(), func(r *events.LambdaFunctionURLResponse) { r.Body = `{"ok":true}` }},
		{"not JSON", gzipAccepting(), func(r *events.LambdaFunctionURLResponse) { r.Headers["Content-Type"] = "text/html" }},
		{"already encoded", gzipAccepting(), func(r *events.LambdaFunctionURLResponse) { r.Headers["Content-Encoding"] = "br" }},
		{"already base64", gzipAccepting(), func(r *events.LambdaFunctionURLResponse) { r.IsBase64Encoded = true }},
		{"no headers", gzipAccepting(), func(r *events.LambdaFunctionURLResponse) { r.Headers = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := largeJSONResponse()
			if tt.mutate != nil {
				tt.mutate(resp)
			}
			body, b64, enc := resp.Body, resp.IsBase64Encoded, resp.Headers["Content-Encoding"]

			compressLambdaResponse(tt.req, resp)

			assert.Equal(t, body, resp.Body)
			assert.Equal(t, b64, resp.IsBase64Encoded)
			assert.Equal(t, enc, resp.Headers["Content-Encoding"])
		})
	}
}

// The wiring test drives the real handleLambdaHTTPEvent: a small API JSON
// response is compressed only because the threshold is lowered for the test.
func TestHandleLambdaHTTPEvent_CompressesAPIJSONResponse(t *testing.T) {
	old := lambdaCompressMinBytes
	lambdaCompressMinBytes = 1
	t.Cleanup(func() { lambdaCompressMinBytes = old })
	app := &Application{API: api.NewHandler(api.HandlerConfig{})}

	rawEvent, err := json.Marshal(events.LambdaFunctionURLRequest{
		RawPath: "/api/recommendations",
		Headers: map[string]string{"accept-encoding": "gzip"},
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{Method: "GET", Path: "/api/recommendations"},
		},
	})
	require.NoError(t, err)

	resp, err := app.handleLambdaHTTPEvent(t.Context(), rawEvent)

	require.NoError(t, err)
	assert.Equal(t, "gzip", resp.Headers["Content-Encoding"], "status %d body %q", resp.StatusCode, resp.Body)
	assert.True(t, resp.IsBase64Encoded)
}
