package email

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ladderNoticeData() NotificationData {
	return NotificationData{
		DashboardURL:             "https://dash.example.com",
		ExecutionID:              "exec-1",
		RevokeURL:                "https://dash.example.com/purchases#history?execution=exec-1",
		RevocationWindowClosesAt: "2026-11-12 12:00 UTC",
		Ladder: &LadderNoticeData{
			HourlyCommitment: 2.5, AnnualExposure: 21900, TermYears: 1, AccountLabel: "prod (acct-a)", TrancheID: "tr-1",
		},
	}
}

func TestRenderPurchaseScheduledDelayEmail_LadderGolden(t *testing.T) {
	body, err := RenderPurchaseScheduledDelayEmail(ladderNoticeData())
	require.NoError(t, err)
	assert.Equal(t, `
An automatic ladder purchase has been scheduled. It was not approved by a person.

What will be bought:
- Hourly commitment: $2.50 per hour
- Annual exposure: $21900.00 (hourly commitment x 8760 hours)
- Term: 1 year, no upfront
- Account: prod (acct-a)
- Ladder tranche: tr-1

Scheduled execution time: 2026-11-12 12:00 UTC (UTC)

You can revoke this purchase at zero cost until it executes:
https://dash.example.com/purchases#history?execution=exec-1

Only users with the revoke-any permission and the admin API key can revoke
it. Users who can revoke only their own purchases cannot, because no person
created it.

After that time the cloud SDK call will have been made and revocation
requires a provider support case (for supported providers).

To view or manage this purchase:
https://dash.example.com/purchases#history?execution=exec-1

-- CUDly
`, body)
	assert.NotContains(t, body, "$0.00")
	assert.NotContains(t, body, "has been approved")
}

func TestRenderPurchaseScheduledDelayEmail_MultiYearTerm(t *testing.T) {
	d := ladderNoticeData()
	d.Ladder.TermYears = 3
	body, err := RenderPurchaseScheduledDelayEmail(d)
	require.NoError(t, err)
	assert.Contains(t, body, "Term: 3 years, no upfront")
}

func TestRenderPurchaseScheduledDelayEmail_WithoutLadderFactsIsTheOldText(t *testing.T) {
	d := ladderNoticeData()
	d.Ladder = nil
	body, err := RenderPurchaseScheduledDelayEmail(d)
	require.NoError(t, err)
	assert.Contains(t, body, "Your CUDly purchase has been approved and is scheduled to execute.")
	assert.Contains(t, body, "- Total upfront cost: $0.00")
	assert.NotContains(t, body, "ladder")
}

func TestPurchaseScheduledSubject(t *testing.T) {
	assert.Equal(t, "CUDly - Automatic ladder purchase scheduled for 2026-11-12 12:00 UTC", purchaseScheduledSubject(ladderNoticeData()))
	d := ladderNoticeData()
	d.Ladder = nil
	assert.Equal(t, "CUDly - Purchase Scheduled for 2026-11-12 12:00 UTC", purchaseScheduledSubject(d))
}

func TestLadderNoticeData_HourlyText(t *testing.T) {
	for in, want := range map[float64]string{
		2.5: "2.50", 0.001: "0.001", 0.12345: "0.12345", 0.1: "0.10", 7: "7.00", 0.0012: "0.0012", 1000000: "1000000.00",
	} {
		assert.Equal(t, want, (&LadderNoticeData{HourlyCommitment: in}).HourlyText(), in)
	}
}

func TestRenderPurchaseScheduledDelayEmail_TinyCommitmentIsNeverZero(t *testing.T) {
	d := ladderNoticeData()
	d.Ladder.HourlyCommitment, d.Ladder.AnnualExposure = 0.001, 8.76
	body, err := RenderPurchaseScheduledDelayEmail(d)
	require.NoError(t, err)
	assert.Contains(t, body, "Hourly commitment: $0.001 per hour")
	assert.NotContains(t, body, "$0.00 per hour")
	assert.Contains(t, body, "Annual exposure: $8.76")
}
