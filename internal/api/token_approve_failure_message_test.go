package api

import (
	"context"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A token-path approver is the contact of one account on the execution, so a
// failure body must not carry run-error text that names the other accounts.

const (
	otherAcctID   = "999988887777"
	otherAcctName = "Beta Corp Production"
)

var errOtherAcct = fmt.Errorf("account %s (%s): assume role arn:aws:iam::%s:role/x failed", otherAcctID, otherAcctName, otherAcctID)

type tokenFailureCase struct {
	name   string
	status string // the row's status; also the expected details status
	err    error
	code   int
}

func tokenFailureCases() []tokenFailureCase {
	refusal := func(msg string) error {
		return fmt.Errorf("%w: %w", purchase.ErrClaimedRowRefused, NewClientError(403, msg))
	}
	return []tokenFailureCase{
		{"audit loss", config.StatusPartiallyCompleted, fmt.Errorf("%w: terminal save failed; original execution error: %w", config.ErrAuditLoss, errOtherAcct), 500},
		{"partial run", config.StatusPartiallyCompleted, fmt.Errorf("some purchases failed: %w", errOtherAcct), 502},
		{"default failure", "failed", fmt.Errorf("provider call failed: %w", errOtherAcct), 502},
		{"post-claim step cap", "failed", refusal(fmt.Sprintf("step total commitment 9000.00 exceeds max_step_commitment 100.00 (%s, %s)", otherAcctID, otherAcctName)), 403},
		{"post-claim ladder", "failed", refusal(fmt.Sprintf("ladder tranche for %s (%s) no longer matches", otherAcctID, otherAcctName)), 403},
	}
}

func multiAcctRow(execID, status string) *config.PurchaseExecution {
	row := partialRunRow(execID)
	row.Status = status
	other := otherAcctID
	row.Recommendations[1].CloudAccountID = &other
	return row
}

func TestTokenApprove_FailureBodyOmitsRunErrorText(t *testing.T) {
	ctx := context.Background()
	execID := "b1234567-b123-b123-b123-b1234567abcd"
	for _, tc := range tokenFailureCases() {
		t.Run(tc.name, func(t *testing.T) {
			handler, _, _ := tokenApproveHandler(t, ctx, execID, "contact@example.com", multiAcctRow(execID, tc.status), "", tc.err)
			_, err := handler.approvePurchase(ctx, &events.LambdaFunctionURLRequest{Headers: map[string]string{"authorization": "Bearer sess-tok"}}, execID, "valid-token")
			ce, ok := IsClientError(err)
			require.True(t, ok, "expected a client error, got %v", err)
			assert.Equal(t, tc.code, ce.code)
			body := fmt.Sprintf("%s %v", ce.message, ce.Details())
			assert.NotContains(t, body, otherAcctID)
			assert.NotContains(t, body, otherAcctName)
			if tc.name != "audit loss" {
				assert.Equal(t, execID, ce.Details()["execution_id"])
				assert.Equal(t, tc.status, ce.Details()["status"])
			}
		})
	}
}

// The session caller holds approve-* scoped to the execution and keeps the
// detailed message.
func TestSessionApprove_FailureKeepsRunErrorText(t *testing.T) {
	ctx := context.Background()
	execID := "c1234567-c123-c123-c123-c1234567abcd"
	for _, tc := range tokenFailureCases()[:3] {
		t.Run(tc.name, func(t *testing.T) {
			handler, _ := sessionApproveHandler(t, ctx, execID, "contact@example.com", multiAcctRow(execID, tc.status), "", tc.err)
			_, err := handler.approvePurchase(ctx, &events.LambdaFunctionURLRequest{Headers: map[string]string{"authorization": "Bearer sess-tok"}}, execID, "")
			ce, ok := IsClientError(err)
			require.True(t, ok, "expected a client error, got %v", err)
			assert.Equal(t, tc.code, ce.code)
			assert.Contains(t, ce.message, otherAcctName)
		})
	}
}
