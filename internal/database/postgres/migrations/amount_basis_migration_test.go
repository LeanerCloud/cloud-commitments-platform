//go:build integration

package migrations_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type amountBasisFixture struct {
	upfront  *float64
	id       string
	provider string
	payment  string
	at       string
	monthly  float64
	flagged  bool
}

func ab(v float64) *float64 { return &v }

func seedAmountBasisRows(ctx context.Context, t *testing.T, pool *pgxpool.Pool, rows []amountBasisFixture) {
	t.Helper()
	for _, r := range rows {
		ts, err := time.Parse(time.RFC3339, r.at)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `
			INSERT INTO purchase_history (account_id, purchase_id, timestamp, provider, service, region,
				resource_type, term, payment, upfront_cost, monthly_cost, estimated_savings)
			VALUES ('acct', $1, $2, $3, 'compute', 'westeurope', 'D2s_v3', 1, $4, $5, $6, 10)`,
			r.id, ts, r.provider, r.payment, r.upfront, r.monthly)
		require.NoError(t, err)
	}
}

func flaggedIDs(ctx context.Context, t *testing.T, pool *pgxpool.Pool) map[string]bool {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT purchase_id, amount_basis IS NOT NULL FROM purchase_history`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		var f bool
		require.NoError(t, rows.Scan(&id, &f))
		out[id] = f
	}
	require.NoError(t, rows.Err())
	return out
}

// runAmountBasisScenario seeds rows at the version below the migration, checks
// the runbook boundary query predicts the flag count, applies the migration
// and asserts each row's flag, then re-runs the up file (idempotent) and rolls
// back.
func runAmountBasisScenario(t *testing.T, rows []amountBasisFixture) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container := testhelpers.RequireEmptyDatabase(ctx, t)
	pool := container.DB.Pool()
	path := getMigrationsPath()
	version, upPath := amountBasisMigration(t)

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, previousMigrationVersion(t, version)))
	seedAmountBasisRows(ctx, t, pool, rows)

	want := 0
	for _, r := range rows {
		if r.flagged {
			want++
		}
	}
	var predicted int
	require.NoError(t, pool.QueryRow(ctx, runbookSQL(t, "predicate-query")).Scan(&predicted, new(any), new(any), new(any)))
	assert.Equal(t, want, predicted, "the runbook boundary query must predict the flagged count")

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, version))
	got := flaggedIDs(ctx, t, pool)
	for _, r := range rows {
		assert.Equal(t, r.flagged, got[r.id], "row %s", r.id)
	}

	// Idempotent: a second run changes nothing.
	up, err := os.ReadFile(upPath) //nolint:gosec // repo test fixture
	require.NoError(t, err)
	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err)
	assert.Equal(t, got, flaggedIDs(ctx, t, pool))

	// The manual un-flag SQL clears a flag at or after its timestamp.
	_, err = pool.Exec(ctx, runbookSQL(t, "unflag"))
	require.NoError(t, err)

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, path, previousMigrationVersion(t, version)))
	var cols int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'purchase_history' AND column_name = 'amount_basis'`).Scan(&cols))
	assert.Zero(t, cols, "the down migration drops the column")
}

// Earliest signature row (2026-10-01, itself flagged) is before the f2d60df8 clamp, so the
// clamp (2026-10-08 09:15:51Z) is the boundary.
func TestMigrationAmountBasis_ClampBoundary(t *testing.T) {
	t.Parallel()
	runAmountBasisScenario(t, []amountBasisFixture{
		{id: "early-sig", provider: "azure", payment: "monthly", at: "2026-10-01T00:00:00Z", upfront: ab(0), monthly: 50, flagged: true},
		{id: "old-all-upfront", provider: "azure", payment: "all-upfront", at: "2026-09-01T00:00:00Z", upfront: ab(1000), monthly: 0, flagged: true},
		{id: "old-null-upfront", provider: "azure", payment: "all-upfront", at: "2026-09-02T00:00:00Z", upfront: nil, monthly: 0, flagged: true},
		{id: "before-clamp", provider: "azure", payment: "all-upfront", at: "2026-10-05T00:00:00Z", upfront: ab(900), monthly: 0, flagged: true},
		{id: "between-old-and-corrected-clamp", provider: "azure", payment: "all-upfront", at: "2026-10-08T08:00:00Z", upfront: ab(900), monthly: 0, flagged: true},
		{id: "after-clamp", provider: "azure", payment: "all-upfront", at: "2026-10-09T00:00:00Z", upfront: ab(900), monthly: 0, flagged: false},
		{id: "monthly-with-upfront-after", provider: "azure", payment: "monthly", at: "2026-11-01T00:00:00Z", upfront: ab(120), monthly: 5, flagged: true},
		{id: "advisor-zero-cost", provider: "azure", payment: "monthly", at: "2026-11-02T00:00:00Z", upfront: ab(0), monthly: 0, flagged: false},
		{id: "mixed-case", provider: "Azure", payment: "no-upfront", at: "2026-09-03T00:00:00Z", upfront: ab(10), monthly: 0, flagged: true},
		{id: "aws-old", provider: "aws", payment: "all-upfront", at: "2026-01-01T00:00:00Z", upfront: ab(100), monthly: 0, flagged: false},
		{id: "gcp-old", provider: "gcp", payment: "monthly", at: "2026-01-01T00:00:00Z", upfront: ab(100), monthly: 3, flagged: false},
	})
}

// Earliest signature row (2026-10-20) is after the clamp, so it is the boundary.
func TestMigrationAmountBasis_SignatureBoundary(t *testing.T) {
	t.Parallel()
	runAmountBasisScenario(t, []amountBasisFixture{
		{id: "sig", provider: "azure", payment: "monthly", at: "2026-10-20T00:00:00Z", upfront: ab(0), monthly: 50, flagged: false},
		{id: "between-clamp-and-sig", provider: "azure", payment: "all-upfront", at: "2026-10-15T00:00:00Z", upfront: ab(900), monthly: 0, flagged: true},
		{id: "after-sig", provider: "azure", payment: "all-upfront", at: "2026-10-21T00:00:00Z", upfront: ab(900), monthly: 0, flagged: false},
		{id: "partial-with-upfront", provider: "azure", payment: "partial-upfront", at: "2026-10-22T00:00:00Z", upfront: ab(50), monthly: 5, flagged: true},
		{id: "aws-old", provider: "aws", payment: "all-upfront", at: "2026-01-01T00:00:00Z", upfront: ab(100), monthly: 0, flagged: false},
	})
}

// With no signature row prod may still run pre-fix code: flag every Azure row.
func TestMigrationAmountBasis_NoSignatureFlagsAllAzure(t *testing.T) {
	t.Parallel()
	runAmountBasisScenario(t, []amountBasisFixture{
		{id: "az-old", provider: "azure", payment: "all-upfront", at: "2026-09-01T00:00:00Z", upfront: ab(1000), monthly: 0, flagged: true},
		{id: "az-new", provider: "azure", payment: "all-upfront", at: "2026-12-01T00:00:00Z", upfront: ab(1000), monthly: 0, flagged: true},
		{id: "advisor-zero-cost", provider: "azure", payment: "monthly", at: "2026-12-02T00:00:00Z", upfront: ab(0), monthly: 0, flagged: true},
		{id: "aws", provider: "aws", payment: "all-upfront", at: "2026-12-01T00:00:00Z", upfront: ab(100), monthly: 0, flagged: false},
	})
}
