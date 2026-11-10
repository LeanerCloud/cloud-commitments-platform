package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The checked_at future bound is exact: dbNow is accepted and one microsecond
// past it is refused. A live database clock cannot probe that edge without
// flaking, so it is pinned here on a fixed clock (#1869).
func TestCheckRIExchangeDirectResolve_FutureBoundIsExact(t *testing.T) {
	dbNow := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	completed := dbNow.Add(-time.Hour)
	row := riExchangeDirectResolveRow{
		status:      RIExchangeDirectStatusUnknown,
		createdAt:   dbNow.Add(-2 * time.Hour),
		completedAt: &completed,
		dbNow:       dbNow,
	}
	req := func(checkedAt time.Time) RIExchangeDirectResolve {
		return RIExchangeDirectResolve{ExpectedStatus: RIExchangeDirectStatusUnknown, CheckedAt: checkedAt}
	}

	require.NoError(t, checkRIExchangeDirectResolve(req(dbNow), row), "exactly the database clock")
	require.NoError(t, checkRIExchangeDirectResolve(req(dbNow.Add(-time.Microsecond)), row))
	require.ErrorIs(t, checkRIExchangeDirectResolve(req(dbNow.Add(time.Microsecond)), row), ErrRIExchangeResolveInvalid)
	require.ErrorIs(t, checkRIExchangeDirectResolve(req(dbNow.Add(2*time.Second)), row), ErrRIExchangeResolveInvalid)
	// a nanosecond tail truncates to the database's microsecond precision
	require.NoError(t, checkRIExchangeDirectResolve(req(dbNow.Add(900*time.Nanosecond)), row))
}
