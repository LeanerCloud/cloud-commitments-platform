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
	id       string
	provider string
	payment  string
	at       string
	upfront  *float64
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
		{"early-sig", "azure", "monthly", "2026-10-01T00:00:00Z", ab(0), 50, true},
		{"old-all-upfront", "azure", "all-upfront", "2026-09-01T00:00:00Z", ab(1000), 0, true},
		{"old-null-upfront", "azure", "all-upfront", "2026-09-02T00:00:00Z", nil, 0, true},
		{"before-clamp", "azure", "all-upfront", "2026-10-05T00:00:00Z", ab(900), 0, true},
		{"between-old-and-corrected-clamp", "azure", "all-upfront", "2026-10-08T08:00:00Z", ab(900), 0, true},
		{"after-clamp", "azure", "all-upfront", "2026-10-09T00:00:00Z", ab(900), 0, false},
		{"monthly-with-upfront-after", "azure", "monthly", "2026-11-01T00:00:00Z", ab(120), 5, true},
		{"advisor-zero-cost", "azure", "monthly", "2026-11-02T00:00:00Z", ab(0), 0, false},
		{"mixed-case", "Azure", "no-upfront", "2026-09-03T00:00:00Z", ab(10), 0, true},
		{"aws-old", "aws", "all-upfront", "2026-01-01T00:00:00Z", ab(100), 0, false},
		{"gcp-old", "gcp", "monthly", "2026-01-01T00:00:00Z", ab(100), 3, false},
	})
}

// Earliest signature row (2026-10-20) is after the clamp, so it is the boundary.
func TestMigrationAmountBasis_SignatureBoundary(t *testing.T) {
	t.Parallel()
	runAmountBasisScenario(t, []amountBasisFixture{
		{"sig", "azure", "monthly", "2026-10-20T00:00:00Z", ab(0), 50, false},
		{"between-clamp-and-sig", "azure", "all-upfront", "2026-10-15T00:00:00Z", ab(900), 0, true},
		{"after-sig", "azure", "all-upfront", "2026-10-21T00:00:00Z", ab(900), 0, false},
		{"partial-with-upfront", "azure", "partial-upfront", "2026-10-22T00:00:00Z", ab(50), 5, true},
		{"aws-old", "aws", "all-upfront", "2026-01-01T00:00:00Z", ab(100), 0, false},
	})
}

// With no signature row prod may still run pre-fix code: flag every Azure row.
func TestMigrationAmountBasis_NoSignatureFlagsAllAzure(t *testing.T) {
	t.Parallel()
	runAmountBasisScenario(t, []amountBasisFixture{
		{"az-old", "azure", "all-upfront", "2026-09-01T00:00:00Z", ab(1000), 0, true},
		{"az-new", "azure", "all-upfront", "2026-12-01T00:00:00Z", ab(1000), 0, true},
		{"advisor-zero-cost", "azure", "monthly", "2026-12-02T00:00:00Z", ab(0), 0, true},
		{"aws", "aws", "all-upfront", "2026-12-01T00:00:00Z", ab(100), 0, false},
	})
}
