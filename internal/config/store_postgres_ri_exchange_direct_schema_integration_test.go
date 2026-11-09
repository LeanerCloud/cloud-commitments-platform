//go:build integration

package config

import (
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The app deploys before migration 163 is applied (#1836): the guard and
// finalize must keep working without the resolution columns, and the operator
// paths must refuse rather than fail on a missing column.
func TestPostgresStoreDB_RIExchangeDirect_BeforeResolutionMigration(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	require.NoError(t, migrations.MigrateToVersion(env.ctx, env.poolA, getTestMigrationsPath(), migrationBelow(t, directResolveMigrationVersion)))
	var cols int
	require.NoError(t, env.poolA.QueryRow(env.ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'ri_exchange_direct_executions' AND column_name = 'resolved_at'`).Scan(&cols))
	require.Zero(t, cols, "precondition: the resolution columns are gone")

	held := env.directRow(t, "aws", "submitted", "ri-g1836-n1-held")
	require.ErrorIs(t, env.reserve(t, testAccountID(836200000001), "ri-g1836-n1-held"), ErrRIExchangeSourceInFlight, "the guard still holds the RI")
	require.NoError(t, env.reserve(t, testAccountID(836200000002), "ri-g1836-n1-free"))

	outcome := RIExchangeDirectOutcome{Status: RIExchangeDirectStatusCompleted, ProviderReference: "riex-1",
		NetPayable: strPtr("40.25"), NetPayableCurrency: "USD"}
	require.NoError(t, env.storeA.FinalizeRIExchangeDirectExecution(env.ctx, held, outcome))
	require.ErrorIs(t, env.storeA.FinalizeRIExchangeDirectExecution(env.ctx, held, outcome), ErrRIExchangeDirectExecutionNotSubmitted)

	_, err := env.storeA.ListUnresolvedRIExchangeDirectAccountRegions(env.ctx)
	assert.ErrorIs(t, err, ErrRIExchangeDirectResolveNotReady)
	_, err = env.storeA.ListUnresolvedRIExchangeDirectExecutions(env.ctx, RIExchangeDirectListFilter{Limit: 10})
	assert.ErrorIs(t, err, ErrRIExchangeDirectResolveNotReady)
	_, err = env.storeA.GetRIExchangeDirectExecution(env.ctx, held)
	assert.ErrorIs(t, err, ErrRIExchangeDirectResolveNotReady)
	_, err = env.resolve(t, directResolveReq(held, "failed", "submitted", time.Now().UTC()))
	assert.ErrorIs(t, err, ErrRIExchangeDirectResolveNotReady)
}
