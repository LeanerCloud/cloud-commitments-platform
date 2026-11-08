package api

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
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

func gateWithClock(t *testing.T, failed *config.PurchaseExecution, clock config.RedshiftRetryClock) *Handler {
	t.Helper()
	store := new(MockConfigStore)
	store.On("GetRedshiftRetryClock", mock.Anything, failed.ExecutionID).Return(clock, nil)
	return &Handler{config: store}
}

func ageOf(d time.Duration) *time.Duration { return &d }

func TestCheckRedshiftRetryGate_LegacyStamplessPermanent(t *testing.T) {
	failed := redshiftFailedRow(1, false)
	h := gateWithClock(t, failed, config.RedshiftRetryClock{SinceUpdate: time.Hour})

	err := h.checkRedshiftRetryGate(context.Background(), failed)

	assertRedriveRefused(t, err, "no first dispatch time recorded")
	ce, _ := IsClientError(err)
	assert.NotContains(t, ce.Details(), "retry_after_seconds")
}

func TestCheckRedshiftRetryGate_PastHorizonPermanent(t *testing.T) {
	failed := redshiftFailedRow(1, false)
	h := gateWithClock(t, failed, config.RedshiftRetryClock{SinceUpdate: time.Hour, SinceStamp: ageOf(24 * time.Hour)})

	assertRedriveRefused(t, h.checkRedshiftRetryGate(context.Background(), failed), "at least 24h ago")
}

func TestCheckRedshiftRetryGate_InsideSettleRetryAfter(t *testing.T) {
	failed := redshiftFailedRow(1, false)
	// 9m59.5s since update leaves 500ms to wait, which must round up to 1s.
	h := gateWithClock(t, failed, config.RedshiftRetryClock{SinceUpdate: 10*time.Minute - 500*time.Millisecond, SinceStamp: ageOf(time.Hour)})

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
	h := gateWithClock(t, failed, config.RedshiftRetryClock{SinceUpdate: 10 * time.Minute, SinceStamp: ageOf(time.Hour)})

	assert.NoError(t, h.checkRedshiftRetryGate(context.Background(), failed))
}

func TestCheckRedshiftRetryGate_NoSubmissionAttempt0Skipped(t *testing.T) {
	failed := redshiftFailedRow(0, true)
	h := gateWithClock(t, failed, config.RedshiftRetryClock{SinceUpdate: 0, SinceStamp: nil})

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

// The provider-level refusal no longer covers Redshift, so a Redshift Retry reaches the new gate.
func TestLoadAndValidateRetry_RedshiftReachesTheNewGate(t *testing.T) {
	failed := redshiftFailedRow(1, false)
	session := &Session{UserID: retryCallerID, Email: "operator@example.com"}
	h, store, _ := buildSessionRetryHandler(t, failed, session, false, true)
	store.On("GetRedshiftRetryClock", mock.Anything, failed.ExecutionID).Return(config.RedshiftRetryClock{SinceUpdate: time.Hour}, nil)

	_, _, err := h.loadAndValidateRetryRequest(context.Background(), sessionRetryReqWithForce(), failed.ExecutionID)

	assertRedriveRefused(t, err, "no first dispatch time recorded")
	store.AssertCalled(t, "GetRedshiftRetryClock", mock.Anything, failed.ExecutionID)
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

// retryRedshiftViaHandler drives the whole retryPurchase handler for a failed Redshift row.
func retryRedshiftViaHandler(t *testing.T, failed *config.PurchaseExecution, clock config.RedshiftRetryClock, req *events.LambdaFunctionURLRequest) (saved []*config.PurchaseExecution, err error) {
	t.Helper()
	session := &Session{UserID: retryCallerID, Email: "operator@example.com"}
	h, store, _ := buildSessionRetryHandler(t, failed, session, false, true)
	store.On("GetRedshiftRetryClock", mock.Anything, failed.ExecutionID).Return(clock, nil)
	store.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).
		Run(func(args mock.Arguments) {
			snap := *args.Get(1).(*config.PurchaseExecution)
			saved = append(saved, &snap)
		}).
		Return(nil).Maybe()
	store.On("LinkRetryExecutionAtomic", mock.Anything, mock.Anything, failed.ExecutionID, mock.AnythingOfType("string")).Return(true, nil).Maybe()

	_, err = h.retryPurchase(context.Background(), req, failed.ExecutionID)
	return saved, err
}

func TestRetryRedshift_InsideSettleRefusedWithRetryAfter(t *testing.T) {
	stamp := time.Now().Add(-time.Hour)
	failed := redshiftFailedRow(1, false)
	failed.FirstDispatchAt = &stamp

	for _, req := range []*events.LambdaFunctionURLRequest{sessionRetryReq(), sessionRetryReqWithForce()} {
		saved, err := retryRedshiftViaHandler(t, failed, config.RedshiftRetryClock{SinceUpdate: 4 * time.Minute, SinceStamp: ageOf(time.Hour)}, req)

		ce, ok := IsClientError(err)
		require.True(t, ok, "got %v", err)
		assert.Equal(t, 409, ce.code)
		assert.Equal(t, 360, ce.Details()["retry_after_seconds"])
		assert.Equal(t, "360", ce.headers["Retry-After"])
		assert.Empty(t, saved, "a refused retry persists no successor")
	}
}

func TestRetryRedshift_AfterSettleCopiesStamp(t *testing.T) {
	stamp := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	failed := redshiftFailedRow(1, false)
	failed.FirstDispatchAt = &stamp

	saved, err := retryRedshiftViaHandler(t, failed, config.RedshiftRetryClock{SinceUpdate: 11 * time.Minute, SinceStamp: ageOf(time.Hour)}, sessionRetryReq())

	require.NoError(t, err)
	require.NotEmpty(t, saved)
	require.NotNil(t, saved[0].FirstDispatchAt)
	assert.True(t, stamp.Equal(*saved[0].FirstDispatchAt), "the successor keeps the lineage stamp")
	assert.Equal(t, 2, saved[0].RetryAttemptN)
}

func TestRetryRedshift_LegacyStamplessRefused(t *testing.T) {
	failed := redshiftFailedRow(1, false)

	saved, err := retryRedshiftViaHandler(t, failed, config.RedshiftRetryClock{SinceUpdate: time.Hour}, sessionRetryReqWithForce())

	assertRedriveRefused(t, err, "no first dispatch time recorded")
	assert.Empty(t, saved)
}
