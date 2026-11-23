package mocks

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
)

// LinkRetryExecutionAtomic mocks the LinkRetryExecutionAtomic operation.
// Defaults to (true, nil) when no expectation is registered so tests that
// only need the happy path don't require explicit mock setup. Tests
// exercising the CAS-race path (a concurrent retry already claimed the
// row) register an expectation that returns (false, nil).
func (m *MockConfigStore) LinkRetryExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID, retryExecutionID string) (bool, error) {
	m.record("LinkRetryExecutionAtomic", ctx, tx, executionID, retryExecutionID)
	if !isExpected(&m.Mock, "LinkRetryExecutionAtomic") {
		return true, nil
	}
	args := m.Called(ctx, tx, executionID, retryExecutionID)
	return args.Bool(0), args.Error(1)
}

// LinkUnsentRetryExecutionAtomic mocks the proven-unsent link CAS (no default: testify panics if unexpected).
func (m *MockConfigStore) LinkUnsentRetryExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID, retryExecutionID string) (bool, error) {
	m.record("LinkUnsentRetryExecutionAtomic", ctx, tx, executionID, retryExecutionID)
	args := m.Called(ctx, tx, executionID, retryExecutionID)
	return args.Bool(0), args.Error(1)
}

// LockRetryFamilyTx mocks the fan-out family advisory lock (no default: testify panics if unexpected).
func (m *MockConfigStore) LockRetryFamilyTx(ctx context.Context, tx pgx.Tx, family string) error {
	m.record("LockRetryFamilyTx", ctx, tx, family)
	return m.Called(ctx, tx, family).Error(0)
}

// FamilyRetryClaimedTx mocks the fan-out family claim query (no default: testify panics if unexpected).
func (m *MockConfigStore) FamilyRetryClaimedTx(ctx context.Context, tx pgx.Tx, family, planID, selfID string, selfIsRoot bool) (bool, error) {
	m.record("FamilyRetryClaimedTx", ctx, tx, family, planID, selfID, selfIsRoot)
	args := m.Called(ctx, tx, family, planID, selfID, selfIsRoot)
	return args.Bool(0), args.Error(1)
}

// LockLadderTrancheForRetryTx mocks the operation. Defaults to (not a ladder
// execution) when no expectation is registered.
func (m *MockConfigStore) LockLadderTrancheForRetryTx(ctx context.Context, tx pgx.Tx, idempotencyKey string) (ladder.TrancheStatus, bool, error) {
	m.record("LockLadderTrancheForRetryTx", ctx, tx, idempotencyKey)
	if !isExpected(&m.Mock, "LockLadderTrancheForRetryTx") {
		return "", false, nil
	}
	args := m.Called(ctx, tx, idempotencyKey)
	status, ok := args.Get(0).(ladder.TrancheStatus)
	if !ok {
		m.reporter.wrongReturnType("LockLadderTrancheForRetryTx", "ladder.TrancheStatus", args.Get(0))
	}
	return status, args.Bool(1), args.Error(2)
}

// GetLadderExecutionGate mocks the operation. Defaults to (nil, nil), "not a
// ladder execution", when no expectation is registered.
func (m *MockConfigStore) GetLadderExecutionGate(ctx context.Context, idempotencyKey string) (*config.LadderExecutionGate, error) {
	m.record("GetLadderExecutionGate", ctx, idempotencyKey)
	if !isExpected(&m.Mock, "GetLadderExecutionGate") {
		return nil, nil
	}
	args := m.Called(ctx, idempotencyKey)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	gate, ok := args.Get(0).(*config.LadderExecutionGate)
	if !ok {
		m.reporter.wrongReturnType("GetLadderExecutionGate", "*config.LadderExecutionGate", args.Get(0))
	}
	return gate, args.Error(1)
}

// CancelExecutionAtomic mocks the CancelExecutionAtomic operation.
// Defaults to (true, "canceled", nil) when no expectation is registered
// so tests that only need the happy path don't require explicit mock setup.
// Tests exercising the CAS-race path (zero rows affected) register an
// expectation that returns (false, <racing_status>, nil).
func (m *MockConfigStore) CancelExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy *string) (bool, string, error) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	m.record("CancelExecutionAtomic", ctx, tx, executionID, cancelledBy)
	if !isExpected(&m.Mock, "CancelExecutionAtomic") {
		return true, "canceled", nil
	}
	args := m.Called(ctx, tx, executionID, cancelledBy)
	return args.Bool(0), args.String(1), args.Error(2)
}

// CancelExecutionAtomicTokenPinned mocks the token-pinned cancel (issue
// #1190). Like TransitionExecutionStatusTokenPinned it has no default and does
// not delegate to CancelExecutionAtomic: a caller that must pin the approval
// token is expected to call this method, so a test has to register it
// explicitly.
func (m *MockConfigStore) CancelExecutionAtomicTokenPinned(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy *string, rawToken string) (bool, string, error) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	m.record("CancelExecutionAtomicTokenPinned", ctx, tx, executionID, cancelledBy, rawToken)
	args := m.Called(ctx, tx, executionID, cancelledBy, rawToken)
	return args.Bool(0), args.String(1), args.Error(2)
}

// CancelScheduledExecutionAtomic mocks the CancelScheduledExecutionAtomic
// operation (Gmail-style pre-fire delay revoke, issue #290 wave-2). Default
// is the happy path (true, "canceled", nil) so the scheduled-revoke tests
// inherit the same low-ceremony pattern as CancelExecutionAtomic above.
// Tests exercising the CAS-race path (scheduler tick already fired) register
// an expectation that returns (false, <racing_status>, nil), typically
// (false, "approved", nil) to simulate the scheduler winning the race.
func (m *MockConfigStore) CancelScheduledExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy *string) (bool, string, error) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	m.record("CancelScheduledExecutionAtomic", ctx, tx, executionID, cancelledBy)
	if !isExpected(&m.Mock, "CancelScheduledExecutionAtomic") {
		return true, "canceled", nil
	}
	args := m.Called(ctx, tx, executionID, cancelledBy)
	return args.Bool(0), args.String(1), args.Error(2)
}

// GetPendingExecutions mocks the GetPendingExecutions operation.
func (m *MockConfigStore) GetPendingExecutions(ctx context.Context) ([]config.PurchaseExecution, error) {
	m.record("GetPendingExecutions", ctx)
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseExecution)
	if !ok {
		m.reporter.wrongReturnType("GetPendingExecutions", "[]config.PurchaseExecution", args.Get(0))
	}
	return v, args.Error(1)
}

// GetExecutionByID mocks the GetExecutionByID operation.
func (m *MockConfigStore) GetExecutionByID(ctx context.Context, executionID string) (*config.PurchaseExecution, error) {
	m.record("GetExecutionByID", ctx, executionID)
	args := m.Called(ctx, executionID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.PurchaseExecution)
	if !ok {
		m.reporter.wrongReturnType("GetExecutionByID", "*config.PurchaseExecution", args.Get(0))
	}
	return v, args.Error(1)
}

// GetExecutionByPlanAndDate mocks the GetExecutionByPlanAndDate operation.
func (m *MockConfigStore) GetExecutionByPlanAndDate(ctx context.Context, planID string, scheduledDate time.Time) (*config.PurchaseExecution, error) {
	m.record("GetExecutionByPlanAndDate", ctx, planID, scheduledDate)
	args := m.Called(ctx, planID, scheduledDate)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.PurchaseExecution)
	if !ok {
		m.reporter.wrongReturnType("GetExecutionByPlanAndDate", "*config.PurchaseExecution", args.Get(0))
	}
	return v, args.Error(1)
}

// GetUserEmailByID mocks the GetUserEmailByID operation. Defaults to ("", nil)
// when no Fn is set and no expectation is registered, so tests that never
// exercise the 4-eyes identity-resolution path (mode off, the default) don't
// need to stub this method.
func (m *MockConfigStore) GetUserEmailByID(ctx context.Context, userID string) (string, error) {
	m.record("GetUserEmailByID", ctx, userID)
	if m.GetUserEmailByIDFn != nil {
		return m.GetUserEmailByIDFn(ctx, userID)
	}
	if !isExpected(&m.Mock, "GetUserEmailByID") {
		return "", nil
	}
	args := m.Called(ctx, userID)
	return args.String(0), args.Error(1)
}
