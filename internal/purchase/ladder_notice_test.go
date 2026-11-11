package purchase

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureNotifier records every notice; hook runs inside the send and err is
// returned after it.
type captureNotifier struct {
	got  []email.NotificationData
	hook func(email.NotificationData)
	err  error
}

func (c *captureNotifier) SendPurchaseScheduledNotification(ctx context.Context, d email.NotificationData) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.got = append(c.got, d)
	if c.hook != nil {
		c.hook(d)
	}
	return c.err
}

func ladderNoticeExec() *config.PurchaseExecution {
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	return &config.PurchaseExecution{ExecutionID: "exec-1", ScheduledExecutionAt: &at, CreatedBySystem: true, IdempotencyKey: "tr-1"}
}

func TestSendLadderNotice_CarriesTheLadderFacts(t *testing.T) {
	n := &captureNotifier{}
	err := SendLadderNotice(context.Background(), n, fireGlobal(nil), "https://dash.example.com", ladderNoticeExec(),
		LadderNoticeFacts{HourlyCommitment: 2.5, TermYears: 1, AccountLabel: "prod", TrancheID: "tr-1"})
	require.NoError(t, err)
	require.Len(t, n.got, 1)
	d := n.got[0]
	require.NotNil(t, d.Ladder)
	assert.InDelta(t, 21900, d.Ladder.AnnualExposure, 1e-9) // 2.5 x 8760, not 730.
	assert.Equal(t, 1, d.Ladder.TermYears)
	assert.Equal(t, "prod", d.Ladder.AccountLabel)
	assert.Equal(t, "tr-1", d.Ladder.TrancheID)
	assert.Equal(t, "ops@example.com", d.RecipientEmail)
	assert.Equal(t, "2026-11-12 12:00 UTC", d.RevocationWindowClosesAt)
	assert.Equal(t, "https://dash.example.com/purchases#history?execution=exec-1", d.RevokeURL)

	body, err := email.RenderPurchaseScheduledDelayEmail(d)
	require.NoError(t, err)
	assert.Contains(t, body, "21900.00")
}

func TestSendScheduledNotice_SystemMintedExecutionWithoutFactsKeepsTheOldNotice(t *testing.T) {
	n := &captureNotifier{}
	require.NoError(t, SendScheduledNotice(context.Background(), n, fireGlobal(nil), "https://dash.example.com", ladderNoticeExec(), ""))
	require.Len(t, n.got, 1)
	assert.Nil(t, n.got[0].Ladder)
	body, err := email.RenderPurchaseScheduledDelayEmail(n.got[0])
	require.NoError(t, err)
	assert.Contains(t, body, "has been approved")
}

func TestSendLadderNotice_RefusesWithoutNotifierOrExecutionID(t *testing.T) {
	assert.ErrorIs(t, SendLadderNotice(context.Background(), nil, fireGlobal(nil), "", ladderNoticeExec(), LadderNoticeFacts{}), ErrNoScheduledNotifier)
	assert.ErrorIs(t, SendLadderNotice(context.Background(), &captureNotifier{}, fireGlobal(nil), "", &config.PurchaseExecution{}, LadderNoticeFacts{}), ErrNoExecutionID)
}
