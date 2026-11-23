package mocks

import (
	"context"
	"errors"
	"fmt"

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

// Plan ↔ account association

// Recommendations cache
// These methods default to zero-value returns when no .On() expectation is
// registered — they were opt-in in the per-package mocks and many callers
// don't set expectations for them. Tests that want to assert on these paths
// register an explicit expectation via .On(...).Return(...).

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
