package purchase

// #1844 S1: a fan-out root records no_submission, and releases its
// suppressions, only when no account committed and every account provably
// submitted nothing (refused before submission, never started, or nothing to
// buy). Each case runs the real executeMultiAccount through the two-account
// fixture, then replays the root's terminal save. A wrong "proven" releases
// suppressions for a commitment that may exist and lets the next step buy it
// again, so every ending that may have reached the cloud must stay unproven.

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/execution"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestExecuteMultiAccount_RootProof(t *testing.T) {
	bothRefused := map[string]recBehavior{skipAcctA: recRefused, skipAcctB: recRefused}
	withA := func(a recBehavior) map[string]recBehavior {
		return map[string]recBehavior{skipAcctA: a, skipAcctB: recRefused}
	}
	cases := map[string]struct {
		fx        fanOutFixture
		wantProof bool
		partial   bool
	}{
		"1 all accounts refused":                      {fx: fanOutFixture{behaviors: bothRefused}, wantProof: true},
		"2 one bought, one refused":                   {fx: fanOutFixture{behaviors: withA(recBuys)}, partial: true},
		"3 one unknown outcome, one refused":          {fx: fanOutFixture{behaviors: withA(recUnknown)}},
		"4 refused, the other never started":          {fx: fanOutFixture{behaviors: bothRefused, cancelAfterSave: skipAcctA}, wantProof: true},
		"5 all never started":                         {fx: fanOutFixture{behaviors: bothRefused, precancel: true}, wantProof: true},
		"5b started account cut off by the context":   {fx: fanOutFixture{behaviors: bothRefused, cancelAtCred: skipAcctA}, wantProof: true}, // cut off in credential load: nothing submitted (#1844 S2)
		"7 one panics after submitting, one refused":  {fx: fanOutFixture{behaviors: withA(recPanics)}},
		"8 refused wrapping unknown, one refused":     {fx: fanOutFixture{behaviors: withA(recRefusedUnknown)}},
		"9 audit loss on a refused account's row":     {fx: fanOutFixture{behaviors: bothRefused, saveFail: map[string]bool{skipAcctA: true}}},
		"9b audit loss on a pin-refused account":      {fx: fanOutFixture{behaviors: bothRefused, pinFail: true, saveFail: map[string]bool{skipAcctA: true}}},
		"pin refused for every account":               {fx: fanOutFixture{behaviors: bothRefused, pinFail: true}, wantProof: true},
		"9c provider factory failure, one refused":    {fx: fanOutFixture{behaviors: bothRefused, factoryFail: map[string]bool{skipAcctA: true}}},
		"9c service client failure, one refused":      {fx: fanOutFixture{behaviors: bothRefused, clientFail: map[string]bool{skipAcctA: true}}},
		"9c success=false result, one refused":        {fx: fanOutFixture{behaviors: withA(recSuccessFalse)}},
		"credential failure, one refused":             {fx: fanOutFixture{behaviors: bothRefused, credFail: map[string]bool{skipAcctA: true}}, wantProof: true},
		"plain purchase failure, one refused":         {fx: fanOutFixture{behaviors: withA(recPlainFail)}},
		"never started, the started one failed plain": {fx: fanOutFixture{behaviors: withA(recPlainFail), cancelAfterSave: skipAcctA}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := tc.fx.runRoot(t)
			require.Error(t, r.err)
			var partial *multiAccountPartialError
			if tc.partial {
				require.ErrorAs(t, r.err, &partial)
			} else {
				require.ErrorIs(t, r.err, errAllAccountsFailed)
			}

			r.saveTerminal(t)

			assert.Equal(t, tc.wantProof, r.root.NoSubmission, "root no_submission")
			store := r.store
			if tc.wantProof {
				store.AssertCalled(t, "DeleteSuppressionsByExecutionTx", mock.Anything, mock.Anything, "root")
			} else {
				store.AssertNotCalled(t, "DeleteSuppressionsByExecutionTx", mock.Anything, mock.Anything, "root")
			}
		})
	}
}

// A pin-refused account whose row save failed is AUDIT LOSS, not proof: the
// before-submission class must not survive the errors.Join, while the save
// error and the cause text stay visible.
func TestPersistFailedAccountExecution_SaveFailureStripsBeforeSubmissionClass(t *testing.T) {
	saveErr := errors.New("database down")
	store := new(MockConfigStore)
	store.SavePurchaseExecutionFn = func(context.Context, *config.PurchaseExecution) error { return saveErr }
	m := &Manager{config: store}
	account := config.CloudAccount{ID: skipAcctA}
	cause := beforeSubmission(errors.New("pin refused"))

	err := m.persistFailedAccountExecution(context.Background(), &config.PurchaseExecution{}, account, cause)

	var notSubmitted *beforeSubmissionError
	assert.False(t, errors.As(err, &notSubmitted), "a lost audit row is not proof")
	assert.ErrorIs(t, err, saveErr)
	assert.ErrorContains(t, err, "AUDIT LOSS: failed to save execution record for account "+skipAcctA)
	assert.ErrorContains(t, err, "pin refused")

	store.SavePurchaseExecutionFn = func(context.Context, *config.PurchaseExecution) error { return nil }
	err = m.persistFailedAccountExecution(context.Background(), &config.PurchaseExecution{}, account, cause)
	assert.ErrorAs(t, err, &notSubmitted, "a saved row keeps the cause, class included")
}

// #1844 9d: no real rec-level refusal exists for a Redshift rec on main
// (preSubmitChecks only refuses GCP), so the refusal is injected through the
// fixture's EC2-shaped recs and the root is then read as a Redshift row. This
// pins only the Go-side consequence: a proven root is NeverSubmitted, which
// lets a retry skip the stamp-horizon refusal. The SQL that mints the fresh
// stamp (StampFirstDispatchTx) is covered by integration tests, not here.
func TestExecuteMultiAccount_AllRefusedRedshiftRootIsNeverSubmitted(t *testing.T) {
	r := fanOutFixture{behaviors: map[string]recBehavior{skipAcctA: recRefused, skipAcctB: recRefused}}.runRoot(t)
	r.saveTerminal(t)
	require.True(t, r.root.NoSubmission)
	for i := range r.root.Recommendations {
		r.root.Recommendations[i].Service = "redshift"
	}

	assert.True(t, IsRedshiftExecution(r.root))
	assert.True(t, NeverSubmitted(r.root))
	assert.Empty(t, RedshiftRetryPermanentRefusal(r.root, nil), "no stamp needed: the retry mints a fresh one")

	r.root.NoSubmission = false
	assert.NotEmpty(t, RedshiftRetryPermanentRefusal(r.root, nil), "an unproven root keeps the no-stamp refusal")
}

// summarizeFanOut never reaches fanOutNotSubmitted with an empty or committed
// result set, so the guards that keep the function safe on its own are pinned
// directly.
func TestFanOutNotSubmitted_GuardsAreSafeOnTheirOwn(t *testing.T) {
	var none sync.Map
	assert.False(t, fanOutNotSubmitted(nil, &none), "no accounts proves nothing")
	assert.False(t, fanOutNotSubmitted([]execution.Result[bool]{{AccountID: "a", Value: true, Err: beforeSubmission(errors.New("x"))}}, &none),
		"an account that bought is never proven, whatever its error says")
	var ran sync.Map
	ran.Store("b", struct{}{})
	assert.True(t, fanOutNotSubmitted([]execution.Result[bool]{
		{AccountID: "a", Err: beforeSubmission(errors.New("refused"))},
		{AccountID: "b"},
	}, &ran), "a started account that finished with no error and bought nothing does not block the proof")
}
