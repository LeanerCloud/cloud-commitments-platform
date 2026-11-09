package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/pashagolub/pgxmock/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func expectDirectResolveVersion(mock pgxmock.PgxPoolIface, version int) {
	mock.ExpectQuery("SELECT version, dirty FROM schema_migrations").
		WillReturnRows(pgxmock.NewRows([]string{"version", "dirty"}).AddRow(version, false))
}

// Before migration 163 the guard and finalize must not name resolved_at or
// resolved_status (#1836); the queries with it are pinned by the neighboring tests.
func TestPGXMock_DirectResolveBeforeMigration_QueriesOmitResolutionColumns(t *testing.T) {
	notReady := directResolveMigrationVersion - 1

	t.Run("source guard", func(t *testing.T) {
		mock := newMock(t)
		store := storeWith(mock)
		rec := validDirectExecution()
		rec.Provider = "aws"
		mock.ExpectBegin()
		mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(riExchangeDailySpendLockKey).
			WillReturnResult(pgxmock.NewResult("SELECT", 1))
		mock.ExpectQuery("SELECT id::text, ARRAY").WithArgs(anyArgsCfg(2)...).
			WillReturnRows(pgxmock.NewRows([]string{"id", "shared"}))
		expectDirectResolveVersion(mock, notReady)
		mock.ExpectQuery(`status IN \('submitted', 'unknown'\)\s*\) d`).WithArgs([]string{"res-1"}).
			WillReturnRows(pgxmock.NewRows([]string{"id", "shared"}))
		mock.ExpectExec("INSERT INTO ri_exchange_direct_executions").WithArgs(anyArgsCfg(15)...).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mock.ExpectCommit()
		require.NoError(t, store.SaveRIExchangeDirectExecution(context.Background(), rec))
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	outcome := RIExchangeDirectOutcome{Status: RIExchangeDirectStatusCompleted, ProviderReference: "riex-1"}
	t.Run("finalize", func(t *testing.T) {
		mock := newMock(t)
		store := storeWith(mock)
		expectDirectResolveVersion(mock, notReady)
		mock.ExpectExec(`UPDATE ri_exchange_direct_executions(.|\n)*AND status = 'submitted'$`).WithArgs(anyArgsCfg(7)...).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		require.NoError(t, store.FinalizeRIExchangeDirectExecution(context.Background(), directExecTestID, outcome))
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("finalize of a finalized row is refused", func(t *testing.T) {
		mock := newMock(t)
		store := storeWith(mock)
		expectDirectResolveVersion(mock, notReady)
		mock.ExpectExec("UPDATE ri_exchange_direct_executions").WithArgs(anyArgsCfg(7)...).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))
		err := store.FinalizeRIExchangeDirectExecution(context.Background(), directExecTestID, outcome)
		require.ErrorIs(t, err, ErrRIExchangeDirectExecutionNotSubmitted)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("list, get and resolve refuse before touching the table", func(t *testing.T) {
		for name, call := range map[string]func(*PostgresStore) error{
			"account regions": func(s *PostgresStore) error {
				_, err := s.ListUnresolvedRIExchangeDirectAccountRegions(context.Background())
				return err
			},
			"list": func(s *PostgresStore) error {
				_, err := s.ListUnresolvedRIExchangeDirectExecutions(context.Background(), RIExchangeDirectListFilter{Limit: 10})
				return err
			},
			"get": func(s *PostgresStore) error {
				_, err := s.GetRIExchangeDirectExecution(context.Background(), directExecTestID)
				return err
			},
			"resolve": func(s *PostgresStore) error {
				_, err := s.ResolveRIExchangeDirectExecution(context.Background(), RIExchangeDirectResolve{ID: directExecTestID, Outcome: "failed", ExpectedStatus: "submitted", Note: "n", CheckedAt: time.Now(), ResolvedBy: "ops"})
				return err
			},
		} {
			t.Run(name, func(t *testing.T) {
				mock := newMock(t)
				expectDirectResolveVersion(mock, notReady)
				require.ErrorIs(t, call(storeWith(mock)), ErrRIExchangeDirectResolveNotReady)
				assert.NoError(t, mock.ExpectationsWereMet())
			})
		}
	})
}

// The constant must name this change's migration file, so a renumbering that
// forgets it fails here instead of failing closed in production.
func TestDirectResolveMigrationVersionNamesTheMigrationFile(t *testing.T) {
	name := fmt.Sprintf("%06d_ri_exchange_direct_resolution.up.sql", directResolveMigrationVersion)
	_, err := os.Stat(filepath.Join("..", "database", "postgres", "migrations", name))
	require.NoError(t, err, "directResolveMigrationVersion no longer matches the migration file")
}

// A failed version read must not lose the provider outcome.
func TestPGXMock_FinalizeDirectExecution_UnreadableSchemaVersionStillFinalizes(t *testing.T) {
	mock := newMock(t)
	mock.ExpectQuery("SELECT version, dirty FROM schema_migrations").WillReturnError(errors.New("connection reset"))
	mock.ExpectExec("UPDATE ri_exchange_direct_executions").WithArgs(anyArgsCfg(7)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	outcome := RIExchangeDirectOutcome{Status: RIExchangeDirectStatusCompleted, ProviderReference: "riex-1"}
	require.NoError(t, storeWith(mock).FinalizeRIExchangeDirectExecution(context.Background(), directExecTestID, outcome))
	assert.NoError(t, mock.ExpectationsWereMet())
}

// A failed version read in the guard refuses the reservation: reading it as
// "not ready" would drop the resolved_at filter and reading it as "ready" would
// name a column that may not exist. Neither may look like a held source RI.
func TestPGXMock_DirectGuard_UnreadableSchemaVersionRefuses(t *testing.T) {
	var logs bytes.Buffer
	prev := logging.SetOutput(&logs)
	t.Cleanup(func() { logging.SetOutput(prev) })

	mock := newMock(t)
	rec := validDirectExecution()
	rec.Provider = "aws"
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(riExchangeDailySpendLockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT id::text, ARRAY").WithArgs(anyArgsCfg(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "shared"}))
	mock.ExpectQuery("SELECT version, dirty FROM schema_migrations").WillReturnError(errors.New("connection reset"))
	mock.ExpectRollback()

	err := storeWith(mock).SaveRIExchangeDirectExecution(context.Background(), rec)

	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrRIExchangeSourceInFlight)
	assert.Contains(t, logs.String(), "schema version check failed", "the refusal comes from the version read, not from the blocker query")
	assert.NotContains(t, logs.String(), "source RI check failed")
	assert.NoError(t, mock.ExpectationsWereMet())
}
