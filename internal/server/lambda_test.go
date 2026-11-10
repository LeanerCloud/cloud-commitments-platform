package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/api"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/scheduler"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/secrets"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/testutil"
)

func TestDetectLambdaEventType(t *testing.T) {
	tests := []struct {
		name         string
		rawEvent     string
		expectedType string
	}{
		{
			name: "Lambda Function URL request",
			rawEvent: `{
				"requestContext": {
					"http": {
						"method": "GET"
					}
				}
			}`,
			expectedType: "http",
		},
		{
			name: "API Gateway v2 request",
			rawEvent: `{
				"httpMethod": "POST",
				"path": "/api/test"
			}`,
			expectedType: "http",
		},
		{
			name: "SQS event",
			rawEvent: `{
				"Records": [
					{
						"eventSource": "aws:sqs",
						"body": "{\"test\": \"data\"}"
					}
				]
			}`,
			expectedType: "unknown",
		},
		{
			name: "EventBridge scheduled event",
			rawEvent: `{
				"source": "aws.events",
				"detail-type": "Scheduled Event"
			}`,
			expectedType: "scheduled",
		},
		{
			name: "Custom scheduled event",
			rawEvent: `{
				"action": "collect_recommendations"
			}`,
			expectedType: "scheduled",
		},
		{
			name:         "migrate action",
			rawEvent:     `{"action":"migrate"}`,
			expectedType: "migrate",
		},
		{
			name:         "EventBridge envelope with migrate action stays scheduled",
			rawEvent:     `{"source":"aws.events","detail-type":"Scheduled Event","action":"migrate"}`,
			expectedType: "scheduled",
		},
		{
			name:         "Function URL event with migrate in body stays http",
			rawEvent:     `{"requestContext":{"http":{"method":"POST"}},"body":"{\"action\":\"migrate\"}"}`,
			expectedType: "http",
		},
		{
			name:         "API Gateway event with top-level migrate action stays http",
			rawEvent:     `{"httpMethod":"POST","action":"migrate"}`,
			expectedType: "http",
		},
		{
			name:         "Unknown event defaults to scheduled",
			rawEvent:     `{"unknown": "event"}`,
			expectedType: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventType := detectLambdaEventType(json.RawMessage(tt.rawEvent))
			testutil.AssertEqual(t, tt.expectedType, eventType)
		})
	}
}

func TestHandleLambdaHTTPEvent(t *testing.T) {
	tests := []struct {
		name           string
		rawEvent       string
		expectError    bool
		expectedStatus int
	}{
		{
			name: "valid HTTP request",
			rawEvent: `{
				"requestContext": {
					"http": {
						"method": "GET",
						"path": "/health"
					},
					"timeEpoch": 1234567890
				},
				"rawPath": "/health",
				"headers": {}
			}`,
			expectError:    false,
			expectedStatus: 200,
		},
		{
			name:           "invalid JSON",
			rawEvent:       `{invalid json}`,
			expectError:    false,
			expectedStatus: 400,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testutil.TestContext(t)

			// Create minimal app with mocked API handler
			app := &Application{
				API: api.NewHandler(api.HandlerConfig{}),
			}

			resp, err := app.handleLambdaHTTPEvent(ctx, json.RawMessage(tt.rawEvent))

			if tt.expectError {
				testutil.AssertError(t, err)
			} else {
				testutil.AssertNoError(t, err)
				if resp != nil {
					testutil.AssertEqual(t, tt.expectedStatus, resp.StatusCode)
				}
			}
		})
	}
}

// testOwnerToken is a fixed, well-formed collection owner token. It has to be
// a real UUID because ParseScheduledEvent rejects malformed non-empty tokens
// at the boundary rather than letting them reach the UUID-typed column.
const testOwnerToken = "6b1f2c34-5d6e-4a7b-8c9d-0e1f2a3b4c5d"

func TestHandleLambdaScheduledEvent(t *testing.T) {
	tests := []struct {
		setupMocks    func(*testutil.MockScheduler)
		name          string
		rawEvent      string
		expectedToken string
		expectError   bool
	}{
		{
			name:     "collect_recommendations event",
			rawEvent: `{"action": "collect_recommendations"}`,
			setupMocks: func(s *testutil.MockScheduler) {
				s.CollectRecommendationsFunc = func(ctx context.Context, ownerToken string) (*scheduler.CollectResult, error) {
					return &scheduler.CollectResult{
						Recommendations: 10,
						TotalSavings:    500.0,
					}, nil
				}
			},
			expectError: false,
		},
		{
			name:     "EventBridge format",
			rawEvent: `{"source": "aws.events", "action": "collect_recommendations"}`,
			setupMocks: func(s *testutil.MockScheduler) {
				s.CollectRecommendationsFunc = func(ctx context.Context, ownerToken string) (*scheduler.CollectResult, error) {
					return &scheduler.CollectResult{
						Recommendations: 5,
						TotalSavings:    250.0,
					}, nil
				}
			},
			expectError: false,
		},
		{
			// Issue #261: the async self-invoke payload's owner_token must
			// survive parsing and reach CollectRecommendations, which is what
			// scopes the deferred clear to this run. Asserting the token the
			// scheduler actually received (rather than only that the event
			// parses) means dropping it anywhere between ParseScheduledEvent
			// and the scheduler fails the test instead of silently stranding
			// the marker for the full 5-minute recovery window.
			name:          "async self-invoke carries owner_token through to the scheduler",
			rawEvent:      `{"source": "aws.events", "action": "collect_recommendations", "owner_token": "` + testOwnerToken + `"}`,
			expectedToken: testOwnerToken,
			expectError:   false,
		},
		{
			// A non-empty owner_token that is not a UUID can only come from a
			// corrupt payload (asyncInvokeSelf always sends a uuid.New()), and
			// could never match a marker owner, so it is rejected at the
			// boundary rather than reaching the UUID-typed persistence layer.
			name:        "malformed owner_token is rejected at the boundary",
			rawEvent:    `{"source": "aws.events", "action": "collect_recommendations", "owner_token": "not-a-uuid"}`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testutil.TestContext(t)

			mockScheduler := &testutil.MockScheduler{}
			// Capture the token the scheduler was handed and assert on it from
			// the subtest goroutine rather than inside the mock callback, so a
			// failed assertion never calls FailNow off the owning goroutine.
			gotToken := ""
			collected := false
			mockScheduler.CollectRecommendationsFunc = func(ctx context.Context, ownerToken string) (*scheduler.CollectResult, error) {
				gotToken = ownerToken
				collected = true
				return &scheduler.CollectResult{}, nil
			}
			if tt.setupMocks != nil {
				tt.setupMocks(mockScheduler)
			}

			app := &Application{
				Scheduler: mockScheduler,
			}

			_, err := app.handleLambdaScheduledEvent(ctx, json.RawMessage(tt.rawEvent))

			if tt.expectError {
				testutil.AssertError(t, err)
				return
			}
			testutil.AssertNoError(t, err)
			if tt.expectedToken != "" {
				if !collected {
					t.Fatal("expected CollectRecommendations to be called")
				}
				testutil.AssertEqual(t, tt.expectedToken, gotToken)
			}
		})
	}
}

// TestHandleLambdaEvent_UnknownEventReturnsError is a regression test for
// 04-N4: before the fix, unrecognized payloads were silently routed to
// handleLambdaScheduledEvent, which then failed with "unknown scheduled task
// action" -- masking the real root cause. The fix returns a distinct error
// so callers (and logs) see "unrecognized Lambda event shape" instead.
func TestHandleLambdaEvent_UnknownEventReturnsError(t *testing.T) {
	ctx := testutil.TestContext(t)

	app := &Application{
		API: api.NewHandler(api.HandlerConfig{}),
	}

	_, err := app.HandleLambdaEvent(ctx, json.RawMessage(`{"unknown": "event"}`))
	testutil.AssertError(t, err)
	testutil.AssertTrue(t, strings.Contains(err.Error(), "unrecognized"),
		"expected 'unrecognised' in error, got: "+err.Error())
}

// TestHandleLambdaEvent_SQSEventIsUnrecognized pins that an aws:sqs event no
// longer reaches purchase execution: nothing in this repo produces one, so a
// stray event-source mapping must fail loudly instead of running the purchase
// path without the gates the HTTP path enforces (issue #399).
func TestHandleLambdaEvent_SQSEventIsUnrecognized(t *testing.T) {
	ctx := testutil.TestContext(t)

	app := &Application{
		API:      api.NewHandler(api.HandlerConfig{}),
		Purchase: &testutil.MockPurchaseManager{},
	}

	_, err := app.HandleLambdaEvent(ctx, json.RawMessage(`{"Records":[{"eventSource":"aws:sqs","messageId":"m","body":"{}"}]}`))
	testutil.AssertError(t, err)
	testutil.AssertTrue(t, strings.Contains(err.Error(), "unrecognized Lambda event shape"),
		"expected 'unrecognized Lambda event shape' in error, got: "+err.Error())
}

func TestHandleLambdaEvent(t *testing.T) {
	tests := []struct {
		setupApp    func(*Application)
		name        string
		rawEvent    string
		expectError bool
	}{
		{
			name: "HTTP event routing",
			rawEvent: `{
				"requestContext": {
					"http": {"method": "GET"}
				}
			}`,
			setupApp: func(app *Application) {
				// API handler will be nil, causing handled response
			},
			expectError: false,
		},
		{
			name: "SQS event is rejected",
			rawEvent: `{
				"Records": [{
					"eventSource": "aws:sqs",
					"body": "{}"
				}]
			}`,
			expectError: true,
		},
		{
			name:     "Scheduled event routing",
			rawEvent: `{"action": "collect_recommendations"}`,
			setupApp: func(app *Application) {
				app.Scheduler = &testutil.MockScheduler{
					CollectRecommendationsFunc: func(ctx context.Context, ownerToken string) (*scheduler.CollectResult, error) {
						return &scheduler.CollectResult{}, nil
					},
				}
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testutil.TestContext(t)

			app := &Application{
				API: api.NewHandler(api.HandlerConfig{}),
			}
			if tt.setupApp != nil {
				tt.setupApp(app)
			}

			_, err := app.HandleLambdaEvent(ctx, json.RawMessage(tt.rawEvent))

			if tt.expectError {
				testutil.AssertError(t, err)
			} else {
				testutil.AssertNoError(t, err)
			}
		})
	}
}

// stubMigrateRunner replaces the migrate event's runner. No t.Parallel in
// callers: it swaps a package var.
func stubMigrateRunner(t *testing.T, fn func(context.Context, *database.Config, secrets.Resolver, MigrateOptions) (MigrateReport, error)) {
	t.Helper()
	prev := runDeployMigrationsFn
	runDeployMigrationsFn = fn
	t.Cleanup(func() { runDeployMigrationsFn = prev })
}

// poisonedEnsureDBApp's ensureDB returns an error, so any route through it
// fails the test's expectations.
func poisonedEnsureDBApp() *Application {
	return &Application{
		API:      api.NewHandler(api.HandlerConfig{}),
		dbConfig: &database.Config{Host: "unreachable"},
		dbErr:    errors.New("ensureDB must not be called"),
	}
}

func TestHandleLambdaEvent_MigrateBypassesEnsureDB(t *testing.T) {
	var gotOpts MigrateOptions
	var gotCfg *database.Config
	stubMigrateRunner(t, func(_ context.Context, cfg *database.Config, _ secrets.Resolver, o MigrateOptions) (MigrateReport, error) {
		gotCfg, gotOpts = cfg, o
		return MigrateReport{From: 70, To: 74}, nil
	})
	app := poisonedEnsureDBApp()

	out, err := app.HandleLambdaEvent(context.Background(), json.RawMessage(`{"action":"migrate","extra":"ignored"}`))
	testutil.AssertNoError(t, err)
	testutil.AssertTrue(t, gotCfg == app.dbConfig, "runner must get the app's dbConfig")
	testutil.AssertTrue(t, gotOpts.ForceVersion == nil && gotOpts.Timeout == 0, "no options expected")
	body, mErr := json.Marshal(out)
	testutil.AssertNoError(t, mErr)
	testutil.AssertEqual(t, `{"from":70,"status":"ok","to":74}`, string(body))
}

func TestHandleLambdaEvent_MigratePassesOptions(t *testing.T) {
	var gotOpts MigrateOptions
	stubMigrateRunner(t, func(_ context.Context, _ *database.Config, _ secrets.Resolver, o MigrateOptions) (MigrateReport, error) {
		gotOpts = o
		return MigrateReport{}, nil
	})
	_, err := poisonedEnsureDBApp().HandleLambdaEvent(context.Background(),
		json.RawMessage(`{"action":"migrate","force_version":0,"timeout_seconds":120}`))
	testutil.AssertNoError(t, err)
	testutil.AssertTrue(t, gotOpts.ForceVersion != nil && *gotOpts.ForceVersion == 0, "force_version 0 must be passed through")
	testutil.AssertEqual(t, 120*time.Second, gotOpts.Timeout)
}

func TestHandleLambdaEvent_MigrateRunnerErrorIsReturned(t *testing.T) {
	boom := errors.New("dirty at 72")
	stubMigrateRunner(t, func(context.Context, *database.Config, secrets.Resolver, MigrateOptions) (MigrateReport, error) {
		return MigrateReport{}, boom
	})
	out, err := poisonedEnsureDBApp().HandleLambdaEvent(context.Background(), json.RawMessage(`{"action":"migrate"}`))
	testutil.AssertTrue(t, errors.Is(err, boom), "runner error must be wrapped and returned")
	testutil.AssertTrue(t, out == nil, "no success body on failure")
}

func TestHandleLambdaEvent_MigrateRejectsBadPayload(t *testing.T) {
	stubMigrateRunner(t, func(context.Context, *database.Config, secrets.Resolver, MigrateOptions) (MigrateReport, error) {
		t.Error("runner must not run for an invalid payload")
		return MigrateReport{}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	for name, payload := range map[string]string{
		"negative force_version":  `{"action":"migrate","force_version":-1}`,
		"string force_version":    `{"action":"migrate","force_version":"5"}`,
		"float force_version":     `{"action":"migrate","force_version":1.5}`,
		"zero timeout":            `{"action":"migrate","timeout_seconds":0}`,
		"negative timeout":        `{"action":"migrate","timeout_seconds":-5}`,
		"string timeout":          `{"action":"migrate","timeout_seconds":"60"}`,
		"timeout over ctx budget": `{"action":"migrate","timeout_seconds":101}`,
		"timeout over Lambda max": `{"action":"migrate","timeout_seconds":901}`,
		"huge timeout":            `{"action":"migrate","timeout_seconds":9223372036854775807}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := poisonedEnsureDBApp().HandleLambdaEvent(ctx, json.RawMessage(payload))
			testutil.AssertError(t, err)
			testutil.AssertContains(t, err.Error(), "invalid migrate event")
		})
	}
}

func TestHandleLambdaEvent_AWSEventsMigrateStaysScheduled(t *testing.T) {
	stubMigrateRunner(t, func(context.Context, *database.Config, secrets.Resolver, MigrateOptions) (MigrateReport, error) {
		t.Error("runner must not run for an aws.events envelope")
		return MigrateReport{}, nil
	})
	_, err := poisonedEnsureDBApp().HandleLambdaEvent(context.Background(),
		json.RawMessage(`{"version":"0","source":"aws.events","detail-type":"Scheduled Event","action":"migrate"}`))
	testutil.AssertError(t, err)
	testutil.AssertContains(t, err.Error(), "ensureDB must not be called")
}

func TestHandleLambdaEvent_FunctionURLBodyMigrateStaysHTTP(t *testing.T) {
	stubMigrateRunner(t, func(context.Context, *database.Config, secrets.Resolver, MigrateOptions) (MigrateReport, error) {
		t.Error("runner must not run for an HTTP event")
		return MigrateReport{}, nil
	})
	_, err := poisonedEnsureDBApp().HandleLambdaEvent(context.Background(),
		json.RawMessage(`{"requestContext":{"http":{"method":"POST"}},"rawPath":"/x","body":"{\"action\":\"migrate\"}"}`))
	testutil.AssertError(t, err)
	testutil.AssertContains(t, err.Error(), "ensureDB must not be called")
}

func TestHandleLambdaEvent_MigrateTimeoutBounds(t *testing.T) {
	var got MigrateOptions
	stubMigrateRunner(t, func(_ context.Context, _ *database.Config, _ secrets.Resolver, o MigrateOptions) (MigrateReport, error) {
		got = o
		return MigrateReport{}, nil
	})
	app := poisonedEnsureDBApp()

	_, err := app.HandleLambdaEvent(context.Background(), json.RawMessage(`{"action":"migrate","timeout_seconds":901}`))
	testutil.AssertError(t, err)
	testutil.AssertContains(t, err.Error(), "1..900")

	_, err = app.HandleLambdaEvent(context.Background(), json.RawMessage(`{"action":"migrate","timeout_seconds":900}`))
	testutil.AssertNoError(t, err)
	testutil.AssertEqual(t, 900*time.Second, got.Timeout)

	expired, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)
	_, err = app.HandleLambdaEvent(expired, json.RawMessage(`{"action":"migrate","timeout_seconds":5}`))
	testutil.AssertError(t, err)
	testutil.AssertContains(t, err.Error(), "too little invocation time")
}
