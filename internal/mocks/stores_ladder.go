package mocks

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

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
