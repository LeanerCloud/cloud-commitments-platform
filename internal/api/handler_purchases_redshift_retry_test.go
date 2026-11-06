package api

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const redshiftRetryExecID = "22222222-3333-4444-5555-666666666701"

func redshiftFailedRow(attempt int, noSubmission bool) *config.PurchaseExecution {
	failed := redriveFailedRow(redshiftRetryExecID, "aws", "redshift")
	failed.RetryAttemptN = attempt
	failed.NoSubmission = noSubmission
	return failed
}

func gateWithClock(t *testing.T, failed *config.PurchaseExecution, clock config.RedshiftRetryClock) (*Handler, *MockConfigStore) {
	t.Helper()
	store := new(MockConfigStore)
	store.On("GetRedshiftRetryClock", mock.Anything, failed.ExecutionID).Return(clock, nil)
	return &Handler{config: store}, store
}

func ageOf(d time.Duration) *time.Duration { return &d }

func TestCheckRedshiftRetryGate_LegacyStamplessPermanent(t *testing.T) {
	failed := redshiftFailedRow(1, false)
	h, _ := gateWithClock(t, failed, config.RedshiftRetryClock{SinceUpdate: time.Hour})

	err := h.checkRedshiftRetryGate(context.Background(), failed)

	assertRedriveRefused(t, err, "no first dispatch time recorded")
	ce, _ := IsClientError(err)
	assert.NotContains(t, ce.Details(), "retry_after_seconds")
}

func TestCheckRedshiftRetryGate_PastHorizonPermanent(t *testing.T) {
	failed := redshiftFailedRow(1, false)
	h, _ := gateWithClock(t, failed, config.RedshiftRetryClock{SinceUpdate: time.Hour, SinceStamp: ageOf(24 * time.Hour)})

	assertRedriveRefused(t, h.checkRedshiftRetryGate(context.Background(), failed), "at least 24h ago")
}

func TestCheckRedshiftRetryGate_InsideSettleRetryAfter(t *testing.T) {
	failed := redshiftFailedRow(1, false)
	// 9m59.5s since update leaves 500ms to wait, which must round up to 1s.
	h, _ := gateWithClock(t, failed, config.RedshiftRetryClock{SinceUpdate: 10*time.Minute - 500*time.Millisecond, SinceStamp: ageOf(time.Hour)})

	err := h.checkRedshiftRetryGate(context.Background(), failed)

	ce, ok := IsClientError(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, 409, ce.code)
	assert.Equal(t, 1, ce.Details()["retry_after_seconds"])
	assert.NotContains(t, ce.Details(), "redrive_unsafe", "a settle wait is temporary")
	assert.Equal(t, "1", ce.headers["Retry-After"])
}

func TestCheckRedshiftRetryGate_AfterSettleAllowed(t *testing.T) {
	failed := redshiftFailedRow(1, false)
	h, _ := gateWithClock(t, failed, config.RedshiftRetryClock{SinceUpdate: 10 * time.Minute, SinceStamp: ageOf(time.Hour)})

	assert.NoError(t, h.checkRedshiftRetryGate(context.Background(), failed))
}

func TestCheckRedshiftRetryGate_NoSubmissionAttempt0Skipped(t *testing.T) {
	failed := redshiftFailedRow(0, true)
	h, _ := gateWithClock(t, failed, config.RedshiftRetryClock{SinceUpdate: 0, SinceStamp: nil})

	assert.NoError(t, h.checkRedshiftRetryGate(context.Background(), failed))
}

func TestCheckRedshiftRetryGate_NonRedshiftNeverReadsClock(t *testing.T) {
	for _, svc := range []string{"ec2", "rds"} {
		failed := redriveFailedRow(redshiftRetryExecID, "aws", svc)
		store := new(MockConfigStore)
		h := &Handler{config: store}

		assert.NoError(t, h.checkRedshiftRetryGate(context.Background(), failed), svc)
		store.AssertNotCalled(t, "GetRedshiftRetryClock", mock.Anything, mock.Anything)
	}
}

func TestCheckRedshiftRetryGate_ClockErrorFailsClosed(t *testing.T) {
	failed := redshiftFailedRow(1, false)
	store := new(MockConfigStore)
	store.On("GetRedshiftRetryClock", mock.Anything, failed.ExecutionID).Return(config.RedshiftRetryClock{}, assert.AnError)
	h := &Handler{config: store}

	err := h.checkRedshiftRetryGate(context.Background(), failed)

	require.ErrorIs(t, err, assert.AnError)
	_, isClient := IsClientError(err)
	assert.False(t, isClient, "a store failure is a 500, not a refusal the UI would render as permanent")
}

// Until the old guard is lifted it refuses every Redshift Retry first, so the
// new gate must not even read the clock.
func TestLoadAndValidateRetry_OldRedriveRefusalStillWinsForRedshift(t *testing.T) {
	failed := redshiftFailedRow(1, false)
	session := &Session{UserID: retryCallerID, Email: "operator@example.com"}
	h, store, _ := buildSessionRetryHandler(t, failed, session, false, true)

	_, _, err := h.loadAndValidateRetryRequest(context.Background(), sessionRetryReqWithForce(), failed.ExecutionID)

	assertRedriveRefused(t, err, "Check EC2 Reserved Instances or Redshift Reserved Nodes")
	store.AssertNotCalled(t, "GetRedshiftRetryClock", mock.Anything, mock.Anything)
}

func persistedSuccessor(t *testing.T, failed *config.PurchaseExecution) *config.PurchaseExecution {
	t.Helper()
	session := &Session{UserID: retryCallerID, Email: "operator@example.com"}
	store := new(MockConfigStore)
	t.Cleanup(func() { store.AssertExpectations(t) })
	store.On("GetGlobalConfig", mock.Anything).Return(&config.GlobalConfig{}, nil).Maybe()
	h := &Handler{config: store}
	var saved *config.PurchaseExecution
	store.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).
		Run(func(args mock.Arguments) { snap := *args.Get(1).(*config.PurchaseExecution); saved = &snap }).
		Return(nil)
	store.On("LinkRetryExecutionAtomic", mock.Anything, mock.Anything, failed.ExecutionID, mock.AnythingOfType("string")).Return(true, nil)

	_, _, err := h.persistRetryExecution(context.Background(), failed, session, 0, 0)

	require.NoError(t, err)
	require.NotNil(t, saved)
	return saved
}

func TestPersistRetryExecution_CopiesFirstDispatchAt(t *testing.T) {
	stamp := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	failed := redshiftFailedRow(1, false)
	failed.FirstDispatchAt = &stamp

	successor := persistedSuccessor(t, failed)

	require.NotNil(t, successor.FirstDispatchAt)
	assert.True(t, stamp.Equal(*successor.FirstDispatchAt), "the successor keeps the lineage's original stamp, not a new one")
	assert.NotSame(t, failed.FirstDispatchAt, successor.FirstDispatchAt, "the successor must not alias the predecessor's pointer")
}

func TestPersistRetryExecution_NoSubmissionAttempt0LeavesStampNil(t *testing.T) {
	stamp := time.Now().Add(-time.Hour)
	failed := redshiftFailedRow(0, true)
	failed.FirstDispatchAt = &stamp

	assert.Nil(t, persistedSuccessor(t, failed).FirstDispatchAt)
}

func TestPersistRetryExecution_StamplessPredecessorStaysStampless(t *testing.T) {
	assert.Nil(t, persistedSuccessor(t, redshiftFailedRow(1, false)).FirstDispatchAt)
}
