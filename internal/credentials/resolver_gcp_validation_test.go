package credentials

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// TestResolveGCPTokenSource_UnsupportedEndpointNotContacted drives the real
// golang.org/x/oauth2 library against a local server and checks that a stored
// configuration with an unsupported token endpoint is rejected without any
// request being made.
func TestResolveGCPTokenSource_UnsupportedEndpointNotContacted(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"x","token_type":"Bearer","expires_in":60}`))
	}))
	defer srv.Close()

	file := filepath.Join(t.TempDir(), "unsupported-path")
	require.NoError(t, os.WriteFile(file, []byte("sample-subject-token"), 0o600))

	payload, err := json.Marshal(map[string]any{
		"type":               "external_account",
		"audience":           "//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/q",
		"subject_token_type": "urn:ietf:params:oauth:token-type:id_token",
		"token_url":          srv.URL,
		"credential_source":  map[string]any{"file": file},
	})
	require.NoError(t, err)

	store := newMockStore()
	store.data["acct1/"+CredTypeGCPWIFConfig] = payload
	acct := &config.CloudAccount{ID: "acct1", GCPAuthMode: "workload_identity_federation"}

	ts, err := ResolveGCPTokenSource(context.Background(), acct, store)
	if err == nil {
		_, err = ts.Token()
	}
	assert.Error(t, err)
	assert.EqualValues(t, 0, atomic.LoadInt32(&hits), "unsupported endpoint must not be contacted")
}
