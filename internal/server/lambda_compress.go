package server

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"log"
	"strings"

	"github.com/aws/aws-lambda-go/events"
)

// lambdaCompressMinBytes is the smallest JSON body worth compressing. A var so
// the wiring test can force compression on a tiny response.
var lambdaCompressMinBytes = 64 * 1024

// compressLambdaResponse gzips a large JSON API response for clients that
// accept it. Lambda rejects responses over 6 MB, and JSON lists such as
// GET /api/recommendations grow with the tenant (#723); gzip leaves several
// times that headroom even after the base64 Lambda requires for a binary
// body. Any condition that is not met leaves the response untouched.
func compressLambdaResponse(req *events.LambdaFunctionURLRequest, resp *events.LambdaFunctionURLResponse) {
	if resp == nil || resp.IsBase64Encoded || len(resp.Body) < lambdaCompressMinBytes {
		return
	}
	if !acceptsGzip(req.Headers) || !isJSONResponse(resp.Headers) {
		return
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(resp.Body)); err != nil {
		log.Printf("lambda response gzip failed, sending uncompressed: %v", err)
		return
	}
	if err := zw.Close(); err != nil {
		log.Printf("lambda response gzip failed, sending uncompressed: %v", err)
		return
	}
	resp.Body = base64.StdEncoding.EncodeToString(buf.Bytes())
	resp.IsBase64Encoded = true
	resp.Headers["Content-Encoding"] = "gzip"
	resp.Headers["Vary"] = "Accept-Encoding"
}

func acceptsGzip(headers map[string]string) bool {
	for k, v := range headers {
		if strings.EqualFold(k, "accept-encoding") {
			return strings.Contains(strings.ToLower(v), "gzip")
		}
	}
	return false
}

// isJSONResponse is false when the response already carries a
// Content-Encoding, so a body is never encoded twice.
func isJSONResponse(headers map[string]string) bool {
	json := false
	for k, v := range headers {
		switch strings.ToLower(k) {
		case "content-encoding":
			return false
		case "content-type":
			json = strings.HasPrefix(strings.ToLower(v), "application/json")
		}
	}
	return json
}
