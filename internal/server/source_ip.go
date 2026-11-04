package server

import (
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/aws/aws-lambda-go/events"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/clientip"
)

// warnXFFIgnored makes the hops=0 warning fire once per process.
var warnXFFIgnored sync.Once

// requestSourceIP returns the client address the API rate limits key on.
//
// trustedHops is the number of proxies that append to X-Forwarded-For before
// the app. The entry that many places from the right is the client; the
// leftmost entries are client-controlled and trivially spoofable. With 0 hops,
// no header, too few entries or an entry that is not an IP address, the TCP
// peer is used. Either way a port is dropped: net/http's RemoteAddr is always
// "ip:port", and some proxies append "ip:port" to X-Forwarded-For, so keying
// on the raw value would give every connection its own bucket and the
// credential-endpoint limits would never bind (issue #104).
func requestSourceIP(r *http.Request, trustedHops int) string {
	// All header lines count as one list: a client can send its own line and
	// a proxy may append a separate one instead of concatenating.
	xff := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	if xff != "" && trustedHops == 0 {
		warnXFFIgnored.Do(func() {
			log.Printf("WARN: X-Forwarded-For received but CUDLY_TRUSTED_PROXY_HOPS=0; using the peer address. Set it to the number of appending proxies in front of the app.")
		})
	}
	return clientip.Resolve(r.RemoteAddr, xff, trustedHops)
}

// lambdaSourceIP returns the client address for a Function URL event.
//
// Behind CloudFront (CUDLY_TRUSTED_PROXY_HOPS=1, set only when enable_cdn is
// on) sourceIp is a CloudFront edge address shared by many users, so keying
// the per-IP limits on it would let one user lock out everyone at that edge.
// CloudFront appends the viewer address to X-Forwarded-For, so the rightmost
// entry is the viewer ("Request and response behavior for custom origins",
// section "Client IP addresses", in the CloudFront developer guide). The
// Lambda "Invoking function URLs" page defines sourceIp as the immediate TCP
// peer and does not say whether Lambda appends that peer to X-Forwarded-For,
// so trailing entries equal to the peer are dropped first: the result is the
// viewer whether or not Lambda appended the edge address. With 0 hops the
// header is ignored, so a direct caller of a public Function URL cannot
// spoof it.
func lambdaSourceIP(req *events.LambdaFunctionURLRequest, trustedHops int) string {
	peer := req.RequestContext.HTTP.SourceIP
	var xff string
	for k, v := range req.Headers {
		if strings.EqualFold(k, "X-Forwarded-For") {
			xff = v
			break
		}
	}
	if trustedHops <= 0 {
		if xff != "" {
			warnXFFIgnored.Do(func() {
				log.Printf("WARN: X-Forwarded-For received but CUDLY_TRUSTED_PROXY_HOPS=0; using the peer address. Set it to the number of appending proxies in front of the app.")
			})
		}
		return peer
	}
	normalizedPeer := clientip.Resolve(peer, "", 0)
	entries := strings.Split(xff, ",")
	for len(entries) > 0 && clientip.Resolve(strings.TrimSpace(entries[len(entries)-1]), "", 0) == normalizedPeer {
		entries = entries[:len(entries)-1]
	}
	return clientip.Resolve(peer, strings.Join(entries, ","), trustedHops)
}
