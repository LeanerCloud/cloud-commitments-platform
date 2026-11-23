package mocks

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
)

// SavePurchaseExecution mocks the SavePurchaseExecution operation.
// SavePurchaseExecutionFn takes priority when non-nil.
func (m *MockConfigStore) SavePurchaseExecution(ctx context.Context, exec *config.PurchaseExecution) error {
	m.record("SavePurchaseExecution", ctx, exec)
	if m.SavePurchaseExecutionFn != nil {
		return m.SavePurchaseExecutionFn(ctx, exec)
	}
	args := m.Called(ctx, exec)
	return args.Error(0)
}

// SaveExecutionIfStatus mocks the guarded save. With no expectation registered
// it delegates to SavePurchaseExecution so existing tests keep their behavior.
func (m *MockConfigStore) SaveExecutionIfStatus(ctx context.Context, exec *config.PurchaseExecution, expected []string) error {
	m.record("SaveExecutionIfStatus", ctx, exec, expected)
	if !isExpected(&m.Mock, "SaveExecutionIfStatus") {
		return m.SavePurchaseExecution(ctx, exec)
	}
	args := m.Called(ctx, exec, expected)
	return args.Error(0)
}

// SaveExecutionIfStatusTx mocks the tx variant; same delegation as above.
func (m *MockConfigStore) SaveExecutionIfStatusTx(ctx context.Context, tx pgx.Tx, exec *config.PurchaseExecution, expected []string) error {
	m.record("SaveExecutionIfStatusTx", ctx, tx, exec, expected)
	if !isExpected(&m.Mock, "SaveExecutionIfStatusTx") {
		return m.SavePurchaseExecutionTx(ctx, tx, exec)
	}
	args := m.Called(ctx, tx, exec, expected)
	return args.Error(0)
}

// RecoverReapedExecution mocks the reaped-row recovery. With no expectation it
// delegates to SavePurchaseExecution.
func (m *MockConfigStore) RecoverReapedExecution(ctx context.Context, exec *config.PurchaseExecution) error {
	m.record("RecoverReapedExecution", ctx, exec)
	if !isExpected(&m.Mock, "RecoverReapedExecution") {
		return m.SavePurchaseExecution(ctx, exec)
	}
	args := m.Called(ctx, exec)
	return args.Error(0)
}

// RecoverReapedExecutionTx mocks the tx variant; same delegation as above.
func (m *MockConfigStore) RecoverReapedExecutionTx(ctx context.Context, tx pgx.Tx, exec *config.PurchaseExecution) error {
	m.record("RecoverReapedExecutionTx", ctx, tx, exec)
	if !isExpected(&m.Mock, "RecoverReapedExecutionTx") {
		return m.RecoverReapedExecution(ctx, exec)
	}
	args := m.Called(ctx, tx, exec)
	return args.Error(0)
}

// StampFirstDispatchTx mocks the first-dispatch stamp (no default: testify panics if unexpected).
func (m *MockConfigStore) StampFirstDispatchTx(ctx context.Context, tx pgx.Tx, executionID string, horizon time.Duration) (config.StampResult, error) {
	m.record("StampFirstDispatchTx", ctx, tx, executionID, horizon)
	args := m.Called(ctx, tx, executionID, horizon)
	res, ok := args.Get(0).(config.StampResult)
	if !ok {
		m.reporter.wrongReturnType("StampFirstDispatchTx", "config.StampResult", args.Get(0))
	}
	return res, args.Error(1)
}

// PredecessorProvenUnsent mocks the predecessor proof lookup (no default: testify panics if unexpected).
func (m *MockConfigStore) PredecessorProvenUnsent(ctx context.Context, executionID string) (bool, error) {
	m.record("PredecessorProvenUnsent", ctx, executionID)
	args := m.Called(ctx, executionID)
	return args.Bool(0), args.Error(1)
}

// LockRedshiftTuplesTx mocks the Redshift tuple advisory locks (no default: testify panics if unexpected).
func (m *MockConfigStore) LockRedshiftTuplesTx(ctx context.Context, tx pgx.Tx, tuples []config.RedshiftTuple) error {
	m.record("LockRedshiftTuplesTx", ctx, tx, tuples)
	return m.Called(ctx, tx, tuples).Error(0)
}

// FindOpenRedshiftLineageConflictsTx mocks the Redshift lineage conflict query (no default: testify panics if unexpected).
func (m *MockConfigStore) FindOpenRedshiftLineageConflictsTx(ctx context.Context, tx pgx.Tx, selfID, family string, tuples []config.RedshiftTuple, accountID *string, horizon time.Duration, stamp time.Time, window, settle time.Duration) ([]config.RedshiftLineageConflict, error) {
	m.record("FindOpenRedshiftLineageConflictsTx", ctx, tx, selfID, family, tuples, accountID, horizon, stamp, window, settle)
	args := m.Called(ctx, tx, selfID, family, tuples, accountID, horizon, stamp, window, settle)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	res, ok := args.Get(0).([]config.RedshiftLineageConflict)
	if !ok {
		m.reporter.wrongReturnType("FindOpenRedshiftLineageConflictsTx", "[]config.RedshiftLineageConflict", args.Get(0))
	}
	return res, args.Error(1)
}

// GetRedshiftRetryClock mocks the Redshift retry clock read (no default: testify panics if unexpected).
func (m *MockConfigStore) GetRedshiftRetryClock(ctx context.Context, executionID string) (config.RedshiftRetryClock, error) {
	m.record("GetRedshiftRetryClock", ctx, executionID)
	args := m.Called(ctx, executionID)
	clock, ok := args.Get(0).(config.RedshiftRetryClock)
	if !ok {
		m.reporter.wrongReturnType("GetRedshiftRetryClock", "config.RedshiftRetryClock", args.Get(0))
	}
	return clock, args.Error(1)
}

// TransitionExecutionStatus mocks the TransitionExecutionStatus operation.
func (m *MockConfigStore) TransitionExecutionStatus(ctx context.Context, executionID string, fromStatuses []string, toStatus string, actor *string) (*config.PurchaseExecution, error) {
	m.record("TransitionExecutionStatus", ctx, executionID, fromStatuses, toStatus, actor)
	args := m.Called(ctx, executionID, fromStatuses, toStatus, actor)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.PurchaseExecution)
	if !ok {
		m.reporter.wrongReturnType("TransitionExecutionStatus", "*config.PurchaseExecution", args.Get(0))
	}
	return v, args.Error(1)
}

// TransitionExecutionStatusPinned mocks the pinned claim. With no expectation
// registered it delegates to TransitionExecutionStatus, so tests that mock the
// plain transition keep working; a test exercising a lost pin registers an
// expectation returning config.ErrExecutionNotInExpectedStatus.
func (m *MockConfigStore) TransitionExecutionStatusPinned(ctx context.Context, executionID string, fromStatuses []string, toStatus string, actor *string, sawEmptyRecommendations bool) (*config.PurchaseExecution, error) {
	if !isExpected(&m.Mock, "TransitionExecutionStatusPinned") {
		return m.TransitionExecutionStatus(ctx, executionID, fromStatuses, toStatus, actor)
	}
	m.record("TransitionExecutionStatusPinned", ctx, executionID, fromStatuses, toStatus, actor, sawEmptyRecommendations)
	args := m.Called(ctx, executionID, fromStatuses, toStatus, actor, sawEmptyRecommendations)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.PurchaseExecution)
	if !ok {
		m.reporter.wrongReturnType("TransitionExecutionStatusPinned", "*config.PurchaseExecution", args.Get(0))
	}
	return v, args.Error(1)
}

// TransitionExecutionStatusPinnedTx mocks the tx variant of the pinned claim.
// With no expectation registered it delegates to TransitionExecutionStatusPinned
// (and from there to TransitionExecutionStatus), so tests that mock the
// non-tx transition keep working.
func (m *MockConfigStore) TransitionExecutionStatusPinnedTx(ctx context.Context, tx pgx.Tx, executionID string, fromStatuses []string, toStatus string, actor *string, sawEmptyRecommendations bool) (*config.PurchaseExecution, error) {
	m.record("TransitionExecutionStatusPinnedTx", ctx, tx, executionID, fromStatuses, toStatus, actor, sawEmptyRecommendations)
	if !isExpected(&m.Mock, "TransitionExecutionStatusPinnedTx") {
		return m.TransitionExecutionStatusPinned(ctx, executionID, fromStatuses, toStatus, actor, sawEmptyRecommendations)
	}
	args := m.Called(ctx, tx, executionID, fromStatuses, toStatus, actor, sawEmptyRecommendations)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.PurchaseExecution)
	if !ok {
		m.reporter.wrongReturnType("TransitionExecutionStatusPinnedTx", "*config.PurchaseExecution", args.Get(0))
	}
	return v, args.Error(1)
}
