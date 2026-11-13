package config

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestRetryPlanScheduleTx(t *testing.T) {
	transient := &pgconn.PgError{Code: pgSerializationFailure}

	calls := 0
	require.NoError(t, retryPlanScheduleTx(func() error {
		calls++
		if calls < planScheduleMaxAttempts {
			return transient
		}
		return nil
	}))
	require.Equal(t, planScheduleMaxAttempts, calls)

	for _, code := range []string{pgSerializationFailure, pgDeadlockDetected, pgLockNotAvailable} {
		calls = 0
		err := retryPlanScheduleTx(func() error { calls++; return &pgconn.PgError{Code: code} })
		require.ErrorIs(t, err, ErrPlanScheduleRetryable, code)
		require.Equal(t, planScheduleMaxAttempts, calls, "retries are bounded")
	}

	permanent := errors.New("boom")
	calls = 0
	require.ErrorIs(t, retryPlanScheduleTx(func() error { calls++; return permanent }), permanent)
	require.Equal(t, 1, calls, "a permanent error is not retried")
}

func TestPlanScheduleCommandValidate(t *testing.T) {
	good := PlanScheduleCommand{
		CommandID: "7b0f8f0e-5e6a-4d0e-8a38-0f2d3a1b9c11", PlanID: "p", Actor: "a",
		RequestHash: "h", ExpectedUpdatedAt: time.Now(),
	}
	require.NoError(t, good.validate())
	for name, mutate := range map[string]func(*PlanScheduleCommand){
		"bad id":   func(c *PlanScheduleCommand) { c.CommandID = "x" },
		"no plan":  func(c *PlanScheduleCommand) { c.PlanID = "" },
		"no actor": func(c *PlanScheduleCommand) { c.Actor = "" },
		"no hash":  func(c *PlanScheduleCommand) { c.RequestHash = "" },
		"no stamp": func(c *PlanScheduleCommand) { c.ExpectedUpdatedAt = time.Time{} },
	} {
		c := good
		mutate(&c)
		require.Error(t, c.validate(), name)
	}
}
