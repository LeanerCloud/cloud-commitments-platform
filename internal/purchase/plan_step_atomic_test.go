package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func stepTestPlan() *config.PurchasePlan {
	next := time.Now().Add(24 * time.Hour)
	return &config.PurchasePlan{ID: "plan-s", RampSchedule: config.RampSchedule{CurrentStep: 1}, NextExecutionDate: &next}
}

func stepRow(status string, mutate func(*config.PurchaseExecution)) config.PurchaseExecution {
	e := config.PurchaseExecution{
		ExecutionID: "row-1", PlanID: "plan-s", StepNumber: 2, Status: status,
		Recommendations: []config.RecommendationRecord{scopedTestRec("acct-A")},
	}
	if mutate != nil {
		mutate(&e)
	}
	return e
}

// E1/E2: any existing row that is not a pending/notified ROOT blocks creation
// quietly, and nothing is written. Mutating adoptableRootRow to a "live status"
// filter makes the failed and partially_completed cases mint a second root.
func TestGetOrCreateExecution_ExistingNonAdoptableRowBlocks(t *testing.T) {
	acct := "acct-A"
	cases := map[string]config.PurchaseExecution{
		"failed root":              stepRow("failed", nil),
		"partially_completed root": stepRow("partially_completed", nil),
		"expired root":             stepRow("expired", nil),
		"canceled root":            stepRow("canceled", nil),
		"completed root":           stepRow("completed", nil),
		"approved root":            stepRow("approved", nil),
		"pending child":            stepRow("pending", func(e *config.PurchaseExecution) { e.CloudAccountID = &acct }),
		"pending retry":            stepRow("pending", func(e *config.PurchaseExecution) { e.RetryAttemptN = 1 }),
	}
	for name, row := range cases {
		t.Run(name, func(t *testing.T) {
			store := new(MockConfigStore)
			plan := stepTestPlan()
			expectStepRows(store, plan, row)
			m := &Manager{config: store}

			exec, _, _, err := m.getOrCreateExecution(context.Background(), plan)

			require.ErrorIs(t, err, errExecutionNotNotifiable)
			assert.Nil(t, exec)
			store.AssertNotCalled(t, "SavePurchaseExecution")
			store.AssertNotCalled(t, "SavePurchaseExecutionTx")
			store.AssertNotCalled(t, "TransitionExecutionStatus")
		})
	}
}

// A pending root with recommendations is adopted (not duplicated) and gets a
// fresh token to email.
func TestGetOrCreateExecution_AdoptsPendingRootOnly(t *testing.T) {
	acct := "acct-A"
	store := new(MockConfigStore)
	plan := stepTestPlan()
	child := stepRow("pending", func(e *config.PurchaseExecution) { e.ExecutionID = "child"; e.CloudAccountID = &acct })
	root := stepRow("notified", func(e *config.PurchaseExecution) { e.ExecutionID = "root" })
	expectStepRows(store, plan, child, root)
	m := &Manager{config: store}

	exec, tok, rotate, err := m.getOrCreateExecution(context.Background(), plan)

	require.NoError(t, err)
	assert.Equal(t, "root", exec.ExecutionID)
	assert.NotEmpty(t, tok)
	assert.True(t, rotate)
}

// The step is keyed on the locked plan's CurrentStep, not on the date.
func TestGetOrCreateExecution_KeysOnStepNotDate(t *testing.T) {
	store := new(MockConfigStore)
	plan := stepTestPlan()
	row := stepRow("pending", nil)
	row.ScheduledDate = time.Now().AddDate(0, 0, -30) // differs from NextExecutionDate
	expectStepRows(store, plan, row)
	m := &Manager{config: store}

	exec, _, _, err := m.getOrCreateExecution(context.Background(), plan)

	require.NoError(t, err)
	assert.Equal(t, "row-1", exec.ExecutionID, "a pre-created row with another date must be adopted, not duplicated")
}

func TestAttachResolvedRecommendations(t *testing.T) {
	recs := []config.RecommendationRecord{scopedTestRec("acct-A"), scopedTestRec("acct-B")}
	t.Run("attaches recs and writes one suppression per tuple in the same tx", func(t *testing.T) {
		store := new(MockConfigStore)
		store.On("GetGlobalConfig", mock.Anything).Return(&config.GlobalConfig{}, nil)
		store.On("SetExecutionRecommendationsIfEmptyTx", mock.Anything, mock.Anything, "row-1", recs, 600.0, 150.0).Return(true, nil)
		store.On("CreateSuppressionTx", mock.Anything, mock.Anything, mock.Anything).Return(nil).Twice()
		exec := &config.PurchaseExecution{ExecutionID: "row-1"}

		ok, err := (&Manager{config: store}).AttachResolvedRecommendations(context.Background(), exec, recs)

		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, 600.0, exec.TotalUpfrontCost)
		assert.Len(t, exec.Recommendations, 2)
		store.AssertNumberOfCalls(t, "CreateSuppressionTx", 2)
	})
	t.Run("lost race writes no suppressions", func(t *testing.T) {
		store := new(MockConfigStore)
		store.On("GetGlobalConfig", mock.Anything).Return(&config.GlobalConfig{}, nil)
		store.On("SetExecutionRecommendationsIfEmptyTx", mock.Anything, mock.Anything, "row-1", recs, 600.0, 150.0).Return(false, nil)
		exec := &config.PurchaseExecution{ExecutionID: "row-1"}

		ok, err := (&Manager{config: store}).AttachResolvedRecommendations(context.Background(), exec, recs)

		require.NoError(t, err)
		assert.False(t, ok)
		assert.Empty(t, exec.Recommendations)
		store.AssertNotCalled(t, "CreateSuppressionTx")
	})
	t.Run("suppression failure surfaces", func(t *testing.T) {
		store := new(MockConfigStore)
		store.On("GetGlobalConfig", mock.Anything).Return(&config.GlobalConfig{}, nil)
		store.On("SetExecutionRecommendationsIfEmptyTx", mock.Anything, mock.Anything, "row-1", recs, 600.0, 150.0).Return(true, nil)
		store.On("CreateSuppressionTx", mock.Anything, mock.Anything, mock.Anything).Return(errors.New("boom"))
		exec := &config.PurchaseExecution{ExecutionID: "row-1"}

		ok, err := (&Manager{config: store}).AttachResolvedRecommendations(context.Background(), exec, recs)

		require.Error(t, err)
		assert.False(t, ok)
		assert.Empty(t, exec.Recommendations, "exec must stay untouched when the tx fails")
	})
	t.Run("empty set refused", func(t *testing.T) {
		_, err := (&Manager{config: new(MockConfigStore)}).AttachResolvedRecommendations(context.Background(), &config.PurchaseExecution{}, nil)
		require.Error(t, err)
	})
}

// E5: a plan row bound to an account refuses recs of another account, or with
// none, before any credential or provider work.
func TestExecuteSingleAccount_PlanRowRecsMustMatchRowAccount(t *testing.T) {
	rowAcct := "acct-A"
	for name, rec := range map[string]config.RecommendationRecord{
		"other account": scopedTestRec("acct-B"),
		"no account":    {Provider: "aws", Service: "ec2", ResourceType: "m5.large", Selected: true},
	} {
		t.Run(name, func(t *testing.T) {
			exec := &config.PurchaseExecution{ExecutionID: "x", PlanID: "plan-s", CloudAccountID: &rowAcct,
				Recommendations: []config.RecommendationRecord{rec}}
			err := (&Manager{config: new(MockConfigStore)}).executeSingleAccount(context.Background(), exec, &config.PurchasePlan{ID: "plan-s"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "is not attributed to it")
		})
	}
	t.Run("direct execute unchanged", func(t *testing.T) {
		exec := &config.PurchaseExecution{ExecutionID: "x", CloudAccountID: &rowAcct,
			Recommendations: []config.RecommendationRecord{scopedTestRec("acct-B")}}
		require.NoError(t, requireRecsMatchRowAccount(exec))
	})
}

// E3: a plan row that ends failed with nothing bought releases its
// suppressions in the same tx as the terminal save; a partial or direct row
// does not.
func TestSaveTerminalExecution_ReleasesSuppressionsOnCleanFailure(t *testing.T) {
	bought := scopedTestRec("acct-A")
	bought.Purchased = true
	cases := []struct {
		name    string
		exec    config.PurchaseExecution
		release bool
	}{
		{"plan row failed, nothing bought", config.PurchaseExecution{ExecutionID: "e", PlanID: "p", Status: "failed", Recommendations: []config.RecommendationRecord{scopedTestRec("acct-A")}}, true},
		{"plan row failed but a rec bought", config.PurchaseExecution{ExecutionID: "e", PlanID: "p", Status: "failed", Recommendations: []config.RecommendationRecord{bought}}, false},
		{"plan row completed", config.PurchaseExecution{ExecutionID: "e", PlanID: "p", Status: "completed"}, false},
		{"direct execute failed", config.PurchaseExecution{ExecutionID: "e", Status: "failed"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := new(MockConfigStore)
			store.On("SavePurchaseExecution", mock.Anything, mock.Anything).Return(nil)
			store.On("DeleteSuppressionsByExecutionTx", mock.Anything, mock.Anything, "e").Return(nil).Maybe()
			exec := c.exec

			require.NoError(t, (&Manager{config: store}).saveTerminalExecution(context.Background(), &exec))

			if c.release {
				store.AssertCalled(t, "DeleteSuppressionsByExecutionTx", mock.Anything, mock.Anything, "e")
			} else {
				store.AssertNotCalled(t, "DeleteSuppressionsByExecutionTx", mock.Anything, mock.Anything, "e")
			}
		})
	}
}
