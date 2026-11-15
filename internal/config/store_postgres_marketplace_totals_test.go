package config

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #1586: the proceeds totals are scoped to the caller's accounts and
// every total is labeled an estimate.
func TestGetMarketplaceListingProceedsTotalsScopesToAccountAndLabelsEstimate(t *testing.T) {
	t.Parallel()
	pool := newMock(t)
	defer pool.Close()
	pool.ExpectQuery(`purchase_history_id IN \(SELECT id FROM purchase_history WHERE \(cloud_account_id = ANY\(\$2\)`).
		WithArgs([]string{"ri-1"}, []string{"uuid-1"}).
		WillReturnRows(pgxmock.NewRows([]string{"purchase_id", "currency", "p", "f", "s", "u"}).
			AddRow("ri-1", "USD", 88.0, 12.0, 1, 0))
	totals, err := storeWith(pool).GetMarketplaceListingProceedsTotals(context.Background(), []string{"ri-1"}, []string{"uuid-1"}, nil)
	require.NoError(t, err)
	require.Len(t, totals, 1)
	assert.True(t, totals[0].IsEstimate)
	require.NoError(t, pool.ExpectationsWereMet())
}

func TestGetMarketplaceListingProceedsTotalsRejectsEmptyScope(t *testing.T) {
	t.Parallel()
	pool := newMock(t)
	defer pool.Close()
	_, err := storeWith(pool).GetMarketplaceListingProceedsTotals(context.Background(), []string{"ri-1"}, nil, nil)
	require.ErrorIs(t, err, ErrMarketplaceTotalsScopeRequired)
	require.NoError(t, pool.ExpectationsWereMet())
}
