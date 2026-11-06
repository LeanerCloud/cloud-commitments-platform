package email

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func outcomeUnknownData() NotificationData {
	return NotificationData{
		DashboardURL:   "https://dashboard.example.com",
		ExecutionID:    "exec-1",
		PlanID:         "plan-1",
		OutcomeUnknown: true,
		Recommendations: []RecommendationSummary{
			{Service: "rds", ResourceType: "db.m5.large", Engine: "postgres", Region: "us-west-2", Count: 2},
		},
	}
}

// Issue #1415: a row a recovery sweep failed after an interrupted run may
// already have bought its commitment, so its email asks for a console check,
// names the execution and never says "retry".
func TestRenderPurchaseFailedEmail_OutcomeUnknown(t *testing.T) {
	body, err := RenderPurchaseFailedEmail(outcomeUnknownData())
	require.NoError(t, err)

	assert.NotContains(t, strings.ToLower(body), "retry")
	assert.NotContains(t, body, "Failed Purchases")
	assert.Contains(t, body, "Purchase Outcome Unknown")
	assert.Contains(t, body, "execution exec-1 (plan plan-1)")
	assert.Contains(t, body, "cloud provider's console")
	assert.Contains(t, body, "- 2x db.m5.large (postgres) in us-west-2\n  Service: rds")
	assert.Contains(t, body, "https://dashboard.example.com/history")
}

func TestRenderPurchaseFailedEmail_OutcomeUnknownWithoutPlan(t *testing.T) {
	data := outcomeUnknownData()
	data.PlanID = ""
	body, err := RenderPurchaseFailedEmail(data)
	require.NoError(t, err)
	assert.Contains(t, body, "execution exec-1 was interrupted")
	assert.NotContains(t, body, "(plan")
}

func TestPurchaseFailedSubject(t *testing.T) {
	assert.Equal(t, "CUDly Purchase Failed", purchaseFailedSubject(NotificationData{}, "CUDly Purchase Failed"))
	got := purchaseFailedSubject(outcomeUnknownData(), "CUDly Purchase Failed")
	assert.Equal(t, purchaseOutcomeUnknownSubject, got)
	assert.NotContains(t, strings.ToLower(got), "retry")
	assert.LessOrEqual(t, len(got), snsMaxSubjectLen)
}

func TestSMTPSender_SendPurchaseFailedNotification_OutcomeUnknown(t *testing.T) {
	for _, tc := range []struct {
		name           string
		outcomeUnknown bool
		wantSubject    string
		wantBody       string
	}{
		{name: "genuine failure", wantSubject: "CUDly Purchase Failed", wantBody: "Please review and retry manually."},
		{name: "outcome unknown", outcomeUnknown: true, wantSubject: purchaseOutcomeUnknownSubject, wantBody: "Outcome unknown:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newMockSMTPServer(t, false)
			server.start(t)
			sender := &SMTPSender{
				host:        "127.0.0.1",
				port:        server.port,
				fromEmail:   "notifications@example.com",
				fromName:    "CUDly",
				notifyEmail: "static-notify@example.com",
			}
			data := outcomeUnknownData()
			data.OutcomeUnknown = tc.outcomeUnknown
			require.NoError(t, sender.SendPurchaseFailedNotification(context.Background(), data))
			server.stop()
			server.mu.Lock()
			msg := server.receivedMsg
			server.mu.Unlock()
			assert.Contains(t, msg, "Subject: "+tc.wantSubject)
			assert.Contains(t, msg, tc.wantBody)
		})
	}
}

func unrecordedCommitmentData() NotificationData {
	data := outcomeUnknownData()
	data.UnrecordedCommitment = true
	data.Recommendations = []RecommendationSummary{
		{Service: "redshift", ResourceType: "ra3.xlplus", Region: "us-east-1", Count: 2, UnrecordedCommitmentID: "rn-123"},
		{Service: "rds", ResourceType: "db.m5.large", Region: "us-west-2", Count: 1},
	}
	return data
}

// cloud-commitments-go#107: a commitment the provider returned but CUDly could
// not record is named, the opening says why, and the email never says "retry".
func TestRenderPurchaseFailedEmail_UnrecordedCommitment(t *testing.T) {
	body, err := RenderPurchaseFailedEmail(unrecordedCommitmentData())
	require.NoError(t, err)

	assert.Contains(t, body, "execution exec-1 (plan plan-1) ended because the provider returned a commitment that CUDly could not record.")
	assert.Contains(t, body, "Redshift reserved node rn-123 was bought but CUDly could not record it; verify it in the cloud provider's console before buying again.")
	assert.Equal(t, 1, strings.Count(body, "Redshift reserved node"), "only the rec that carries an ID gets the line")
	assert.NotContains(t, body, "recovery sweep")
	assert.NotContains(t, body, "was interrupted")
	assert.NotContains(t, strings.ToLower(body), "retry")
}

func TestRenderPurchaseFailedEmail_UnrecordedCommitmentAbsent(t *testing.T) {
	body, err := RenderPurchaseFailedEmail(outcomeUnknownData())
	require.NoError(t, err)

	assert.Contains(t, body, "recovery sweep")
	assert.Contains(t, body, "was interrupted")
	assert.NotContains(t, body, "could not record")
	assert.NotContains(t, body, "Redshift reserved node")
}
