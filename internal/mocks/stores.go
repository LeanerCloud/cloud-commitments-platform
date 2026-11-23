package mocks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/mock"
)

// MockOwnerToken is the default token MockConfigStore.MarkCollectionStarted
// returns when no expectation is registered, so existing tests that call it
// implicitly keep getting a non-empty token to thread through.
const MockOwnerToken = "mock-owner-token"

// MockInvocationToken is the default token MockConfigStore.ClaimCollectionInvocation
// returns when no expectation is registered. It differs from MockOwnerToken
// so tests can tell the re-stamped invocation token from the marker token.
const MockInvocationToken = "mock-invocation-token"

// MockConfigStore is a shared testify-based mock for config.StoreInterface.
//
// Most methods dispatch through m.Called only when an expectation has been
// registered via .On(). Methods that pre-existing tests call implicitly
// (without expectations) default to sensible zero-values so those tests
// keep working without changes. The "default or dispatch" behavior is
// controlled by the isExpected helper at the bottom of this file.
//
// Fn-override fields allow tests to inject behavior without registering
// testify expectations. The precedence order for every overridable method is:
//  1. FnField (non-nil closure wins first)
//  2. Registered .On() expectation (dispatches through m.Called)
//  3. Hardcoded default (zero-value / sensible stub)
//
// Because paths 1 and 3 never reach m.Called, every method records its own
// invocation in callLog before branching, and AssertCalled / AssertNotCalled /
// AssertNumberOfCalls read that log instead of mock.Mock.Calls. See
// assertions.go for why reading mock.Mock.Calls made those assertions
// unfailable.
type MockConfigStore struct {
	// reporter fails the registered test on a mistyped .Return value. See
	// report.go.
	reporter testReporter
	// fanOutShapes backs PinExecutionFanOutShape's default: first writer wins
	// per lineage key, like the real table.
	fanOutShapes                        fanOutShapePins
	CountLiveExecutionsForAccountFn     func(ctx context.Context, accountID string) (int, error)
	ListPendingExecutionIDsForAccountFn func(ctx context.Context, accountID string) ([]string, error)
	ListCloudAccountsFn                 func(ctx context.Context, filter config.CloudAccountFilter) ([]config.CloudAccount, error)
	CreateCloudAccountFn                func(ctx context.Context, account *config.CloudAccount) error
	SetPlanAccountsFn                   func(ctx context.Context, planID string, accountIDs []string) error
	GetPlanAccountsFn                   func(ctx context.Context, planID string) ([]config.CloudAccount, error)
	SaveAccountServiceOverrideFn        func(ctx context.Context, override *config.AccountServiceOverride) error
	CountPendingExecutionsForAccountFn  func(ctx context.Context, accountID string) (int, error)
	GetPurchasePlanFn                   func(ctx context.Context, planID string) (*config.PurchasePlan, error)
	DeleteCloudAccountFn                func(ctx context.Context, id string) error
	SavePurchaseExecutionFn             func(ctx context.Context, exec *config.PurchaseExecution) error
	PinExecutionFanOutShapeFn           func(ctx context.Context, lineageKey, shape string) (string, error)
	GetExecutionFanOutShapeFn           func(ctx context.Context, lineageKey string) (string, error)
	GetUserEmailByIDFn                  func(ctx context.Context, userID string) (string, error)
	// NameTakenFixture, when set, stands in for an existing plan of the same
	// name: CreatePurchasePlanWithAccounts passes its Accounts to the visible
	// predicate and refuses with a PlanNameTakenError when it reports true.
	NameTakenFixture              *PlanNameFixture
	GetCloudAccountFn             func(ctx context.Context, id string) (*config.CloudAccount, error)
	GetCloudAccountByExternalIDFn func(ctx context.Context, provider, externalID string) (*config.CloudAccount, error)
	mock.Mock
	// callLog records every invocation, including the ones served from a
	// default without reaching mock.Called. See assertions.go.
	callLog callLog
}

// GetGlobalConfig mocks the GetGlobalConfig operation. Returns an empty
// GlobalConfig when no expectation is registered so callers that only
// need default field values don't require explicit mock setup.
func (m *MockConfigStore) GetGlobalConfig(ctx context.Context) (*config.GlobalConfig, error) {
	m.record("GetGlobalConfig", ctx)
	if !isExpected(&m.Mock, "GetGlobalConfig") {
		return &config.GlobalConfig{}, nil
	}
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.GlobalConfig)
	if !ok {
		m.reporter.wrongReturnType("GetGlobalConfig", "*config.GlobalConfig", args.Get(0))
	}
	return v, args.Error(1)
}

// SaveGlobalConfig mocks the SaveGlobalConfig operation.
func (m *MockConfigStore) SaveGlobalConfig(ctx context.Context, cfg *config.GlobalConfig) error {
	m.record("SaveGlobalConfig", ctx, cfg)
	args := m.Called(ctx, cfg)
	return args.Error(0)
}

// UpdateGlobalConfigAtomic mocks the atomic read-modify-write. When an
// expectation is registered it dispatches through m.Called (tests can drive the
// apply closure via a Run callback). Otherwise it emulates the real locked
// read-modify-write by routing through the mocked GetGlobalConfig + apply +
// SaveGlobalConfig, so existing tests that seed those two continue to work.
func (m *MockConfigStore) UpdateGlobalConfigAtomic(ctx context.Context, apply func(*config.GlobalConfig) error) (*config.GlobalConfig, error) {
	m.record("UpdateGlobalConfigAtomic", ctx, apply)
	if isExpected(&m.Mock, "UpdateGlobalConfigAtomic") {
		args := m.Called(ctx, apply)
		if args.Get(0) == nil {
			return nil, args.Error(1)
		}
		v, ok := args.Get(0).(*config.GlobalConfig)
		if !ok {
			m.reporter.wrongReturnType("UpdateGlobalConfigAtomic", "*config.GlobalConfig", args.Get(0))
		}
		return v, args.Error(1)
	}
	// This fallback routes through the mocked GetGlobalConfig and
	// SaveGlobalConfig, which record themselves; see UpdatePurchasePlanTx.
	existing, err := m.GetGlobalConfig(ctx)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		existing = &config.GlobalConfig{}
	}
	if err := apply(existing); err != nil {
		return nil, err
	}
	if err := m.SaveGlobalConfig(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// GetServiceConfig mocks the GetServiceConfig operation.
func (m *MockConfigStore) GetServiceConfig(ctx context.Context, provider, service string) (*config.ServiceConfig, error) {
	m.record("GetServiceConfig", ctx, provider, service)
	args := m.Called(ctx, provider, service)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.ServiceConfig)
	if !ok {
		m.reporter.wrongReturnType("GetServiceConfig", "*config.ServiceConfig", args.Get(0))
	}
	return v, args.Error(1)
}

// SaveServiceConfig mocks the SaveServiceConfig operation.
func (m *MockConfigStore) SaveServiceConfig(ctx context.Context, cfg *config.ServiceConfig) error {
	m.record("SaveServiceConfig", ctx, cfg)
	args := m.Called(ctx, cfg)
	return args.Error(0)
}

// UpdateServiceConfigAtomic mocks the locked read-merge-write of one service
// config row. When an expectation is registered it dispatches through
// m.Called (tests can drive the apply closure via a Run callback). Otherwise
// it emulates the real store by routing through the mocked GetServiceConfig +
// apply + SaveServiceConfig, so tests that seed those two keep working. Like
// the real store, a not-found read hands apply a nil row.
func (m *MockConfigStore) UpdateServiceConfigAtomic(ctx context.Context, provider, service string, apply func(*config.ServiceConfig) (*config.ServiceConfig, error)) (*config.ServiceConfig, error) {
	m.record("UpdateServiceConfigAtomic", ctx, provider, service, apply)
	if isExpected(&m.Mock, "UpdateServiceConfigAtomic") {
		args := m.Called(ctx, provider, service, apply)
		if args.Get(0) == nil {
			return nil, args.Error(1)
		}
		v, ok := args.Get(0).(*config.ServiceConfig)
		if !ok {
			m.reporter.wrongReturnType("UpdateServiceConfigAtomic", "*config.ServiceConfig", args.Get(0))
		}
		return v, args.Error(1)
	}
	return m.updateServiceConfigFallback(ctx, provider, service, apply)
}

// updateServiceConfigFallback is UpdateServiceConfigAtomic's default. It
// routes through the mocked GetServiceConfig and SaveServiceConfig, which
// record themselves; see UpdatePurchasePlanTx.
func (m *MockConfigStore) updateServiceConfigFallback(ctx context.Context, provider, service string, apply func(*config.ServiceConfig) (*config.ServiceConfig, error)) (*config.ServiceConfig, error) {
	existing, err := m.GetServiceConfig(ctx, provider, service)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return nil, err
	}
	next, err := apply(existing)
	if err != nil {
		return nil, err
	}
	if next == nil || next.Provider != provider || next.Service != service {
		return nil, fmt.Errorf("service config update for %s/%s must return a row with the same key", provider, service)
	}
	if err := m.SaveServiceConfig(ctx, next); err != nil {
		return nil, err
	}
	return next, nil
}

// ListServiceConfigs mocks the ListServiceConfigs operation.
func (m *MockConfigStore) ListServiceConfigs(ctx context.Context) ([]config.ServiceConfig, error) {
	m.record("ListServiceConfigs", ctx)
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.ServiceConfig)
	if !ok {
		m.reporter.wrongReturnType("ListServiceConfigs", "[]config.ServiceConfig", args.Get(0))
	}
	return v, args.Error(1)
}

// API Key operations

// Cloud accounts

// Account credentials

// Account service overrides

// UpdateAccountServiceOverrideAtomic mocks the locked read-merge-write of one
// override. With an expectation it dispatches through m.Called; otherwise it
// emulates the store via the mocked WithTx, GetAccountServiceOverride, merge
// and SaveAccountServiceOverrideAuditedTx, so tests that seed those keep working.
func (m *MockConfigStore) UpdateAccountServiceOverrideAtomic(ctx context.Context, accountID, provider, service string, audit config.AuditContext, merge func(*config.AccountServiceOverride) (*config.AccountServiceOverride, error)) (*config.AccountServiceOverride, error) {
	m.record("UpdateAccountServiceOverrideAtomic", ctx, accountID, provider, service, audit, merge)
	if isExpected(&m.Mock, "UpdateAccountServiceOverrideAtomic") {
		args := m.Called(ctx, accountID, provider, service, audit, merge)
		if args.Get(0) == nil {
			return nil, args.Error(1)
		}
		v, ok := args.Get(0).(*config.AccountServiceOverride)
		if !ok {
			m.reporter.wrongReturnType("UpdateAccountServiceOverrideAtomic", "*config.AccountServiceOverride", args.Get(0))
		}
		return v, args.Error(1)
	}
	var next *config.AccountServiceOverride
	err := m.WithTx(ctx, func(tx pgx.Tx) error {
		existing, err := m.GetAccountServiceOverride(ctx, accountID, provider, service)
		if err != nil {
			return err
		}
		if next, err = merge(existing); err != nil {
			return err
		}
		return m.SaveAccountServiceOverrideAuditedTx(ctx, tx, next, audit)
	})
	if err != nil {
		return nil, err
	}
	return next, nil
}

// DeleteAccountServiceOverrideAuditedTx falls back to
// DeleteAccountServiceOverride when no expectation is registered.
func (m *MockConfigStore) DeleteAccountServiceOverrideAuditedTx(ctx context.Context, tx pgx.Tx, accountID, provider, service string, audit config.AuditContext) error {
	m.record("DeleteAccountServiceOverrideAuditedTx", ctx, tx, accountID, provider, service, audit)
	if !isExpected(&m.Mock, "DeleteAccountServiceOverrideAuditedTx") {
		return m.DeleteAccountServiceOverride(ctx, accountID, provider, service)
	}
	args := m.Called(ctx, tx, accountID, provider, service, audit)
	return args.Error(0)
}

// SaveConfigAuditEventTx records the event and returns nil when no
// expectation is registered.
func (m *MockConfigStore) SaveConfigAuditEventTx(ctx context.Context, tx pgx.Tx, event *config.AuditEvent) error {
	m.record("SaveConfigAuditEventTx", ctx, tx, event)
	if !isExpected(&m.Mock, "SaveConfigAuditEventTx") {
		return nil
	}
	args := m.Called(ctx, tx, event)
	return args.Error(0)
}

func (m *MockConfigStore) DeleteAccountServiceOverride(ctx context.Context, accountID, provider, service string) error {
	m.record("DeleteAccountServiceOverride", ctx, accountID, provider, service)
	if !isExpected(&m.Mock, "DeleteAccountServiceOverride") {
		return nil
	}
	args := m.Called(ctx, accountID, provider, service)
	return args.Error(0)
}

func (m *MockConfigStore) ListAccountServiceOverrides(ctx context.Context, accountID string) ([]config.AccountServiceOverride, error) {
	m.record("ListAccountServiceOverrides", ctx, accountID)
	if !isExpected(&m.Mock, "ListAccountServiceOverrides") {
		return nil, nil
	}
	args := m.Called(ctx, accountID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.AccountServiceOverride)
	if !ok {
		m.reporter.wrongReturnType("ListAccountServiceOverrides", "[]config.AccountServiceOverride", args.Get(0))
	}
	return v, args.Error(1)
}

// Plan ↔ account association

// Recommendations cache
// These methods default to zero-value returns when no .On() expectation is
// registered — they were opt-in in the per-package mocks and many callers
// don't set expectations for them. Tests that want to assert on these paths
// register an explicit expectation via .On(...).Return(...).

func (m *MockConfigStore) UpsertRecommendations(ctx context.Context, collectedAt time.Time, recs []config.RecommendationRecord, successfulCollects []config.SuccessfulCollect) error {
	m.record("UpsertRecommendations", ctx, collectedAt, recs, successfulCollects)
	if !isExpected(&m.Mock, "UpsertRecommendations") {
		return nil
	}
	args := m.Called(ctx, collectedAt, recs, successfulCollects)
	return args.Error(0)
}

func (m *MockConfigStore) UpsertRecommendationsWithPartial(ctx context.Context, collectedAt time.Time, recs []config.RecommendationRecord, successfulCollects []config.SuccessfulCollect, partial *config.RecommendationsCollectionPartial) error {
	m.record("UpsertRecommendationsWithPartial", ctx, collectedAt, recs, successfulCollects, partial)
	if !isExpected(&m.Mock, "UpsertRecommendationsWithPartial") {
		return nil
	}
	args := m.Called(ctx, collectedAt, recs, successfulCollects, partial)
	return args.Error(0)
}

func (m *MockConfigStore) PruneStaleRecommendations(ctx context.Context, retentionDays int) (int64, error) {
	m.record("PruneStaleRecommendations", ctx, retentionDays)
	if !isExpected(&m.Mock, "PruneStaleRecommendations") {
		return 0, nil
	}
	args := m.Called(ctx, retentionDays)
	v, ok := args.Get(0).(int64)
	if !ok {
		m.reporter.wrongReturnType("PruneStaleRecommendations", "int64", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) ListStoredRecommendations(ctx context.Context, filter config.RecommendationFilter) ([]config.RecommendationRecord, error) {
	m.record("ListStoredRecommendations", ctx, filter)
	if !isExpected(&m.Mock, "ListStoredRecommendations") {
		return nil, nil
	}
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.RecommendationRecord)
	if !ok {
		m.reporter.wrongReturnType("ListStoredRecommendations", "[]config.RecommendationRecord", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) ListStoredRecommendationsChunk(ctx context.Context, filter config.RecommendationFilter, after *config.RecommendationScanKey, limit int) (config.RecommendationChunk, error) {
	m.record("ListStoredRecommendationsChunk", ctx, filter, after, limit)
	if !isExpected(&m.Mock, "ListStoredRecommendationsChunk") {
		return config.RecommendationChunk{Done: true}, nil
	}
	args := m.Called(ctx, filter, after, limit)
	v, ok := args.Get(0).(config.RecommendationChunk)
	if !ok {
		m.reporter.wrongReturnType("ListStoredRecommendationsChunk", "config.RecommendationChunk", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) ListStoredRecommendationTriples(ctx context.Context, scope config.RecommendationListScope) ([]config.RecommendationTriple, error) {
	m.record("ListStoredRecommendationTriples", ctx, scope)
	if !isExpected(&m.Mock, "ListStoredRecommendationTriples") {
		return nil, nil
	}
	args := m.Called(ctx, scope)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.RecommendationTriple)
	if !ok {
		m.reporter.wrongReturnType("ListStoredRecommendationTriples", "[]config.RecommendationTriple", args.Get(0))
	}
	return v, args.Error(1)
}

// PageStoredRecommendations defaults to an empty cache: an empty page with a
// zero summary.
func (m *MockConfigStore) PageStoredRecommendations(ctx context.Context, q config.RecommendationPageQuery) (config.RecommendationPage, error) {
	m.record("PageStoredRecommendations", ctx, q)
	if !isExpected(&m.Mock, "PageStoredRecommendations") {
		return config.RecommendationPage{}, nil
	}
	args := m.Called(ctx, q)
	v, ok := args.Get(0).(config.RecommendationPage)
	if !ok {
		m.reporter.wrongReturnType("PageStoredRecommendations", "config.RecommendationPage", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) GetRecommendationsFreshness(ctx context.Context) (*config.RecommendationsFreshness, error) {
	m.record("GetRecommendationsFreshness", ctx)
	if !isExpected(&m.Mock, "GetRecommendationsFreshness") {
		return &config.RecommendationsFreshness{}, nil
	}
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.RecommendationsFreshness)
	if !ok {
		m.reporter.wrongReturnType("GetRecommendationsFreshness", "*config.RecommendationsFreshness", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) SetRecommendationsCollectionError(ctx context.Context, errMsg string) error {
	m.record("SetRecommendationsCollectionError", ctx, errMsg)
	if !isExpected(&m.Mock, "SetRecommendationsCollectionError") {
		return nil
	}
	args := m.Called(ctx, errMsg)
	return args.Error(0)
}

func (m *MockConfigStore) GetRIUtilizationCache(ctx context.Context, key config.RIUtilizationCacheKey) (*config.RIUtilizationCacheEntry, error) {
	m.record("GetRIUtilizationCache", ctx, key)
	if !isExpected(&m.Mock, "GetRIUtilizationCache") {
		return nil, nil
	}
	args := m.Called(ctx, key)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.RIUtilizationCacheEntry)
	if !ok {
		m.reporter.wrongReturnType("GetRIUtilizationCache", "*config.RIUtilizationCacheEntry", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) UpsertRIUtilizationCache(ctx context.Context, key config.RIUtilizationCacheKey, payload []byte, fetchedAt time.Time) error {
	m.record("UpsertRIUtilizationCache", ctx, key, payload, fetchedAt)
	if !isExpected(&m.Mock, "UpsertRIUtilizationCache") {
		return nil
	}
	args := m.Called(ctx, key, payload, fetchedAt)
	return args.Error(0)
}

func (m *MockConfigStore) CreateAccountRegistration(ctx context.Context, reg *config.AccountRegistration) error {
	m.record("CreateAccountRegistration", ctx, reg)
	args := m.Called(ctx, reg)
	return args.Error(0)
}

func (m *MockConfigStore) GetAccountRegistration(ctx context.Context, id string) (*config.AccountRegistration, error) {
	m.record("GetAccountRegistration", ctx, id)
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.AccountRegistration)
	if !ok {
		m.reporter.wrongReturnType("GetAccountRegistration", "*config.AccountRegistration", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) GetAccountRegistrationByToken(ctx context.Context, token string) (*config.AccountRegistration, error) {
	m.record("GetAccountRegistrationByToken", ctx, token)
	args := m.Called(ctx, token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.AccountRegistration)
	if !ok {
		m.reporter.wrongReturnType("GetAccountRegistrationByToken", "*config.AccountRegistration", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) ListAccountRegistrations(ctx context.Context, filter config.AccountRegistrationFilter) ([]config.AccountRegistration, error) {
	m.record("ListAccountRegistrations", ctx, filter)
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.AccountRegistration)
	if !ok {
		m.reporter.wrongReturnType("ListAccountRegistrations", "[]config.AccountRegistration", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) UpdateAccountRegistration(ctx context.Context, reg *config.AccountRegistration) error {
	m.record("UpdateAccountRegistration", ctx, reg)
	args := m.Called(ctx, reg)
	return args.Error(0)
}

func (m *MockConfigStore) TransitionRegistrationStatus(ctx context.Context, reg *config.AccountRegistration, fromStatus string, actor *string) error {
	m.record("TransitionRegistrationStatus", ctx, reg, fromStatus, actor)
	args := m.Called(ctx, reg, fromStatus, actor)
	return args.Error(0)
}

func (m *MockConfigStore) ClearRegistrationCredentialPayload(ctx context.Context, registrationID string) error {
	m.record("ClearRegistrationCredentialPayload", ctx, registrationID)
	args := m.Called(ctx, registrationID)
	return args.Error(0)
}

func (m *MockConfigStore) ApproveAccountRegistration(ctx context.Context, reg *config.AccountRegistration, account *config.CloudAccount, actor *string) error {
	m.record("ApproveAccountRegistration", ctx, reg, account, actor)
	args := m.Called(ctx, reg, account, actor)
	return args.Error(0)
}

func (m *MockConfigStore) DeleteAccountRegistration(ctx context.Context, id string) error {
	m.record("DeleteAccountRegistration", ctx, id)
	args := m.Called(ctx, id)
	return args.Error(0)
}

// ── Purchase suppressions (Commit 2 of bulk-purchase-with-grace)
// Default to no-op success when no .On() expectation is registered so
// existing tests don't need to be aware of the suppression lifecycle.

// WithTx invokes fn with a nil sentinel Tx so tests exercise the inner
// *Tx mocks directly. Tests that want to assert on WithTx itself
// register .On("WithTx", ...) and that takes precedence.
func (m *MockConfigStore) WithTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	m.record("WithTx", ctx, fn)
	if isExpected(&m.Mock, "WithTx") {
		args := m.Called(ctx, fn)
		return args.Error(0)
	}
	return fn(nil)
}

// GetExecutionsByStatusesFiltered mocks the GetExecutionsByStatusesFiltered
// operation. Without an expectation for it, it answers from the
// GetExecutionsByStatuses expectation: the unfiltered rows are a superset of
// the filtered ones, and the History handler re-applies every filter in Go, so
// tests written against GetExecutionsByStatuses keep working.
func (m *MockConfigStore) GetExecutionsByStatusesFiltered(ctx context.Context, q config.ExecutionsByStatusesQuery) ([]config.PurchaseExecution, error) {
	if !isExpected(&m.Mock, "GetExecutionsByStatusesFiltered") {
		return m.GetExecutionsByStatuses(ctx, q.Statuses, q.Limit)
	}
	m.record("GetExecutionsByStatusesFiltered", ctx, q)
	args := m.Called(ctx, q)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseExecution)
	if !ok {
		m.reporter.wrongReturnType("GetExecutionsByStatusesFiltered", "[]config.PurchaseExecution", args.Get(0))
	}
	return filterExecutionsLikeStore(v, q.Statuses, q.Limit), args.Error(1)
}

// GetExecutionsByStatuses mocks the GetExecutionsByStatuses operation.
func (m *MockConfigStore) GetExecutionsByStatuses(ctx context.Context, statuses []string, limit int) ([]config.PurchaseExecution, error) {
	m.record("GetExecutionsByStatuses", ctx, statuses, limit)
	args := m.Called(ctx, statuses, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseExecution)
	if !ok {
		m.reporter.wrongReturnType("GetExecutionsByStatuses", "[]config.PurchaseExecution", args.Get(0))
	}
	return filterExecutionsLikeStore(v, statuses, limit), args.Error(1)
}

// ListHistoryExecutionsPage mocks the ListHistoryExecutionsPage operation.
// With no expectation registered it returns no rows. A registered
// expectation's rows go through the store contract (see
// pageExecutionsLikeStore), so a test can return one unordered fixture and
// page through it.
func (m *MockConfigStore) ListHistoryExecutionsPage(ctx context.Context, q config.HistoryExecutionsPageQuery) ([]config.PurchaseExecution, error) {
	m.record("ListHistoryExecutionsPage", ctx, q)
	if !isExpected(&m.Mock, "ListHistoryExecutionsPage") {
		return nil, nil
	}
	args := m.Called(ctx, q)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseExecution)
	if !ok {
		m.reporter.wrongReturnType("ListHistoryExecutionsPage", "[]config.PurchaseExecution", args.Get(0))
	}
	return pageExecutionsLikeStore(v, &q), args.Error(1)
}

// ListPurchaseHistoryPage mocks the ListPurchaseHistoryPage operation. With
// no expectation registered it returns no rows. A registered expectation's
// rows are ordered, resumed after `after` and capped like the store does; the
// filter's predicates are not applied (match on the filter argument instead).
// The expectation may return []config.PurchaseHistoryPageRow, or plain
// []config.PurchaseHistoryRecord fixtures, which get row IDs from their
// position in the fixture (see PurchaseHistoryFixtureRowID).
func (m *MockConfigStore) ListPurchaseHistoryPage(ctx context.Context, filter config.PurchaseHistoryFilter, after *config.HistoryPageKey) ([]config.PurchaseHistoryPageRow, error) {
	m.record("ListPurchaseHistoryPage", ctx, filter, after)
	if !isExpected(&m.Mock, "ListPurchaseHistoryPage") {
		return nil, nil
	}
	args := m.Called(ctx, filter, after)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	var v []config.PurchaseHistoryPageRow
	switch rows := args.Get(0).(type) {
	case []config.PurchaseHistoryPageRow:
		v = rows
	case []config.PurchaseHistoryRecord:
		v = make([]config.PurchaseHistoryPageRow, len(rows))
		for i := range rows {
			v[i] = config.PurchaseHistoryPageRow{PurchaseHistoryRecord: rows[i], RowID: PurchaseHistoryFixtureRowID(i)}
		}
	default:
		m.reporter.wrongReturnType("ListPurchaseHistoryPage", "[]config.PurchaseHistoryPageRow or []config.PurchaseHistoryRecord", args.Get(0))
	}
	out := make([]config.PurchaseHistoryPageRow, 0, len(v))
	for i := range v {
		if after == nil || after.SortsBefore(v[i].Key()) {
			out = append(out, v[i])
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key().SortsBefore(out[j].Key()) })
	return capHistoryPage(out, filter.Limit), args.Error(1)
}

// pageExecutionsLikeStore applies ListHistoryExecutionsPage's contract to
// canned rows: status filter, clean completed rows dropped, inclusive
// scheduled_date bounds, rows after q.After only, stream order, then the cap.
func pageExecutionsLikeStore(rows []config.PurchaseExecution, q *config.HistoryExecutionsPageQuery) []config.PurchaseExecution {
	var out []config.PurchaseExecution
	for i := range rows {
		if executionMatchesPageQuery(&rows[i], q) {
			out = append(out, rows[i])
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return executionPageKey(&out[i]).SortsBefore(executionPageKey(&out[j]))
	})
	return capHistoryPage(out, q.Limit)
}

// PurchaseHistoryFixtureRowID is the row ID ListPurchaseHistoryPage gives the
// i-th row of a []config.PurchaseHistoryRecord fixture: a valid UUID, so a
// cursor built from it decodes, and later fixture rows sort lower.
func PurchaseHistoryFixtureRowID(i int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", 0xffffffffffff-i)
}

func executionMatchesPageQuery(r *config.PurchaseExecution, q *config.HistoryExecutionsPageQuery) bool {
	if !slices.Contains(q.Statuses, r.Status) || (r.Status == "completed" && r.Error == "") {
		return false
	}
	if (q.Start != nil && r.ScheduledDate.Before(*q.Start)) || (q.End != nil && r.ScheduledDate.After(*q.End)) {
		return false
	}
	return q.After == nil || q.After.SortsBefore(executionPageKey(r))
}

func executionPageKey(e *config.PurchaseExecution) config.HistoryPageKey {
	return config.HistoryPageKey{Timestamp: e.ScheduledDate, Source: config.HistorySourceExecution, ID: e.ExecutionID}
}

// capHistoryPage applies the History page limit bounds the store uses.
func capHistoryPage[T any](rows []T, limit int) []T {
	if limit <= 0 {
		limit = config.DefaultListLimit
	}
	limit = min(limit, config.MaxListLimit)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

// filterExecutionsLikeStore applies the store contract to canned rows: filter by
// status, drop clean completed rows, order by ScheduledDate DESC with future
// pending rows last and soonest first (issue #680), then cap.
func filterExecutionsLikeStore(rows []config.PurchaseExecution, statuses []string, limit int) []config.PurchaseExecution {
	if len(statuses) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = config.DefaultListLimit
	}
	limit = min(limit, config.MaxListLimit)
	var out []config.PurchaseExecution
	for i := range rows {
		r := &rows[i]
		if !slices.Contains(statuses, r.Status) || (r.Status == "completed" && r.Error == "") {
			continue
		}
		out = append(out, *r)
	}
	now := time.Now()
	sort.SliceStable(out, func(i, j int) bool { return executionSortsBefore(&out[i], &out[j], now) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// executionSortsBefore mirrors GetExecutionsByStatuses' ORDER BY: rows that
// are not future pending steps first, newest first; future pending steps
// after them, soonest first.
func executionSortsBefore(a, b *config.PurchaseExecution, now time.Time) bool {
	aFuture := a.Status == "pending" && a.ScheduledDate.After(now)
	bFuture := b.Status == "pending" && b.ScheduledDate.After(now)
	if aFuture != bFuture {
		return bFuture
	}
	if aFuture {
		return a.ScheduledDate.Before(b.ScheduledDate)
	}
	return a.ScheduledDate.After(b.ScheduledDate)
}

// ErrHistorySummaryAggregateNotStubbed is HistorySummaryAggregate's default
// error. The History handler treats an aggregate error as "totals unavailable"
// and falls back to summarizing the rows it fetched, so tests written before
// the aggregate existed (issue #675) keep asserting the page summary without
// registering an expectation.
var ErrHistorySummaryAggregateNotStubbed = errors.New("mock: HistorySummaryAggregate not stubbed")

// HistorySummaryAggregate mocks the HistorySummaryAggregate operation. With no
// expectation registered it returns ErrHistorySummaryAggregateNotStubbed.
func (m *MockConfigStore) HistorySummaryAggregate(ctx context.Context, filter config.HistorySummaryFilter) (*config.HistorySummaryGroups, error) {
	m.record("HistorySummaryAggregate", ctx, filter)
	if !isExpected(&m.Mock, "HistorySummaryAggregate") {
		return nil, ErrHistorySummaryAggregateNotStubbed
	}
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.HistorySummaryGroups)
	if !ok {
		m.reporter.wrongReturnType("HistorySummaryAggregate", "*config.HistorySummaryGroups", args.Get(0))
	}
	return v, args.Error(1)
}

// MarkCollectionStarted mocks the MarkCollectionStarted operation.
// Defaults to (MockOwnerToken, true, nil) when no expectation is registered.
func (m *MockConfigStore) MarkCollectionStarted(ctx context.Context) (token string, ok bool, err error) {
	m.record("MarkCollectionStarted", ctx)
	if !isExpected(&m.Mock, "MarkCollectionStarted") {
		return MockOwnerToken, true, nil
	}
	args := m.Called(ctx)
	return args.String(0), args.Bool(1), args.Error(2)
}

// ClearCollectionStarted mocks the ClearCollectionStarted operation.
// Defaults to nil when no expectation is registered.
func (m *MockConfigStore) ClearCollectionStarted(ctx context.Context, token string) error {
	m.record("ClearCollectionStarted", ctx, token)
	if !isExpected(&m.Mock, "ClearCollectionStarted") {
		return nil
	}
	return m.Called(ctx, token).Error(0)
}

// ClaimCollectionInvocation mocks the ClaimCollectionInvocation operation.
// Defaults to (MockInvocationToken, true, nil) when no expectation is
// registered, so a token-carrying collect run that wins the lock keeps
// owning the marker under a distinct invocation token.
func (m *MockConfigStore) ClaimCollectionInvocation(ctx context.Context, markerToken string) (token string, ok bool, err error) {
	m.record("ClaimCollectionInvocation", ctx, markerToken)
	if !isExpected(&m.Mock, "ClaimCollectionInvocation") {
		return MockInvocationToken, true, nil
	}
	args := m.Called(ctx, markerToken)
	return args.String(0), args.Bool(1), args.Error(2)
}

// GetLadderConfigs mocks the GetLadderConfigs operation.
// Returns an empty slice when no expectation is registered so tests that do
// not exercise laddering keep working without mock setup.
func (m *MockConfigStore) GetLadderConfigs(ctx context.Context) ([]config.LadderConfigDB, error) {
	m.record("GetLadderConfigs", ctx)
	if !isExpected(&m.Mock, "GetLadderConfigs") {
		return []config.LadderConfigDB{}, nil
	}
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.LadderConfigDB)
	if !ok {
		m.reporter.wrongReturnType("GetLadderConfigs", "[]config.LadderConfigDB", args.Get(0))
	}
	return v, args.Error(1)
}

// GetLadderConfig mocks the GetLadderConfig operation.
// Returns (nil, nil) when no expectation is registered.
func (m *MockConfigStore) GetLadderConfig(ctx context.Context, cloudAccountID, provider string) (*config.LadderConfigDB, error) {
	m.record("GetLadderConfig", ctx, cloudAccountID, provider)
	if !isExpected(&m.Mock, "GetLadderConfig") {
		return nil, nil
	}
	args := m.Called(ctx, cloudAccountID, provider)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.LadderConfigDB)
	if !ok {
		m.reporter.wrongReturnType("GetLadderConfig", "*config.LadderConfigDB", args.Get(0))
	}
	return v, args.Error(1)
}

// UpsertLadderConfig mocks the UpsertLadderConfig operation.
func (m *MockConfigStore) UpsertLadderConfig(ctx context.Context, cfg *config.LadderConfigDB) (*config.LadderConfigDB, error) {
	m.record("UpsertLadderConfig", ctx, cfg)
	args := m.Called(ctx, cfg)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.LadderConfigDB)
	if !ok {
		m.reporter.wrongReturnType("UpsertLadderConfig", "*config.LadderConfigDB", args.Get(0))
	}
	return v, args.Error(1)
}

// GetLadderSweepCeiling mocks the GetLadderSweepCeiling operation.
// Returns (nil, nil) when no expectation is registered.
func (m *MockConfigStore) GetLadderSweepCeiling(ctx context.Context, configID string) (*int64, error) {
	m.record("GetLadderSweepCeiling", ctx, configID)
	if !isExpected(&m.Mock, "GetLadderSweepCeiling") {
		return nil, nil
	}
	args := m.Called(ctx, configID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*int64)
	if !ok {
		m.reporter.wrongReturnType("GetLadderSweepCeiling", "*int64", args.Get(0))
	}
	return v, args.Error(1)
}

// SetLadderSweepCeiling mocks the SetLadderSweepCeiling operation.
func (m *MockConfigStore) SetLadderSweepCeiling(ctx context.Context, configID string, usdPerHr *float64) error {
	m.record("SetLadderSweepCeiling", ctx, configID, usdPerHr)
	return m.Called(ctx, configID, usdPerHr).Error(0)
}

// SaveLadderRun mocks the SaveLadderRun operation.
// Returns (nil, nil) when no expectation is registered.
func (m *MockConfigStore) SaveLadderRun(ctx context.Context, run *config.LadderRunDB) (*config.LadderRunDB, error) {
	m.record("SaveLadderRun", ctx, run)
	if !isExpected(&m.Mock, "SaveLadderRun") {
		return nil, nil
	}
	args := m.Called(ctx, run)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.LadderRunDB)
	if !ok {
		m.reporter.wrongReturnType("SaveLadderRun", "*config.LadderRunDB", args.Get(0))
	}
	return v, args.Error(1)
}

// PlanAndSaveLadderRun mocks the PlanAndSaveLadderRun operation. With no
// expectation registered it runs build with zero in-flight and returns the run
// it produced (transaction succeeds), so tests that only care about other
// calls stay green.
func (m *MockConfigStore) PlanAndSaveLadderRun(ctx context.Context, configID string, asOf time.Time, build config.LadderPlanBuilder) (*config.LadderRunDB, error) {
	m.record("PlanAndSaveLadderRun", ctx, configID, asOf)
	if !isExpected(&m.Mock, "PlanAndSaveLadderRun") {
		run, _, err := build(nil)
		return run, err
	}
	args := m.Called(ctx, configID, asOf)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.LadderRunDB)
	if !ok {
		m.reporter.wrongReturnType("PlanAndSaveLadderRun", "*config.LadderRunDB", args.Get(0))
	}
	return v, args.Error(1)
}

// GetLadderRun mocks the GetLadderRun operation.
// Returns (nil, nil) when no expectation is registered.
func (m *MockConfigStore) GetLadderRun(ctx context.Context, id string) (*config.LadderRunDB, error) {
	m.record("GetLadderRun", ctx, id)
	if !isExpected(&m.Mock, "GetLadderRun") {
		return nil, nil
	}
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.LadderRunDB)
	if !ok {
		m.reporter.wrongReturnType("GetLadderRun", "*config.LadderRunDB", args.Get(0))
	}
	return v, args.Error(1)
}

// SaveLadderTranches mocks the SaveLadderTranches operation.
// Returns nil (no-op) when no expectation is registered.
func (m *MockConfigStore) SaveLadderTranches(ctx context.Context, tranches []config.LadderTrancheDB) error {
	m.record("SaveLadderTranches", ctx, tranches)
	if !isExpected(&m.Mock, "SaveLadderTranches") {
		return nil
	}
	return m.Called(ctx, tranches).Error(0)
}

// LatestLadderRunStartedAt mocks the LatestLadderRunStartedAt operation.
// Returns (nil, nil) when no expectation is registered.
func (m *MockConfigStore) LatestLadderRunStartedAt(ctx context.Context, configID string) (*time.Time, error) {
	m.record("LatestLadderRunStartedAt", ctx, configID)
	if !isExpected(&m.Mock, "LatestLadderRunStartedAt") {
		return nil, nil
	}
	args := m.Called(ctx, configID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*time.Time)
	if !ok {
		m.reporter.wrongReturnType("LatestLadderRunStartedAt", "*time.Time", args.Get(0))
	}
	return v, args.Error(1)
}

// LatestLadderRunID mocks the LatestLadderRunID operation.
// Returns config.ErrNotFound when no expectation is registered (fail closed).
func (m *MockConfigStore) LatestLadderRunID(ctx context.Context, configID string) (string, error) {
	m.record("LatestLadderRunID", ctx, configID)
	if !isExpected(&m.Mock, "LatestLadderRunID") {
		return "", config.ErrNotFound
	}
	args := m.Called(ctx, configID)
	return args.String(0), args.Error(1)
}

// CancelLadderRun mocks the CancelLadderRun operation.
// Returns (nil, nil) when no expectation is registered (CAS race-lost path).
func (m *MockConfigStore) CancelLadderRun(ctx context.Context, id string, fromStatuses []ladder.RunStatus, cancelledBy string) (*config.LadderRunDB, error) {
	m.record("CancelLadderRun", ctx, id, fromStatuses, cancelledBy)
	if !isExpected(&m.Mock, "CancelLadderRun") {
		return nil, nil
	}
	args := m.Called(ctx, id, fromStatuses, cancelledBy)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.LadderRunDB)
	if !ok {
		m.reporter.wrongReturnType("CancelLadderRun", "*config.LadderRunDB", args.Get(0))
	}
	return v, args.Error(1)
}

// TransitionLadderRunStatus mocks the TransitionLadderRunStatus operation.
// Returns (nil, nil) when no expectation is registered (CAS race-lost path).
func (m *MockConfigStore) TransitionLadderRunStatus(ctx context.Context, id string, fromStatuses []ladder.RunStatus, toStatus ladder.RunStatus) (*config.LadderRunDB, error) {
	m.record("TransitionLadderRunStatus", ctx, id, fromStatuses, toStatus)
	if !isExpected(&m.Mock, "TransitionLadderRunStatus") {
		return nil, nil
	}
	args := m.Called(ctx, id, fromStatuses, toStatus)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.LadderRunDB)
	if !ok {
		m.reporter.wrongReturnType("TransitionLadderRunStatus", "*config.LadderRunDB", args.Get(0))
	}
	return v, args.Error(1)
}

// CountLadderRunTrancheStatuses mocks the CountLadderRunTrancheStatuses operation.
// Defaults to an empty count when no expectation is registered.
func (m *MockConfigStore) CountLadderRunTrancheStatuses(ctx context.Context, runID string) (map[ladder.TrancheStatus]int, error) {
	m.record("CountLadderRunTrancheStatuses", ctx, runID)
	if !isExpected(&m.Mock, "CountLadderRunTrancheStatuses") {
		return map[ladder.TrancheStatus]int{}, nil
	}
	args := m.Called(ctx, runID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(map[ladder.TrancheStatus]int)
	if !ok {
		m.reporter.wrongReturnType("CountLadderRunTrancheStatuses", "map[ladder.TrancheStatus]int", args.Get(0))
	}
	return v, args.Error(1)
}

// UpsertNotificationMute mocks the UpsertNotificationMute operation.
// Defaults to nil when no expectation is registered.
func (m *MockConfigStore) UpsertNotificationMute(ctx context.Context, recipientEmail, scope, unmuteToken string) error {
	m.record("UpsertNotificationMute", ctx, recipientEmail, scope, unmuteToken)
	if !isExpected(&m.Mock, "UpsertNotificationMute") {
		return nil
	}
	return m.Called(ctx, recipientEmail, scope, unmuteToken).Error(0)
}

// IsNotificationMuted mocks the IsNotificationMuted operation.
// Defaults to (false, nil) when no expectation is registered.
func (m *MockConfigStore) IsNotificationMuted(ctx context.Context, recipientEmail, scope string) (bool, error) {
	m.record("IsNotificationMuted", ctx, recipientEmail, scope)
	if !isExpected(&m.Mock, "IsNotificationMuted") {
		return false, nil
	}
	args := m.Called(ctx, recipientEmail, scope)
	return args.Bool(0), args.Error(1)
}

// isExpected reports whether mock has any .On() expectation for method.
func isExpected(m *mock.Mock, method string) bool {
	for _, call := range m.ExpectedCalls {
		if call.Method == method {
			return true
		}
	}
	return false
}

// wrongReturnType reports that method's registered .Return value got is not of
// type want. It fails the test registered through the mock's Test method, or
// panics when there is none. See report.go.
func (r *testReporter) wrongReturnType(method, want string, got interface{}) {
	r.fail(fmt.Sprintf("mock: %s returned %T from its .Return(...), but the method returns %s; "+
		"fix the value passed to .Return for this expectation", method, got, want))
}

// Compile-time interface compliance checks.
var _ config.StoreInterface = (*MockConfigStore)(nil)
var _ auth.StoreInterface = (*MockAuthStore)(nil)
