package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/api"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/mocks"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const diagnosticsRawKey = "synthetic-diagnostic-request-key"     // #nosec G101 -- synthetic fixture
const diagnosticsRawToken = "synthetic-diagnostic-session-token" // #nosec G101 -- synthetic fixture
const diagnosticsKeyID = "synthetic-private-key-row"
const diagnosticsPlanID = "11111111-1111-1111-1111-111111111111"

type diagnosticsCase struct {
	name, key, bearer, scope, usage string
	status, validations, deletions  int
	multi, retained                 bool
	diagnostic, marker              string
}

func diagnosticsCases() []diagnosticsCase {
	return []diagnosticsCase{
		{name: "ordinary-key", key: "valid", status: 200, validations: 2},
		{name: "ordinary-session", bearer: "valid", status: 200},
		{name: "mixed-key-preferred", key: "key-denied", bearer: "valid", status: 403, validations: 2},
		{name: "mixed-session-retained", key: "valid", bearer: "valid", status: 200, validations: 1, retained: true},
		{name: "initial-error-bearer-fallback", key: "error", bearer: "valid", status: 200, validations: 1, diagnostic: "User API key validation failed", marker: "initial-error-marker"},
		{name: "initial-error-denied", key: "error", status: 401, validations: 1, diagnostic: "User API key validation failed", marker: "initial-error-marker"},
		{name: "invalid-key-bearer-fallback", key: "unknown", bearer: "valid", status: 200, validations: 1},
		{name: "invalid-key-denied", key: "unknown", status: 401, validations: 1},
		{name: "missing-credentials", status: 401},
		{name: "malformed-bearer", bearer: "malformed", status: 401},
		{name: "invalid-bearer", bearer: "invalid", status: 401},
		{name: "mismatched-bearer-hash", bearer: "mismatch", status: 401},
		{name: "inactive-session-owner", bearer: "inactive", status: 401},
		{name: "inactive-key", key: "inactive", status: 401, validations: 1},
		{name: "expired-key", key: "expired", status: 401, validations: 1},
		{name: "inactive-key-owner", key: "inactive-owner", status: 401, validations: 1},
		{name: "mismatched-key-hash", key: "mismatch", status: 401, validations: 1},
		{name: "key-owner-intersection-denied", key: "owner-denied", status: 403, validations: 2},
		{name: "single-revalidation-error", key: "later-error", bearer: "valid", status: 401, validations: 2, diagnostic: "User API key permission check failed", marker: "permission-error-marker"},
		{name: "multi-revalidation-error", key: "later-error", bearer: "valid", multi: true, status: 401, validations: 2, diagnostic: "User API key permission check failed", marker: "permission-error-marker"},
		{name: "multi-verb-denied", key: "valid", multi: true, status: 403, validations: 5},
		{name: "expiry-delete-error-denied", bearer: "expired-error", status: 401, deletions: 1, diagnostic: "Failed to delete expired session", marker: "expiry-delete-marker"},
		{name: "expiry-delete-error-key-fallback", key: "valid", bearer: "expired-error", status: 200, validations: 2, deletions: 1, diagnostic: "Failed to delete expired session", marker: "expiry-delete-marker"},
		{name: "expiry-delete-success-denied", bearer: "expired", status: 401, deletions: 1},
		{name: "id-scope-on-lookup-error", key: "valid", scope: "id-error", status: 200, validations: 2, diagnostic: "account scope: cannot load cloud accounts", marker: "account-name-marker"},
		{name: "name-scope-withheld-on-lookup-error", key: "valid", scope: "name-error", status: 404, validations: 2, diagnostic: "account scope: cannot load cloud accounts", marker: "account-name-marker"},
		{name: "outside-scope-on-lookup-error", key: "valid", scope: "outside-error", status: 404, validations: 2, diagnostic: "account scope: cannot load cloud accounts", marker: "account-name-marker"},
		{name: "name-scope-success", key: "valid", scope: "name-ok", status: 200, validations: 2},
		{name: "strict-key-config-denial", key: "strict", status: 403, validations: 2},
		{name: "usage-store-error", key: "valid", usage: "error", status: 200, validations: 2, diagnostic: "Failed to record", marker: "usage-store-marker"},
		{name: "usage-store-panic", key: "valid", usage: "panic", status: 200, validations: 2, diagnostic: "service_apikeys: RecordUsage goroutine panic", marker: "usage-panic-marker"},
	}
}

func TestAuthApplicationDiagnostics(t *testing.T) {
	if childCase := os.Getenv("AUTH_APPLICATION_DIAGNOSTICS_CASE"); childCase != "" {
		for _, tc := range diagnosticsCases() {
			if tc.name == childCase {
				runDiagnosticsCase(t, tc)
				return
			}
		}
		t.Fatal("unknown helper scenario")
	}
	for _, tc := range diagnosticsCases() {
		t.Run(tc.name, func(t *testing.T) {
			binary, err := os.Executable()
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestAuthApplicationDiagnostics$", "-test.count=1", "-test.timeout=8s")
			cmd.Dir = t.TempDir()
			cmd.WaitDelay = time.Second
			cmd.Env = []string{
				"AUTH_APPLICATION_DIAGNOSTICS_CASE=" + tc.name,
				"PATH=/nonexistent", "LOG_LEVEL=debug", "HOME=" + cmd.Dir,
				"AWS_EC2_METADATA_DISABLED=true", "AWS_CONFIG_FILE=/dev/null", "AWS_SHARED_CREDENTIALS_FILE=/dev/null",
				"GOTOOLCHAIN=go1.26.9", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOFLAGS=-mod=readonly",
			}
			output, err := cmd.CombinedOutput()
			require.NoError(t, ctx.Err(), "inconclusive: helper deadline; output: %s", output)
			require.NoError(t, err, "inconclusive: helper did not exit cleanly; output: %s", output)
			require.Contains(t, string(output), "SEMANTICS_OK "+tc.name)
			if tc.key == "valid" || tc.key == "later-error" || tc.key == "key-denied" || tc.key == "owner-denied" || tc.key == "strict" {
				require.Equal(t, 1, strings.Count(string(output), "USAGE_WRITE "), "one store write through clean process exit")
				require.Contains(t, string(output), "USAGE_WRITE delta=1")
			} else {
				require.NotContains(t, string(output), "USAGE_WRITE ")
			}
			if tc.diagnostic != "" {
				require.Contains(t, string(output), tc.diagnostic, "diagnostic branch must emit")
				require.NotContains(t, string(output), tc.marker, "diagnostic_marker_absent_"+tc.name)
			}
			require.NotContains(t, string(output), diagnosticsRawKey, "raw_request_key_absent")
			require.NotContains(t, string(output), diagnosticsRawToken, "raw_session_token_absent")
			require.NotContains(t, string(output), diagnosticsKeyID, "private_key_id_absent")
		})
	}
}

type diagnosticsWriter struct {
	mu      sync.Mutex
	prefix  string
	written chan struct{}
}

func (w *diagnosticsWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := os.Stdout.Write(p)
	if err == nil && n == len(p) && w.prefix != "" && strings.Contains(string(p), w.prefix) {
		select {
		case w.written <- struct{}{}:
		default:
		}
	}
	return n, err
}

type diagnosticsAdapter struct {
	*authServiceAdapter
	bookings  atomic.Int64
	bookedKey string
}

type diagnosticsUsageStore struct {
	*auth.MockStore
	returned chan error
}

func (s *diagnosticsUsageStore) RecordAPIKeyUsage(ctx context.Context, keyID string, delta int64) error {
	err := s.MockStore.RecordAPIKeyUsage(ctx, keyID, delta)
	select {
	case s.returned <- err:
	default:
	}
	return err
}

func (a *diagnosticsAdapter) RecordAPIKeyUsageAsync(keyID string) {
	a.bookings.Add(1)
	a.bookedKey = keyID
	a.authServiceAdapter.RecordAPIKeyUsageAsync(keyID)
}

func runDiagnosticsCase(t *testing.T, tc diagnosticsCase) {
	writer := &diagnosticsWriter{prefix: tc.diagnostic, written: make(chan struct{}, 1)}
	logging.SetOutput(writer)
	logging.SetLevel("debug")
	log.SetOutput(writer)
	log.SetFlags(0)
	log.SetPrefix("")
	slog.SetDefault(slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	keyDigest := sha256.Sum256([]byte(diagnosticsRawKey))
	keyHash := base64.RawURLEncoding.EncodeToString(keyDigest[:])
	tokenDigest := sha256.Sum256([]byte(diagnosticsRawToken))
	tokenHash := hex.EncodeToString(tokenDigest[:])
	view := auth.Permission{Action: auth.ActionView, Resource: auth.ResourcePlans}
	key := &auth.UserAPIKey{ID: diagnosticsKeyID, UserID: "key-owner", KeyHash: keyHash, IsActive: true, Permissions: []auth.Permission{view}}
	owner := &auth.User{ID: "key-owner", Email: "key-owner@example.invalid", Active: true, GroupIDs: []string{"key-group"}}
	sessionOwner := &auth.User{ID: "session-owner", Email: "session-owner@example.invalid", Active: true, GroupIDs: []string{"session-group"}}
	group := &auth.Group{ID: "key-group", Permissions: []auth.Permission{view}}
	sessionGroup := &auth.Group{ID: "session-group", Permissions: []auth.Permission{view}}
	past := time.Now().Add(-time.Hour)
	switch tc.key {
	case "inactive":
		key.IsActive = false
	case "expired":
		key.ExpiresAt = &past
	case "inactive-owner":
		owner.Active = false
	case "mismatch":
		key.KeyHash = "different-hash"
	case "key-denied":
		key.Permissions = nil
	case "owner-denied":
		group.Permissions = nil
	case "strict":
		group.Permissions = []auth.Permission{{Action: auth.ActionUpdate, Resource: auth.ResourceConfig}}
		key.Permissions = []auth.Permission{{Action: auth.ActionUpdate, Resource: auth.ResourceConfig, Constraints: &auth.PermissionConstraints{Providers: []string{"azure"}}}}
	}
	account := config.CloudAccount{ID: "allowed-account", Name: "allowed-name", Provider: "aws"}
	switch tc.scope {
	case "id-error":
		group.AllowedAccounts = []string{account.ID}
	case "name-error", "name-ok":
		group.AllowedAccounts = []string{account.Name}
	case "outside-error":
		group.AllowedAccounts = []string{"different-account"}
	}
	store := new(auth.MockStore)
	var validations, sessionReads, deletions, keyIDReads, usageCalls, usageTotal atomic.Int64
	var persistedUsage atomic.Int64
	usageEntered := make(chan int64, 1)
	lookup := store.On("GetAPIKeyByHash", mock.Anything, keyHash).Run(func(mock.Arguments) { validations.Add(1) })
	switch tc.key {
	case "error":
		lookup.Return(nil, errors.New("initial-error-marker")).Maybe()
	case "unknown":
		lookup.Return(nil, nil).Maybe()
	case "later-error":
		lookup.Return(key, nil).Once()
		store.On("GetAPIKeyByHash", mock.Anything, keyHash).Run(func(mock.Arguments) { validations.Add(1) }).Return(nil, errors.New("permission-error-marker")).Once()
	default:
		lookup.Return(key, nil).Maybe()
	}
	store.On("GetUserByID", mock.Anything, owner.ID).Return(owner, nil).Maybe()
	store.On("GetUserByID", mock.Anything, sessionOwner.ID).Return(sessionOwner, nil).Maybe()
	store.On("GetGroup", mock.Anything, group.ID).Return(group, nil).Maybe()
	store.On("GetGroup", mock.Anything, sessionGroup.ID).Return(sessionGroup, nil).Maybe()
	store.On("GetAPIKeyByID", mock.Anything, diagnosticsKeyID).Run(func(mock.Arguments) { keyIDReads.Add(1) }).Return(key, nil).Maybe()
	session := &auth.Session{Token: tokenHash, UserID: sessionOwner.ID, Email: sessionOwner.Email, ExpiresAt: time.Now().Add(time.Hour)}
	if strings.HasPrefix(tc.bearer, "expired") {
		// PostgreSQL filters expired rows; this synthetic result models expiry after fetch.
		session.ExpiresAt = past
	}
	if tc.bearer == "inactive" {
		sessionOwner.Active = false
	}
	if tc.bearer == "mismatch" {
		session.Token = "different-hash"
	}
	sessionLookup := store.On("GetSession", mock.Anything, tokenHash).Run(func(mock.Arguments) { sessionReads.Add(1) })
	if tc.bearer == "invalid" {
		sessionLookup.Return(nil, nil).Maybe()
	} else {
		sessionLookup.Return(session, nil).Maybe()
	}
	var deleteErr error
	if tc.bearer == "expired-error" {
		deleteErr = errors.New("expiry-delete-marker")
	}
	store.On("DeleteSession", mock.Anything, tokenHash).Run(func(mock.Arguments) {
		deletions.Add(1)
		if deleteErr == nil {
			sessionLookup.ReturnArguments[0] = nil
		}
	}).Return(deleteErr).Maybe()
	store.On("RecordAPIKeyUsage", mock.Anything, diagnosticsKeyID, mock.Anything).Run(func(args mock.Arguments) {
		delta, ok := args.Get(2).(int64)
		if !ok {
			delta = -1
		}
		usageCalls.Add(1)
		usageTotal.Add(delta)
		fmt.Printf("USAGE_WRITE delta=%d\n", delta)
		select {
		case usageEntered <- delta:
		default:
		}
		if tc.usage == "panic" {
			panic("usage-panic-marker")
		}
		if tc.usage == "" {
			persistedUsage.Add(delta)
		}
	}).Return(func() error {
		if tc.usage == "error" {
			return errors.New("usage-store-marker")
		}
		return nil
	}()).Maybe()
	plan := config.PurchasePlan{ID: diagnosticsPlanID, Name: "synthetic diagnostic plan"}
	planBefore, err := json.Marshal(plan)
	require.NoError(t, err)
	cfg := &mocks.MockConfigStore{
		GetPurchasePlanFn: func(context.Context, string) (*config.PurchasePlan, error) {
			copy := plan
			return &copy, nil
		},
		GetPlanAccountsFn: func(context.Context, string) ([]config.CloudAccount, error) {
			return []config.CloudAccount{account}, nil
		},
		ListCloudAccountsFn: func(context.Context, config.CloudAccountFilter) ([]config.CloudAccount, error) {
			if strings.HasSuffix(tc.scope, "error") {
				return nil, errors.New("account-name-marker")
			}
			return []config.CloudAccount{account}, nil
		},
	}
	usageStore := &diagnosticsUsageStore{MockStore: store, returned: make(chan error, 1)}
	service := auth.NewService(auth.ServiceConfig{Store: usageStore, SessionDuration: time.Hour, CSRFKey: auth.TestCSRFKey()})
	adapter := &diagnosticsAdapter{authServiceAdapter: newAuthServiceAdapter(service)}
	handler := api.NewHandler(api.HandlerConfig{AuthService: adapter, ConfigStore: cfg, CORSAllowedOrigin: "https://dashboard.example.invalid"})
	headers := map[string]string{}
	if tc.key != "" {
		headers["x-api-key"] = diagnosticsRawKey
	}
	if tc.bearer != "" {
		headers["authorization"] = "Bearer " + diagnosticsRawToken
	}
	if tc.bearer == "malformed" {
		headers["authorization"] = "Basic malformed"
	}
	method, path, body := "GET", "/api/plans/"+diagnosticsPlanID, ""
	if tc.multi {
		method, path = "DELETE", "/api/purchases/planned/"+diagnosticsPlanID
	}
	if tc.retained {
		path = "/api/auth/me"
	}
	if tc.key == "strict" {
		method, path, body = "PUT", "/api/ri-exchange/config", `{"auto_exchange_enabled":false,"mode":"manual","lookback_days":30}`
		headers["content-type"] = "application/json"
	}
	response, returnedErr := handler.HandleRequest(context.Background(), &events.LambdaFunctionURLRequest{
		Headers: headers, Body: body,
		RequestContext: events.LambdaFunctionURLRequestContext{HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{Method: method, Path: path}},
	})
	require.NoError(t, returnedErr)
	require.NotNil(t, response)
	require.Equal(t, tc.status, response.StatusCode, "body: %s", response.Body)
	require.EqualValues(t, tc.validations, validations.Load(), "actual credential validations")
	require.EqualValues(t, tc.deletions, deletions.Load(), "exact expired-session cleanup attempts")
	if tc.bearer == "expired" {
		require.Nil(t, sessionLookup.ReturnArguments[0], "successful cleanup removes synthetic expired row")
	} else if tc.bearer == "expired-error" {
		require.Same(t, session, sessionLookup.ReturnArguments[0], "failed cleanup retains synthetic row")
	}
	wantSessionReads := int64(0)
	if tc.bearer != "" && tc.bearer != "malformed" {
		wantSessionReads = 1
	}
	require.Equal(t, wantSessionReads, sessionReads.Load(), "bearer evaluated even with a valid key")
	resolved := tc.key == "valid" || tc.key == "later-error" || tc.key == "key-denied" || tc.key == "owner-denied" || tc.key == "strict"
	if resolved {
		require.EqualValues(t, 1, adapter.bookings.Load(), "exactly one synchronous booking")
		require.Equal(t, diagnosticsKeyID, adapter.bookedKey)
		select {
		case delta := <-usageEntered:
			require.EqualValues(t, 1, delta, "one request delta reaches the actual store")
		case <-time.After(2 * time.Second):
			t.Fatal("actual usage store was not entered")
		}
		if tc.usage != "panic" {
			select {
			case usageErr := <-usageStore.returned:
				if tc.usage == "error" {
					require.EqualError(t, usageErr, "usage-store-marker")
				} else {
					require.NoError(t, usageErr)
					require.EqualValues(t, 1, persistedUsage.Load(), "successful synthetic usage result")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("synthetic usage store result not observed")
			}
		}
		if tc.usage != "" {
			select {
			case <-writer.written:
			case <-time.After(2 * time.Second):
				t.Fatal("intended usage diagnostic Write did not finish")
			}
		}
		require.EqualValues(t, 1, usageCalls.Load())
		require.EqualValues(t, 1, usageTotal.Load())
		if tc.usage != "" {
			require.Zero(t, persistedUsage.Load(), "failed usage stays lost, not persisted")
		}
	} else {
		require.Zero(t, adapter.bookings.Load(), "unresolved/session principal must not book")
		require.Zero(t, usageCalls.Load())
	}
	if tc.diagnostic != "" && tc.usage == "" {
		select {
		case <-writer.written:
		default:
			t.Fatal("intended synchronous diagnostic branch did not emit")
		}
	}
	if tc.key == "strict" {
		require.EqualValues(t, 1, keyIDReads.Load(), "real key constraint lookup")
		require.Contains(t, response.Body, "exceeds the constraints")
	} else {
		require.Zero(t, keyIDReads.Load())
	}
	if tc.scope != "" {
		cfg.AssertNumberOfCalls(t, "ListCloudAccounts", 1)
		cfg.AssertNumberOfCalls(t, "GetPlanAccounts", 1)
	}
	if tc.status == 200 && !tc.retained {
		require.JSONEq(t, string(planBefore), response.Body, "positive plan fields remain exact and present")
	}
	if tc.retained {
		require.Contains(t, response.Body, sessionOwner.ID, "session-only handler uses the retained bearer")
		require.NotContains(t, response.Body, owner.ID)
	}
	if tc.status != 200 {
		require.NotContains(t, response.Body, plan.Name, "denials conceal plan payload")
		cfg.AssertNumberOfCalls(t, "GetPurchasePlan", 0)
	}
	for _, forbidden := range []string{"UpdateGlobalConfigAtomic", "SaveGlobalConfig", "UpdatePurchasePlan", "UpdatePurchasePlanTx", "DeletePurchasePlan", "SavePurchaseExecution", "SavePurchaseExecutionTx", "DeletePurchaseExecution"} {
		cfg.AssertNotCalled(t, forbidden)
	}
	planAfter, err := json.Marshal(plan)
	require.NoError(t, err)
	require.Equal(t, string(planBefore), string(planAfter), "plan domain baseline unchanged")
	store.AssertExpectations(t)
	require.NotContains(t, response.Body, diagnosticsRawKey)
	require.NotContains(t, response.Body, diagnosticsRawToken)
	require.NotContains(t, response.Body, diagnosticsKeyID)
	if tc.marker != "" {
		require.NotContains(t, response.Body, tc.marker, "response marker absent")
	}
	fmt.Println("SEMANTICS_OK " + tc.name)
}
