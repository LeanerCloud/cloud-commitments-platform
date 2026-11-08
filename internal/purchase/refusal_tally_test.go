package purchase

// #682 P0: a run whose every failed rec was refused before submission (and
// nothing bought, nothing of unknown outcome) ends no_submission and releases
// its suppressions. Fake provider and service client; no cloud call. A rec-level
// refusal reaches the aggregator as a beforeSubmission error out of
// PurchaseCommitment, which is how the GCP recent-commitment refusal (#682 P1)
// will surface.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type recBehavior int

const (
	recBuys recBehavior = iota
	recRefused
	recUnknown
	recPlainFail
	recRefusedUnknown
	recPanics
)

func (b recBehavior) result() (common.PurchaseResult, error) {
	switch b {
	case recRefused:
		return common.PurchaseResult{}, beforeSubmission(errors.New("refused before submission"))
	case recUnknown:
		return common.PurchaseResult{}, fmt.Errorf("lost response: %w", common.ErrOutcomeUnknown)
	case recRefusedUnknown:
		// A pre-submission class that still wraps an unknown outcome may have
		// reached the cloud, so it must not count as refused.
		return common.PurchaseResult{}, beforeSubmission(fmt.Errorf("guard: %w", common.ErrOutcomeUnknown))
	case recPlainFail:
		return common.PurchaseResult{}, errors.New("offering not available")
	}
	return common.PurchaseResult{Success: true, CommitmentID: "ri-ok"}, nil
}

// refusalRecs builds one selected EC2 rec per behavior, keyed by ResourceType.
func refusalRecs(behaviors map[string]recBehavior) []config.RecommendationRecord {
	recs := make([]config.RecommendationRecord, 0, len(behaviors))
	for rt := range behaviors {
		recs = append(recs, config.RecommendationRecord{Provider: "aws", Service: "ec2", ResourceType: rt,
			Region: "us-east-1", Count: 1, UpfrontCost: 100, Savings: 10, Selected: true})
	}
	return recs
}

// refusalManager wires fakes whose PurchaseCommitment answers by ResourceType.
func refusalManager(t *testing.T, behaviors map[string]recBehavior, saved *sync.Map) (*Manager, *MockConfigStore) {
	t.Helper()
	store := new(MockConfigStore)
	mailer := allowFailedNotification(new(MockEmailSender))
	factory, prov, client := new(MockProviderFactory), new(MockProvider), new(MockServiceClient)
	store.SavePurchaseExecutionFn = func(_ context.Context, e *config.PurchaseExecution) error {
		c := *e
		key := "" // single-account rows carry no CloudAccountID
		if e.CloudAccountID != nil {
			key = *e.CloudAccountID
		}
		saved.Store(key, &c)
		return nil
	}
	store.On("SavePurchaseHistory", mock.Anything, mock.Anything).Return(nil).Maybe()
	mailer.On("SendPurchaseConfirmation", mock.Anything, mock.Anything).Return(nil).Maybe()
	factory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(prov, nil)
	prov.On("GetServiceClient", mock.Anything, common.ServiceEC2, "us-east-1").Return(client, nil)
	for rt, b := range behaviors {
		res, err := b.result()
		call := client.On("PurchaseCommitment", mock.Anything,
			mock.MatchedBy(func(r common.Recommendation) bool { return r.ResourceType == rt }),
			mock.AnythingOfType("common.PurchaseOptions"))
		if b == recPanics {
			// The fan-out recovers this into a closure-level error after the
			// call may have reached the provider.
			call = call.Run(func(mock.Arguments) { panic("lost after submitting") })
		}
		call.Return(res, err)
	}
	return &Manager{config: store, email: mailer, providerFactory: factory, credStore: awsAccessKeyCredStore()}, store
}

func refusalCases() map[string]struct {
	behaviors      map[string]recBehavior
	wantNoSubmit   bool
	wantProvedFail bool
} {
	return map[string]struct {
		behaviors      map[string]recBehavior
		wantNoSubmit   bool
		wantProvedFail bool
	}{
		"all refused":                  {map[string]recBehavior{"a": recRefused, "b": recRefused}, true, true},
		"purchased plus refused":       {map[string]recBehavior{"a": recBuys, "b": recRefused}, false, false},
		"unknown outcome plus refused": {map[string]recBehavior{"a": recUnknown, "b": recRefused}, false, false},
		"plain failure plus refused":   {map[string]recBehavior{"a": recPlainFail, "b": recRefused}, false, false},
		"refused with unknown outcome": {map[string]recBehavior{"a": recRefusedUnknown, "b": recRefused}, false, false},
		"panic plus refused":           {map[string]recBehavior{"a": recPanics, "b": recRefused}, false, false},
	}
}

func TestSingleAccount_AllRefusedBeforeSubmission_EndsNoSubmissionAndReleasesSuppressions(t *testing.T) {
	for name, tc := range refusalCases() {
		if !tc.wantProvedFail {
			continue
		}
		t.Run(name, func(t *testing.T) {
			var saved sync.Map
			m, store := refusalManager(t, tc.behaviors, &saved)
			exec := &config.PurchaseExecution{ExecutionID: "e", PlanID: "p", Status: "running",
				Recommendations: refusalRecs(tc.behaviors)}
			store.On("DeleteSuppressionsByExecutionTx", mock.Anything, mock.Anything, "e").Return(nil).Maybe()

			err := m.executeSingleAccount(context.Background(), exec, &config.PurchasePlan{ID: "p", Name: "P"})
			require.Error(t, err)
			var notSubmitted *beforeSubmissionError
			require.True(t, errors.As(err, &notSubmitted), "beforeSubmission class on the returned error")
			exec.Status = "failed"
			require.NoError(t, m.saveTerminalExecution(context.Background(), exec, err))
			assert.True(t, exec.NoSubmission)
			store.AssertCalled(t, "DeleteSuppressionsByExecutionTx", mock.Anything, mock.Anything, "e")
		})
	}
}

func TestSingleAccount_MixedOutcomesAreNotAllRefused(t *testing.T) {
	for name, tc := range refusalCases() {
		if tc.wantProvedFail {
			continue
		}
		t.Run(name, func(t *testing.T) {
			var saved sync.Map
			m, store := refusalManager(t, tc.behaviors, &saved)
			exec := &config.PurchaseExecution{ExecutionID: "e", PlanID: "p", Status: "running",
				Recommendations: refusalRecs(tc.behaviors)}
			_, _, errs, allRefused, procErr := m.processPurchaseRecommendations(context.Background(), exec, &config.PurchasePlan{Name: "P"}, "111111111111", nil, "")
			require.NoError(t, procErr)
			require.NotEmpty(t, errs)
			assert.False(t, allRefused)
			store.AssertNotCalled(t, "DeleteSuppressionsByExecutionTx", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestMultiAccount_RefusedAccountRowIsNoSubmission(t *testing.T) {
	const refusedAcct, boughtAcct = "aaaaaaaa-0000-0000-0000-000000000001", "bbbbbbbb-0000-0000-0000-000000000002"
	behaviors := map[string]recBehavior{"r1": recRefused, "r2": recRefused, "ok": recBuys}
	var saved sync.Map
	m, _ := refusalManager(t, behaviors, &saved)
	plan := &config.PurchasePlan{ID: "p", Name: "P"}
	base := func(types ...string) *config.PurchaseExecution {
		sub := map[string]recBehavior{}
		for _, ty := range types {
			sub[ty] = behaviors[ty]
		}
		return &config.PurchaseExecution{ExecutionID: "root", PlanID: "p", IdempotencyKey: "lineage", Recommendations: refusalRecs(sub)}
	}
	acct := func(id, ext string) config.CloudAccount {
		return config.CloudAccount{ID: id, Name: id, Provider: "aws", ExternalID: ext, AWSAuthMode: "access_keys"}
	}
	_, err := m.executeForAccount(context.Background(), base("r1", "r2"), plan, acct(refusedAcct, "111111111111"))
	require.Error(t, err)
	_, err = m.executeForAccount(context.Background(), base("ok"), plan, acct(boughtAcct, "222222222222"))
	require.NoError(t, err)

	row := func(id string) *config.PurchaseExecution {
		v, ok := saved.Load(id)
		require.True(t, ok)
		return v.(*config.PurchaseExecution)
	}
	assert.Equal(t, "failed", row(refusedAcct).Status)
	assert.True(t, row(refusedAcct).NoSubmission)
	assert.Equal(t, "completed", row(boughtAcct).Status)
	assert.False(t, row(boughtAcct).NoSubmission)
}

func TestMultiAccount_MixedOutcomesAreNotNoSubmission(t *testing.T) {
	for name, tc := range refusalCases() {
		if tc.wantNoSubmit {
			continue
		}
		t.Run(name, func(t *testing.T) {
			const acctID = "aaaaaaaa-0000-0000-0000-000000000001"
			var saved sync.Map
			m, _ := refusalManager(t, tc.behaviors, &saved)
			base := &config.PurchaseExecution{ExecutionID: "root", PlanID: "p", IdempotencyKey: "lineage", Recommendations: refusalRecs(tc.behaviors)}
			account := config.CloudAccount{ID: acctID, Name: "A", Provider: "aws", ExternalID: "111111111111", AWSAuthMode: "access_keys"}
			_, err := m.executeForAccount(context.Background(), base, &config.PurchasePlan{ID: "p", Name: "P"}, account)
			require.Error(t, err)
			v, ok := saved.Load(acctID)
			require.True(t, ok)
			assert.False(t, v.(*config.PurchaseExecution).NoSubmission)
		})
	}
}
