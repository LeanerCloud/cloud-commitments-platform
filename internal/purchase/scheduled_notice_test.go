package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeScheduledNotifier struct {
	got email.NotificationData
	err error
}

func (f *fakeScheduledNotifier) SendPurchaseScheduledNotification(_ context.Context, d email.NotificationData) error {
	f.got = d
	return f.err
}

func TestSendScheduledNotice_NilNotifier(t *testing.T) {
	err := SendScheduledNotice(context.Background(), nil, &config.GlobalConfig{}, "", &config.PurchaseExecution{ExecutionID: "e1"}, "a")
	assert.ErrorIs(t, err, ErrNoScheduledNotifier)
}

func TestSendScheduledNotice_EmptyID(t *testing.T) {
	n := &fakeScheduledNotifier{}
	err := SendScheduledNotice(context.Background(), n, &config.GlobalConfig{}, "", &config.PurchaseExecution{}, "a")
	assert.ErrorIs(t, err, ErrNoExecutionID)
	assert.Empty(t, n.got.ExecutionID)
}

func TestSendScheduledNotice_SendErrorWrapped(t *testing.T) {
	boom := errors.New("smtp down")
	n := &fakeScheduledNotifier{err: boom}
	err := SendScheduledNotice(context.Background(), n, &config.GlobalConfig{}, "", &config.PurchaseExecution{ExecutionID: "e1"}, "a")
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "e1")
}

func TestSendScheduledNotice_RecipientAndRevokeURL(t *testing.T) {
	closes := time.Date(2026, 11, 5, 14, 30, 0, 0, time.FixedZone("x", 3600))
	notify := "ops@example.com"
	exec := &config.PurchaseExecution{ExecutionID: "e1", ScheduledExecutionAt: &closes}

	cases := []struct {
		name, dashboard, actor, wantTo, wantRevoke string
		cfg                                        *config.GlobalConfig
	}{
		{"global email wins", "https://d.example", "bob@example.com", notify, "https://d.example/purchases#history?execution=e1", &config.GlobalConfig{NotificationEmail: &notify}},
		{"actor fallback, no dashboard", "", "bob@example.com", "bob@example.com", "", &config.GlobalConfig{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := &fakeScheduledNotifier{}
			require.NoError(t, SendScheduledNotice(context.Background(), n, tc.cfg, tc.dashboard, exec, tc.actor))
			assert.Equal(t, tc.wantTo, n.got.RecipientEmail)
			assert.Equal(t, tc.wantRevoke, n.got.RevokeURL)
			assert.Equal(t, "2026-11-05 13:30 UTC", n.got.RevocationWindowClosesAt)
		})
	}
}

// The summaries and totals are what the notice shows; each field must come
// from the stored recommendation, not a default.
func TestBuildScheduledNoticeData_SummariesAndTotals(t *testing.T) {
	exec := &config.PurchaseExecution{
		ExecutionID:      "e1",
		TotalUpfrontCost: 120.5,
		EstimatedSavings: 33.25,
		Recommendations: []config.RecommendationRecord{{
			Service: "ec2", ResourceType: "m5.large", Region: "eu-west-1",
			Count: 3, Term: 1, Payment: "no-upfront", UpfrontCost: 0,
		}},
	}
	d := BuildScheduledNoticeData("", exec, &config.GlobalConfig{}, "")
	require.Len(t, d.Recommendations, 1)
	got := d.Recommendations[0]
	assert.Equal(t, "ec2", got.Service)
	assert.Equal(t, "m5.large", got.ResourceType)
	assert.Equal(t, "eu-west-1", got.Region)
	assert.Equal(t, 3, got.Count)
	assert.Equal(t, 1, got.Term)
	assert.Equal(t, "no-upfront", got.Payment)
	assert.Equal(t, 120.5, d.TotalUpfrontCost)
	assert.Equal(t, 33.25, d.TotalSavings)
	assert.Equal(t, "e1", d.ExecutionID)
}

func TestBuildScheduledNoticeData_NoWindow(t *testing.T) {
	d := BuildScheduledNoticeData("", &config.PurchaseExecution{ExecutionID: "e1"}, &config.GlobalConfig{}, "")
	assert.Empty(t, d.RevocationWindowClosesAt)
	assert.Empty(t, d.RecipientEmail)
}
