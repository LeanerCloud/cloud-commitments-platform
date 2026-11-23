package mocks

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
)

// CreatePurchasePlan mocks the CreatePurchasePlan operation.
func (m *MockConfigStore) CreatePurchasePlan(ctx context.Context, plan *config.PurchasePlan) error {
	m.record("CreatePurchasePlan", ctx, plan)
	args := m.Called(ctx, plan)
	return args.Error(0)
}

// PlanNameFixture describes an existing plan for NameTakenFixture.
type PlanNameFixture struct {
	ID       string
	Name     string
	Accounts []config.CloudAccount
}

// CreatePurchasePlanWithAccounts mocks the operation. It calls visible with
// NameTakenFixture's accounts, so a handler test fails if the predicate is
// wrong. With the fixture unset or invisible it uses a registered expectation
// (arguments: ctx, plan, accountIDs, the predicate) and otherwise returns nil.
func (m *MockConfigStore) CreatePurchasePlanWithAccounts(ctx context.Context, plan *config.PurchasePlan, accountIDs []string, visible func(accounts []config.CloudAccount) bool) error {
	m.record("CreatePurchasePlanWithAccounts", ctx, plan, accountIDs)
	if f := m.NameTakenFixture; f != nil && visible(f.Accounts) {
		return &config.PlanNameTakenError{ExistingID: f.ID, ExistingName: f.Name}
	}
	if !isExpected(&m.Mock, "CreatePurchasePlanWithAccounts") {
		return nil
	}
	return m.Called(ctx, plan, accountIDs, visible).Error(0)
}

// GetPurchasePlan mocks the GetPurchasePlan operation. When GetPurchasePlanFn is
// non-nil it takes priority. When no expectation is registered a minimal plan
// stub {ID: planID} is returned so tests that don't care about the plan fields
// keep working without explicit setup.
func (m *MockConfigStore) GetPurchasePlan(ctx context.Context, planID string) (*config.PurchasePlan, error) {
	m.record("GetPurchasePlan", ctx, planID)
	if m.GetPurchasePlanFn != nil {
		return m.GetPurchasePlanFn(ctx, planID)
	}
	if !isExpected(&m.Mock, "GetPurchasePlan") {
		return &config.PurchasePlan{ID: planID}, nil
	}
	args := m.Called(ctx, planID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.PurchasePlan)
	if !ok {
		m.reporter.wrongReturnType("GetPurchasePlan", "*config.PurchasePlan", args.Get(0))
	}
	return v, args.Error(1)
}

// UpdatePurchasePlan mocks the UpdatePurchasePlan operation.
func (m *MockConfigStore) UpdatePurchasePlan(ctx context.Context, plan *config.PurchasePlan) error {
	m.record("UpdatePurchasePlan", ctx, plan)
	args := m.Called(ctx, plan)
	return args.Error(0)
}

func (m *MockConfigStore) StampPlanNotificationSent(ctx context.Context, planID string, at time.Time) error {
	m.record("StampPlanNotificationSent", ctx, planID, at)
	return m.Called(ctx, planID, at).Error(0)
}

// CompletePlanStep mocks the idempotent ramp-step advance operation.
func (m *MockConfigStore) CompletePlanStep(ctx context.Context, planID string, stepNumber int) error {
	m.record("CompletePlanStep", ctx, planID, stepNumber)
	args := m.Called(ctx, planID, stepNumber)
	return args.Error(0)
}

// GetStuckRampSteps mocks the stuck-ramp report backing plan health.
// Unregistered by default so the many tests that only care about the health
// score's execution counts stay green: no stuck steps is the shape of a plan
// with nothing wrong with it.
func (m *MockConfigStore) GetStuckRampSteps(ctx context.Context) (map[string]config.RampStepBlock, error) {
	m.record("GetStuckRampSteps", ctx)
	if !isExpected(&m.Mock, "GetStuckRampSteps") {
		return map[string]config.RampStepBlock{}, nil
	}
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(map[string]config.RampStepBlock)
	if !ok {
		m.reporter.wrongReturnType("GetStuckRampSteps", "map[string]config.RampStepBlock", args.Get(0))
	}
	return v, args.Error(1)
}

// GetDoomedNextRoots mocks the doomed-root report backing plan health.
// Unregistered by default, like GetStuckRampSteps: no doomed root is the shape
// of a plan with nothing wrong with it.
func (m *MockConfigStore) GetDoomedNextRoots(ctx context.Context, now time.Time) (map[string]config.DoomedRoot, error) {
	m.record("GetDoomedNextRoots", ctx, now)
	if !isExpected(&m.Mock, "GetDoomedNextRoots") {
		return map[string]config.DoomedRoot{}, nil
	}
	args := m.Called(ctx, now)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(map[string]config.DoomedRoot)
	if !ok {
		m.reporter.wrongReturnType("GetDoomedNextRoots", "map[string]config.DoomedRoot", args.Get(0))
	}
	return v, args.Error(1)
}

// LockPurchasePlanTx mocks the per-plan ramp lock. Falls back to
// GetPurchasePlan when no expectation is registered, so tests that only care
// about what the transaction writes keep working: the lock is a concurrency
// property, and a mock cannot hold one anyway. The real contract is measured
// against Postgres in the api integration tests.
func (m *MockConfigStore) LockPurchasePlanTx(ctx context.Context, tx pgx.Tx, planID string) (*config.PurchasePlan, error) {
	m.record("LockPurchasePlanTx", ctx, tx, planID)
	if !isExpected(&m.Mock, "LockPurchasePlanTx") {
		return m.GetPurchasePlan(ctx, planID)
	}
	args := m.Called(ctx, tx, planID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.PurchasePlan)
	if !ok {
		m.reporter.wrongReturnType("LockPurchasePlanTx", "*config.PurchasePlan", args.Get(0))
	}
	return v, args.Error(1)
}

// LockPlanProvidersTx mocks the per-provider advisory lock. A mock cannot hold
// a lock, so it succeeds unless an expectation says otherwise; the real
// contract is measured against Postgres in the purchase integration tests.
func (m *MockConfigStore) LockPlanProvidersTx(ctx context.Context, tx pgx.Tx, providers []string) error {
	m.record("LockPlanProvidersTx", ctx, tx, providers)
	if !isExpected(&m.Mock, "LockPlanProvidersTx") {
		return nil
	}
	return m.Called(ctx, tx, providers).Error(0)
}

// OccupiedRampStepsInRangeTx mocks the already-covered ramp-step probe.
// Unregistered by default so the many create-purchases tests that predate the
// probe keep exercising the create path: no occupied steps in range is the
// shape of a plan whose next steps are still unscheduled, which is what those
// tests set up.
func (m *MockConfigStore) ListExecutionsForPlanStepTx(ctx context.Context, tx pgx.Tx, planID string, stepNumber int) ([]config.PurchaseExecution, error) {
	m.record("ListExecutionsForPlanStepTx", ctx, tx, planID, stepNumber)
	if !isExpected(&m.Mock, "ListExecutionsForPlanStepTx") {
		return nil, nil
	}
	args := m.Called(ctx, tx, planID, stepNumber)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseExecution)
	if !ok {
		m.reporter.wrongReturnType("ListExecutionsForPlanStepTx", "[]config.PurchaseExecution", args.Get(0))
	}
	return v, args.Error(1)
}
