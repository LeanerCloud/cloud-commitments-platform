//go:build integration
// +build integration

package config

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #1586: unpriced terminal listings are polled until
// MarketplaceTerminalPollCutoffDays after terminal_at, the time the listing
// first became closed or cancelled, never after its (possibly much older)
// AWS creation date. All instants are relative to now().
func TestMarketplaceTerminalPollCutoff_Postgres(t *testing.T) {
	ctx := context.Background()
	conn := testhelpers.RequireMigratedDatabase(ctx, t, getTestMigrationsPath()).DB
	store := NewPostgresStore(conn)
	pool := conn.Pool()

	t.Run("LongLivedListingClosedUnpricedIsStillPolled", func(t *testing.T) { checkLongLivedClosedUnpriced(ctx, t, store, pool) })
	t.Run("BoundaryAroundCutoff", func(t *testing.T) { checkCutoffBoundary(ctx, t, store, pool) })
	t.Run("NullTerminalAtIsPolled", func(t *testing.T) { checkNullTerminalAtPolled(ctx, t, store, pool) })
	t.Run("ActiveAndPendingNeverCutOff", func(t *testing.T) { checkActivePendingNeverCutOff(ctx, t, store, pool) })
	t.Run("PricedTerminalNotPolled", func(t *testing.T) { checkPricedTerminalNotPolled(ctx, t, store, pool) })
	t.Run("RelistedListingGetsFreshClock", func(t *testing.T) { checkRelistFreshClock(ctx, t, store, pool) })
	t.Run("InsertedTerminalRowsAreStamped", func(t *testing.T) { checkInsertStamped(ctx, t, store, pool) })
	t.Run("SupersededListingCutOffAndUnsettledInTotals", func(t *testing.T) { checkSupersededCutOff(ctx, t, store, pool) })
}

func daysAgo(d int) time.Time { return time.Now().AddDate(0, 0, -d) }

func setPollTerminalAt(ctx context.Context, t *testing.T, pool *pgxpool.Pool, id string, at *time.Time) {
	t.Helper()
	_, err := pool.Exec(ctx, `UPDATE purchase_history SET terminal_at = $2::timestamptz WHERE purchase_id = $1`, id, at)
	require.NoError(t, err)
	// SettleMarketplaceListing also keeps a settlement row with its own copy.
	setSettlementTerminalAt(ctx, t, pool, `purchase_id = $1`, id, at)
}

// setSettlementTerminalAt overrides terminal_at on settlement rows; the stamp
// trigger keeps the first value by design, so it is disabled around the write.
func setSettlementTerminalAt(ctx context.Context, t *testing.T, pool *pgxpool.Pool, where, key string, at *time.Time) {
	t.Helper()
	const tbl = `marketplace_listing_settlements`
	_, err := pool.Exec(ctx, `ALTER TABLE `+tbl+` DISABLE TRIGGER marketplace_listing_settlements_stamp_terminal_at`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE `+tbl+` SET terminal_at = $2::timestamptz WHERE `+where, key, at)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `ALTER TABLE `+tbl+` ENABLE TRIGGER marketplace_listing_settlements_stamp_terminal_at`)
	require.NoError(t, err)
}

func readTerminalAt(ctx context.Context, t *testing.T, pool *pgxpool.Pool, id string) *time.Time {
	t.Helper()
	var at *time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT terminal_at FROM purchase_history WHERE purchase_id = $1`, id).Scan(&at))
	return at
}

// seedUnpricedTerminal seeds an RI whose listing was seen active at listedAt
// and then settled terminal with no price.
func seedUnpricedTerminal(ctx context.Context, t *testing.T, store *PostgresStore, id, listingID string, listedAt time.Time) {
	t.Helper()
	seedListing(ctx, t, store, "aws", id, listingID, ListingStateActive)
	_, err := store.RecordMarketplaceListingActive(ctx, id, listingID, listedAt)
	require.NoError(t, err)
	applied, err := store.SettleMarketplaceListing(ctx, MarketplaceListingSettlement{
		PurchaseID: id, ListingID: listingID, State: ListingStateClosed, ListedAt: &listedAt})
	require.NoError(t, err)
	require.True(t, applied)
}

func checkLongLivedClosedUnpriced(ctx context.Context, t *testing.T, store *PostgresStore, pool *pgxpool.Pool) {
	const id = "tc-long"
	seedUnpricedTerminal(ctx, t, store, id, "ril-tc-long", daysAgo(200))
	assert.Contains(t, pollListings(ctx, t, store, id), "ril-tc-long", "closed just now: polled however old the listing is")

	first := readTerminalAt(ctx, t, pool, id)
	require.NotNil(t, first, "stamped on the first transition")
	assert.WithinDuration(t, time.Now(), *first, time.Minute)
	// A later unpriced poll write must not move the clock.
	at := daysAgo(200)
	applied, err := store.SettleMarketplaceListing(ctx, MarketplaceListingSettlement{
		PurchaseID: id, ListingID: "ril-tc-long", State: ListingStateClosed, ListedAt: &at})
	require.NoError(t, err)
	require.True(t, applied)
	second := readTerminalAt(ctx, t, pool, id)
	require.NotNil(t, second)
	assert.True(t, first.Equal(*second), "terminal_at is stamped once")

	stale := daysAgo(MarketplaceTerminalPollCutoffDays + 1)
	setPollTerminalAt(ctx, t, pool, id, &stale)
	assert.NotContains(t, pollListings(ctx, t, store, id), "ril-tc-long", "181 days after closing: dropped")
}

func checkCutoffBoundary(ctx context.Context, t *testing.T, store *PostgresStore, pool *pgxpool.Pool) {
	n := MarketplaceTerminalPollCutoffDays
	for _, c := range []struct {
		name   string
		age    time.Duration
		polled bool
	}{
		{"n-1d", time.Duration(n-1) * 24 * time.Hour, true},
		{"n-1m", time.Duration(n)*24*time.Hour - time.Minute, true},
		// The clock advances between the write and the poll, so N days old
		// really means N days plus an instant: not polled. Equality (the
		// > versus >= question) cannot be observed.
		{"n", time.Duration(n) * 24 * time.Hour, false},
		{"n+1d", time.Duration(n+1) * 24 * time.Hour, false},
	} {
		id := "tc-b-" + c.name
		seedUnpricedTerminal(ctx, t, store, id, "ril-"+id, daysAgo(10))
		at := time.Now().Add(-c.age)
		setPollTerminalAt(ctx, t, pool, id, &at)
		assert.Equal(t, c.polled, pollListings(ctx, t, store, id)["ril-"+id].ListingID != "", c.name)
	}
}

func checkNullTerminalAtPolled(ctx context.Context, t *testing.T, store *PostgresStore, pool *pgxpool.Pool) {
	const id = "tc-null"
	seedUnpricedTerminal(ctx, t, store, id, "ril-tc-null", daysAgo(500))
	setPollTerminalAt(ctx, t, pool, id, nil)
	assert.Contains(t, pollListings(ctx, t, store, id), "ril-tc-null", "unknown close time is never cut off")
}

func checkActivePendingNeverCutOff(ctx context.Context, t *testing.T, store *PostgresStore, pool *pgxpool.Pool) {
	old := daysAgo(MarketplaceTerminalPollCutoffDays + 200)
	seedListing(ctx, t, store, "aws", "tc-active", "ril-tc-active", ListingStateActive)
	seedListing(ctx, t, store, "aws", "tc-pending", "ril-tc-pending", ListingStatePending)
	for _, id := range []string{"tc-active", "tc-pending"} {
		_, err := pool.Exec(ctx, `UPDATE purchase_history SET listed_at = $2::timestamptz, terminal_at = $2::timestamptz WHERE purchase_id = $1`, id, old)
		require.NoError(t, err)
	}
	assert.Contains(t, pollListings(ctx, t, store, "tc-active"), "ril-tc-active")
	assert.Contains(t, pollListings(ctx, t, store, "tc-pending"), "ril-tc-pending")
}

func checkPricedTerminalNotPolled(ctx context.Context, t *testing.T, store *PostgresStore, pool *pgxpool.Pool) {
	const id = "tc-priced"
	seedListing(ctx, t, store, "aws", id, "ril-tc-priced", ListingStateActive)
	applied, err := store.SettleMarketplaceListing(ctx, closedSettlement(id, "ril-tc-priced", 88, 12))
	require.NoError(t, err)
	require.True(t, applied)
	assert.Empty(t, pollListings(ctx, t, store, id))
}

func checkSupersededCutOff(ctx context.Context, t *testing.T, store *PostgresStore, pool *pgxpool.Pool) {
	const id = "tc-super"
	seedCanceledSeenListing(ctx, t, store, id, "ril-tc-a")
	relist(ctx, t, store, id, "ril-tc-b")
	var carried *time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT terminal_at FROM marketplace_listing_settlements WHERE listing_id = 'ril-tc-a'`).Scan(&carried))
	require.NotNil(t, carried, "the superseded listing keeps its terminal_at")
	assert.Contains(t, pollListings(ctx, t, store, id), "ril-tc-a")

	stale := daysAgo(MarketplaceTerminalPollCutoffDays + 1)
	setSettlementTerminalAt(ctx, t, pool, `listing_id = $1`, "ril-tc-a", &stale)
	due := pollListings(ctx, t, store, id)
	assert.NotContains(t, due, "ril-tc-a")
	assert.Contains(t, due, "ril-tc-b", "the current listing is still polled")
	requireTotal(ctx, t, store, id, 0, 0, 0, 1)
}

// A listing closed long ago must not lend its clock to the RI's next listing:
// B, relisted from A and closed unpriced just now, is polled; A is cut off.
func checkRelistFreshClock(ctx context.Context, t *testing.T, store *PostgresStore, pool *pgxpool.Pool) {
	const id = "tc-relist"
	seedUnpricedTerminal(ctx, t, store, id, "ril-tc-ra", daysAgo(400))
	old := daysAgo(MarketplaceTerminalPollCutoffDays + 20)
	setPollTerminalAt(ctx, t, pool, id, &old)
	relist(ctx, t, store, id, "ril-tc-rb")
	listed := daysAgo(1)
	_, err := store.RecordMarketplaceListingActive(ctx, id, "ril-tc-rb", listed)
	require.NoError(t, err)
	assert.Nil(t, readTerminalAt(ctx, t, pool, id), "an active listing has no terminal_at")
	applied, err := store.SettleMarketplaceListing(ctx, MarketplaceListingSettlement{
		PurchaseID: id, ListingID: "ril-tc-rb", State: ListingStateClosed, ListedAt: &listed})
	require.NoError(t, err)
	require.True(t, applied)

	at := readTerminalAt(ctx, t, pool, id)
	require.NotNil(t, at)
	assert.WithinDuration(t, time.Now(), *at, time.Minute, "B's clock starts at its own close")
	due := pollListings(ctx, t, store, id)
	assert.Contains(t, due, "ril-tc-rb")
	assert.NotContains(t, due, "ril-tc-ra")
}

// Rows inserted already terminal are stamped now(), in both tables.
func checkInsertStamped(ctx context.Context, t *testing.T, store *PostgresStore, pool *pgxpool.Pool) {
	_, err := pool.Exec(ctx, `INSERT INTO purchase_history (account_id, purchase_id, timestamp, provider, service, region,
		resource_type, count, term, payment, listing_id, listing_state)
		VALUES ('acct', 'tc-ins', now(), 'aws', 'ec2', 'us-east-1', 't3.micro', 1, 1, 'all-upfront', 'ril-tc-ins', 'closed')`)
	require.NoError(t, err)
	at := readTerminalAt(ctx, t, pool, "tc-ins")
	require.NotNil(t, at, "purchase_history insert of a terminal row")
	assert.WithinDuration(t, time.Now(), *at, time.Minute)

	_, err = pool.Exec(ctx, `INSERT INTO marketplace_listing_settlements (listing_id, purchase_history_id, purchase_id, status)
		SELECT 'ril-tc-ins-s', id, purchase_id, 'cancelled' FROM purchase_history WHERE purchase_id = 'tc-ins'`)
	require.NoError(t, err)
	var sat *time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT terminal_at FROM marketplace_listing_settlements WHERE listing_id = 'ril-tc-ins-s'`).Scan(&sat))
	require.NotNil(t, sat, "settlement insert of a terminal row")
	assert.WithinDuration(t, time.Now(), *sat, time.Minute)
}
