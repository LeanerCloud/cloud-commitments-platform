//go:build integration

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// [fail-first] #1050: the outcome-unknown note lands on a processing row, which
// stays processing with its reserved payment; a settled row is never rewritten.
func TestPostgresStoreDB_NoteRIExchangeOutcomeUnknown_OnlyProcessingRows(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	old := time.Now().UTC().Add(-time.Hour)
	live := env.processing(t, "manual", testAccountID(820000000101), 10, old)
	defer env.release(t, live.ID)

	require.NoError(t, env.storeA.NoteRIExchangeOutcomeUnknown(env.ctx, live.ID, "outcome unknown: note"))
	row, err := env.storeA.GetRIExchangeRecord(env.ctx, live.ID)
	require.NoError(t, err)
	assert.Equal(t, "processing", row.Status, "the row stays processing for the operator to resolve")
	assert.Equal(t, "outcome unknown: note", row.Error)
	assert.Equal(t, live.PaymentDue, row.PaymentDue, "the reserved ceiling is kept")
	assert.True(t, row.UpdatedAt.After(old), "the note bumps updated_at")

	settled := env.processing(t, "manual", testAccountID(820000000102), 10, old)
	require.NoError(t, env.storeA.FailRIExchange(env.ctx, settled.ID, "plain failure"))
	err = env.storeA.NoteRIExchangeOutcomeUnknown(env.ctx, settled.ID, "outcome unknown: late")
	require.Error(t, err, "a row that is no longer processing is refused")
	row, err = env.storeA.GetRIExchangeRecord(env.ctx, settled.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", row.Status)
	assert.Equal(t, "plain failure", row.Error, "a settled row's reason is never rewritten")
}
