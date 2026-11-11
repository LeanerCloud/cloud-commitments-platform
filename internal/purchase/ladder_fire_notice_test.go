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

// noticeSweep runs the sweep for one config with two claimable tranches and the
// given notifier; the returned store records the stamps.
func noticeSweep(t *testing.T, n *captureNotifier, mut func(*fakeFireStore)) (*fakeFireStore, LadderFireResult, error) {
	t.Helper()
	f := newFireStore(fireCfg("c1"))
	if mut != nil {
		mut(f)
	}
	d := fireDepsFor(f)
	d.Notifier = n
	res, err := RunLadderFire(context.Background(), d)
	return f, res, err
}

func TestAnnounce_OneNoticePerClaimedTrancheInClaimOrder(t *testing.T) {
	n := &captureNotifier{}
	f, res, err := noticeSweep(t, n, nil)
	require.NoError(t, err)
	require.Len(t, n.got, 2)
	require.Len(t, f.saved, 2)
	for i, d := range n.got {
		assert.Equal(t, f.saved[i].ExecutionID, d.ExecutionID)
		assert.Equal(t, "ops@example.com", d.RecipientEmail)
		assert.Equal(t, fireNow.Add(48*time.Hour).Format("2006-01-02 15:04 UTC"), d.RevocationWindowClosesAt)
		assert.Equal(t, "https://dash.example.com/purchases#history?execution="+d.ExecutionID, d.RevokeURL)
		require.NotNil(t, d.Ladder)
		assert.Equal(t, "own-acct", d.Ladder.AccountLabel)
	}
	assert.InDelta(t, 2.5, n.got[0].Ladder.HourlyCommitment, 1e-9)
	assert.InDelta(t, 21900, n.got[0].Ladder.AnnualExposure, 1e-9)
	assert.InDelta(t, 3.0, n.got[1].Ladder.HourlyCommitment, 1e-9)
	assert.Equal(t, 2, res.Announced)
}

func TestAnnounce_StampsOnlyAfterASuccessfulSendAndOutsideTheClaim(t *testing.T) {
	var f *fakeFireStore
	n := &captureNotifier{}
	n.hook = func(d email.NotificationData) {
		assert.True(t, f.claimDone, "the notice must go out after the claim returned")
		for _, e := range f.saved {
			if e.ExecutionID == d.ExecutionID {
				assert.Nil(t, e.NotificationSent, "an execution may not be stamped before its notice was sent")
			}
		}
	}
	f, _, err := noticeSweep(t, n, func(s *fakeFireStore) { f = s })
	require.NoError(t, err)
	require.Len(t, f.stamped, 2)
	assert.Equal(t, []string{"scheduled"}, f.stampExpected)
	for _, e := range f.stamped {
		require.NotNil(t, e.NotificationSent)
		assert.Equal(t, fireNow, *e.NotificationSent)
	}
}

func TestAnnounce_AFailingSendNeverStampsAndDoesNotStopTheRest(t *testing.T) {
	buf := captureLadderLog(t)
	n := &captureNotifier{err: errors.New("ses down")}
	f, res, err := noticeSweep(t, n, nil)
	require.NoError(t, err)
	assert.Len(t, n.got, 2, "one tranche's failure must not stop the other")
	assert.Empty(t, f.stamped)
	assert.Equal(t, LadderFireResult{Configs: 1, Due: 2, Claimed: 2, NoticeFailed: 2}, res)
	assert.Contains(t, buf.String(), "ERROR")
}

func TestAnnounce_RevokedBeforeTheStampIsInfoNotAnError(t *testing.T) {
	buf := captureLadderLog(t)
	_, res, err := noticeSweep(t, &captureNotifier{}, func(f *fakeFireStore) { f.stampErr = config.ErrExecutionNotInExpectedStatus })
	require.NoError(t, err)
	assert.Equal(t, LadderFireResult{Configs: 1, Due: 2, Claimed: 2}, res)
	assert.NotContains(t, buf.String(), "ERROR")
	assert.Contains(t, buf.String(), "revoked before it could be stamped")
}

func TestAnnounce_StampErrorIsCountedAndTheSweepContinues(t *testing.T) {
	buf := captureLadderLog(t)
	f, res, err := noticeSweep(t, &captureNotifier{}, func(f *fakeFireStore) { f.stampErr = errors.New("db down") })
	require.NoError(t, err)
	assert.Len(t, f.stamped, 2)
	assert.Equal(t, 2, res.StampFailed)
	assert.Zero(t, res.Announced)
	assert.Contains(t, buf.String(), "will not fire")
}

func TestAnnounce_NotEnoughTimeDefersTheRest(t *testing.T) {
	buf := captureLadderLog(t)
	prevCap, prevBudget := ladderNoticeLoopCap, ladderNoticeBudget
	ladderNoticeLoopCap, ladderNoticeBudget = 150*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { ladderNoticeLoopCap, ladderNoticeBudget = prevCap, prevBudget })
	n := &captureNotifier{hook: func(email.NotificationData) { time.Sleep(80 * time.Millisecond) }}
	f, res, err := noticeSweep(t, n, nil)
	require.NoError(t, err)
	assert.Len(t, n.got, 1, "the second send must not start")
	assert.Len(t, f.stamped, 1)
	assert.Equal(t, 1, res.Announced)
	assert.Equal(t, 1, res.NoticeDeferred)
	assert.Contains(t, buf.String(), "not enough time left")
}

func TestAnnounce_ACanceledParentContextStillSendsAndStamps(t *testing.T) {
	f := newFireStore(fireCfg("c1"))
	n := &captureNotifier{}
	d := fireDepsFor(f)
	d.Notifier = n
	exec := newLadderExecution(dueRow("T1", 2.5, true).Tranche, mustBuild(t, 2.5), &config.LadderConfigDB{CloudAccountID: "acct-a"}, 48*time.Hour, fireNow)
	plan := &ladderFirePlan{global: fireGlobal(nil), acct: &config.CloudAccount{Name: "prod", ExternalID: "own-acct"}}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second)) // already done.
	defer cancel()
	var res LadderFireResult
	announceClaimed(ctx, d, plan, []config.LadderTrancheDB{{ID: "T1"}}, map[string]*config.PurchaseExecution{"T1": exec}, &res)
	assert.Equal(t, 1, res.Announced)
	assert.Equal(t, "prod (own-acct)", n.got[0].Ladder.AccountLabel)
}

func mustBuild(t *testing.T, amount float64) []config.RecommendationRecord {
	t.Helper()
	recs, err := NewLadderInstrumentRegistry().Build(spInput(spTranche(amount, "1yr")))
	require.NoError(t, err)
	return recs
}

func TestLadderNoticeFacts_UseTheFlooredAmountThatIsBought(t *testing.T) {
	exec := newLadderExecution(dueRow("T1", 0.123456, true).Tranche, mustBuild(t, 0.123456), &config.LadderConfigDB{CloudAccountID: "acct-a"}, time.Hour, fireNow)
	facts, err := ladderNoticeFactsFor(&config.LadderTrancheDB{ID: "T1", AmountUSDHr: 0.123456}, exec, &config.CloudAccount{ExternalID: "x"})
	require.NoError(t, err)
	assert.InDelta(t, 0.12345, facts.HourlyCommitment, 1e-12)
	assert.Equal(t, 1, facts.TermYears)
	_, err = ladderNoticeFactsFor(&config.LadderTrancheDB{}, &config.PurchaseExecution{}, &config.CloudAccount{})
	require.Error(t, err)
}
