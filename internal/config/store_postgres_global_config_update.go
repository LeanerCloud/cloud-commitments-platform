package config

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

// UpdateGlobalConfigAtomic performs a serialized read-modify-write of the
// global_config singleton. It opens a transaction, takes a transaction-scoped
// advisory lock (which serializes even the first insert, since a row-level
// FOR UPDATE cannot lock a not-yet-existing singleton row), reads the current
// config, applies the caller's in-place mutation, and upserts the result in
// the SAME transaction. This eliminates the lost-update race where two
// concurrent partial PUTs each read the same stale base and the later save
// silently drops the earlier change.
//
// apply mutates the loaded config in place (e.g. json.Unmarshal the request
// body over it and validate); an error it returns aborts the transaction and
// is propagated unchanged (so callers can surface a 400 from validation while
// transport/DB errors surface as 500).
func (s *PostgresStore) UpdateGlobalConfigAtomic(ctx context.Context, apply func(*GlobalConfig) error) (*GlobalConfig, error) {
	var merged *GlobalConfig
	err := s.WithTx(ctx, func(tx pgx.Tx) error {
		if _, lockErr := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", globalConfigLockKey); lockErr != nil {
			return fmt.Errorf("failed to acquire global_config advisory lock: %w", lockErr)
		}
		cfg, readErr := getGlobalConfigFrom(ctx, tx)
		if readErr != nil {
			return readErr
		}
		before := *cfg
		if applyErr := apply(cfg); applyErr != nil {
			return applyErr
		}
		if saveErr := saveGlobalConfigWith(ctx, tx, cfg); saveErr != nil {
			return saveErr
		}
		if propErr := propagateChangedDefaults(ctx, tx, &before, cfg); propErr != nil {
			return propErr
		}
		merged = cfg
		return nil
	})
	if err != nil {
		return nil, err
	}
	return merged, nil
}

// propagateChangedDefaults overwrites a service-config column only when its
// global default changed (the payment default only on AWS rows), so a round-tripped or partial PUT keeps per-service
// customisations.
func propagateChangedDefaults(ctx context.Context, tx pgx.Tx, before, after *GlobalConfig) error {
	termChanged := after.DefaultTerm != before.DefaultTerm
	// The global default payment is an AWS token. Azure and GCP rows keep their
	// own tokens (monthly/upfront): copying "all-upfront" onto them is an
	// upfront charge nobody chose on Azure and fails GCP validation on every
	// later save. Write it only to AWS rows, and only when AWS accepts it.
	paymentChanged := after.DefaultPayment != before.DefaultPayment &&
		slices.Contains(ValidPaymentOptionsByProvider["aws"], after.DefaultPayment)
	coverageChanged := after.DefaultCoverage != before.DefaultCoverage
	rampChanged := after.DefaultRampSchedule != before.DefaultRampSchedule
	if !termChanged && !paymentChanged && !coverageChanged && !rampChanged {
		return nil
	}
	const query = `
		UPDATE service_configs SET
			term = CASE WHEN $1 THEN $2 ELSE term END,
			payment = CASE WHEN $3 AND provider = 'aws' THEN $4 ELSE payment END,
			coverage = CASE WHEN $5 THEN $6 ELSE coverage END,
			ramp_schedule = CASE WHEN $7 THEN $8 ELSE ramp_schedule END
	`
	if _, err := tx.Exec(ctx, query,
		termChanged, after.DefaultTerm,
		paymentChanged, after.DefaultPayment,
		coverageChanged, after.DefaultCoverage,
		rampChanged, after.DefaultRampSchedule,
	); err != nil {
		return fmt.Errorf("failed to propagate global defaults to service configs: %w", err)
	}
	return nil
}
