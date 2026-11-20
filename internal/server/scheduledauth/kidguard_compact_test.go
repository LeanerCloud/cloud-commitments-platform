package scheduledauth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

type countingKeySet struct{ calls int }

func (c *countingKeySet) VerifySignature(context.Context, string) ([]byte, error) {
	c.calls++
	return []byte("{}"), nil
}

// The guard only accepts a compact JWS: a JSON-serialized token (which go-oidc
// would still parse) or a malformed one is refused before it reaches the keyset.
func TestGuardedKeySet_RejectsNonCompactJWS(t *testing.T) {
	inner := &countingKeySet{}
	g := newKidGuard(&http.Client{Transport: http.DefaultTransport}, time.Now, []jose.SignatureAlgorithm{jose.RS256})
	ks := &guardedKeySet{inner: inner, g: g}

	flattened := `{"protected":"eyJhbGciOiJSUzI1NiJ9","payload":"e30","signature":"AAAA"}`
	for name, tok := range map[string]string{"json serialization": flattened, "garbage": "a.b"} {
		if _, err := ks.VerifySignature(context.Background(), tok); err == nil || !strings.Contains(err.Error(), "malformed jwt") {
			t.Errorf("%s: want a malformed jwt error, got %v", name, err)
		}
	}
	if inner.calls != 0 {
		t.Errorf("the keyset was reached %d times, want 0", inner.calls)
	}
}
