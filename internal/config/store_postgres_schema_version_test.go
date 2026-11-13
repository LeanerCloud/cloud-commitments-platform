package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeVersionRow struct {
	version int
	dirty   bool
	err     error
}

func (r fakeVersionRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*dest[0].(*int) = r.version
	*dest[1].(*bool) = r.dirty
	return nil
}

type fakeVersionQuerier struct{ row fakeVersionRow }

func (q fakeVersionQuerier) QueryRow(context.Context, string, ...any) pgx.Row { return q.row }

// main deploys before prod migrations are applied, so the settlement paths
// must know whether the columns exist (#999).
func TestSettlementSchemaReady(t *testing.T) {
	t.Parallel()
	v := settlementMigrationVersion
	cases := []struct {
		name string
		row  fakeVersionRow
		want bool
	}{
		{"nothing applied", fakeVersionRow{err: pgx.ErrNoRows}, false},
		{"before the migration", fakeVersionRow{version: v - 1}, false},
		{"at the migration", fakeVersionRow{version: v}, true},
		{"at the migration, dirty", fakeVersionRow{version: v, dirty: true}, false},
		{"after it, even with a later dirty migration", fakeVersionRow{version: v + 1, dirty: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := settlementSchemaReady(context.Background(), fakeVersionQuerier{tc.row})
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// The direct-resolve paths run before migration 163 is applied (#1836).
func TestDirectResolveSchemaReady(t *testing.T) {
	t.Parallel()
	v := directResolveMigrationVersion
	cases := []struct {
		name string
		row  fakeVersionRow
		want bool
	}{
		{"nothing applied", fakeVersionRow{err: pgx.ErrNoRows}, false},
		{"before the migration", fakeVersionRow{version: v - 1}, false},
		{"at the migration", fakeVersionRow{version: v}, true},
		{"at the migration, dirty", fakeVersionRow{version: v, dirty: true}, false},
		{"after it, even with a later dirty migration", fakeVersionRow{version: v + 1, dirty: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := directResolveSchemaReady(context.Background(), fakeVersionQuerier{tc.row})
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// The re-adoption paths run before migration 164 is applied (#1785).
func TestReadoptSchemaReady(t *testing.T) {
	t.Parallel()
	v := readoptMigrationVersion
	cases := []struct {
		name string
		row  fakeVersionRow
		want bool
	}{
		{"nothing applied", fakeVersionRow{err: pgx.ErrNoRows}, false},
		{"before the migration", fakeVersionRow{version: v - 1}, false},
		{"at the migration", fakeVersionRow{version: v}, true},
		{"at the migration, dirty", fakeVersionRow{version: v, dirty: true}, false},
		{"after it, even with a later dirty migration", fakeVersionRow{version: v + 1, dirty: true}, true},
		{"unreadable", fakeVersionRow{err: assert.AnError}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := readoptSchemaReady(context.Background(), fakeVersionQuerier{tc.row})
			if errors.Is(tc.row.err, assert.AnError) {
				require.ErrorIs(t, err, assert.AnError)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// The constant must name this change's migration file, so a renumbering that
// forgets it fails here instead of failing closed in production.
func TestReadoptMigrationVersionNamesTheMigrationFile(t *testing.T) {
	name := fmt.Sprintf("%06d_purchase_executions_approval_readopted_at.up.sql", readoptMigrationVersion)
	_, err := os.Stat(filepath.Join("..", "database", "postgres", "migrations", name))
	require.NoError(t, err, "readoptMigrationVersion no longer matches the migration file")
}

// The ceiling paths run before migration 165 is applied (#70).
func TestSweepCeilingSchemaReady(t *testing.T) {
	t.Parallel()
	v := sweepCeilingMigrationVersion
	cases := []struct {
		name string
		row  fakeVersionRow
		want bool
	}{
		{"nothing applied", fakeVersionRow{err: pgx.ErrNoRows}, false},
		{"before the migration", fakeVersionRow{version: v - 1}, false},
		{"at the migration", fakeVersionRow{version: v}, true},
		{"at the migration, dirty", fakeVersionRow{version: v, dirty: true}, false},
		{"after it, even with a later dirty migration", fakeVersionRow{version: v + 1, dirty: true}, true},
		{"unreadable", fakeVersionRow{err: assert.AnError}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := sweepCeilingSchemaReady(context.Background(), fakeVersionQuerier{tc.row})
			if errors.Is(tc.row.err, assert.AnError) {
				require.ErrorIs(t, err, assert.AnError)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRequireSweepCeilingSchema(t *testing.T) {
	t.Parallel()
	err := requireSweepCeilingSchema(context.Background(), fakeVersionQuerier{fakeVersionRow{version: sweepCeilingMigrationVersion - 1}})
	require.ErrorIs(t, err, ErrLadderCeilingUnavailable)
	require.NoError(t, requireSweepCeilingSchema(context.Background(), fakeVersionQuerier{fakeVersionRow{version: sweepCeilingMigrationVersion}}))
	require.ErrorIs(t, requireSweepCeilingSchema(context.Background(), fakeVersionQuerier{fakeVersionRow{err: assert.AnError}}), assert.AnError)
}

// The constant must name this change's migration file, so a renumbering that
// forgets it fails here instead of failing closed in production.
func TestSweepCeilingMigrationVersionNamesTheMigrationFile(t *testing.T) {
	name := fmt.Sprintf("%06d_ladder_configs_sweep_ceiling.up.sql", sweepCeilingMigrationVersion)
	_, err := os.Stat(filepath.Join("..", "database", "postgres", "migrations", name))
	require.NoError(t, err, "sweepCeilingMigrationVersion no longer matches the migration file")
}

// main deploys before prod migrations are applied, so purchase_history reads
// must work on a schema without amount_basis (#669).
func TestAmountBasisMigrationApplied(t *testing.T) {
	t.Parallel()
	v := amountBasisMigrationVersion
	cases := []struct {
		name string
		row  fakeVersionRow
		want bool
	}{
		{"nothing applied", fakeVersionRow{err: pgx.ErrNoRows}, false},
		{"before the migration", fakeVersionRow{version: v - 1}, false},
		{"at the migration", fakeVersionRow{version: v}, true},
		{"at the migration, dirty", fakeVersionRow{version: v, dirty: true}, false},
		{"after it, with a later dirty migration", fakeVersionRow{version: v + 1, dirty: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := migrationApplied(context.Background(), fakeVersionQuerier{tc.row}, amountBasisMigrationVersion)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// The gate sits on hot read paths: true is latched once per store, false and
// errors are re-checked on the next read.
func TestAmountBasisColumn_Latches(t *testing.T) {
	mock := newMock(t)
	store := &PostgresStore{db: mock}
	ctx := context.Background()
	const versionQuery = `SELECT version, dirty FROM schema_migrations`
	versionRows := func(v int, dirty bool) *pgxmock.Rows {
		return pgxmock.NewRows([]string{"version", "dirty"}).AddRow(v, dirty)
	}

	mock.ExpectQuery(versionQuery).WillReturnError(errors.New("db down"))
	_, err := store.amountBasisColumn(ctx)
	require.Error(t, err)

	mock.ExpectQuery(versionQuery).WillReturnRows(versionRows(amountBasisMigrationVersion-1, false))
	col, err := store.amountBasisColumn(ctx)
	require.NoError(t, err)
	assert.Equal(t, amountBasisNotMigrated, col, "below the version the column reads as NULL")

	mock.ExpectQuery(versionQuery).WillReturnRows(versionRows(amountBasisMigrationVersion, true))
	col, err = store.amountBasisColumn(ctx)
	require.NoError(t, err)
	assert.Equal(t, amountBasisNotMigrated, col, "a dirty migration is not ready")

	mock.ExpectQuery(versionQuery).WillReturnRows(versionRows(amountBasisMigrationVersion, false))
	col, err = store.amountBasisColumn(ctx)
	require.NoError(t, err)
	assert.Equal(t, "amount_basis", col, "at the version the column is read")

	// Latched: no further version query is expected.
	col, err = store.amountBasisColumn(ctx)
	require.NoError(t, err)
	assert.Equal(t, "amount_basis", col)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// Stage 1 only reads the column: no writer names it.
func TestSavePurchaseHistoryQuery_DoesNotWriteAmountBasis(t *testing.T) {
	t.Parallel()
	assert.NotContains(t, savePurchaseHistoryQuery, "amount_basis")
}

// The constant must name this change's migration file, so a renumbering that
// forgets it fails here instead of failing closed in production.
func TestAmountBasisMigrationVersionNamesTheMigrationFile(t *testing.T) {
	name := fmt.Sprintf("%06d_purchase_history_amount_basis.up.sql", amountBasisMigrationVersion)
	_, err := os.Stat(filepath.Join("..", "database", "postgres", "migrations", name))
	require.NoError(t, err, "amountBasisMigrationVersion no longer matches the migration file")
}
