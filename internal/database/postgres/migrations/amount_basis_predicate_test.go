//go:build integration

package migrations_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The owner confirms the boundary with the runbook query, so its predicate
// must be exactly the one the migration applies (issue #669).
func TestAmountBasisRunbookPredicateMatchesMigration(t *testing.T) {
	_, up := amountBasisMigration(t)
	migration := amountBasisPredicate(t, up)
	assert.NotEmpty(t, migration)
	assert.Equal(t, migration, amountBasisPredicate(t, runbookPath()))
}
