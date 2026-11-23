package mocks

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
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
