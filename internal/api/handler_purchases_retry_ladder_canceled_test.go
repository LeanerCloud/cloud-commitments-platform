package api

import (
	"context"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// #1792: LockLadderTrancheForRetryTx returns the stored tranche status raw
// (store_postgres_ladder_reconcile.go). Rows written before
// cloud-commitments-go#554 keep "cancelled", new ones write "canceled"; Retry
// must refuse a canceled tranche under either spelling.
func TestGuardLadderTrancheForRetry_RefusesBothCanceledSpellings(t *testing.T) {
	for _, stored := range []string{"canceled", "cancelled"} { //nolint:misspell // legacy stored ladder status value
		t.Run(stored, func(t *testing.T) {
			store := new(MockConfigStore)
			store.On("LockLadderTrancheForRetryTx", mock.Anything, mock.Anything, "tranche-key").
				Return(ladder.TrancheStatus(stored), true, nil)
			h := &Handler{config: store}
			err := h.guardLadderTrancheForRetry(context.Background(), nil, "tranche-key")
			require.True(t, errors.Is(err, errRetryLadderTrancheSettled), "a %q tranche must refuse Retry, got %v", stored, err)
		})
	}
}
