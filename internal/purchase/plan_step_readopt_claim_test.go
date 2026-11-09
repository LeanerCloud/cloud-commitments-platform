package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/planstep"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Claim-side re-adoption (#1785): a fake CAS over the mock store; the
// real-PostgreSQL behavior is in expired_root_readopt_integration_test.go.

type readoptClaim struct {
	m     *Manager
	store *readoptStore
	plan  *config.PurchasePlan
	rows  []config.PurchaseExecution
	date  time.Time
}

// newReadoptClaim builds an overdue manual plan with one expired root. The fake
// CAS returns the root re-armed to pending with no recommendations.
func newReadoptClaim(t *testing.T, global *config.GlobalConfig, res *fakeResolver, mutate func(*config.PurchasePlan)) *readoptClaim {
	t.Helper()
	plan := attachPlan(time.Now().Add(-48*time.Hour), 1e6)
	plan.AutoPurchase = false
	if mutate != nil {
		mutate(plan)
	}
	root := stepRow(config.StatusExpired, func(e *config.PurchaseExecution) {
		e.ExecutionID, e.PlanID, e.StepNumber = "root", plan.ID, 1
		e.Recommendations = attachRecs()[:1]
	})
	mocked := new(MockConfigStore)
	assertMocksOnCleanup(t, mocked)
	mocked.On("GetGlobalConfig", mock.Anything).Return(global, nil).Maybe()
	mocked.On("SetExecutionRecommendationsIfEmptyTx", mock.Anything, mock.Anything, "root", mock.Anything, mock.Anything, mock.Anything).Return(true, nil).Maybe()
	rc := &readoptClaim{plan: plan, rows: []config.PurchaseExecution{root}, store: &readoptStore{MockConfigStore: mocked}}
	rc.store.cas = func(_ string, date time.Time, _ bool) (*config.PurchaseExecution, error) {
		rc.date = date
		armed := root
		armed.Status, armed.Recommendations, armed.ScheduledDate = "pending", nil, date
		return &armed, nil
	}
	rc.m = &Manager{config: rc.store, resolver: res}
	return rc
}

func (rc *readoptClaim) claim() (stepClaim, error) {
	return rc.m.claimFromRows(context.Background(), nil, rc.plan, stepClaim{number: 1}, rc.rows)
}

func TestReadoptClaim_OnlyApprovalNeedingPlansReachTheCAS(t *testing.T) {
	for _, tc := range []struct {
		name    string
		global  *config.GlobalConfig
		mutate  func(*config.PurchasePlan)
		wantCAS bool
	}{
		{"manual plan", &config.GlobalConfig{}, nil, true},
		{"auto plan with 4-eyes on", &config.GlobalConfig{AutoPurchaseEnabled: true, RequireDifferentApprover: true}, func(p *config.PurchasePlan) { p.AutoPurchase = true }, true},
		{"auto plan with 4-eyes off", &config.GlobalConfig{AutoPurchaseEnabled: true}, func(p *config.PurchasePlan) { p.AutoPurchase = true }, false},
		{"auto plan with the kill switch off", &config.GlobalConfig{RequireDifferentApprover: true}, func(p *config.PurchasePlan) { p.AutoPurchase = true }, false},
		{"nil global config", nil, nil, false},
		{"dates managed plan", &config.GlobalConfig{}, func(p *config.PurchasePlan) { p.RampSchedule.DatesManaged = true }, false},
		{"no veto window", &config.GlobalConfig{}, func(p *config.PurchasePlan) { p.NotificationDaysBefore = 0 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := &fakeResolver{recs: attachRecs()}
			rc := newReadoptClaim(t, tc.global, res, tc.mutate)
			claim, err := rc.claim()
			require.NoError(t, err)
			if !tc.wantCAS {
				assert.Zero(t, rc.store.casCall)
				assert.Zero(t, res.calls.Load())
				require.ErrorIs(t, claim.blocked, errExecutionNotNotifiable, "the step stays blocked as before")
				return
			}
			assert.Equal(t, 1, rc.store.casCall)
			require.NoError(t, claim.blocked)
			require.NotNil(t, claim.existing)
		})
	}
}

func TestReadoptClaim_AttachesFreshRecsAtTheCASDate(t *testing.T) {
	res := &fakeResolver{recs: attachRecs()}
	rc := newReadoptClaim(t, &config.GlobalConfig{RequireDifferentApprover: true}, res, nil)
	var gotApprover bool
	cas := rc.store.cas
	rc.store.cas = func(id string, date time.Time, requireDifferentApprover bool) (*config.PurchaseExecution, error) {
		gotApprover = requireDifferentApprover
		return cas(id, date, requireDifferentApprover)
	}
	claim, err := rc.claim()
	require.NoError(t, err)
	require.NotNil(t, claim.existing)
	assert.True(t, gotApprover, "the CAS gets the 4-eyes setting")
	assert.Equal(t, 1, int(res.calls.Load()))
	require.Len(t, claim.existing.Recommendations, 2, "the old recommendations are replaced by the resolved ones")
	assert.Equal(t, "rec-a", claim.existing.Recommendations[0].ID)
	assert.True(t, claim.existing.ScheduledDate.Equal(rc.date), "the row carries the CAS date, not the expired one")
	assert.WithinDuration(t, time.Now().Add(3*24*time.Hour), rc.date, time.Minute, "a full veto window from now")
}

func TestReadoptClaim_NotReArmableRowsStayBlocked(t *testing.T) {
	rc := newReadoptClaim(t, &config.GlobalConfig{}, &fakeResolver{recs: attachRecs()}, nil)
	rc.rows = append(rc.rows, stepRow("failed", func(e *config.PurchaseExecution) { e.ExecutionID, e.StepNumber = "sibling", 1 }))
	claim, err := rc.claim()
	require.NoError(t, err)
	assert.Zero(t, rc.store.casCall)
	require.ErrorIs(t, claim.blocked, errExecutionNotNotifiable)
}

func TestReadoptClaim_CASOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		wantSkipped bool
	}{
		{"lost CAS is the quiet skip", config.ErrExecutionNotInExpectedStatus, true},
		{"unmigrated schema is the quiet skip", config.ErrReadoptNotReady, true},
		{"any other error is real", errors.New("db down"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := &fakeResolver{recs: attachRecs()}
			rc := newReadoptClaim(t, &config.GlobalConfig{}, res, nil)
			rc.store.cas = func(string, time.Time, bool) (*config.PurchaseExecution, error) { return nil, tc.err }
			_, err := rc.claim()
			require.Error(t, err)
			assert.Equal(t, tc.wantSkipped, errors.Is(err, errPlanStepSkipped))
			assert.Zero(t, res.calls.Load())
		})
	}
}

func TestReadoptClaim_NoMatchWaitsForGraceThenFailsTheRootLoud(t *testing.T) {
	for _, tc := range []struct {
		name     string
		next     time.Duration
		wantFail bool
	}{
		{"before the deadline", -2 * 24 * time.Hour, false},
		{"after the deadline", -10 * 24 * time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := newReadoptClaim(t, &config.GlobalConfig{}, &fakeResolver{err: planstep.ErrNoMatch}, func(p *config.PurchasePlan) {
				next := time.Now().Add(tc.next)
				p.NextExecutionDate = &next
			})
			claim, err := rc.claim()
			if !tc.wantFail {
				// The tx rolls back, so the root stays expired and unmarked; the
				// next tick tries again, unthrottled and bounded by the grace deadline.
				require.ErrorIs(t, err, errPlanStepSkipped)
				_, err = rc.claim()
				require.ErrorIs(t, err, errPlanStepSkipped)
				assert.Equal(t, 2, rc.store.casCall)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, claim.emptyRoot, "the re-armed root is handed back to be failed")
			assert.Equal(t, "root", claim.emptyRoot.ExecutionID)
			assert.Contains(t, claim.failureReason, "no current recommendation matched")
		})
	}
}

func TestReadoptClaim_CapBreachFailsTheRoot(t *testing.T) {
	rc := newReadoptClaim(t, &config.GlobalConfig{}, &fakeResolver{recs: attachRecs()}, func(p *config.PurchasePlan) {
		limit := 5.0
		p.MaxStepCommitment = &limit
	})
	claim, err := rc.claim()
	require.NoError(t, err)
	require.NotNil(t, claim.emptyRoot)
	assert.Contains(t, claim.failureReason, "exceeds max_step_commitment")
}

func TestReadoptClaim_StoreWithoutTheCapabilityStaysBlocked(t *testing.T) {
	rc := newReadoptClaim(t, &config.GlobalConfig{}, &fakeResolver{recs: attachRecs()}, nil)
	rc.m.config = rc.store.MockConfigStore
	claim, err := rc.claim()
	require.NoError(t, err)
	require.ErrorIs(t, claim.blocked, errExecutionNotNotifiable)
}
