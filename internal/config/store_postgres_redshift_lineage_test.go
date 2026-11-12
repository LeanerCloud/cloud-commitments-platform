package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRedshiftFailedClause(t *testing.T) {
	ready, notReady := redshiftFailedClause(true), redshiftFailedClause(false)

	assert.Contains(t, ready, "outcome_settled_at IS NULL")
	assert.NotContains(t, notReady, "outcome_settled_at", "the column does not exist before the settlement migration")
	for _, want := range []string{"e.status = 'failed'", "e.retry_execution_id IS NULL", "NOT (e.no_submission AND e.retry_attempt_n = 0)", "e.first_dispatch_at > now()"} {
		assert.Contains(t, ready, want)
		assert.Contains(t, notReady, want)
	}
}
