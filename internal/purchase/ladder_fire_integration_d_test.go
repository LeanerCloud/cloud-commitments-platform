//go:build integration

package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// fireManager is a Manager over the rig's store whose provider records every
// purchase (a labeled fixture: nothing reaches a cloud API).
type fireManager struct {
	*Manager
	purchases    []common.Recommendation
	factoryCalls int
}

func (f *fireRig) manager(provider bool) *fireManager {
	fm := &fireManager{}
	mockEmail := new(MockEmailSender)
	mockEmail.On("SendPurchaseConfirmation", mock.Anything, mock.Anything).Return(nil).Maybe()
	mockEmail.On("SendPurchaseFailedNotification", mock.Anything, mock.Anything).Return(nil).Maybe()
	// Credential resolution must succeed, so a refusal below is the recheck's.
	_, err := f.pool.Exec(f.ctx, `UPDATE cloud_accounts SET aws_auth_mode = 'access_keys' WHERE id = $1`, f.acctID())
	require.NoError(f.t, err)
	factory := new(MockProviderFactory)
	if !provider {
		// The Manager recovers a mock's unexpected-call panic into a failed row,
		// so count the calls instead of relying on the mock failing the test.
		factory.On("CreateAndValidateProvider", mock.Anything, mock.Anything, mock.Anything).
			Run(func(mock.Arguments) { fm.factoryCalls++ }).Return(nil, errors.New("fixture: no provider expected"))
	}
	if provider {
		prov, client := new(MockProvider), new(MockServiceClient)
		factory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(prov, nil)
		prov.On("GetServiceClient", mock.Anything, mock.Anything, mock.Anything).Return(client, nil)
		client.On("PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) { fm.purchases = append(fm.purchases, args.Get(1).(common.Recommendation)) }).
			Return(common.PurchaseResult{Success: true, CommitmentID: "sp-fixture"}, nil)
	}
	fm.Manager = &Manager{config: f.store, email: mockEmail, providerFactory: factory,
		credStore: awsAccessKeyCredStore(), dashboardURL: "https://dash.example.com"}
	return fm
}

func (f *fireRig) firstExecution() *config.PurchaseExecution {
	var id string
	require.NoError(f.t, f.pool.QueryRow(f.ctx, `SELECT execution_id FROM purchase_executions WHERE idempotency_key = $1`, f.ids[0]).Scan(&id))
	row, err := f.store.GetExecutionByID(f.ctx, id)
	require.NoError(f.t, err)
	return row
}

func (f *fireRig) pastWindow() {
	_, err := f.pool.Exec(f.ctx, `UPDATE purchase_executions SET scheduled_execution_at = NOW() - interval '1 minute'
		WHERE idempotency_key = $1`, f.ids[0])
	require.NoError(f.t, err)
}

// A crash between the claim commit and the notice leaves an unannounced
// execution: the recheck refuses it, the reconciler fails the tranche and the
// config is disabled for an operator to re-enable. No spend.
func TestLadderFire_CrashBeforeTheNoticeCostsADisableNotASpend(t *testing.T) {
	f := newFireRig(t)
	d := f.deps()
	execs := map[string]*config.PurchaseExecution{}
	_, err := f.store.ClaimDueLadderTranches(f.ctx, f.cfgID, f.now, ladderFireClaimLimit,
		config.LadderClaimFilter{Layers: d.Registry.Layers(), TrancheIDs: []string{f.ids[0]}, CeilingMicros: 6_000_000},
		ladderClaimCreate(f.store, d.Registry, &config.LadderConfigDB{CloudAccountID: f.acctID()}, d.Region, 48*time.Hour, f.now, execs))
	require.NoError(t, err)
	f.pastWindow()

	m := f.manager(false)
	assert.Equal(t, fireErrored, m.fireOneDue(f.ctx, f.firstExecution()))
	got := f.firstExecution()
	assert.Equal(t, "failed", got.Status)
	assert.True(t, got.NoSubmission)
	assert.Contains(t, got.Error, ladderNoticeNotSentNote)
	assert.Zero(t, m.factoryCalls, "a refused execution must never reach a provider")

	rec := f.reconcile(false)
	assert.Equal(t, 1, rec.ConfigsDisabled)
	status, reason := f.trancheReason(0)
	assert.Equal(t, "failed", status)
	assert.Equal(t, "no_submission", reason)
	assert.False(t, f.configEnabled())
}

func (f *fireRig) acctID() string {
	var id string
	require.NoError(f.t, f.pool.QueryRow(f.ctx, `SELECT cloud_account_id FROM ladder_configs WHERE id = $1`, f.cfgID).Scan(&id))
	return id
}

// A stamped execution whose window has passed is the one thing that buys, and
// it buys exactly the tranche's Savings Plan.
func TestLadderFire_AStampedExecutionBuysTheTrancheOnceItsWindowPasses(t *testing.T) {
	f := newFireRig(t)
	_, err := f.sweep(nil)
	require.NoError(t, err)
	f.pastWindow()

	m := f.manager(true)
	assert.Equal(t, fireFired, m.fireOneDue(f.ctx, f.firstExecution()))
	require.Len(t, m.purchases, 1)
	assert.Equal(t, "1yr", m.purchases[0].Term)
	assert.Equal(t, "no-upfront", m.purchases[0].PaymentOption)
	assert.Equal(t, common.ServiceSavingsPlansCompute, m.purchases[0].Service)
	assert.Equal(t, 1, m.purchases[0].Count)
	assert.InDelta(t, 2.5, hourlyOfDetails(t, m.purchases[0]), 1e-9)
	assert.Equal(t, "completed", f.firstExecution().Status)
	assert.Equal(t, 1, f.reconcile(true).Completed, "the tranche settles through the existing completion path")
	status, reason := f.trancheReason(0)
	assert.Equal(t, "completed", status)
	assert.Equal(t, "commitment_visible", reason)
}

func hourlyOfDetails(t *testing.T, r common.Recommendation) float64 {
	t.Helper()
	d, ok := r.Details.(*common.SavingsPlanDetails)
	require.True(t, ok, "%T", r.Details)
	return d.HourlyCommitment
}

func TestLadderFire_SwitchesOffAtFireTimeRefuseEvenAStampedExecution(t *testing.T) {
	f := newFireRig(t)
	_, err := f.sweep(nil)
	require.NoError(t, err)
	f.pastWindow()
	f.global(func(g *config.GlobalConfig) { g.LadderExecutionEnabled = false })

	m := f.manager(false)
	assert.Equal(t, fireErrored, m.fireOneDue(f.ctx, f.firstExecution()))
	got := f.firstExecution()
	assert.Equal(t, "failed", got.Status)
	assert.True(t, got.NoSubmission)
	assert.Contains(t, got.Error, "ladder execution is switched off")
	assert.Zero(t, m.factoryCalls, "a refused execution must never reach a provider")
}

// failingTransitionStore fails the tranche transition, which must roll the
// execution cancel back with it.
type failingTransitionStore struct{ *config.PostgresStore }

func (s failingTransitionStore) TransitionLadderTrancheTx(context.Context, pgx.Tx, string, ladder.TrancheStatus, ladder.TrancheStatus, string) (bool, error) {
	return false, errors.New("transition failed")
}

func TestLadderFire_SettleIsAtomic(t *testing.T) {
	f := newFireRig(t)
	res, err := f.sweep(func(d *LadderFireDeps) {
		d.Notifier = &captureNotifier{err: errors.New("ses down")}
		d.Store = failingTransitionStore{f.store}
	})
	require.NoError(t, err)
	assert.Equal(t, 2, res.SettleFailed)
	for i := 0; i < 2; i++ {
		status, stamped := f.executionRow(f.ids[i])
		assert.Equal(t, "scheduled", status, "the cancel must roll back with the failed transition")
		assert.False(t, stamped)
		assert.Equal(t, "fired", f.status(i))
	}
}

func TestLadderFire_ARevokedTailIsLeftToTheReconciler(t *testing.T) {
	f := newFireRig(t)
	n := &captureNotifier{err: errors.New("ses down")}
	n.hook = func(d email.NotificationData) { f.revoke(d.ExecutionID) } // the operator revokes first.
	res, err := f.sweep(func(d *LadderFireDeps) { d.Notifier = n })
	require.NoError(t, err)
	assert.Zero(t, res.SettleFailed)
	execStatus, _ := f.executionRow(f.ids[0])
	assert.Equal(t, "canceled", execStatus)
	assert.Equal(t, "fired", f.status(0), "settle must not touch a tranche whose execution was revoked")
	rec := f.reconcile(false)
	assert.Equal(t, 1, rec.ConfigsDisabled, "a revoke disables the config through D5")
	status, reason := f.trancheReason(0)
	assert.Equal(t, "failed", status)
	assert.Equal(t, "approval_canceled", reason)
	assert.False(t, f.configEnabled())
}
