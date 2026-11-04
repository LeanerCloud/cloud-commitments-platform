package server

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The source IP keys the per-IP rate limits on login, setup-admin and
// password reset. A port-bearing key would put every TCP connection in a
// fresh bucket, so those limits would never bind (issue #104).
func TestRequestSourceIP(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		want       string
	}{
		{name: "no XFF: IPv4 peer port is dropped", remoteAddr: "203.0.113.7:51234", want: "203.0.113.7"},
		{name: "no XFF: same client on another connection shares the key", remoteAddr: "203.0.113.7:40001", want: "203.0.113.7"},
		{name: "no XFF: IPv6 peer port and brackets are dropped", remoteAddr: "[2001:db8::1]:443", want: "2001:db8::1"},
		{name: "no XFF: peer without a port is kept", remoteAddr: "203.0.113.7", want: "203.0.113.7"},
		{name: "XFF: rightmost entry wins", remoteAddr: "10.0.0.5:9000", xff: "198.51.100.1, 203.0.113.9", want: "203.0.113.9"},
		{name: "XFF: rightmost entry with a port is reduced to the address", remoteAddr: "10.0.0.5:9000", xff: "198.51.100.1, 203.0.113.9:6123", want: "203.0.113.9"},
		{name: "XFF: bare IPv6 rightmost entry is kept", remoteAddr: "10.0.0.5:9000", xff: "2001:db8::2", want: "2001:db8::2"},
		{name: "XFF: bracketed IPv6 rightmost entry with a port is reduced", remoteAddr: "10.0.0.5:9000", xff: "[2001:db8::2]:6123", want: "2001:db8::2"},
		{name: "XFF: unparseable rightmost entry falls back to the peer address", remoteAddr: "10.0.0.5:9000", xff: "198.51.100.1, not-an-ip", want: "10.0.0.5"},
		{name: "XFF: empty rightmost entry falls back to the peer address", remoteAddr: "10.0.0.5:9000", xff: "198.51.100.1, ", want: "10.0.0.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/auth/login", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			assert.Equal(t, tc.want, requestSourceIP(req, 1))
		})
	}
}

// httpToLambdaRequest must take its SourceIP from requestSourceIP. This reads
// the source rather than calling httpToLambdaRequest so it does not depend on
// that function's signature (other in-flight changes alter it).
func TestHTTPToLambdaRequest_UsesRequestSourceIP(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "http.go", nil, 0)
	require.NoError(t, err)
	var found, calls bool
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "httpToLambdaRequest" {
			return true
		}
		found = true
		ast.Inspect(fn.Body, func(m ast.Node) bool {
			if c, ok := m.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "requestSourceIP" {
					calls = true
				}
			}
			return true
		})
		return false
	})
	require.True(t, found, "httpToLambdaRequest not found in http.go")
	assert.True(t, calls, "httpToLambdaRequest must derive SourceIP via requestSourceIP")
}

func lambdaEvent(peer, xff string) *events.LambdaFunctionURLRequest {
	req := &events.LambdaFunctionURLRequest{Headers: map[string]string{}}
	req.RequestContext.HTTP.SourceIP = peer
	if xff != "" {
		req.Headers["x-forwarded-for"] = xff
	}
	return req
}

// Behind CloudFront (hops=1) the viewer, not the shared edge address, is the
// rate-limit identity; a client-supplied left entry is ignored.
func TestLambdaSourceIP_CDNMode(t *testing.T) {
	const edge = "130.176.1.1"
	cases := []struct {
		name, xff, want string
	}{
		{"viewer appended by CloudFront", "203.0.113.9", "203.0.113.9"},
		{"spoofed left entry is ignored", "198.51.100.66, 203.0.113.9", "203.0.113.9"},
		{"Lambda also appended the edge address", "198.51.100.66, 203.0.113.9, " + edge, "203.0.113.9"},
		{"IPv6 viewer", "2001:db8::2", "2001:db8::2"},
		{"empty header falls back to the peer", "", edge},
		{"malformed rightmost entry falls back to the peer", "198.51.100.66, not-an-ip", edge},
		{"only the peer in the header falls back to the peer", edge, edge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, lambdaSourceIP(lambdaEvent(edge, tc.xff), 1))
		})
	}

	a := lambdaSourceIP(lambdaEvent(edge, "203.0.113.9"), 1)
	b := lambdaSourceIP(lambdaEvent(edge, "203.0.113.10"), 1)
	assert.NotEqual(t, a, b, "two viewers behind the same edge must get different keys")
}

// Without a CDN (hops=0) a direct caller controls X-Forwarded-For, so it must
// have no effect on the key.
func TestLambdaSourceIP_DirectModeIgnoresXFF(t *testing.T) {
	const peer = "203.0.113.7"
	for _, xff := range []string{"", "198.51.100.66", "198.51.100.66, 192.0.2.1", "not-an-ip"} {
		assert.Equal(t, peer, lambdaSourceIP(lambdaEvent(peer, xff), 0), "xff=%q", xff)
	}
}

func TestLambdaSourceIP_HeaderNameIsCaseInsensitive(t *testing.T) {
	req := lambdaEvent("130.176.1.1", "")
	req.Headers["X-Forwarded-For"] = "203.0.113.9"
	assert.Equal(t, "203.0.113.9", lambdaSourceIP(req, 1))
}

// handleLambdaHTTPEvent must route SourceIP through lambdaSourceIP; the unit
// tests above cannot notice the call being dropped.
func TestHandleLambdaHTTPEvent_UsesLambdaSourceIP(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "lambda.go", nil, 0)
	require.NoError(t, err)
	var found, calls bool
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "handleLambdaHTTPEvent" {
			return true
		}
		found = true
		ast.Inspect(fn.Body, func(m ast.Node) bool {
			if c, ok := m.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "lambdaSourceIP" {
					calls = true
				}
			}
			return true
		})
		return false
	})
	require.True(t, found, "handleLambdaHTTPEvent not found in lambda.go")
	assert.True(t, calls, "handleLambdaHTTPEvent must derive SourceIP via lambdaSourceIP")
}
