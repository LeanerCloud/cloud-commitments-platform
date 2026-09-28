package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/api"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/scheduler"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/testutil"
	"github.com/aws/aws-lambda-go/events"
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
			expectedType: "sqs",
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

func TestHandleLambdaSQSEvent(t *testing.T) {
	tests := []struct {
		setupMocks  func(*testutil.MockPurchaseManager)
		name        string
		rawEvent    string
		expectError bool
	}{
		{
			name: "valid SQS event with single message",
			rawEvent: `{
				"Records": [
					{
						"messageId": "msg-123",
						"eventSource": "aws:sqs",
						"body": "{\"purchase_id\": \"123\"}"
					}
				]
			}`,
			setupMocks: func(p *testutil.MockPurchaseManager) {
				p.ProcessMessageFunc = func(ctx context.Context, body string) error {
					return nil
				}
			},
			expectError: false,
		},
		{
			name: "valid SQS event with multiple messages",
			rawEvent: `{
				"Records": [
					{
						"messageId": "msg-123",
						"eventSource": "aws:sqs",
						"body": "{\"purchase_id\": \"123\"}"
					},
					{
						"messageId": "msg-456",
						"eventSource": "aws:sqs",
						"body": "{\"purchase_id\": \"456\"}"
					}
				]
			}`,
			setupMocks: func(p *testutil.MockPurchaseManager) {
				callCount := 0
				p.ProcessMessageFunc = func(ctx context.Context, body string) error {
					callCount++
					return nil
				}
			},
			expectError: false,
		},
		{
			name:        "invalid JSON",
			rawEvent:    `{invalid json}`,
			setupMocks:  func(p *testutil.MockPurchaseManager) {},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testutil.TestContext(t)

			mockPurchase := &testutil.MockPurchaseManager{}
			tt.setupMocks(mockPurchase)

			app := &Application{
				Purchase: mockPurchase,
			}

			_, err := app.handleLambdaSQSEvent(ctx, json.RawMessage(tt.rawEvent))

			if tt.expectError {
				testutil.AssertError(t, err)
			} else {
				testutil.AssertNoError(t, err)
			}
		})
	}
}

// partialFailureRawEvent is a 3-record batch where only "msg-2" fails,
// shared by the flag-on and flag-off partial-failure tests below.
const partialFailureRawEvent = `{
	"Records": [
		{"messageId": "msg-1", "eventSource": "aws:sqs", "body": "{\"execution_id\": \"msg-1\"}"},
		{"messageId": "msg-2", "eventSource": "aws:sqs", "body": "{\"execution_id\": \"msg-2\"}"},
		{"messageId": "msg-3", "eventSource": "aws:sqs", "body": "{\"execution_id\": \"msg-3\"}"}
	]
}`

func failOnMsg2Purchase() *testutil.MockPurchaseManager {
	return &testutil.MockPurchaseManager{
		ProcessMessageFunc: func(_ context.Context, body string) error {
			if strings.Contains(body, "msg-2") {
				return fmt.Errorf("execution not found: msg-2")
			}
			return nil
		},
	}
}

// TestHandleLambdaSQSEvent_PartialBatchFailure_FlagOn pins issue #108's
// fix, gated behind ReportSQSBatchItemFailures=true: a batch where only
// some records fail must leave the succeeded records ACKed rather than
// redelivering the whole batch. Pre-fix, handleLambdaSQSEvent collected
// every failed message ID and returned a single aggregate error for the
// batch; Lambda's SQS integration treats ANY non-nil handler error as "the
// entire batch failed", so records 1 and 3 (which HandleSQSMessage already
// processed successfully) would be redelivered right alongside the
// genuinely failed record 2 -- and would typically fail a second time
// since their status already advanced past what the CAS-guarded handlers
// expect, repeating until the whole batch (not just the poison message)
// hits the DLQ.
//
// Asserted on the returned events.SQSEventResponse.BatchItemFailures set,
// per the issue's own fix direction, rather than on the error value: the
// function must return a nil error alongside the partial-failure response,
// since a non-nil error here is exactly the all-or-nothing behavior being
// removed.
func TestHandleLambdaSQSEvent_PartialBatchFailure_FlagOn(t *testing.T) {
	ctx := testutil.TestContext(t)

	app := &Application{
		Purchase:  failOnMsg2Purchase(),
		appConfig: ApplicationConfig{ReportSQSBatchItemFailures: true},
	}

	result, err := app.handleLambdaSQSEvent(ctx, json.RawMessage(partialFailureRawEvent))
	testutil.AssertNoError(t, err)

	resp, ok := result.(events.SQSEventResponse)
	if !ok {
		t.Fatalf("expected events.SQSEventResponse, got %T", result)
	}
	if len(resp.BatchItemFailures) != 1 {
		t.Fatalf("expected exactly 1 batch item failure, got %d: %+v", len(resp.BatchItemFailures), resp.BatchItemFailures)
	}
	if resp.BatchItemFailures[0].ItemIdentifier != "msg-2" {
		t.Fatalf("expected the failed record's own messageId (msg-2) in BatchItemFailures, got %q",
			resp.BatchItemFailures[0].ItemIdentifier)
	}
}

// TestHandleLambdaSQSEvent_PartialBatchFailure_FlagOff pins the review
// finding on PR #382: without the SQS event source mapping's
// function_response_types actually including "ReportBatchItemFailures",
// AWS ignores the events.SQSEventResponse body entirely, so returning a
// nil error there means the WHOLE BATCH is acked and deleted -- silently
// dropping the genuinely failed record instead of redelivering it. With
// ReportSQSBatchItemFailures left at its default (false), a partial
// failure must still return the old aggregate error so the whole batch is
// redelivered (safe, if noisier) until an operator confirms the Terraform
// side is live and flips the flag.
func TestHandleLambdaSQSEvent_PartialBatchFailure_FlagOff(t *testing.T) {
	ctx := testutil.TestContext(t)

	// Zero-value Application.appConfig: ReportSQSBatchItemFailures defaults
	// to false, matching a real, unconfigured deployment.
	app := &Application{Purchase: failOnMsg2Purchase()}

	result, err := app.handleLambdaSQSEvent(ctx, json.RawMessage(partialFailureRawEvent))
	testutil.AssertError(t, err)
	if result != nil {
		t.Fatalf("expected a nil result alongside the aggregate error, got %+v", result)
	}
	if !strings.Contains(err.Error(), "msg-2") {
		t.Fatalf("expected the aggregate error to name the failed message, got: %v", err)
	}
}

// TestHandleLambdaSQSEvent_AllFailed_ReturnsErrorRegardlessOfFlag pins the
// review finding that a batch where every record failed must never be
// acked, flag or no flag -- there is nothing to partially succeed on, so
// returning the partial-success shape (or a nil error) would ack a batch
// that accomplished nothing.
func TestHandleLambdaSQSEvent_AllFailed_ReturnsErrorRegardlessOfFlag(t *testing.T) {
	for _, flagValue := range []bool{true, false} {
		t.Run(fmt.Sprintf("flag=%v", flagValue), func(t *testing.T) {
			ctx := testutil.TestContext(t)

			app := &Application{
				Purchase: &testutil.MockPurchaseManager{
					ProcessMessageFunc: func(_ context.Context, _ string) error {
						return fmt.Errorf("execution not found")
					},
				},
				appConfig: ApplicationConfig{ReportSQSBatchItemFailures: flagValue},
			}

			rawEvent := `{"Records": [
				{"messageId": "msg-1", "eventSource": "aws:sqs", "body": "{}"},
				{"messageId": "msg-2", "eventSource": "aws:sqs", "body": "{}"}
			]}`

			result, err := app.handleLambdaSQSEvent(ctx, json.RawMessage(rawEvent))
			testutil.AssertError(t, err)
			if result != nil {
				t.Fatalf("expected a nil result alongside the all-failed error, got %+v", result)
			}
		})
	}
}

// TestLoadReportSQSBatchItemFailures pins the "error on garbage" contract
// for SQS_REPORT_BATCH_ITEM_FAILURES: unset defaults to false (fail
// closed), a recognized boolean parses, and anything else is a startup
// error rather than a silently-ignored typo.
func TestLoadReportSQSBatchItemFailures(t *testing.T) {
	t.Cleanup(func() { os.Unsetenv("SQS_REPORT_BATCH_ITEM_FAILURES") })

	tests := []struct {
		name      string
		envValue  string
		unset     bool
		wantVal   bool
		wantError bool
	}{
		{name: "unset defaults to false", unset: true, wantVal: false},
		{name: "true", envValue: "true", wantVal: true},
		{name: "false", envValue: "false", wantVal: false},
		{name: "garbage errors", envValue: "yes-please", wantError: true},
		{name: "empty string treated as unset", envValue: "", wantVal: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.unset {
				os.Unsetenv("SQS_REPORT_BATCH_ITEM_FAILURES")
			} else {
				os.Setenv("SQS_REPORT_BATCH_ITEM_FAILURES", tt.envValue)
			}

			got, err := loadReportSQSBatchItemFailures()
			if tt.wantError {
				testutil.AssertError(t, err)
				return
			}
			testutil.AssertNoError(t, err)
			if got != tt.wantVal {
				t.Fatalf("expected %v, got %v", tt.wantVal, got)
			}
		})
	}
}

// TestHandleLambdaSQSEvent_AllSucceedReturnsEmptyFailures documents the
// all-succeed shape: a nil error and an explicitly empty (not omitted)
// BatchItemFailures list, matching the SQS partial-batch-response contract.
// This holds regardless of ReportSQSBatchItemFailures, since an empty
// failure list is equivalent to plain success either way.
func TestHandleLambdaSQSEvent_AllSucceedReturnsEmptyFailures(t *testing.T) {
	ctx := testutil.TestContext(t)

	mockPurchase := &testutil.MockPurchaseManager{
		ProcessMessageFunc: func(_ context.Context, _ string) error { return nil },
	}
	app := &Application{Purchase: mockPurchase}

	rawEvent := `{"Records": [{"messageId": "msg-1", "eventSource": "aws:sqs", "body": "{}"}]}`

	result, err := app.handleLambdaSQSEvent(ctx, json.RawMessage(rawEvent))
	testutil.AssertNoError(t, err)

	resp, ok := result.(events.SQSEventResponse)
	if !ok {
		t.Fatalf("expected events.SQSEventResponse, got %T", result)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Fatalf("expected no batch item failures, got %+v", resp.BatchItemFailures)
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
			name: "SQS event routing",
			rawEvent: `{
				"Records": [{
					"eventSource": "aws:sqs",
					"body": "{}"
				}]
			}`,
			setupApp: func(app *Application) {
				app.Purchase = &testutil.MockPurchaseManager{
					ProcessMessageFunc: func(ctx context.Context, body string) error {
						return nil
					},
				}
			},
			expectError: false,
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
