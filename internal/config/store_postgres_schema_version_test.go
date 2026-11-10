package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
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
