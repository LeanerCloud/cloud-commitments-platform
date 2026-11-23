package mocks

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// ClaimRIExchangeIdempotencyKey mocks the atomic RI exchange submit claim
// (issue #1642). With no registered expectation it defaults to "this call won
// the claim" so the many pre-existing execute-path tests, whose subject is a
// gate upstream of the claim, keep exercising the commit call. Tests whose
// subject IS the claim register an explicit expectation and assert on it.
func (m *MockConfigStore) ClaimRIExchangeIdempotencyKey(ctx context.Context, key string, window time.Duration) (bool, error) {
	m.record("ClaimRIExchangeIdempotencyKey", ctx, key, window)
	if !isExpected(&m.Mock, "ClaimRIExchangeIdempotencyKey") {
		return true, nil
	}
	args := m.Called(ctx, key, window)
	return args.Bool(0), args.Error(1)
}

func (m *MockConfigStore) SaveRIExchangeRecord(ctx context.Context, record *config.RIExchangeRecord) error {
	m.record("SaveRIExchangeRecord", ctx, record)
	args := m.Called(ctx, record)
	return args.Error(0)
}

func (m *MockConfigStore) ReserveRIExchange(ctx context.Context, record *config.RIExchangeRecord, dailyCapUSD, perExchangeCapUSD string) (string, error) {
	m.record("ReserveRIExchange", ctx, record, dailyCapUSD, perExchangeCapUSD)
	args := m.Called(ctx, record, dailyCapUSD, perExchangeCapUSD)
	return args.String(0), args.Error(1)
}

func (m *MockConfigStore) ReserveApprovedRIExchange(ctx context.Context, id, dailyCapUSD, perExchangeCapUSD string) (string, error) {
	m.record("ReserveApprovedRIExchange", ctx, id, dailyCapUSD, perExchangeCapUSD)
	args := m.Called(ctx, id, dailyCapUSD, perExchangeCapUSD)
	return args.String(0), args.Error(1)
}

func (m *MockConfigStore) GetRIExchangeRecord(ctx context.Context, id string) (*config.RIExchangeRecord, error) {
	m.record("GetRIExchangeRecord", ctx, id)
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.RIExchangeRecord)
	if !ok {
		m.reporter.wrongReturnType("GetRIExchangeRecord", "*config.RIExchangeRecord", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) GetRIExchangeRecordByToken(ctx context.Context, token string) (*config.RIExchangeRecord, error) {
	m.record("GetRIExchangeRecordByToken", ctx, token)
	args := m.Called(ctx, token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.RIExchangeRecord)
	if !ok {
		m.reporter.wrongReturnType("GetRIExchangeRecordByToken", "*config.RIExchangeRecord", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) GetRIExchangeHistory(ctx context.Context, since time.Time, limit int) ([]config.RIExchangeRecord, error) {
	m.record("GetRIExchangeHistory", ctx, since, limit)
	args := m.Called(ctx, since, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.RIExchangeRecord)
	if !ok {
		m.reporter.wrongReturnType("GetRIExchangeHistory", "[]config.RIExchangeRecord", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) GetRIExchangeHistoryForAccounts(ctx context.Context, since time.Time, limit int, accountIDs []string) ([]config.RIExchangeRecord, error) {
	m.record("GetRIExchangeHistoryForAccounts", ctx, since, limit, accountIDs)
	args := m.Called(ctx, since, limit, accountIDs)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.RIExchangeRecord)
	if !ok {
		m.reporter.wrongReturnType("GetRIExchangeHistoryForAccounts", "[]config.RIExchangeRecord", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) TransitionRIExchangeStatus(ctx context.Context, id, fromStatus, toStatus string, actor *string) (*config.RIExchangeRecord, error) {
	m.record("TransitionRIExchangeStatus", ctx, id, fromStatus, toStatus, actor)
	args := m.Called(ctx, id, fromStatus, toStatus, actor)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.RIExchangeRecord)
	if !ok {
		m.reporter.wrongReturnType("TransitionRIExchangeStatus", "*config.RIExchangeRecord", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) CompleteRIExchange(ctx context.Context, id, exchangeID string) error {
	m.record("CompleteRIExchange", ctx, id, exchangeID)
	args := m.Called(ctx, id, exchangeID)
	return args.Error(0)
}

func (m *MockConfigStore) CompleteRIExchangeWithPayment(ctx context.Context, id, exchangeID, acceptedPaymentDue string) error {
	m.record("CompleteRIExchangeWithPayment", ctx, id, exchangeID, acceptedPaymentDue)
	args := m.Called(ctx, id, exchangeID, acceptedPaymentDue)
	return args.Error(0)
}

func (m *MockConfigStore) FailRIExchange(ctx context.Context, id, errorMsg string) error {
	m.record("FailRIExchange", ctx, id, errorMsg)
	args := m.Called(ctx, id, errorMsg)
	return args.Error(0)
}

func (m *MockConfigStore) NoteRIExchangeOutcomeUnknown(ctx context.Context, id, note string) error {
	m.record("NoteRIExchangeOutcomeUnknown", ctx, id, note)
	args := m.Called(ctx, id, note)
	return args.Error(0)
}

func (m *MockConfigStore) GetRIExchangeDailySpend(ctx context.Context, date time.Time) (string, error) {
	m.record("GetRIExchangeDailySpend", ctx, date)
	args := m.Called(ctx, date)
	return args.String(0), args.Error(1)
}

func (m *MockConfigStore) CancelAllPendingExchanges(ctx context.Context) (int64, error) {
	m.record("CancelAllPendingExchanges", ctx)
	args := m.Called(ctx)
	v, ok := args.Get(0).(int64)
	if !ok {
		m.reporter.wrongReturnType("CancelAllPendingExchanges", "int64", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) CancelPendingExchangesByOrigin(ctx context.Context, origin common.ExchangeOrigin) (int64, error) {
	m.record("CancelPendingExchangesByOrigin", ctx, origin)
	args := m.Called(ctx, origin)
	v, ok := args.Get(0).(int64)
	if !ok {
		m.reporter.wrongReturnType("CancelPendingExchangesByOrigin", "int64", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) GetStaleProcessingExchanges(ctx context.Context, olderThan time.Duration) ([]config.RIExchangeRecord, error) {
	m.record("GetStaleProcessingExchanges", ctx, olderThan)
	args := m.Called(ctx, olderThan)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.RIExchangeRecord)
	if !ok {
		m.reporter.wrongReturnType("GetStaleProcessingExchanges", "[]config.RIExchangeRecord", args.Get(0))
	}
	return v, args.Error(1)
}

// SaveRIExchangeDirectExecution mocks the direct-execute audit insert (issue
// #82). With no registered expectation it succeeds, so the many execute-path
// tests whose subject is a gate or the provider call keep reaching it; tests
// whose subject IS the audit trail register an expectation or record the row.
func (m *MockConfigStore) SaveRIExchangeDirectExecution(ctx context.Context, record *config.RIExchangeDirectExecution) error {
	m.record("SaveRIExchangeDirectExecution", ctx, record)
	if !isExpected(&m.Mock, "SaveRIExchangeDirectExecution") {
		return nil
	}
	args := m.Called(ctx, record)
	return args.Error(0)
}

// FinalizeRIExchangeDirectExecution mocks recording the direct-execute
// outcome (issue #82). Defaults to success like SaveRIExchangeDirectExecution.
func (m *MockConfigStore) FinalizeRIExchangeDirectExecution(ctx context.Context, id string, outcome config.RIExchangeDirectOutcome) error {
	m.record("FinalizeRIExchangeDirectExecution", ctx, id, outcome)
	if !isExpected(&m.Mock, "FinalizeRIExchangeDirectExecution") {
		return nil
	}
	args := m.Called(ctx, id, outcome)
	return args.Error(0)
}

// StampRIExchangeApprovedBy mocks the StampRIExchangeApprovedBy operation.
func (m *MockConfigStore) StampRIExchangeApprovedBy(ctx context.Context, id, approverEmail string) error {
	m.record("StampRIExchangeApprovedBy", ctx, id, approverEmail)
	args := m.Called(ctx, id, approverEmail)
	return args.Error(0)
}
