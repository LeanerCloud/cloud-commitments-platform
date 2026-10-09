package config

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAmendLadderTranche_InvalidAmendmentMatchesErrLadderAmendInvalid(t *testing.T) {
	zero := int64(0)
	store := &PostgresStore{} // validation fails before the database is used
	_, err := store.AmendLadderTranche(context.Background(), "id", "account", "aws", "actor",
		LadderAmendment{ExpectedRevision: &zero, ScheduledDate: time.Now(), AmountUSDHr: "0"})
	require.ErrorIs(t, err, ErrLadderAmendInvalid)
	assert.Contains(t, err.Error(), "amount_usd_hr must be a positive decimal")
	assert.NotContains(t, err.Error(), "per-run cap")
}
