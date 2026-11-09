//go:build integration

package config

import (
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real-PostgreSQL coverage for #145: interactive (direct) AWS and Azure RI
// exchanges count toward the daily cap through GetRIExchangeDailySpend and
// reserveRIExchangeCeiling.

// directSpendRow describes one ri_exchange_direct_executions row as the cap
// query sees it. Zero values mean: aws, USD, 100 cap, completed_at stamped as
// the status requires (migration 000131's CHECK).
type directSpendRow struct {
	provider, status       string
	maxDue, maxCurrency    string
	netPayable, netCurrent *string
	resolved               string // resolved_status; "" leaves the row unresolved
	account                string
	createdAt              time.Time
	completedAt            *time.Time
}

func at(t time.Time) *time.Time { return &t }

func (e *riExchangeIntegrationEnv) insertSpendRow(t *testing.T, r directSpendRow) string {
	t.Helper()
	if r.provider == "" {
		r.provider = "aws"
	}
	if r.maxDue == "" {
		r.maxDue = "100"
	}
	if r.maxCurrency == "" {
		r.maxCurrency = "USD"
	}
	if r.completedAt == nil && r.status != "submitted" {
		r.completedAt = at(r.createdAt)
	}
	var resolved *string
	if r.resolved != "" {
		resolved = &r.resolved
	}
	var account *string
	if r.account != "" {
		account = &r.account
	}
	var id string
	require.NoError(t, e.poolA.QueryRow(e.ctx, `
		INSERT INTO ri_exchange_direct_executions
			(provider, cloud_account_id, source_ids, targets, max_payment_due, max_payment_currency,
			 status, net_payable, net_payable_currency, executed_by, created_at, completed_at,
			 resolved_status, resolved_by, resolved_at, resolution_note)
		VALUES ($1, $2, ARRAY[$3::text], '[{"offering_id":"o","count":1}]'::jsonb, $4::numeric, $5,
			$6, $7::numeric, $8, 'tester', $9, $10,
			$11::text, CASE WHEN $11::text IS NULL THEN NULL ELSE 'operator' END,
			CASE WHEN $11::text IS NULL THEN NULL ELSE $9::timestamptz END,
			CASE WHEN $11::text IS NULL THEN NULL ELSE 'checked' END)
		RETURNING id::text`,
		r.provider, account, "ri-spend-"+uuid.NewString(), r.maxDue, r.maxCurrency,
		r.status, r.netPayable, r.netCurrent, r.createdAt, r.completedAt, resolved).Scan(&id))
	return id
}

func (e *riExchangeIntegrationEnv) clearDirect(t *testing.T) {
	t.Helper()
	_, err := e.poolA.Exec(e.ctx, "DELETE FROM ri_exchange_direct_executions")
	require.NoError(t, err)
	_, err = e.poolA.Exec(e.ctx, "DELETE FROM ri_exchange_history")
	require.NoError(t, err)
}

func TestGetRIExchangeDailySpend_CountsDirectExecutions(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	now := time.Date(2026, 11, 10, 0, 2, 0, 0, time.UTC)
	todayStart := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	today := now.Add(-time.Minute)
	yesterday := now.Add(-12 * time.Hour)
	long := now.Add(-40 * 24 * time.Hour)

	cases := []struct {
		name string
		row  directSpendRow
		want string
	}{
		{"submitted AWS today counts max_payment_due", directSpendRow{status: "submitted", createdAt: today}, "100"},
		{"submitted AWS of any age counts", directSpendRow{status: "submitted", createdAt: long}, "100"},
		{"unknown unresolved AWS counts", directSpendRow{status: "unknown", createdAt: long}, "100"},
		{"azure unknown created 23:55 yesterday counts at 00:02", directSpendRow{provider: "azure", status: "unknown", createdAt: now.Add(-7 * time.Minute)}, "100"},
		{"azure hold 25h old does not count", directSpendRow{provider: "azure", status: "unknown", createdAt: now.Add(-25 * time.Hour)}, "0"},
		{"azure submitted 25h old does not count", directSpendRow{provider: "azure", status: "submitted", createdAt: now.Add(-25 * time.Hour)}, "0"},
		{"completed today counts net_payable", directSpendRow{status: "completed", createdAt: today, netPayable: strPtr("40.25")}, "40.25"},
		{"completed yesterday excluded", directSpendRow{status: "completed", createdAt: yesterday, netPayable: strPtr("40.25")}, "0"},
		{"completed exactly at 00:00:00 UTC counts today", directSpendRow{status: "completed", createdAt: today, completedAt: at(todayStart), netPayable: strPtr("40.25")}, "40.25"},
		{"failed excluded", directSpendRow{status: "failed", createdAt: today}, "0"},
		{"unknown resolved failed excluded", directSpendRow{status: "unknown", createdAt: today, resolved: "failed"}, "0"},
		{"unknown resolved completed counts once", directSpendRow{status: "unknown", createdAt: today, resolved: "completed"}, "100"},
		{"submitted resolved completed counts on created_at day", directSpendRow{status: "submitted", createdAt: today, resolved: "completed"}, "100"},
		{"submitted resolved completed from yesterday excluded", directSpendRow{status: "submitted", createdAt: yesterday, resolved: "completed"}, "0"},
		{"submitted resolved failed excluded", directSpendRow{status: "submitted", createdAt: today, resolved: "failed"}, "0"},
		{"NULL net_payable falls back to max_payment_due", directSpendRow{status: "completed", createdAt: today}, "100"},
		{"negative net_payable counts 0", directSpendRow{status: "completed", createdAt: today, netPayable: strPtr("-12.5")}, "0"},
		{"non-USD azure completed excluded", directSpendRow{provider: "azure", status: "completed", createdAt: today, netPayable: strPtr("40"), netCurrent: strPtr("EUR")}, "0"},
		{"non-USD azure hold excluded", directSpendRow{provider: "azure", status: "unknown", createdAt: today, maxCurrency: "JPY"}, "0"},
		{"lowercase usd azure hold counts", directSpendRow{provider: "azure", status: "unknown", createdAt: today, maxCurrency: "usd"}, "100"},
		{"completed net currency decides over a non-USD cap currency", directSpendRow{provider: "azure", status: "completed", createdAt: today, maxCurrency: "EUR", netPayable: strPtr("7"), netCurrent: strPtr("USD")}, "7"},
		{"row of another account counts toward the global cap", directSpendRow{status: "submitted", createdAt: today, account: "some-other-account"}, "100"},
		{"net_payable above 6 decimals rounds up", directSpendRow{status: "completed", createdAt: today, netPayable: strPtr("0.1234561")}, "0.123457"},
		{"hold max_payment_due above 6 decimals rounds up", directSpendRow{status: "submitted", createdAt: today, maxDue: "0.1234561"}, "0.123457"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env.clearDirect(t)
			env.insertSpendRow(t, c.row)
			want, err := riExchangeMicros(c.want)
			require.NoError(t, err)
			assert.Equal(t, want.String(), env.spend(t, now).String())
		})
	}

	t.Run("completed at 00:00:00 UTC is excluded from the previous day's window", func(t *testing.T) {
		env.clearDirect(t)
		env.insertSpendRow(t, directSpendRow{status: "completed", createdAt: today, completedAt: at(todayStart), netPayable: strPtr("40.25")})
		assert.Zero(t, env.spend(t, todayStart.Add(-time.Second)).Sign())
	})

	t.Run("negative hold amount counts 0", func(t *testing.T) {
		env.clearDirect(t)
		env.insertSpendRow(t, directSpendRow{status: "submitted", createdAt: today})
		env.insertSpendRow(t, directSpendRow{status: "unknown", createdAt: today, maxDue: "-500"})
		assert.Equal(t, usdMicros(100).String(), env.spend(t, now).String())
	})

	t.Run("sums history and direct spend", func(t *testing.T) {
		env.clearDirect(t)
		env.processing(t, "manual", testAccountID(145000000001), 50, now)
		env.insertSpendRow(t, directSpendRow{status: "completed", createdAt: today, netPayable: strPtr("25")})
		env.insertSpendRow(t, directSpendRow{status: "submitted", createdAt: today})
		assert.Equal(t, usdMicros(175).String(), env.spend(t, now).String())
	})

	t.Run("excludeID applies to the history branch only", func(t *testing.T) {
		env.clearDirect(t)
		history := env.processing(t, "manual", testAccountID(145000000002), 70, now)
		directID := env.insertSpendRow(t, directSpendRow{status: "submitted", createdAt: today})
		spendExcluding := func(id string) string {
			var text string
			require.NoError(t, env.poolA.QueryRow(env.ctx, riExchangeDailySpendQuery, now, &id).Scan(&text))
			micros, err := riExchangeMicros(text)
			require.NoError(t, err)
			return micros.String()
		}
		assert.Equal(t, usdMicros(100).String(), spendExcluding(history.ID), "a history row's id leaves the direct row counted")
		assert.Equal(t, usdMicros(170).String(), spendExcluding(directID), "a direct row's id excludes nothing")
	})
}

// Huge and over-precise direct amounts must never make the Scan text fail
// riExchangeMicros, which would turn every reservation into a hard error.
func TestGetRIExchangeDailySpend_DirectAmountsStayParsable(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	now := time.Now().UTC()
	scan := func() string {
		var text string
		require.NoError(t, env.poolA.QueryRow(env.ctx, riExchangeDailySpendQuery, now, nil).Scan(&text))
		_, err := riExchangeMicros(text)
		require.NoError(t, err, "the spend text must parse: %q", text)
		return text
	}

	env.insertSpendRow(t, directSpendRow{status: "submitted", createdAt: now, maxDue: "90000000000000"})
	env.insertSpendRow(t, directSpendRow{status: "submitted", createdAt: now, maxDue: "90000000000000"})
	env.insertSpendRow(t, directSpendRow{status: "completed", createdAt: now, netPayable: strPtr("0.1234561")})
	assert.Equal(t, "2000000000000.123457", scan(), "each amount is clamped to 1e12 and rounded up")

	cap := "1000.000000"
	r := guardCandidate(testAccountID(145000000011), "ri-145-huge")
	_, err := env.storeA.ReserveRIExchange(env.ctx, r, cap, cap)
	require.ErrorIs(t, err, ErrRIExchangeDailyCapExceeded)
	assert.NotContains(t, err.Error(), "invalid RI exchange daily spend")

	// 100 clamped rows reach 1e14; the final LEAST keeps the total at 14 digits.
	_, err = env.poolA.Exec(env.ctx, `
		INSERT INTO ri_exchange_direct_executions
			(provider, source_ids, targets, max_payment_due, max_payment_currency, status, executed_by)
		SELECT 'aws', ARRAY['ri-145-bulk-' || g], '[{"offering_id":"o","count":1}]'::jsonb, 10000000000000, 'USD', 'submitted', 'tester'
		FROM generate_series(1, 100) g`)
	require.NoError(t, err)
	assert.Equal(t, "99999999999999.000000", scan())
	_, err = env.storeA.ReserveRIExchange(env.ctx, guardCandidate(testAccountID(145000000012), "ri-145-huge-2"), cap, cap)
	require.ErrorIs(t, err, ErrRIExchangeDailyCapExceeded)
	assert.NotContains(t, err.Error(), "invalid RI exchange daily spend")
}

// The #145 regression: an interactive exchange that completed must use up the
// daily headroom the scheduled flow reserves against. Both fail on a query
// that reads ri_exchange_history only.
func TestReserveRIExchange_CountsInteractiveSpend(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	env.completeInteractive(t, "ri-145-auto", "60")

	over := guardCandidate(testAccountID(145000000021), "ri-145-auto-a")
	over.PaymentDue = riExchangeUSD(usdMicros(50))
	_, err := env.storeA.ReserveRIExchange(env.ctx, over, "100", "100")
	require.ErrorIs(t, err, ErrRIExchangeDailyCapExceeded)
	assert.Zero(t, env.rowCount(t, over.AccountID), "a refused reservation writes no row")

	fits := guardCandidate(testAccountID(145000000022), "ri-145-auto-b")
	fits.PaymentDue = riExchangeUSD(usdMicros(40))
	ceiling, err := env.storeA.ReserveRIExchange(env.ctx, fits, "100", "100")
	require.NoError(t, err)
	assert.Equal(t, riExchangeUSD(usdMicros(40)), ceiling)
}

func TestReserveApprovedRIExchange_CountsInteractiveSpend(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	env.completeInteractive(t, "ri-145-approve", "60")

	over := env.processing(t, "manual", testAccountID(145000000031), 50, time.Now().UTC())
	_, err := env.storeA.ReserveApprovedRIExchange(env.ctx, over.ID, "100", "100")
	require.ErrorIs(t, err, ErrRIExchangeDailyCapExceeded)
	env.release(t, over.ID)

	fits := env.processing(t, "manual", testAccountID(145000000032), 40, time.Now().UTC())
	ceiling, err := env.storeA.ReserveApprovedRIExchange(env.ctx, fits.ID, "100", "100")
	require.NoError(t, err)
	assert.Equal(t, riExchangeUSD(usdMicros(40)), ceiling)
}

// completeInteractive records an AWS direct exchange through the real store
// path: Save (submitted) then Finalize (completed with a USD net payable).
func (e *riExchangeIntegrationEnv) completeInteractive(t *testing.T, source, netPayable string) {
	t.Helper()
	rec := awsDirectSave(source)
	require.NoError(t, e.storeA.SaveRIExchangeDirectExecution(e.ctx, rec))
	require.NoError(t, e.storeA.FinalizeRIExchangeDirectExecution(e.ctx, rec.ID, RIExchangeDirectOutcome{
		Status: RIExchangeDirectStatusCompleted, ProviderReference: "riex-145", NetPayable: &netPayable, NetPayableCurrency: "USD"}))
}

// The app deploys before migration 163: the same SQL must run without the
// resolution columns and treat every submitted/unknown row as unresolved.
func TestGetRIExchangeDailySpend_BeforeResolutionMigration(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	require.NoError(t, migrations.MigrateToVersion(env.ctx, env.poolA, getTestMigrationsPath(), migrationBelow(t, directResolveMigrationVersion)))
	env.directRow(t, "aws", "submitted", "ri-145-pre-a")
	env.directRow(t, "aws", "unknown", "ri-145-pre-b")
	env.directRow(t, "aws", "completed", "ri-145-pre-c")
	env.directRow(t, "aws", "failed", "ri-145-pre-d")

	assert.Equal(t, usdMicros(300).String(), env.spend(t, time.Now().UTC()).String())
	_, err := env.storeA.ReserveRIExchange(env.ctx, guardCandidate(testAccountID(145000000041), "ri-145-pre-free"), "250", "250")
	require.ErrorIs(t, err, ErrRIExchangeDailyCapExceeded)
}
