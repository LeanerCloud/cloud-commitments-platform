//go:build integration

package purchase

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// executionRow reads the columns the announce scenarios assert on.
func (f *fireRig) executionRow(key string) (status string, stamped bool) {
	require.NoError(f.t, f.pool.QueryRow(f.ctx, `SELECT status, notification_sent IS NOT NULL FROM purchase_executions
		WHERE idempotency_key = $1`, key).Scan(&status, &stamped))
	return
}

func (f *fireRig) trancheReason(i int) (status string, reason string) {
	var r *string
	require.NoError(f.t, f.pool.QueryRow(f.ctx, `SELECT status, status_reason FROM ladder_tranches WHERE id = $1`, f.ids[i]).Scan(&status, &r))
	if r != nil {
		reason = *r
	}
	return
}

func (f *fireRig) configEnabled() bool {
	var on bool
	require.NoError(f.t, f.pool.QueryRow(f.ctx, `SELECT enabled FROM ladder_configs WHERE id = $1`, f.cfgID).Scan(&on))
	return on
}

// revoke is what the revoke handler does to a scheduled row.
func (f *fireRig) revoke(executionID string) {
	require.NoError(f.t, f.store.WithTx(f.ctx, func(tx pgx.Tx) error {
		canceled, _, err := f.store.CancelScheduledExecutionAtomic(f.ctx, tx, executionID, ptr("operator@example.com"))
		require.NoError(f.t, err)
		require.True(f.t, canceled)
		return nil
	}))
}

func (f *fireRig) goldenLadderNotice(executionID, trancheID, hourly, annual string) string {
	url := "https://dash.example.com/purchases#history?execution=" + executionID
	return fmt.Sprintf(`
An automatic ladder purchase has been scheduled. It was not approved by a person.

What will be bought:
- Hourly commitment: $%s per hour
- Annual exposure: $%s (hourly commitment x 8760 hours)
- Term: 1 year, no upfront
- Account: c2 (%s)
- Ladder tranche: %s

Scheduled execution time: %s (UTC)

You can revoke this purchase at zero cost until it executes:
%s

Only users with the revoke-any permission and the admin API key can revoke
it. Users who can revoke only their own purchases cannot, because no person
created it.

After that time the cloud SDK call will have been made and revocation
requires a provider support case (for supported providers).

To view or manage this purchase:
%s

-- CUDly
`, hourly, annual, fireRigOwnAccount, trancheID, f.now.Add(48*time.Hour).Format("2006-01-02 15:04 UTC"), url, url)
}

func TestLadderFire_AnnouncedPathStampsAfterTheSend(t *testing.T) {
	f := newFireRig(t)
	n := &captureNotifier{}
	res, err := f.sweep(func(d *LadderFireDeps) { d.Notifier = n })
	require.NoError(t, err)
	assert.Equal(t, 2, res.Announced)
	require.Len(t, n.got, 2)

	for i, want := range []struct{ hourly, annual string }{{"2.50", "21900.00"}, {"3.00", "26280.00"}} {
		status, stamped := f.executionRow(f.ids[i])
		assert.Equal(t, "scheduled", status)
		assert.True(t, stamped, "tranche %d", i)
		assert.Equal(t, "fired", f.status(i))
		var execID string
		require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT execution_id FROM purchase_executions WHERE idempotency_key = $1`, f.ids[i]).Scan(&execID))
		assert.Equal(t, execID, n.got[i].ExecutionID, "the notice must be about this tranche's execution")
		body, renderErr := email.RenderPurchaseScheduledDelayEmail(n.got[i])
		require.NoError(t, renderErr)
		assert.Equal(t, f.goldenLadderNotice(execID, f.ids[i], want.hourly, want.annual), body)
	}
	assert.Equal(t, "scheduled", f.status(2))
}

func TestLadderFire_NothingIsStampedBeforeItsNoticeWasSent(t *testing.T) {
	f := newFireRig(t)
	n := &captureNotifier{}
	n.hook = func(d email.NotificationData) {
		var stamped bool
		require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT notification_sent IS NOT NULL FROM purchase_executions WHERE execution_id = $1`, d.ExecutionID).Scan(&stamped))
		assert.False(t, stamped, "the execution must not be stamped while its notice is being sent")
	}
	_, err := f.sweep(func(d *LadderFireDeps) { d.Notifier = n })
	require.NoError(t, err)
	require.Len(t, n.got, 2)
}

func TestLadderFire_AFailedSendSettlesTheTrancheAndKeepsTheConfigEnabled(t *testing.T) {
	f := newFireRig(t)
	res, err := f.sweep(func(d *LadderFireDeps) { d.Notifier = &captureNotifier{err: errors.New("ses down")} })
	require.NoError(t, err)
	assert.Equal(t, LadderFireResult{Configs: 1, Due: 3, Claimed: 2, Trimmed: 1, NoticeFailed: 2}, res)
	for i := 0; i < 2; i++ {
		status, stamped := f.executionRow(f.ids[i])
		assert.Equal(t, "canceled", status)
		assert.False(t, stamped)
		tStatus, reason := f.trancheReason(i)
		assert.Equal(t, "failed", tStatus)
		assert.Equal(t, "notice_send_failed", reason)
	}
	rec := f.reconcile(false)
	assert.Zero(t, rec.ConfigsDisabled)
	assert.True(t, f.configEnabled())
	status, _ := f.trancheReason(0)
	assert.Equal(t, "failed", status)
}

func TestLadderFire_RevokedWhileTheNoticeIsBeingSentIsNotStamped(t *testing.T) {
	f := newFireRig(t)
	n := &captureNotifier{}
	n.hook = func(d email.NotificationData) { f.revoke(d.ExecutionID) }
	res, err := f.sweep(func(d *LadderFireDeps) { d.Notifier = n })
	require.NoError(t, err)
	assert.Zero(t, res.Announced)
	assert.Zero(t, res.StampFailed)
	for i := 0; i < 2; i++ {
		status, stamped := f.executionRow(f.ids[i])
		assert.Equal(t, "canceled", status)
		assert.False(t, stamped)
	}
	// A revoke is a revoke: the reconciler fails the tranche and disables the config.
	rec := f.reconcile(false)
	assert.Equal(t, 1, rec.ConfigsDisabled)
	for i := 0; i < 2; i++ {
		status, reason := f.trancheReason(i)
		assert.Equal(t, "failed", status)
		assert.Equal(t, "approval_canceled", reason)
	}
	assert.False(t, f.configEnabled())
}

func TestLadderFire_RevokeInsideTheWindowEndToEnd(t *testing.T) {
	f := newFireRig(t)
	_, err := f.sweep(nil)
	require.NoError(t, err)
	before := f.inFlight()
	var execID string
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT execution_id FROM purchase_executions WHERE idempotency_key = $1`, f.ids[0]).Scan(&execID))
	f.revoke(execID)
	rec := f.reconcile(false)
	assert.Equal(t, 1, rec.ConfigsDisabled)
	status, reason := f.trancheReason(0)
	assert.Equal(t, "failed", status)
	assert.Equal(t, "approval_canceled", reason)
	assert.False(t, f.configEnabled())
	assert.InDelta(t, before-5, f.inFlight(), 1e-9, "the revoked 2.5 USD/hr (5 on-demand units) must stop netting")
}

func TestLadderFire_ACanceledExecutionStillCountsAgainstTheCeilingForADay(t *testing.T) {
	f := newFireRig(t)
	_, err := f.sweep(func(d *LadderFireDeps) { d.Notifier = &captureNotifier{err: errors.New("ses down")} })
	require.NoError(t, err) // T1 2.5 and T2 3.0 are claimed, then canceled: 5.5 in flight.
	for _, c := range []struct {
		ceiling float64
		want    int
	}{{9.4, 0}, {9.5, 1}} {
		require.NoError(t, f.store.SetLadderSweepCeiling(f.ctx, f.cfgID, ptr(c.ceiling)))
		res, err := f.sweep(func(d *LadderFireDeps) { d.Notifier = &captureNotifier{} })
		require.NoError(t, err)
		assert.Equal(t, c.want, res.Claimed, "ceiling %v", c.ceiling)
	}
}
