package mocks

import (
	"context"
	"sort"
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

// The ramp-ledger mocks default to an empty answer when unregistered so tests
// that predate them keep exercising their own path.
func (m *MockConfigStore) SetPlanRampBaselineTx(ctx context.Context, tx pgx.Tx, planID string, baseline config.PlanRampBaseline) (bool, error) {
	m.record("SetPlanRampBaselineTx", ctx, tx, planID, baseline)
	if !isExpected(&m.Mock, "SetPlanRampBaselineTx") {
		return false, nil
	}
	args := m.Called(ctx, tx, planID, baseline)
	return args.Bool(0), args.Error(1)
}

func (m *MockConfigStore) GetPlanRampBaselineTx(ctx context.Context, tx pgx.Tx, planID string) (config.PlanRampBaseline, error) {
	m.record("GetPlanRampBaselineTx", ctx, tx, planID)
	if !isExpected(&m.Mock, "GetPlanRampBaselineTx") {
		return nil, nil
	}
	args := m.Called(ctx, tx, planID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(config.PlanRampBaseline)
	if !ok {
		m.reporter.wrongReturnType("GetPlanRampBaselineTx", "config.PlanRampBaseline", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) ListPlanCommittedTx(ctx context.Context, tx pgx.Tx, planID string) (map[string]config.PlanRampQuantity, error) {
	m.record("ListPlanCommittedTx", ctx, tx, planID)
	if !isExpected(&m.Mock, "ListPlanCommittedTx") {
		return nil, nil
	}
	args := m.Called(ctx, tx, planID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(map[string]config.PlanRampQuantity)
	if !ok {
		m.reporter.wrongReturnType("ListPlanCommittedTx", "map[string]config.PlanRampQuantity", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) CompleteTargetMetStepTx(ctx context.Context, tx pgx.Tx, planID string, stepNumber int, rootID, note string) error {
	m.record("CompleteTargetMetStepTx", ctx, tx, planID, stepNumber, rootID, note)
	return m.Called(ctx, tx, planID, stepNumber, rootID, note).Error(0)
}

func (m *MockConfigStore) SetExecutionRecommendationsIfEmptyTx(ctx context.Context, tx pgx.Tx, executionID string, recs []config.RecommendationRecord, upfront, savings float64) (bool, error) {
	m.record("SetExecutionRecommendationsIfEmptyTx", ctx, tx, executionID, recs, upfront, savings)
	args := m.Called(ctx, tx, executionID, recs, upfront, savings)
	return args.Bool(0), args.Error(1)
}

func (m *MockConfigStore) ExpireExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID string, observedScheduledDate time.Time) (bool, error) {
	m.record("ExpireExecutionAtomic", ctx, tx, executionID, observedScheduledDate)
	args := m.Called(ctx, tx, executionID, observedScheduledDate)
	return args.Bool(0), args.Error(1)
}

func (m *MockConfigStore) OccupiedRampStepsInRangeTx(ctx context.Context, tx pgx.Tx, planID string, from, to int) ([]int, error) {
	m.record("OccupiedRampStepsInRangeTx", ctx, tx, planID, from, to)
	if !isExpected(&m.Mock, "OccupiedRampStepsInRangeTx") {
		return nil, nil
	}
	args := m.Called(ctx, tx, planID, from, to)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]int)
	if !ok {
		m.reporter.wrongReturnType("OccupiedRampStepsInRangeTx", "[]int", args.Get(0))
	}
	return v, args.Error(1)
}

// UpdatePurchasePlanTx mocks the UpdatePurchasePlanTx operation. Falls
// back to UpdatePurchasePlan when no expectation is registered so tests
// that don't care about the Tx variant stay green -- same pattern as
// SavePurchaseExecutionTx below.
func (m *MockConfigStore) UpdatePurchasePlanTx(ctx context.Context, tx pgx.Tx, plan *config.PurchasePlan) error {
	m.record("UpdatePurchasePlanTx", ctx, tx, plan)
	if !isExpected(&m.Mock, "UpdatePurchasePlanTx") {
		// The delegate records itself too, so a single Tx call shows up in the
		// call log under both names. Intended: assertions about the non-Tx
		// method see the write the Tx variant performed on their behalf.
		return m.UpdatePurchasePlan(ctx, plan)
	}
	args := m.Called(ctx, tx, plan)
	return args.Error(0)
}

// runPlanGate gives gate the plan and account ids the mock holds for planID,
// the way the real store reads them under the lock (ids only when withIDs).
func (m *MockConfigStore) runPlanGate(ctx context.Context, planID string, fallback *config.PurchasePlan, withIDs bool, gate config.PlanWriteGate) error {
	if gate == nil {
		return nil
	}
	locked := fallback
	if isExpected(&m.Mock, "GetPurchasePlan") {
		if p, err := m.GetPurchasePlan(ctx, planID); err == nil && p != nil {
			locked = p
		}
	}
	var accounts []config.CloudAccount
	if withIDs && (m.GetPlanAccountsFn != nil || isExpected(&m.Mock, "GetPlanAccounts")) {
		var err error
		if accounts, err = m.GetPlanAccounts(ctx, planID); err != nil {
			return err
		}
	}
	ids := make([]string, 0, len(accounts))
	for i := range accounts {
		ids = append(ids, accounts[i].ID)
	}
	sort.Strings(ids)
	return gate(locked, ids)
}

// UpdatePurchasePlanGated runs gate, then delegates to UpdatePurchasePlan, the
// order the real store uses under the plan lock.
func (m *MockConfigStore) UpdatePurchasePlanGated(ctx context.Context, plan *config.PurchasePlan, gate config.PlanWriteGate) error {
	m.record("UpdatePurchasePlanGated", ctx, plan)
	if err := m.runPlanGate(ctx, plan.ID, plan, true, gate); err != nil {
		return err
	}
	return m.UpdatePurchasePlan(ctx, plan)
}

// DeletePurchasePlan mocks the DeletePurchasePlan operation.
func (m *MockConfigStore) DeletePurchasePlan(ctx context.Context, planID string) error {
	m.record("DeletePurchasePlan", ctx, planID)
	args := m.Called(ctx, planID)
	return args.Error(0)
}

// ListPurchasePlans mocks the ListPurchasePlans operation.
func (m *MockConfigStore) ListPurchasePlans(ctx context.Context, filter config.PurchasePlanFilter) ([]config.PurchasePlan, error) {
	m.record("ListPurchasePlans", ctx, filter)
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchasePlan)
	if !ok {
		m.reporter.wrongReturnType("ListPurchasePlans", "[]config.PurchasePlan", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) SetPlanAccounts(ctx context.Context, planID string, accountIDs []string) error {
	m.record("SetPlanAccounts", ctx, planID, accountIDs)
	if m.SetPlanAccountsFn != nil {
		return m.SetPlanAccountsFn(ctx, planID, accountIDs)
	}
	if !isExpected(&m.Mock, "SetPlanAccounts") {
		return nil
	}
	args := m.Called(ctx, planID, accountIDs)
	return args.Error(0)
}

// SetPlanAccountsGated runs gate, then delegates to SetPlanAccounts.
func (m *MockConfigStore) SetPlanAccountsGated(ctx context.Context, planID string, accountIDs []string, gate config.PlanWriteGate) error {
	m.record("SetPlanAccountsGated", ctx, planID, accountIDs)
	if err := m.runPlanGate(ctx, planID, &config.PurchasePlan{ID: planID}, false, gate); err != nil {
		return err
	}
	return m.SetPlanAccounts(ctx, planID, accountIDs)
}

func (m *MockConfigStore) GetPlanAccounts(ctx context.Context, planID string) ([]config.CloudAccount, error) {
	m.record("GetPlanAccounts", ctx, planID)
	if m.GetPlanAccountsFn != nil {
		return m.GetPlanAccountsFn(ctx, planID)
	}
	if !isExpected(&m.Mock, "GetPlanAccounts") {
		return nil, nil
	}
	args := m.Called(ctx, planID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.CloudAccount)
	if !ok {
		m.reporter.wrongReturnType("GetPlanAccounts", "[]config.CloudAccount", args.Get(0))
	}
	return v, args.Error(1)
}
