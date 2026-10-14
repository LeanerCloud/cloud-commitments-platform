package config

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An empty purchase id would be dropped silently after the first one (#774),
// so SavePurchaseHistory must refuse it before touching the database. The
// store has no connection here: reaching the insert would panic.
func TestSavePurchaseHistory_RejectsEmptyPurchaseID(t *testing.T) {
	store := &PostgresStore{}
	for _, id := range []string{"", "   "} {
		err := store.SavePurchaseHistory(context.Background(), &PurchaseHistoryRecord{AccountID: "123456789012", PurchaseID: id, Provider: "aws"})
		require.ErrorIs(t, err, ErrEmptyPurchaseID, "id %q", id)
	}
	assert.NoError(t, (&PurchaseHistoryRecord{PurchaseID: "ri-1"}).Validate())
}
