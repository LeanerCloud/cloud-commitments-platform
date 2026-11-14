package config

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math/big"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

var riExchangeDailySpendLockKey = func() int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("cudly:ri_exchange:global_daily_spend"))
	return int64(h.Sum64())
}()

// ErrRIExchangeDailyCapExceeded marks a reservation refused because the
// initial quote does not fit the remaining daily or per-exchange headroom.
var ErrRIExchangeDailyCapExceeded = errors.New("RI exchange daily cap exceeded")

// ErrRIExchangeReservationInvalid marks a reservation that can never succeed
// as stored: a malformed cap, quote or spend value, or a row that is not
// processing. Retrying cannot help, unlike a connection or lock failure.
var ErrRIExchangeReservationInvalid = errors.New("RI exchange reservation is invalid")

// lockRIExchangeSpend takes the transaction-scoped advisory lock every RI
// exchange reservation and direct-execute save serializes on.
func lockRIExchangeSpend(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", riExchangeDailySpendLockKey); err != nil {
		return fmt.Errorf("failed to lock RI exchange daily spend: %w", err)
	}
	return nil
}

// reserveRIExchangeCeiling takes the global daily-spend lock (held until tx ends)
// and returns min(perExchangeCap, dailyCap - spend) as the execution ceiling,
// refusing when the initial quote does not fit. excludeID leaves one row's own
// amount out of spend so an approval does not count itself twice.
func reserveRIExchangeCeiling(ctx context.Context, tx pgx.Tx, excludeID *string, initialText string, initial, dailyCap, perCap *big.Int, dailyCapUSD string) (string, error) {
	if lockErr := lockRIExchangeSpend(ctx, tx); lockErr != nil {
		return "", lockErr
	}
	var spendText string
	if scanErr := tx.QueryRow(ctx, riExchangeDailySpendQuery, time.Now().UTC(), excludeID).Scan(&spendText); scanErr != nil {
		return "", fmt.Errorf("failed to read RI exchange daily spend: %w", scanErr)
	}
	spend, parseErr := riExchangeMicros(spendText)
	if parseErr != nil {
		return "", fmt.Errorf("%w: invalid RI exchange daily spend: %w", ErrRIExchangeReservationInvalid, parseErr)
	}
	remaining := new(big.Int).Sub(dailyCap, spend)
	if remaining.Cmp(perCap) > 0 {
		remaining.Set(perCap)
	}
	if remaining.Sign() < 0 {
		return "", fmt.Errorf("%w: spend %s exceeds cap %s", ErrRIExchangeDailyCapExceeded, spendText, dailyCapUSD)
	}
	if initial.Cmp(remaining) > 0 {
		return "", fmt.Errorf("%w: initial %s, remaining %s", ErrRIExchangeDailyCapExceeded, initialText, riExchangeUSD(remaining))
	}
	return riExchangeUSD(remaining), nil
}

func riExchangeReservationMicros(record *RIExchangeRecord, dailyCapUSD, perExchangeCapUSD string) (initial, dailyCap, perCap *big.Int, err error) {
	if record == nil || record.Status != "processing" || record.Mode != "auto" {
		return nil, nil, nil, errors.New("auto RI exchange reservation requires a processing auto record")
	}
	initial, err = riExchangeMicros(record.PaymentDue)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid initial RI exchange payment: %w", err)
	}
	dailyCap, err = riExchangeMicros(dailyCapUSD)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid daily RI exchange cap: %w", err)
	}
	perCap, err = riExchangeMicros(perExchangeCapUSD)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid per-exchange RI cap: %w", err)
	}
	return initial, dailyCap, perCap, nil
}

var riExchangeMicroScale = big.NewInt(1_000_000)
var riExchangeDecimalPattern = regexp.MustCompile(`^\d+(?:\.\d+)?$`)

func riExchangeMicros(value string) (*big.Int, error) {
	if !riExchangeDecimalPattern.MatchString(value) {
		return nil, fmt.Errorf("invalid nonnegative decimal %q", value)
	}
	amount, ok := new(big.Rat).SetString(value)
	if !ok {
		return nil, fmt.Errorf("invalid nonnegative decimal %q", value)
	}
	amount.Mul(amount, new(big.Rat).SetInt(riExchangeMicroScale))
	if !amount.IsInt() || len(new(big.Int).Quo(amount.Num(), riExchangeMicroScale).String()) > 14 {
		return nil, fmt.Errorf("decimal exceeds DECIMAL(20,6): %q", value)
	}
	return new(big.Int).Set(amount.Num()), nil
}

func riExchangeUSD(micros *big.Int) string {
	whole, fractional := new(big.Int), new(big.Int)
	whole.QuoRem(micros, riExchangeMicroScale, fractional)
	return fmt.Sprintf("%s.%06d", whole, fractional)
}
