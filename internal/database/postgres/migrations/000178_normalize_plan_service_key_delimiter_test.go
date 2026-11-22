//go:build integration
// +build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const m178Version = 178

// m178Cases are plans as stored before the migration (before) and the exact
// JSON expected after it (after). The entries reuse the m136 helpers, which
// build full entries with every field set, so a dropped field fails JSONEq.
func m178Cases() []m136Case {
	sp := func(plan string) string { return m136Entry("aws", "savings-plans-"+plan, true) }
	ec2 := m136EnabledEC2Entry // a complete "aws/ec2": {...} member
	ec2Value := m136Entry("aws", "ec2", true)
	return []m136Case{
		{"colon keys from migration 000040",
			`{"aws:savings-plans-compute":` + sp("compute") + `,"aws:savings-plans-database":` + sp("database") + `}`,
			`{"aws/savings-plans-compute":` + sp("compute") + `,"aws/savings-plans-database":` + sp("database") + `}`},
		{"colon umbrella key",
			`{"aws:savings-plans":` + m136Entry("aws", "savings-plans", true) + `}`,
			`{"aws/savings-plans":` + m136Entry("aws", "savings-plans", true) + `}`},
		{"mixed spellings in one plan",
			`{"aws:savings-plans-compute":` + sp("compute") + `,` + ec2 + `}`,
			`{"aws/savings-plans-compute":` + sp("compute") + `,` + ec2 + `}`},
		{"other providers and disabled entries",
			`{"azure:compute":` + m136Entry("azure", "compute", false) + `,"gcp:compute":` + m136Entry("gcp", "compute", true) + `}`,
			`{"azure/compute":` + m136Entry("azure", "compute", false) + `,"gcp/compute":` + m136Entry("gcp", "compute", true) + `}`},
		{"identical twin: the colon entry is dropped",
			`{"aws:savings-plans-compute":` + sp("compute") + `,"aws/savings-plans-compute":` + sp("compute") + `}`,
			`{"aws/savings-plans-compute":` + sp("compute") + `}`},
		{name: "already normalized", before: `{"aws/savings-plans-compute":` + sp("compute") + `,` + ec2 + `}`,
			after: `{"aws/savings-plans-compute":` + sp("compute") + `,` + ec2 + `}`},
		{name: "keys that are not provider:service stay",
			before: `{"a:b:c":` + ec2Value + `,"x/y:z":` + ec2Value + `,":x":` + ec2Value + `,"x:":` + ec2Value + `}`,
			after:  `{"a:b:c":` + ec2Value + `,"x/y:z":` + ec2Value + `,":x":` + ec2Value + `,"x:":` + ec2Value + `}`},
		{name: "empty services", before: `{}`, after: `{}`},
		{name: "JSON null services", before: `null`, after: `null`},
	}
}

// TestMigration_NormalizePlanServiceKeyDelimiter locks down migration 000178
// (issue #7): "provider:service" keys become "provider/service", values are
// untouched, other plans are not affected, and the ramp ledger follows the
// values (not the keys), so a sized ramp keeps its baseline and trust.
func TestMigration_NormalizePlanServiceKeyDelimiter(t *testing.T) {
	ctx := context.Background()
	migrationsPath := getMigrationsPath()
	container, err := testhelpers.SetupPostgresContainer(ctx, t)
	require.NoError(t, err)
	defer container.Cleanup(ctx)
	pool := container.DB.Pool()

	prev := previousMigrationVersion(t, m178Version)
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, prev))

	cases := m178Cases()
	ids := make([]string, len(cases))
	before := make([]m136Row, len(cases))
	for i, c := range cases {
		ids[i] = m136Insert(ctx, t, pool, c.before, m136ImmediateRamp)
		before[i] = m136Read(ctx, t, pool, ids[i])
	}
	sizedServices := `{"aws:savings-plans-compute":` + m136Entry("aws", "savings-plans-compute", true) + `,` + m136EnabledEC2Entry + `}`
	sized := m136Insert(ctx, t, pool, sizedServices, m136ProtectedRamp)
	m136Trust(ctx, t, pool, sized)
	sizedBefore := m136Read(ctx, t, pool, sized)
	require.Equal(t, "trusted", sizedBefore.state)
	require.NotNil(t, sizedBefore.baseline)

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, m178Version))

	for i, c := range cases {
		after := m136Read(ctx, t, pool, ids[i])
		assert.JSONEq(t, c.after, after.services, c.name)
		if c.before == c.after {
			assert.True(t, before[i].updatedAt.Equal(after.updatedAt), "%s: an untouched row keeps updated_at", c.name)
		} else {
			assert.True(t, after.updatedAt.After(before[i].updatedAt), "%s: a rewritten row bumps updated_at", c.name)
		}
	}

	t.Run("sized ramp keeps its baseline and trust", func(t *testing.T) {
		r := m136Read(ctx, t, pool, sized)
		assert.JSONEq(t, `{"aws/savings-plans-compute":`+m136Entry("aws", "savings-plans-compute", true)+`,`+m136EnabledEC2Entry+`}`, r.services)
		require.NotNil(t, r.baseline)
		assert.JSONEq(t, m136Baseline, *r.baseline)
		assert.Equal(t, "trusted", r.state)
	})

	// Idempotent: down is a no-op, so down + up changes nothing, not even updated_at.
	all := append(append([]string{}, ids...), sized)
	snapshot := map[string]m136Row{}
	for _, id := range all {
		snapshot[id] = m136Read(ctx, t, pool, id)
	}
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, prev))
	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, m178Version))
	for _, id := range all {
		assert.Equal(t, snapshot[id], m136Read(ctx, t, pool, id), "down + up must leave plan %s unchanged", id)
	}
	var leftover bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'normalize_plan_service_keys_000178')`).Scan(&leftover))
	assert.False(t, leftover, "the helper function must not outlive the migration")
}

// TestMigration_NormalizePlanServiceKeyDelimiter_ConflictFailsLoud checks that
// a plan holding both spellings with different values aborts the migration and
// leaves every plan as it was, rather than picking a winner.
func TestMigration_NormalizePlanServiceKeyDelimiter_ConflictFailsLoud(t *testing.T) {
	ctx := context.Background()
	migrationsPath := getMigrationsPath()
	container, err := testhelpers.SetupPostgresContainer(ctx, t)
	require.NoError(t, err)
	defer container.Cleanup(ctx)
	pool := container.DB.Pool()

	require.NoError(t, migrations.MigrateToVersion(ctx, pool, migrationsPath, previousMigrationVersion(t, m178Version)))

	clean := m136Insert(ctx, t, pool, `{"aws:savings-plans-compute":`+m136Entry("aws", "savings-plans-compute", true)+`}`, m136ImmediateRamp)
	conflictServices := `{"aws:savings-plans-compute":` + m136Entry("aws", "savings-plans-compute", true) +
		`,"aws/savings-plans-compute":` + m136Entry("aws", "savings-plans-compute", false) + `}`
	conflict := m136Insert(ctx, t, pool, conflictServices, m136ImmediateRamp)
	cleanBefore := m136Read(ctx, t, pool, clean)

	err = migrations.MigrateToVersion(ctx, pool, migrationsPath, m178Version)
	require.Error(t, err)
	assert.Contains(t, err.Error(), conflict, "the error names the conflicting plan")
	assert.NotContains(t, err.Error(), clean)

	assert.Equal(t, cleanBefore, m136Read(ctx, t, pool, clean), "the transaction rolls back the clean plan too")
	assert.JSONEq(t, conflictServices, m136Read(ctx, t, pool, conflict).services)
}
