//go:build integration
// +build integration

package commitmentopts_test

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/commitmentopts"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// getMigrationsPath mirrors the helper in internal/config/store_postgres_test.go —
// the migrations directory is a sibling of internal/<package>/.
func getMigrationsPath() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "database", "postgres", "migrations")
}

// setupStore returns a store bound to a fresh, migrated database of its own on
// the package's shared Postgres server. The helper drops the database when the
// test ends.
func setupStore(ctx context.Context, t *testing.T) *commitmentopts.PostgresStore {
	t.Helper()
	return commitmentopts.NewPostgresStore(testhelpers.RequireMigratedDatabase(ctx, t, getMigrationsPath()).DB)
}

func TestPostgresStore_HasData_EmptyIsFalse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := setupStore(ctx, t)

	has, err := store.HasData(ctx, "aws")
	require.NoError(t, err)
	assert.False(t, has)
}

func TestPostgresStore_Get_EmptyReturnsFalseBool(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := setupStore(ctx, t)

	opts, has, err := store.Get(ctx)
	require.NoError(t, err)
	assert.False(t, has)
	assert.Empty(t, opts)
}

func TestPostgresStore_SaveAndGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := setupStore(ctx, t)

	combos := []commitmentopts.Combo{
		{Provider: "aws", Service: "rds", TermYears: 1, Payment: "all-upfront"},
		{Provider: "aws", Service: "rds", TermYears: 1, Payment: "no-upfront"},
		{Provider: "aws", Service: "rds", TermYears: 3, Payment: "all-upfront"},
		{Provider: "aws", Service: "elasticache", TermYears: 1, Payment: "partial-upfront"},
	}
	require.NoError(t, store.Save(ctx, "aws", combos, "123456789012"))

	has, err := store.HasData(ctx, "aws")
	require.NoError(t, err)
	assert.True(t, has)

	opts, has, err := store.Get(ctx)
	require.NoError(t, err)
	assert.True(t, has)
	require.Contains(t, opts, "aws")
	require.Contains(t, opts["aws"], "rds")
	require.Contains(t, opts["aws"], "elasticache")

	rds := opts["aws"]["rds"]
	sort.Slice(rds, func(i, j int) bool {
		if rds[i].TermYears != rds[j].TermYears {
			return rds[i].TermYears < rds[j].TermYears
		}
		return rds[i].Payment < rds[j].Payment
	})
	assert.Equal(t, []commitmentopts.Combo{
		{Provider: "aws", Service: "rds", TermYears: 1, Payment: "all-upfront"},
		{Provider: "aws", Service: "rds", TermYears: 1, Payment: "no-upfront"},
		{Provider: "aws", Service: "rds", TermYears: 3, Payment: "all-upfront"},
	}, rds)

	assert.Equal(t, []commitmentopts.Combo{
		{Provider: "aws", Service: "elasticache", TermYears: 1, Payment: "partial-upfront"},
	}, opts["aws"]["elasticache"])
}

func TestPostgresStore_Save_IdempotentOnRerun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := setupStore(ctx, t)

	combos := []commitmentopts.Combo{
		{Provider: "aws", Service: "rds", TermYears: 1, Payment: "all-upfront"},
	}
	require.NoError(t, store.Save(ctx, "aws", combos, "111111111111"))
	// Second call must not fail (singleton PK conflict on the run row,
	// compound PK conflict on the combos row).
	require.NoError(t, store.Save(ctx, "aws", combos, "222222222222"))

	opts, has, err := store.Get(ctx)
	require.NoError(t, err)
	assert.True(t, has)
	assert.Equal(t, combos, opts["aws"]["rds"])
}

func TestPostgresStore_Save_EmptyCombosStillMarksRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := setupStore(ctx, t)

	// A legitimate "AWS returned no matching offerings" scenario — we
	// persist the run row so the next Get short-circuits via the bool.
	require.NoError(t, store.Save(ctx, "aws", nil, "333333333333"))

	has, err := store.HasData(ctx, "aws")
	require.NoError(t, err)
	assert.True(t, has)

	opts, has, err := store.Get(ctx)
	require.NoError(t, err)
	assert.True(t, has)
	assert.Empty(t, opts)
}

func TestPostgresStore_PerProviderRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testhelpers.RequireMigratedDatabase(ctx, t, getMigrationsPath()).DB
	store := commitmentopts.NewPostgresStore(db)

	aws := []commitmentopts.Combo{{Provider: "aws", Service: "rds", TermYears: 1, Payment: "all-upfront"}}
	azure := []commitmentopts.Combo{{Provider: "azure", Service: "compute", TermYears: 3, Payment: "no-upfront"}}

	require.NoError(t, store.Save(ctx, "aws", aws, "111111111111"))
	has, err := store.HasData(ctx, "azure")
	require.NoError(t, err)
	assert.False(t, has, "an aws row does not warm azure")

	require.NoError(t, store.Save(ctx, "azure", azure, "sub-1"))
	for _, p := range []string{"aws", "azure"} {
		has, err = store.HasData(ctx, p)
		require.NoError(t, err)
		assert.True(t, has, p)
	}
	opts, has, err := store.Get(ctx)
	require.NoError(t, err)
	assert.True(t, has)
	assert.Equal(t, aws, opts["aws"]["rds"], "saving azure leaves the aws combos alone")
	assert.Equal(t, azure, opts["azure"]["compute"])

	// A provider with no run row is hidden even if combos linger for it.
	_, err = db.Pool().Exec(ctx, `INSERT INTO commitment_options_combos VALUES ('gcp','compute',1,'no-upfront')`)
	require.NoError(t, err)
	opts, _, err = store.Get(ctx)
	require.NoError(t, err)
	assert.NotContains(t, opts, "gcp")
}

func TestPostgresStore_ProbedAt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := setupStore(ctx, t)

	_, ok, err := store.ProbedAt(ctx, "azure")
	require.NoError(t, err)
	assert.False(t, ok)

	before := time.Now().Add(-time.Minute)
	require.NoError(t, store.Save(ctx, "azure", nil, "sub-1"))
	at, ok, err := store.ProbedAt(ctx, "azure")
	require.NoError(t, err)
	require.True(t, ok)
	assert.True(t, at.After(before))
	_, ok, err = store.ProbedAt(ctx, "aws")
	require.NoError(t, err)
	assert.False(t, ok, "another provider's row is not reported")
}

func TestPostgresStore_Replace_SwapsRowAndCombosAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testhelpers.RequireMigratedDatabase(ctx, t, getMigrationsPath()).DB
	store := commitmentopts.NewPostgresStore(db)

	oldAzure := []commitmentopts.Combo{{Provider: "azure", Service: "compute", TermYears: 1, Payment: "upfront"}}
	newAzure := []commitmentopts.Combo{{Provider: "azure", Service: "savingsplans", TermYears: 3, Payment: "monthly"}}
	aws := []commitmentopts.Combo{{Provider: "aws", Service: "rds", TermYears: 1, Payment: "all-upfront"}}
	require.NoError(t, store.Save(ctx, "aws", aws, "111111111111"))
	require.NoError(t, store.Save(ctx, "azure", oldAzure, "sub-1"))
	staleAt := time.Now().Add(-8 * 24 * time.Hour).UTC()
	_, err := db.Pool().Exec(ctx, `UPDATE commitment_options_probe_runs SET probed_at = $1 WHERE provider = 'azure'`, staleAt)
	require.NoError(t, err)

	// A failing write rolls back: the old row, its probed_at and its combos stay.
	bad := []commitmentopts.Combo{newAzure[0], {Provider: "azure", Service: "bad\x00", TermYears: 1, Payment: "upfront"}}
	require.Error(t, store.Replace(ctx, "azure", bad, "sub-2"))
	at, ok, err := store.ProbedAt(ctx, "azure")
	require.NoError(t, err)
	require.True(t, ok)
	assert.WithinDuration(t, staleAt, at, time.Second)
	opts, _, err := store.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, oldAzure, opts["azure"]["compute"])
	assert.NotContains(t, opts["azure"], "savingsplans")

	require.NoError(t, store.Replace(ctx, "azure", newAzure, "sub-2"))
	at, _, err = store.ProbedAt(ctx, "azure")
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), at, time.Minute)
	opts, _, err = store.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string][]commitmentopts.Combo{"savingsplans": newAzure}, opts["azure"], "the old combos are gone")
	assert.Equal(t, aws, opts["aws"]["rds"], "other providers are untouched")
	var src string
	require.NoError(t, db.Pool().QueryRow(ctx, `SELECT source_account_id FROM commitment_options_probe_runs WHERE provider = 'azure'`).Scan(&src))
	assert.Equal(t, "sub-2", src)

	require.NoError(t, store.Replace(ctx, "gcp", nil, "p1"))
	_, ok, err = store.ProbedAt(ctx, "gcp")
	require.NoError(t, err)
	assert.True(t, ok, "Replace creates a missing row")
}

func TestPostgresStore_Save_FirstWriterWins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testhelpers.RequireMigratedDatabase(ctx, t, getMigrationsPath()).DB
	store := commitmentopts.NewPostgresStore(db)

	first := []commitmentopts.Combo{{Provider: "aws", Service: "rds", TermYears: 1, Payment: "all-upfront"}}
	second := []commitmentopts.Combo{{Provider: "aws", Service: "ec2", TermYears: 3, Payment: "no-upfront"}}
	require.NoError(t, store.Save(ctx, "aws", first, "sA"))
	staleAt := time.Now().Add(-48 * time.Hour).UTC()
	_, err := db.Pool().Exec(ctx, `UPDATE commitment_options_probe_runs SET probed_at = $1 WHERE provider = 'aws'`, staleAt)
	require.NoError(t, err)

	require.NoError(t, store.Save(ctx, "aws", second, "sB"))
	at, _, err := store.ProbedAt(ctx, "aws")
	require.NoError(t, err)
	assert.WithinDuration(t, staleAt, at, time.Second, "probed_at is the first writer's")
	var src string
	require.NoError(t, db.Pool().QueryRow(ctx, `SELECT source_account_id FROM commitment_options_probe_runs WHERE provider = 'aws'`).Scan(&src))
	assert.Equal(t, "sA", src)
	opts, _, err := store.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, first, opts["aws"]["rds"], "the first combos stay")
}

// Two overlapping Replace calls must leave exactly one snapshot, never a mix.
// A's large snapshot widens the window in which B's DELETE could run between
// A's DELETE and inserts.
func TestPostgresStore_Replace_ConcurrentWritersNeverMix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := setupStore(ctx, t)

	var big []commitmentopts.Combo
	for i := 0; i < 300; i++ {
		big = append(big, commitmentopts.Combo{Provider: "azure", Service: fmt.Sprintf("svc%03d", i), TermYears: 1, Payment: "upfront"})
	}
	small := []commitmentopts.Combo{{Provider: "azure", Service: "other", TermYears: 1, Payment: "upfront"}}
	require.NoError(t, store.Replace(ctx, "azure", small, "seed"))

	for i := 0; i < 15; i++ {
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, w := range []struct {
			src    string
			combos []commitmentopts.Combo
		}{{"sA", big}, {"sB", small}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				assert.NoError(t, store.Replace(ctx, "azure", w.combos, w.src))
			}()
		}
		close(start)
		wg.Wait()

		opts, _, err := store.Get(ctx)
		require.NoError(t, err)
		got := make([]commitmentopts.Combo, 0, len(big))
		for _, c := range opts["azure"] {
			got = append(got, c...)
		}
		assert.True(t, len(got) == len(big) || len(got) == len(small), "round %d: a mix of both snapshots: %d combos", i, len(got))
	}
}
