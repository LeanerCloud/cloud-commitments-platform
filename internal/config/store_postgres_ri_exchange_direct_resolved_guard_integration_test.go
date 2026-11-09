//go:build integration

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real-PostgreSQL coverage for #1836: an operator-resolved direct row no
// longer holds its source RIs, on all three guarded paths; an unresolved one
// still does.

// resolveDirectRow stamps the resolution columns directly, as the resolve
// store method will, without changing status or completed_at.
func (e *riExchangeIntegrationEnv) resolveDirectRow(t *testing.T, id, resolvedStatus string) {
	t.Helper()
	tag, err := e.poolA.Exec(e.ctx, `
		UPDATE ri_exchange_direct_executions
		SET resolved_status = $2, resolved_by = 'operator', resolved_at = now(), resolution_note = 'checked at AWS'
		WHERE id = $1`, id, resolvedStatus)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected())
}

// [fail-first] A resolved submitted or unknown row stops refusing an AUTO
// reservation, while an unresolved row of the same status still refuses.
func TestPostgresStoreDB_ReserveRIExchange_ResolvedDirectRowDoesNotBlock(t *testing.T) {
	for _, status := range []string{"submitted", "unknown"} {
		for _, resolved := range []string{"completed", "failed"} {
			t.Run(status+"_"+resolved, func(t *testing.T) {
				env := newRIExchangeIntegrationEnv(t)
				id := env.directRow(t, "aws", status, "ri-g1836-res")
				env.directRow(t, "aws", status, "ri-g1836-open")

				err := env.reserve(t, testAccountID(836000000001), "ri-g1836-res")
				require.ErrorIs(t, err, ErrRIExchangeSourceInFlight, "unresolved row blocks")

				env.resolveDirectRow(t, id, resolved)
				require.NoError(t, env.reserve(t, testAccountID(836000000002), "ri-g1836-res"), "resolved row releases its RI")
				require.ErrorIs(t, env.reserve(t, testAccountID(836000000003), "ri-g1836-open"), ErrRIExchangeSourceInFlight, "another unresolved row still blocks")
			})
		}
	}
}

// [fail-first] The approval path releases a resolved row's RI too, and the
// refusal text for a still-unresolved row never names a resolved row.
func TestPostgresStoreDB_ReserveApprovedRIExchange_ResolvedDirectRowDoesNotBlock(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	cap := env.capAbove(t, time.Now().UTC())
	resolved := env.directRow(t, "aws", "unknown", "ri-g1836-appr-res", "ri-g1836-appr-shared")
	open := env.directRow(t, "aws", "unknown", "ri-g1836-appr-open", "ri-g1836-appr-shared")

	row := env.processingWith(t, testAccountID(836000000011), "ri-g1836-appr-res", "ri-g1836-appr-open")
	defer env.release(t, row.ID)
	_, err := env.storeA.ReserveApprovedRIExchange(env.ctx, row.ID, cap, cap)
	require.ErrorIs(t, err, ErrRIExchangeSourceInFlight, "both rows unresolved")

	env.resolveDirectRow(t, resolved, "completed")
	_, err = env.storeA.ReserveApprovedRIExchange(env.ctx, row.ID, cap, cap)
	require.ErrorIs(t, err, ErrRIExchangeSourceInFlight, "the open row still blocks")
	assert.Contains(t, err.Error(), "ri-g1836-appr-open")
	assert.NotContains(t, err.Error(), "ri-g1836-appr-res", "a resolved row's RI is not named")
	assert.NotContains(t, err.Error(), open)
	assert.NotContains(t, err.Error(), resolved)

	env.resolveDirectRow(t, open, "failed")
	_, err = env.storeA.ReserveApprovedRIExchange(env.ctx, row.ID, cap, cap)
	require.NoError(t, err, "every holder resolved")
}

// [fail-first] The direct Save admits a source RI whose holder was resolved,
// and still refuses one whose holder is unresolved.
func TestRIExchangeDirectSave_ResolvedHolderDoesNotBlock(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	held := awsDirectSave("ri-g1836-save")
	require.NoError(t, env.storeA.SaveRIExchangeDirectExecution(env.ctx, held))
	require.ErrorIs(t, env.storeA.SaveRIExchangeDirectExecution(env.ctx, awsDirectSave("ri-g1836-save")), ErrRIExchangeSourceInFlight, "unresolved holder")

	env.resolveDirectRow(t, held.ID, "failed")
	require.NoError(t, env.storeA.SaveRIExchangeDirectExecution(env.ctx, awsDirectSave("ri-g1836-save")), "resolved holder releases the RI")
	require.ErrorIs(t, env.storeA.SaveRIExchangeDirectExecution(env.ctx, awsDirectSave("ri-g1836-save")), ErrRIExchangeSourceInFlight, "the new submitted row holds it again")
}

// [fail-first] The full matrix on the approval path: a submitted or unknown
// row, resolved as completed or as failed, stops blocking; unresolved blocks.
func TestPostgresStoreDB_ReserveApprovedRIExchange_ResolvedMatrix(t *testing.T) {
	for _, status := range []string{"submitted", "unknown"} {
		for _, resolved := range []string{"completed", "failed"} {
			t.Run(status+"_"+resolved, func(t *testing.T) {
				env := newRIExchangeIntegrationEnv(t)
				cap := env.capAbove(t, time.Now().UTC())
				id := env.directRow(t, "aws", status, "ri-g1836-mx")
				row := env.processingWith(t, testAccountID(836000000021), "ri-g1836-mx")
				defer env.release(t, row.ID)

				_, err := env.storeA.ReserveApprovedRIExchange(env.ctx, row.ID, cap, cap)
				require.ErrorIs(t, err, ErrRIExchangeSourceInFlight, "unresolved row blocks")
				env.resolveDirectRow(t, id, resolved)
				_, err = env.storeA.ReserveApprovedRIExchange(env.ctx, row.ID, cap, cap)
				require.NoError(t, err, "resolved row releases its RI")
			})
		}
	}
}

// [fail-first] The full matrix on the direct Save path.
func TestRIExchangeDirectSave_ResolvedMatrix(t *testing.T) {
	for _, status := range []string{"submitted", "unknown"} {
		for _, resolved := range []string{"completed", "failed"} {
			t.Run(status+"_"+resolved, func(t *testing.T) {
				env := newRIExchangeIntegrationEnv(t)
				id := env.directRow(t, "aws", status, "ri-g1836-sv")

				require.ErrorIs(t, env.storeA.SaveRIExchangeDirectExecution(env.ctx, awsDirectSave("ri-g1836-sv")), ErrRIExchangeSourceInFlight, "unresolved row blocks")
				env.resolveDirectRow(t, id, resolved)
				require.NoError(t, env.storeA.SaveRIExchangeDirectExecution(env.ctx, awsDirectSave("ri-g1836-sv")), "resolved row releases its RI")
			})
		}
	}
}
