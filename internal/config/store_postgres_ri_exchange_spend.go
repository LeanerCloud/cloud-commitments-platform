package config

// The RI exchange daily-cap spend query. It reads two ledgers:
//   - ri_exchange_history: the scheduled approve/auto flow (completed rows in
//     the UTC day plus every processing hold);
//   - ri_exchange_direct_executions: the interactive AWS and Azure exchanges,
//     whose spend the cap must also see (#145).
//
// $1 is "now" and $2 optionally leaves one history row out (an approval's own
// reservation; it never names a direct row). Both branches are summed in SQL
// and cast once. The direct branch reads the resolution columns through
// to_jsonb(d) so the same text runs on a pre-000163 schema, where the absent
// columns read as NULL (every row unresolved).

// riExchangeSpendDayStart and riExchangeSpendDayEnd bound the UTC day of $1.
const riExchangeSpendDayStart = `(date_trunc('day', $1::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')`
const riExchangeSpendDayEnd = `(date_trunc('day', $1::timestamptz AT TIME ZONE 'UTC') + INTERVAL '1 day') AT TIME ZONE 'UTC'`

// riExchangeHistorySpend counts completed exchanges accepted inside the UTC day
// plus every processing reservation. A processing row is an unsettled hold, so
// it stays counted across UTC midnight until it completes or fails.
const riExchangeHistorySpend = `(
		SELECT COALESCE(SUM(payment_due), 0)
		FROM ri_exchange_history
		WHERE ($2::uuid IS NULL OR id <> $2::uuid)
		  AND (status = 'processing'
		       OR (status = 'completed'
		           AND COALESCE(completed_at, updated_at) >= ` + riExchangeSpendDayStart + `
		           AND COALESCE(completed_at, updated_at) < ` + riExchangeSpendDayEnd + `))
	)`

// Each direct amount is clamped to [0, 1e12] and rounded UP to 6 decimals, so
// the total always parses as DECIMAL(20,6) in riExchangeMicros, refunds count 0
// and rounding can only over-count.
const (
	riExchangeDirectCompletedAmount = `CEIL(LEAST(GREATEST(COALESCE(d.net_payable, d.max_payment_due), 0), 1000000000000) * 1000000)::numeric / 1000000`
	riExchangeDirectHoldAmount      = `CEIL(LEAST(GREATEST(d.max_payment_due, 0), 1000000000000) * 1000000)::numeric / 1000000`
)

const (
	riExchangeDirectResolvedStatus = `to_jsonb(d)->>'resolved_status'`
	riExchangeDirectResolvedAt     = `to_jsonb(d)->>'resolved_at'`

	// A completed exchange (or a stuck one an operator resolved completed,
	// which keeps completed_at NULL) counts its net payable in the UTC day of
	// its completion, or of its creation when completed_at is NULL. Only USD
	// amounts count, because the cap is in USD.
	riExchangeDirectCompleted = `
		SELECT ` + riExchangeDirectCompletedAmount + ` AS amount
		FROM ri_exchange_direct_executions d
		WHERE (d.status = 'completed' OR ` + riExchangeDirectResolvedStatus + ` = 'completed')
		  AND upper(COALESCE(d.net_payable_currency, d.max_payment_currency)) = 'USD'
		  AND COALESCE(d.completed_at, d.created_at) >= ` + riExchangeSpendDayStart + `
		  AND COALESCE(d.completed_at, d.created_at) < ` + riExchangeSpendDayEnd

	// An unresolved submitted or unknown exchange may have reached the
	// provider, so it holds its max_payment_due. AWS holds last until an
	// operator resolves them; Azure holds only for 24 hours, because the
	// resolve path is AWS-only and one Azure timeout would otherwise hold the
	// global cap forever.
	riExchangeDirectHolds = `
		SELECT ` + riExchangeDirectHoldAmount + ` AS amount
		FROM ri_exchange_direct_executions d
		WHERE d.status IN ('submitted', 'unknown')
		  AND ` + riExchangeDirectResolvedAt + ` IS NULL
		  AND upper(d.max_payment_currency) = 'USD'
		  AND (d.provider <> 'azure' OR d.created_at > $1::timestamptz - INTERVAL '24 hours')`
)

// riExchangeDailySpendQuery returns the spend as one DECIMAL(20,6) text row,
// clamped to 14 integer digits so two clamped branches cannot overflow
// riExchangeMicros.
const riExchangeDailySpendQuery = `
		SELECT LEAST((` + riExchangeHistorySpend + ` + (
			SELECT COALESCE(SUM(amount), 0) FROM (` + riExchangeDirectCompleted + `
				UNION ALL ` + riExchangeDirectHolds + `
			) direct_amounts
		)), 99999999999999)::numeric(20,6)::text
	`
