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

// TransitionExecutionStatusTokenPinned mocks the token-pinned transition
// (issue #590). It does not delegate to TransitionExecutionStatus: a caller
// that must pin the approval token is expected to call this method, so a test
// has to register it explicitly.
func (m *MockConfigStore) TransitionExecutionStatusTokenPinned(ctx context.Context, executionID string, fromStatuses []string, toStatus string, actor *string, rawToken string) (*config.PurchaseExecution, error) {
	m.record("TransitionExecutionStatusTokenPinned", ctx, executionID, fromStatuses, toStatus, actor, rawToken)
	args := m.Called(ctx, executionID, fromStatuses, toStatus, actor, rawToken)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.PurchaseExecution)
	if !ok {
		m.reporter.wrongReturnType("TransitionExecutionStatusTokenPinned", "*config.PurchaseExecution", args.Get(0))
	}
	return v, args.Error(1)
}

// ScheduleApprovedExecution mocks the atomic delayed-approval schedule. It has
// no default: a test of the delayed-approve path must register it.
func (m *MockConfigStore) ScheduleApprovedExecution(ctx context.Context, executionID string, fromStatuses []string, transitionedBy *string, scheduledAt time.Time, approvedBy *string) (*config.PurchaseExecution, error) {
	m.record("ScheduleApprovedExecution", ctx, executionID, fromStatuses, transitionedBy, scheduledAt, approvedBy)
	args := m.Called(ctx, executionID, fromStatuses, transitionedBy, scheduledAt, approvedBy)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.PurchaseExecution)
	if !ok {
		m.reporter.wrongReturnType("ScheduleApprovedExecution", "*config.PurchaseExecution", args.Get(0))
	}
	return v, args.Error(1)
}

// ScheduleApprovedExecutionTokenPinned mocks the token-pinned schedule. Like
// the other pinned mocks it does not delegate to the unpinned one.
func (m *MockConfigStore) ScheduleApprovedExecutionTokenPinned(ctx context.Context, executionID string, fromStatuses []string, transitionedBy *string, rawToken string, scheduledAt time.Time, approvedBy *string) (*config.PurchaseExecution, error) {
	m.record("ScheduleApprovedExecutionTokenPinned", ctx, executionID, fromStatuses, transitionedBy, rawToken, scheduledAt, approvedBy)
	args := m.Called(ctx, executionID, fromStatuses, transitionedBy, rawToken, scheduledAt, approvedBy)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.PurchaseExecution)
	if !ok {
		m.reporter.wrongReturnType("ScheduleApprovedExecutionTokenPinned", "*config.PurchaseExecution", args.Get(0))
	}
	return v, args.Error(1)
}

// RescheduleAwaitingExecution mocks the conditional scheduled_date update.
func (m *MockConfigStore) RescheduleAwaitingExecution(ctx context.Context, executionID string, scheduledDate time.Time) (bool, error) {
	args := m.Called(ctx, executionID, scheduledDate)
	return args.Bool(0), args.Error(1)
}

// SetCancelledBy mocks the SetCancelledBy targeted-update operation.
func (m *MockConfigStore) SetCancelledBy(ctx context.Context, executionID, cancelledBy string) error {
	m.record("SetCancelledBy", ctx, executionID, cancelledBy)
	args := m.Called(ctx, executionID, cancelledBy)
	return args.Error(0)
}

// RotatePendingApprovalToken mocks the RotatePendingApprovalToken operation.
// Defaults to (true, nil) when no expectation is registered.
func (m *MockConfigStore) RotatePendingApprovalToken(ctx context.Context, executionID, tokenHash string, expiresAt time.Time) (bool, error) {
	m.record("RotatePendingApprovalToken", ctx, executionID, tokenHash, expiresAt)
	if !isExpected(&m.Mock, "RotatePendingApprovalToken") {
		return true, nil
	}
	args := m.Called(ctx, executionID, tokenHash, expiresAt)
	return args.Bool(0), args.Error(1)
}

func (m *MockConfigStore) CreateSuppression(ctx context.Context, sup *config.PurchaseSuppression) error {
	m.record("CreateSuppression", ctx, sup)
	if !isExpected(&m.Mock, "CreateSuppression") {
		return nil
	}
	args := m.Called(ctx, sup)
	return args.Error(0)
}

func (m *MockConfigStore) CreateSuppressionTx(ctx context.Context, tx pgx.Tx, sup *config.PurchaseSuppression) error {
	m.record("CreateSuppressionTx", ctx, tx, sup)
	if !isExpected(&m.Mock, "CreateSuppressionTx") {
		return nil
	}
	args := m.Called(ctx, tx, sup)
	return args.Error(0)
}

func (m *MockConfigStore) DeleteSuppressionsByExecution(ctx context.Context, executionID string) error {
	m.record("DeleteSuppressionsByExecution", ctx, executionID)
	if !isExpected(&m.Mock, "DeleteSuppressionsByExecution") {
		return nil
	}
	args := m.Called(ctx, executionID)
	return args.Error(0)
}

func (m *MockConfigStore) DeleteSuppressionsByExecutionTx(ctx context.Context, tx pgx.Tx, executionID string) error {
	m.record("DeleteSuppressionsByExecutionTx", ctx, tx, executionID)
	if !isExpected(&m.Mock, "DeleteSuppressionsByExecutionTx") {
		return nil
	}
	args := m.Called(ctx, tx, executionID)
	return args.Error(0)
}

func (m *MockConfigStore) ListActiveSuppressions(ctx context.Context) ([]config.PurchaseSuppression, error) {
	m.record("ListActiveSuppressions", ctx)
	if !isExpected(&m.Mock, "ListActiveSuppressions") {
		return nil, nil
	}
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseSuppression)
	if !ok {
		m.reporter.wrongReturnType("ListActiveSuppressions", "[]config.PurchaseSuppression", args.Get(0))
	}
	return v, args.Error(1)
}

// ListRecentSubmitsTx mocks the ListRecentSubmitsTx operation. It falls back
// to GetPendingExecutions when no expectation is registered so tests that
// exercise the WithTx path keep receiving the pending list; the caller
// filters by creator and window. With neither registered it returns no rows:
// retry tests (which now pass through the submit dedupe, issue #760) need no
// stub unless they exercise a duplicate.
func (m *MockConfigStore) ListRecentSubmitsTx(ctx context.Context, tx pgx.Tx, creatorID string, since time.Time) ([]config.PurchaseExecution, error) {
	m.record("ListRecentSubmitsTx", ctx, tx, creatorID, since)
	if !isExpected(&m.Mock, "ListRecentSubmitsTx") {
		if !isExpected(&m.Mock, "GetPendingExecutions") {
			return nil, nil
		}
		return m.GetPendingExecutions(ctx)
	}
	args := m.Called(ctx, tx, creatorID, since)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseExecution)
	if !ok {
		m.reporter.wrongReturnType("ListRecentSubmitsTx", "[]config.PurchaseExecution", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) SavePurchaseExecutionTx(ctx context.Context, tx pgx.Tx, execution *config.PurchaseExecution) error {
	m.record("SavePurchaseExecutionTx", ctx, tx, execution)
	if !isExpected(&m.Mock, "SavePurchaseExecutionTx") {
		// Recorded under both names; see UpdatePurchasePlanTx.
		return m.SavePurchaseExecution(ctx, execution)
	}
	args := m.Called(ctx, tx, execution)
	return args.Error(0)
}
