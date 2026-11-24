package commitmentopts

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/billingbenefits/armbillingbenefits"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// providerStore keeps one combo set per provider, like the real store.
type providerStore struct {
	saveErr  map[string]error
	rows     map[string][]Combo
	probedAt map[string]time.Time
	// stamp is the probed_at a write records; zero means time.Now().
	stamp time.Time
	mu    sync.Mutex
}

func newProviderStore() *providerStore {
	return &providerStore{rows: map[string][]Combo{}, saveErr: map[string]error{}, probedAt: map[string]time.Time{}}
}

func (p *providerStore) Get(context.Context) (Options, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	all := make([]Combo, 0, len(p.rows))
	for _, c := range p.rows {
		all = append(all, c...)
	}
	return buildOptions(all), len(p.rows) > 0, nil
}

func (p *providerStore) Save(_ context.Context, provider string, combos []Combo, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.saveErr[provider]; err != nil {
		return err
	}
	p.rows[provider] = combos
	return nil
}

func (p *providerStore) Replace(ctx context.Context, provider string, combos []Combo, id string) error {
	if err := p.Save(ctx, provider, combos, id); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	at := p.stamp
	if at.IsZero() {
		at = time.Now()
	}
	p.probedAt[provider] = at
	return nil
}

func (p *providerStore) ProbedAt(_ context.Context, provider string) (time.Time, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.rows[provider]; !ok {
		return time.Time{}, false, nil
	}
	at, ok := p.probedAt[provider]
	if !ok {
		at = time.Now()
	}
	return at, true, nil
}

func (p *providerStore) HasData(_ context.Context, provider string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.rows[provider]
	return ok, nil
}

func (p *providerStore) has(provider string) bool {
	ok, _ := p.HasData(context.Background(), provider)
	return ok
}

const testAzureAccountSub = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

func azureAccount() config.CloudAccount {
	return config.CloudAccount{ID: "az1", Provider: "azure", ExternalID: "az-ext", AzureSubscriptionID: testAzureAccountSub, Enabled: true}
}

type azureHarness struct {
	validateErr    error
	validateErrFor func(armbillingbenefits.Term) error
	seen           []armbillingbenefits.SavingsPlanPurchaseValidateRequest
	credCalls      int32
	validations    int32
	mu             sync.Mutex
}

func (h *azureHarness) service(store Store, accounts []config.CloudAccount, awsProbers []Prober, currency AzureCurrencyFn) *Service {
	cred := func(context.Context, *config.CloudAccount) (azcore.TokenCredential, error) {
		atomic.AddInt32(&h.credCalls, 1)
		return nil, nil
	}
	if currency == nil {
		currency = func(context.Context, *config.CloudAccount) (string, error) { return "EUR", nil }
	}
	prober := &AzureSPProber{NewClient: func(azcore.TokenCredential) (AzureSPValidateAPI, error) {
		return &fakeAzureSPValidate{fn: func(body armbillingbenefits.SavingsPlanPurchaseValidateRequest) (armbillingbenefits.RPClientValidatePurchaseResponse, error) {
			atomic.AddInt32(&h.validations, 1)
			h.mu.Lock()
			h.seen = append(h.seen, body)
			h.mu.Unlock()
			if h.validateErrFor != nil {
				if err := h.validateErrFor(*body.Benefits[0].Properties.Term); err != nil {
					return armbillingbenefits.RPClientValidatePurchaseResponse{}, err
				}
			}
			if h.validateErr != nil {
				return armbillingbenefits.RPClientValidatePurchaseResponse{}, h.validateErr
			}
			return validTrue(), nil
		}}, nil
	}}
	return New(store, &fakeAccounts{accounts: accounts}, noopBuildConfig, awsProbers).
		WithAzure(cred, currency, []*AzureSPProber{prober})
}

func awsRDSProber() []Prober {
	return []Prober{&stubProber{name: "rds", combos: []Combo{{Provider: "aws", Service: "rds", TermYears: 1, Payment: "no-upfront"}}}}
}

func TestService_RefreshAzure_UsesRealSubscriptionCurrencyAndPayments(t *testing.T) {
	store := newProviderStore()
	h := &azureHarness{}
	svc := h.service(store, []config.CloudAccount{awsAccount("a"), azureAccount()}, awsRDSProber(), nil)

	require.NoError(t, svc.RefreshAzure(context.Background()))
	_, err := svc.Get(context.Background())
	require.NoError(t, err)
	opts, err := svc.Get(context.Background()) // warm read returns every provider
	require.NoError(t, err)

	require.True(t, store.has("azure"))
	require.True(t, store.has("aws"))
	payments := map[string]bool{}
	for _, c := range store.rows["azure"] {
		assert.Equal(t, "azure", c.Provider)
		payments[c.Payment] = true
	}
	assert.Equal(t, map[string]bool{"upfront": true, "monthly": true}, payments)
	assert.Contains(t, opts["azure"], "savingsplans")

	require.NotEmpty(t, h.seen)
	for _, body := range h.seen {
		b := body.Benefits[0]
		assert.Equal(t, "/subscriptions/"+testAzureAccountSub, *b.Properties.BillingScopeID)
		assert.Equal(t, "EUR", *b.Properties.Commitment.CurrencyCode)
		assert.Equal(t, "Compute_Savings_Plan", *b.SKU.Name)
	}
}

// listingRecorder records the providers the Service asked accounts for.
type listingRecorder struct {
	accounts []config.CloudAccount
	asked    []string
	mu       sync.Mutex
}

func (l *listingRecorder) ListCloudAccounts(_ context.Context, f config.CloudAccountFilter) ([]config.CloudAccount, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if f.Provider != nil {
		l.asked = append(l.asked, *f.Provider)
	}
	return l.accounts, nil
}

func TestService_Get_NeverTouchesAzure(t *testing.T) {
	store := newProviderStore()
	h := &azureHarness{}
	svc := h.service(store, nil, awsRDSProber(), nil)
	lister := &listingRecorder{accounts: []config.CloudAccount{awsAccount("a"), azureAccount()}}
	svc.accounts = lister

	for i := 0; i < 3; i++ {
		_, err := svc.Get(context.Background())
		require.NoError(t, err)
	}
	assert.Equal(t, []string{"aws"}, lister.asked[:1], "AWS is probed once, then served from the cache")
	for _, p := range lister.asked {
		assert.Equal(t, "aws", p, "Get must never list Azure accounts")
	}
	assert.Zero(t, atomic.LoadInt32(&h.credCalls))
	assert.Zero(t, atomic.LoadInt32(&h.validations))
	assert.False(t, store.has("azure"))
}

func TestService_RefreshAzure_NoEnabledAccountIsNoOp(t *testing.T) {
	store := newProviderStore()
	h := &azureHarness{}
	disabled := azureAccount()
	disabled.Enabled = false
	svc := h.service(store, []config.CloudAccount{awsAccount("a"), disabled}, awsRDSProber(), nil)

	require.NoError(t, svc.RefreshAzure(context.Background()))
	assert.False(t, store.has("azure"))
	assert.Zero(t, atomic.LoadInt32(&h.credCalls))
	assert.Zero(t, atomic.LoadInt32(&h.validations))
}

func TestService_RefreshAzure_WithoutWithAzureIsNoOp(t *testing.T) {
	store := newProviderStore()
	lister := &listingRecorder{accounts: []config.CloudAccount{azureAccount()}}
	svc := New(store, lister, noopBuildConfig, awsRDSProber())
	require.NoError(t, svc.RefreshAzure(context.Background()))
	assert.Empty(t, lister.asked)
}

func TestService_RefreshAzure_FailureLeavesAWSIntact(t *testing.T) {
	store := newProviderStore()
	h := &azureHarness{validateErr: errors.New("azure 503")}
	svc := h.service(store, []config.CloudAccount{awsAccount("a"), azureAccount()}, awsRDSProber(), nil)

	opts, err := svc.Get(context.Background())
	require.NoError(t, err)
	require.Error(t, svc.RefreshAzure(context.Background()))
	assert.Contains(t, opts["aws"], "rds")
	assert.True(t, store.has("aws"))
	assert.False(t, store.has("azure"), "a failed Azure probe persists nothing")
}

func TestService_RefreshAzure_SaveFailureLeavesAWSIntact(t *testing.T) {
	store := newProviderStore()
	store.saveErr["azure"] = errors.New("disk full")
	svc := (&azureHarness{}).service(store, []config.CloudAccount{awsAccount("a"), azureAccount()}, awsRDSProber(), nil)

	_, err := svc.Get(context.Background())
	require.NoError(t, err)
	require.Error(t, svc.RefreshAzure(context.Background()))
	assert.True(t, store.has("aws"))
	assert.False(t, store.has("azure"))
}

func TestService_AWSFailureLeavesAzureIntact(t *testing.T) {
	store := newProviderStore()
	h := &azureHarness{}
	bad := []Prober{&stubProber{name: "rds", err: errors.New("denied")}}
	svc := h.service(store, []config.CloudAccount{awsAccount("a"), azureAccount()}, bad, nil)

	require.NoError(t, svc.RefreshAzure(context.Background()))
	_, err := svc.Get(context.Background())
	require.ErrorIs(t, err, ErrNoData)
	assert.False(t, store.has("aws"))
	assert.True(t, store.has("azure"), "the Azure row must survive an AWS failure")
}

func TestService_RefreshAzure_FailsClosedBeforeAnyRequest(t *testing.T) {
	noSub := azureAccount()
	noSub.AzureSubscriptionID = ""
	cases := map[string]struct {
		currency AzureCurrencyFn
		account  config.CloudAccount
	}{
		"missing subscription": {nil, noSub},
		"currency unknown": {func(context.Context, *config.CloudAccount) (string, error) {
			return "", errors.New("unknown currency")
		}, azureAccount()},
		"currency malformed": {func(context.Context, *config.CloudAccount) (string, error) { return "", nil }, azureAccount()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := newProviderStore()
			h := &azureHarness{}
			svc := h.service(store, []config.CloudAccount{awsAccount("a"), tc.account}, awsRDSProber(), tc.currency)

			require.Error(t, svc.RefreshAzure(context.Background()))
			assert.False(t, store.has("azure"))
			assert.Zero(t, atomic.LoadInt32(&h.validations), "no ValidatePurchase without subscription and currency")
			if name == "missing subscription" {
				assert.Zero(t, atomic.LoadInt32(&h.credCalls), "no credential is resolved for an account without a subscription")
			}
		})
	}
}

func TestService_RefreshAzure_WarmRowIsNotReprobed(t *testing.T) {
	store := newProviderStore()
	store.rows["azure"] = []Combo{{Provider: "azure", Service: "savingsplans", TermYears: 1, Payment: "upfront"}}
	h := &azureHarness{}
	svc := h.service(store, []config.CloudAccount{awsAccount("a"), azureAccount()}, awsRDSProber(), nil)

	require.NoError(t, svc.RefreshAzure(context.Background()))
	assert.Zero(t, atomic.LoadInt32(&h.validations))
	assert.Zero(t, atomic.LoadInt32(&h.credCalls))
}

func TestService_RefreshAzure_FailureIsRememberedForTTL(t *testing.T) {
	store := newProviderStore()
	h := &azureHarness{validateErr: errors.New("azure 503")}
	svc := h.service(store, []config.CloudAccount{azureAccount()}, awsRDSProber(), nil)
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.azure.now = func() time.Time { return clock }

	first := svc.RefreshAzure(context.Background())
	require.Error(t, first)
	calls := atomic.LoadInt32(&h.credCalls)

	clock = clock.Add(azureFailureTTL - time.Second)
	again := svc.RefreshAzure(context.Background())
	assert.Equal(t, first, again, "the remembered failure is returned")
	assert.Equal(t, calls, atomic.LoadInt32(&h.credCalls), "no Azure call inside the TTL")

	clock = clock.Add(2 * time.Second)
	h.validateErr = nil
	require.NoError(t, svc.RefreshAzure(context.Background()), "the TTL expired, so Azure is probed again")
	assert.Greater(t, atomic.LoadInt32(&h.credCalls), calls)
	assert.True(t, store.has("azure"))
}

func TestService_RefreshAzure_IsBounded(t *testing.T) {
	store := newProviderStore()
	h := &azureHarness{}
	svc := h.service(store, []config.CloudAccount{azureAccount()}, awsRDSProber(), nil)
	var remaining time.Duration
	svc.azure.credential = func(ctx context.Context, _ *config.CloudAccount) (azcore.TokenCredential, error) {
		d, ok := ctx.Deadline()
		require.True(t, ok, "the refresh must carry a deadline")
		remaining = time.Until(d)
		return nil, nil
	}
	require.NoError(t, svc.RefreshAzure(context.Background()))
	assert.Greater(t, remaining, azureRefreshTimeout-5*time.Second)
	assert.LessOrEqual(t, remaining, azureRefreshTimeout)
}

func TestService_RefreshAzure_ConcurrentCallsProbeOnce(t *testing.T) {
	store := newProviderStore()
	h := &azureHarness{}
	svc := h.service(store, []config.CloudAccount{awsAccount("a"), azureAccount()}, awsRDSProber(), nil)
	// Hold the first probe inside its credential lookup so every other call
	// reaches the lock with the azure row still missing.
	release := make(chan struct{})
	svc.azure.credential = func(context.Context, *config.CloudAccount) (azcore.TokenCredential, error) {
		atomic.AddInt32(&h.credCalls, 1)
		<-release
		return nil, nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = svc.RefreshAzure(context.Background())
		}()
	}
	require.Eventually(t, func() bool { return atomic.LoadInt32(&h.credCalls) >= 1 }, 5*time.Second, time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	assert.Equal(t, int32(1), atomic.LoadInt32(&h.credCalls), "the Azure lock and re-check must single-flight the probe")
}

func TestService_RefreshAzure_CallerCancelIsNotRemembered(t *testing.T) {
	store := newProviderStore()
	h := &azureHarness{}
	svc := h.service(store, []config.CloudAccount{azureAccount()}, awsRDSProber(), nil)
	svc.azure.credential = func(ctx context.Context, _ *config.CloudAccount) (azcore.TokenCredential, error) {
		return nil, ctx.Err()
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, svc.RefreshAzure(canceled), context.Canceled)

	// A fresh caller probes Azure again instead of getting "context canceled".
	svc.azure.credential = func(context.Context, *config.CloudAccount) (azcore.TokenCredential, error) { return nil, nil }
	require.NoError(t, svc.RefreshAzure(context.Background()))
	assert.True(t, store.has("azure"))
}

func TestService_RefreshAzure_OwnTimeoutIsRemembered(t *testing.T) {
	store := newProviderStore()
	h := &azureHarness{}
	svc := h.service(store, []config.CloudAccount{azureAccount()}, awsRDSProber(), nil)
	svc.azure.timeout = time.Millisecond
	var calls int32
	svc.azure.credential = func(ctx context.Context, _ *config.CloudAccount) (azcore.TokenCredential, error) {
		atomic.AddInt32(&calls, 1)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	require.ErrorIs(t, svc.RefreshAzure(context.Background()), context.DeadlineExceeded)
	require.ErrorIs(t, svc.RefreshAzure(context.Background()), context.DeadlineExceeded)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "the timeout failure is throttled")
}

func TestService_RefreshAzure_AllRejectedPersistsNothingThenRecovers(t *testing.T) {
	store := newProviderStore()
	h := &azureHarness{validateErr: &azcore.ResponseError{StatusCode: 400}}
	svc := h.service(store, []config.CloudAccount{azureAccount()}, awsRDSProber(), nil)
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.azure.now = func() time.Time { return clock }

	require.Error(t, svc.RefreshAzure(context.Background()))
	assert.False(t, store.has("azure"), "no empty snapshot may mark the cache warm")

	h.validateErr = nil
	clock = clock.Add(azureFailureTTL + time.Second)
	require.NoError(t, svc.RefreshAzure(context.Background()))
	assert.NotEmpty(t, store.rows["azure"])
}

func TestService_RefreshAzure_SoldTermRejectionPersistsNothingAndIsRemembered(t *testing.T) {
	store := newProviderStore()
	h := &azureHarness{validateErrFor: func(term armbillingbenefits.Term) error {
		if term == armbillingbenefits.TermP3Y {
			return &azcore.ResponseError{StatusCode: 400}
		}
		return nil
	}}
	svc := h.service(store, []config.CloudAccount{azureAccount()}, awsRDSProber(), nil)
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.azure.now = func() time.Time { return clock }

	first := svc.RefreshAzure(context.Background())
	require.Error(t, first)
	assert.False(t, store.has("azure"), "a snapshot missing a sold term must not be persisted")
	calls := atomic.LoadInt32(&h.validations)

	assert.Equal(t, first, svc.RefreshAzure(context.Background()), "the failure memory applies")
	assert.Equal(t, calls, atomic.LoadInt32(&h.validations), "no Azure call inside the TTL")
}

func TestService_RefreshAzure_FiveYearRejectionPersistsTheRest(t *testing.T) {
	store := newProviderStore()
	h := &azureHarness{validateErrFor: func(term armbillingbenefits.Term) error {
		if term == armbillingbenefits.TermP5Y {
			return &azcore.ResponseError{StatusCode: 422}
		}
		return nil
	}}
	svc := h.service(store, []config.CloudAccount{azureAccount()}, awsRDSProber(), nil)

	require.NoError(t, svc.RefreshAzure(context.Background()))
	assert.Len(t, store.rows["azure"], 4)
}

// seedAzureRow stores an azure row probed age before clock.
func seedAzureRow(store *providerStore, clock time.Time, age time.Duration) []Combo {
	old := []Combo{{Provider: "azure", Service: "savingsplans", TermYears: 1, Payment: "upfront"}}
	store.rows["azure"] = old
	store.probedAt["azure"] = clock.Add(-age)
	return old
}

func ttlService(store *providerStore, h *azureHarness, clock *time.Time) *Service {
	svc := h.service(store, []config.CloudAccount{azureAccount()}, awsRDSProber(), nil)
	svc.azure.now = func() time.Time { return *clock }
	store.stamp = *clock
	return svc
}

func TestService_RefreshAzure_FreshRowIsNotReprobed(t *testing.T) {
	store := newProviderStore()
	h := &azureHarness{}
	clock := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	seedAzureRow(store, clock, azureOptionsTTL-time.Second)
	svc := ttlService(store, h, &clock)

	require.NoError(t, svc.RefreshAzure(context.Background()))
	assert.Zero(t, atomic.LoadInt32(&h.credCalls))
}

func TestService_RefreshAzure_StaleRowIsReplaced(t *testing.T) {
	for name, age := range map[string]time.Duration{"older": azureOptionsTTL + time.Hour, "exactly the TTL": azureOptionsTTL} {
		t.Run(name, func(t *testing.T) {
			store := newProviderStore()
			h := &azureHarness{}
			clock := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
			old := seedAzureRow(store, clock, age)
			svc := ttlService(store, h, &clock)

			require.NoError(t, svc.RefreshAzure(context.Background()))
			assert.Positive(t, atomic.LoadInt32(&h.credCalls))
			assert.NotEqual(t, old, store.rows["azure"], "the probe result replaced the old combos")
			assert.Equal(t, clock, store.probedAt["azure"])

			credCalls := atomic.LoadInt32(&h.credCalls)
			require.NoError(t, svc.RefreshAzure(context.Background()))
			assert.Equal(t, credCalls, atomic.LoadInt32(&h.credCalls), "the replaced row is fresh")
		})
	}
}

func TestService_RefreshAzure_StaleRowSurvivesFailedProbe(t *testing.T) {
	store := newProviderStore()
	h := &azureHarness{validateErr: errors.New("azure 503")}
	clock := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	old := seedAzureRow(store, clock, azureOptionsTTL+time.Hour)
	staleAt := store.probedAt["azure"]
	svc := ttlService(store, h, &clock)

	first := svc.RefreshAzure(context.Background())
	require.Error(t, first)
	assert.Equal(t, old, store.rows["azure"])
	assert.Equal(t, staleAt, store.probedAt["azure"])
	opts, ok, err := svc.CachedOptions(context.Background(), "azure")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, old, opts["savingsplans"], "the stale combos are still served")

	calls := atomic.LoadInt32(&h.credCalls)
	clock = clock.Add(azureFailureTTL - time.Second)
	assert.Equal(t, first, svc.RefreshAzure(context.Background()))
	assert.Equal(t, calls, atomic.LoadInt32(&h.credCalls), "no re-probe inside the failure TTL")
}

func TestService_RefreshAzure_TTLIsSevenDays(t *testing.T) {
	cases := map[string]struct {
		age   time.Duration
		probe bool
	}{
		"6 days is fresh":           {6 * 24 * time.Hour, false},
		"8 days is stale":           {8 * 24 * time.Hour, true},
		"future probed_at is stale": {-time.Hour, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			store := newProviderStore()
			h := &azureHarness{}
			clock := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
			seedAzureRow(store, clock, c.age)
			svc := ttlService(store, h, &clock)

			require.NoError(t, svc.RefreshAzure(context.Background()))
			assert.Equal(t, c.probe, atomic.LoadInt32(&h.credCalls) > 0)
		})
	}
}

func TestService_FreshAzureOptions(t *testing.T) {
	cases := map[string]struct {
		age    time.Duration
		seeded bool
		wantOK bool
	}{
		"fresh row is served":       {age: 6 * 24 * time.Hour, seeded: true, wantOK: true},
		"exactly the TTL is stale":  {age: azureOptionsTTL, seeded: true},
		"older row is stale":        {age: 8 * 24 * time.Hour, seeded: true},
		"future probed_at is stale": {age: -time.Hour, seeded: true},
		"no row is unprobed":        {},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			store := newProviderStore()
			clock := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
			if c.seeded {
				seedAzureRow(store, clock, c.age)
			}
			svc := ttlService(store, &azureHarness{}, &clock)

			opts, ok, err := svc.FreshAzureOptions(context.Background())
			require.NoError(t, err)
			assert.Equal(t, c.wantOK, ok)
			if c.wantOK {
				assert.Len(t, opts["savingsplans"], 1)
			} else {
				assert.Nil(t, opts)
			}
		})
	}
}

func TestService_FreshAzureOptions_WithoutWithAzureIsUnavailable(t *testing.T) {
	store := newProviderStore()
	seedAzureRow(store, time.Now(), 0)
	svc := New(store, &fakeAccounts{}, noopBuildConfig, nil)

	_, ok, err := svc.FreshAzureOptions(context.Background())
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestService_FreshAzureOptions_StoreErrorPropagates(t *testing.T) {
	store := &erroringProbedAt{providerStore: newProviderStore()}
	svc := New(store, &fakeAccounts{}, noopBuildConfig, nil).WithAzure(nil, nil, nil)

	_, ok, err := svc.FreshAzureOptions(context.Background())
	require.Error(t, err)
	assert.False(t, ok)
}

type erroringProbedAt struct{ *providerStore }

func (erroringProbedAt) ProbedAt(context.Context, string) (time.Time, bool, error) {
	return time.Time{}, false, errors.New("boom")
}
