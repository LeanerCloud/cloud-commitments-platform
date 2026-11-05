package purchase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// MessageType defines the types of async messages that can be processed.
type MessageType string

const (
	// MessageTypeExecutePurchase triggers execution of a scheduled purchase.
	MessageTypeExecutePurchase MessageType = "execute_purchase"
	// MessageTypeSendNotification sends a notification for upcoming purchase.
	MessageTypeSendNotification MessageType = "send_notification"
)

// AsyncMessage represents an SQS message for async purchase processing.
type AsyncMessage struct {
	Type        MessageType `json:"type"`
	ExecutionID string      `json:"execution_id,omitempty"`
	PlanID      string      `json:"plan_id,omitempty"`
}

// ProcessMessage handles SQS messages for async purchase processing.
// Supported message types:
//   - execute_purchase: Execute a scheduled purchase by execution_id
//   - send_notification: Send notification for upcoming purchases
func (m *Manager) ProcessMessage(ctx context.Context, body string) error {
	logging.Debug("Processing async message")

	// Parse the message
	var msg AsyncMessage
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		// If not valid JSON, log and skip (don't fail the queue)
		logging.Warnf("Invalid message format (not JSON), skipping: %v", err)
		return nil
	}

	switch msg.Type {
	case MessageTypeExecutePurchase:
		return m.handleExecutePurchase(ctx, &msg)
	case MessageTypeSendNotification:
		_, err := m.SendUpcomingPurchaseNotifications(ctx)
		return err
	default:
		logging.Warnf("Unknown message type: %s, skipping", msg.Type)
		return nil
	}
}

// handleExecutePurchase processes an execute_purchase message.
// checkAutoExecuteGate enforces the AutoPurchase and source gate for
// pending/notified rows on the SQS execute_purchase path. Extracted from
// handleExecutePurchase to keep that function under the gocyclo:10 limit.
// Returns nil when execution may proceed; a non-nil error means the message
// should be dead-lettered (the row is not eligible for auto-execution).
//
// "approved" rows bypass the gate: they have already passed the human
// approval path and are always eligible for execution.
func (m *Manager) checkAutoExecuteGate(ctx context.Context, execution *config.PurchaseExecution) error {
	if execution.Status != "pending" && execution.Status != "notified" {
		return nil
	}
	eligible, err := m.executableByScheduler(ctx, execution)
	if err != nil {
		return fmt.Errorf("AutoPurchase gate check failed for execution %s: %w", execution.ExecutionID, err)
	}
	if !eligible {
		return fmt.Errorf("execution %s (status=%s source=%q) is not eligible for auto-execution: AutoPurchase=false, kill switch off, source=web, 4-eyes on (it needs a human approval in the approval queue), or recommendation-bearing row not yet notified - approve it in the approval queue",
			execution.ExecutionID, execution.Status, execution.Source)
	}
	return nil
}

func (m *Manager) handleExecutePurchase(ctx context.Context, msg *AsyncMessage) error {
	if msg.ExecutionID == "" {
		return fmt.Errorf("execution_id required for execute_purchase message")
	}

	execution, err := m.config.GetExecutionByID(ctx, msg.ExecutionID)
	if errors.Is(err, config.ErrNotFound) {
		return fmt.Errorf("execution not found: %s", msg.ExecutionID)
	}
	if err != nil {
		return fmt.Errorf("failed to get execution %s: %w", msg.ExecutionID, err)
	}

	logging.Infof("Executing purchase from async message: %s", msg.ExecutionID)

	// AutoPurchase gate: pending/notified rows require AutoPurchase=true on the
	// owning plan; web-sourced rows must use the token-link approval path.
	// "approved" rows bypass the gate (already human-approved). Fail closed.
	if err := m.checkAutoExecuteGate(ctx, execution); err != nil {
		return err
	}

	// Atomically claim the row before touching the cloud (issue #1013). SQS
	// delivery is at-least-once: a redelivered execute_purchase message (slow
	// purchase whose visibility timeout expired, partial-batch replay, operator
	// re-drive) would otherwise pass the old non-atomic status check and execute
	// the same row a second time concurrently. claimAndExecute CASes
	// approved/pending/notified -> running and only proceeds on a won claim; a
	// lost claim is a benign duplicate that we ack (return nil) without
	// re-executing.
	claimed, execErr := m.claimAndExecute(ctx, execution)
	if !claimed {
		// Either a benign CAS race-loss (execErr == nil — ack the duplicate) or
		// a real DB error during the claim (execErr != nil — surface it so the
		// message is redelivered, since nothing was executed).
		if execErr != nil {
			return fmt.Errorf("failed to claim execution %s: %w", msg.ExecutionID, execErr)
		}
		return nil
	}

	// A multi-account run where at least one account committed must be ACKed,
	// not redelivered (issue #1014): redelivery would re-run the fan-out and,
	// absent the per-account idempotency key (#1012), double-buy the accounts
	// that already succeeded. errAllAccountsFailed (nothing committed) and
	// single-account errors fall through and are returned so SQS redelivers.
	if isMultiAccountAckable(execErr) {
		return nil
	}
	return execErr
}
