package purchase

// #1844 S0: a two-account fixture for tests that go through executeMultiAccount
// (the existing multi-account tests call executeForAccount directly). It builds
// on refusalManager: each account owns one rec whose ResourceType is the account
// ID, so recBehavior is chosen per account, and the hooks below fail one account
// at the credential, provider-factory or row-save step.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// fanOutFixture runs accounts A then B (parallelism 1) through
// executeMultiAccount. Every map is keyed by account ID.
type fanOutFixture struct {
	behaviors   map[string]recBehavior // the account's single rec; zero value buys
	credFail    map[string]bool        // credential load fails
	factoryFail map[string]bool        // provider factory fails
	saveFail    map[string]bool        // the account's row save fails (AUDIT LOSS)
	clientFail  map[string]bool        // the provider's service client lookup fails
	pinFail     bool                   // the fan-out shape pin fails for every account (before submission)
	precancel   bool                   // the run context is already done, so no account starts
	// cancelAtCred cancels the run context while that account's credentials
	// load (the load also fails): a started account cut off mid-run.
	cancelAtCred string
	// cancelAfterSave cancels the run context once that account's row saved, so
	// the accounts after it never start.
	cancelAfterSave string
}

// keyedFactory fails the provider factory for the accounts in failKeys. The
// factory only sees the provider config, so the account is recognized by the
// access key the credential hook issues: the account ID.
type keyedFactory struct {
	inner      provider.FactoryInterface
	failKeys   map[string]bool
	clientFail map[string]bool
}

// noClientProvider is a provider whose service client lookup fails.
type noClientProvider struct{ provider.Provider }

func (noClientProvider) GetServiceClient(context.Context, common.ServiceType, string) (provider.ServiceClient, error) {
	return nil, errors.New("service client down")
}

func (f *keyedFactory) CreateAndValidateProvider(ctx context.Context, name string, cfg *provider.Config) (provider.Provider, error) {
	creds, err := cfg.AWSCredentialsProvider.Retrieve(ctx)
	if err != nil {
		return nil, err
	}
	if f.failKeys[creds.AccessKeyID] {
		return nil, fmt.Errorf("factory down for %s", creds.AccessKeyID)
	}
	prov, err := f.inner.CreateAndValidateProvider(ctx, name, cfg)
	if err == nil && f.clientFail[creds.AccessKeyID] {
		return noClientProvider{prov}, nil
	}
	return prov, err
}

// fanOutOutcome is one executeMultiAccount run: the root it ran, the store behind
// it, the saved account rows and the returned error.
type fanOutOutcome struct {
	root  *config.PurchaseExecution
	store *MockConfigStore
	m     *Manager
	saved *sync.Map
	err   error
}

// saveTerminal replays the root's terminal save with the fan-out's error, the
// way executeAndFinalize does after executeMultiAccount returns.
func (r *fanOutOutcome) saveTerminal(t *testing.T) {
	t.Helper()
	r.root.Status = "failed"
	var partial *multiAccountPartialError
	if errors.As(r.err, &partial) {
		r.root.Status = "partially_completed"
	}
	require.NoError(t, r.m.saveTerminalExecution(context.Background(), r.root, r.err))
}

// run returns the saved account rows (root rows are not saved by the fan-out)
// and executeMultiAccount's error.
func (f fanOutFixture) run(t *testing.T) (*sync.Map, error) {
	t.Helper()
	r := f.runRoot(t)
	return r.saved, r.err
}

func (f fanOutFixture) runRoot(t *testing.T) *fanOutOutcome {
	t.Helper()
	t.Setenv("CUDLY_MAX_ACCOUNT_PARALLELISM", "1")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if f.precancel {
		cancel()
	}
	var saved sync.Map
	m, store := refusalManager(t, f.behaviors, &saved)
	store.On("DeleteSuppressionsByExecutionTx", mock.Anything, mock.Anything, "root").Return(nil).Maybe()
	if f.pinFail {
		store.PinExecutionFanOutShapeFn = func(context.Context, string, string) (string, error) {
			return "", errors.New("pin store down")
		}
	}

	record := store.SavePurchaseExecutionFn
	store.SavePurchaseExecutionFn = func(c context.Context, e *config.PurchaseExecution) error {
		id := accountIDOf(e)
		if f.saveFail[id] {
			return fmt.Errorf("database down saving %s", id)
		}
		err := record(c, e)
		if f.cancelAfterSave != "" && id == f.cancelAfterSave {
			cancel()
		}
		return err
	}
	m.providerFactory = &keyedFactory{inner: m.providerFactory, failKeys: f.factoryFail, clientFail: f.clientFail}
	m.credStore = &MockCredentialStore{LoadRawFn: func(_ context.Context, id, _ string) ([]byte, error) {
		if f.cancelAtCred == id {
			cancel()
			return nil, fmt.Errorf("run ended while loading credentials for %s", id)
		}
		if f.credFail[id] {
			return nil, fmt.Errorf("secret store down for %s", id)
		}
		return []byte(fmt.Sprintf(`{"access_key_id":%q,"secret_access_key":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`, id)), nil
	}}

	accounts := []config.CloudAccount{
		{ID: skipAcctA, Name: "A", Provider: "aws", ExternalID: "111111111111", AWSAuthMode: "access_keys"},
		{ID: skipAcctB, Name: "B", Provider: "aws", ExternalID: "222222222222", AWSAuthMode: "access_keys"},
	}
	recs := make([]config.RecommendationRecord, 0, len(accounts))
	for _, a := range accounts {
		recs = append(recs, withAccount(refusalRecs(map[string]recBehavior{a.ID: f.behaviors[a.ID]})[0], a.ID))
	}
	exec := &config.PurchaseExecution{ExecutionID: "root", PlanID: "p", IdempotencyKey: "lineage", Recommendations: recs}
	err := m.executeMultiAccount(ctx, exec, &config.PurchasePlan{ID: "p", Name: "P"}, accounts)
	return &fanOutOutcome{root: exec, store: store, m: m, saved: &saved, err: err}
}

func savedAccountRow(saved *sync.Map, id string) *config.PurchaseExecution {
	v, ok := saved.Load(id)
	if !ok {
		return nil
	}
	return v.(*config.PurchaseExecution)
}

// rowWant is an account row's expected end state; status "" means no row.
type rowWant struct {
	status       string
	noSubmission bool
}

func TestExecuteMultiAccount_FanOutFixtureHooks(t *testing.T) {
	refusedBoth := map[string]recBehavior{skipAcctA: recRefused, skipAcctB: recRefused}
	cases := map[string]struct {
		fx         fanOutFixture
		errContain string
		// wantProof is whether the returned error proves nothing was submitted
		// (#1844 S1): only the refused-then-never-started run does today.
		wantProof bool
		a, b      rowWant
	}{
		"credential failure on A, B refused": {
			fx:         fanOutFixture{behaviors: refusedBoth, credFail: map[string]bool{skipAcctA: true}},
			errContain: "credential resolution failed for account " + skipAcctA,
			a:          rowWant{"failed", false}, // the unflagged row #1844 S2 will flag
			b:          rowWant{"failed", true},
		},
		"row save failure on A (AUDIT LOSS), B refused": {
			fx:         fanOutFixture{behaviors: refusedBoth, saveFail: map[string]bool{skipAcctA: true}},
			errContain: "AUDIT LOSS: failed to save execution record for account " + skipAcctA,
			a:          rowWant{},
			b:          rowWant{"failed", true},
		},
		"factory failure on A, B refused": {
			fx:         fanOutFixture{behaviors: refusedBoth, factoryFail: map[string]bool{skipAcctA: true}},
			errContain: "failed to create aws provider",
			a:          rowWant{"failed", false},
			b:          rowWant{"failed", true},
		},
		"A refused then cancels the run, B never starts": {
			fx:         fanOutFixture{behaviors: refusedBoth, cancelAfterSave: skipAcctA},
			errContain: "some purchases failed",
			wantProof:  true,
			a:          rowWant{"failed", true},
			b:          rowWant{},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			saved, err := tc.fx.run(t)
			require.ErrorIs(t, err, errAllAccountsFailed)
			assert.ErrorContains(t, err, tc.errContain)
			var notSubmitted *beforeSubmissionError
			assert.Equal(t, tc.wantProof, errors.As(err, &notSubmitted), "before-submission class on the returned error")
			for id, want := range map[string]rowWant{skipAcctA: tc.a, skipAcctB: tc.b} {
				row := savedAccountRow(saved, id)
				if want.status == "" {
					assert.Nil(t, row, "account %s has no row", id)
					continue
				}
				require.NotNil(t, row, "account %s row", id)
				assert.Equal(t, want.status, row.Status, id)
				assert.Equal(t, want.noSubmission, row.NoSubmission, id)
			}
		})
	}
}
