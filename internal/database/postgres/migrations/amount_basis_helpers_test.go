//go:build integration

package migrations_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const amountBasisMigrationName = "purchase_history_amount_basis"

// amountBasisMigration returns the migration's number and up file, found by
// name so a renumber at merge time needs no edit here.
func amountBasisMigration(t *testing.T) (version uint, upPath string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(getMigrationsPath(), "*_"+amountBasisMigrationName+".up.sql"))
	require.NoError(t, err)
	require.Len(t, matches, 1)
	base := filepath.Base(matches[0])
	n, err := strconv.ParseUint(base[:strings.IndexByte(base, '_')], 10, 64)
	require.NoError(t, err)
	return uint(n), matches[0]
}

var amountBasisPredicateRe = regexp.MustCompile(`(?s)-- predicate-begin\n(.*?)-- predicate-end`)

// amountBasisPredicate returns the whitespace-normalised text between the
// predicate markers of a file.
func amountBasisPredicate(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // repo-relative test fixture
	require.NoError(t, err)
	m := amountBasisPredicateRe.FindSubmatch(data)
	require.NotNil(t, m, "%s has no predicate markers", path)
	return strings.Join(strings.Fields(string(m[1])), " ")
}

// runbookPath is the runbook holding the boundary query and the un-flag SQL.
func runbookPath() string {
	return filepath.Join(getMigrationsPath(), "..", "..", "..", "..", "runbooks", "azure-legacy-amount-basis.md")
}

// runbookSQL returns the fenced SQL block that starts with the given marker line.
func runbookSQL(t *testing.T, marker string) string {
	t.Helper()
	data, err := os.ReadFile(runbookPath()) //nolint:gosec // repo-relative test fixture
	require.NoError(t, err)
	re := regexp.MustCompile("(?s)```sql\n(-- " + regexp.QuoteMeta(marker) + "\n.*?)```")
	m := re.FindSubmatch(data)
	require.NotNil(t, m, "runbook has no %q block", marker)
	return string(m[1])
}
