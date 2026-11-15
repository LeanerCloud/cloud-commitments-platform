package config

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/jackc/pgx/v5"
)

// PostgresStore is the platform's common.PurchaseClaimer (cloud-commitments-go#745).
// The Azure reservation flow finds it by type assertion on the AttemptJournal it is
// handed; deleting a method breaks the build instead of silently dropping the
// cross-process exclusion.
var _ common.PurchaseClaimer = (*PostgresStore)(nil)

// maxAcquireAttempts bounds the re-read when a row vanishes between the conditional
// upsert and the diagnostic read (a concurrent Release deleting a claimed row).
const maxAcquireAttempts = 3

// acquireClaimSQL is the one atomic statement behind AcquireClaim. Every instant is
// the database's now(); callers pass durations only. The ON CONFLICT ... WHERE is
// the takeover rule: a row is taken over only when it is not completed, its lease
// has expired, and (for a sending row) the settle window since the last renewal
// has elapsed. A takeover bumps generation and keeps the phase, so a sending row
// stays sending. $3 is the ttl and $4 the settle window, both in seconds.
const acquireClaimSQL = `
INSERT INTO purchase_claims AS c (idempotency_token, holder, generation, phase, expires_at, last_renewed_at)
VALUES ($1::text, $2::text, 1, 'claimed', now() + make_interval(secs => $3::float8), now())
ON CONFLICT (idempotency_token) DO UPDATE SET
    holder = EXCLUDED.holder,
    generation = c.generation + 1,
    expires_at = EXCLUDED.expires_at,
    last_renewed_at = EXCLUDED.last_renewed_at,
    updated_at = now()
WHERE c.phase <> 'completed'
  AND c.expires_at <= now()
  AND (c.phase = 'claimed' OR c.last_renewed_at + make_interval(secs => $4::float8) <= now())
RETURNING c.generation, c.phase`

// inspectClaimSQL explains a refused acquire. The remaining times are computed by
// the database for the same reason as above.
const inspectClaimSQL = `
SELECT phase, COALESCE(order_id, ''),
       GREATEST(extract(epoch FROM expires_at - now()), 0)::float8,
       GREATEST(extract(epoch FROM last_renewed_at + make_interval(secs => $2::float8) - now()), 0)::float8
FROM purchase_claims WHERE idempotency_token = $1`

const renewClaimSQL = `
UPDATE purchase_claims
SET expires_at = now() + make_interval(secs => $4::float8), last_renewed_at = now(), updated_at = now()
WHERE idempotency_token = $1 AND holder = $2 AND generation = $3 AND phase <> 'completed'`

const advanceClaimSQL = `
UPDATE purchase_claims SET phase = 'sending', updated_at = now()
WHERE idempotency_token = $1 AND holder = $2 AND generation = $3 AND phase IN ('claimed', 'sending')`

const completeClaimSQL = `
UPDATE purchase_claims SET phase = 'completed', order_id = $4, updated_at = now()
WHERE idempotency_token = $1 AND holder = $2 AND generation = $3 AND phase <> 'completed'`

// dropClaimSQL deletes only a claimed row still owned by the handle (a sending
// row may hold a provider order, so it is never deleted). One statement also
// reports whether the handle owns a sending row, which is a no-op success; both
// false means the claim was lost.
const dropClaimSQL = `
WITH d AS (
    DELETE FROM purchase_claims
    WHERE idempotency_token = $1 AND holder = $2 AND generation = $3 AND phase = 'claimed'
    RETURNING 1
)
SELECT (SELECT count(*) FROM d) > 0,
       EXISTS (SELECT 1 FROM purchase_claims
               WHERE idempotency_token = $1 AND holder = $2 AND generation = $3 AND phase = 'sending')`

// AcquireClaim implements common.PurchaseClaimer.
func (s *PostgresStore) AcquireClaim(ctx context.Context, token, holder string, ttl, settle time.Duration) (common.Claim, error) {
	if token == "" || holder == "" {
		return common.Claim{}, errors.New("acquire purchase claim: token and holder are required")
	}
	if ttl <= 0 {
		return common.Claim{}, fmt.Errorf("acquire purchase claim %s: ttl must be positive", common.MaskToken(token))
	}
	for i := 0; i < maxAcquireAttempts; i++ {
		var gen int64
		var phase string
		err := s.db.QueryRow(ctx, acquireClaimSQL, token, holder, ttl.Seconds(), settle.Seconds()).Scan(&gen, &phase)
		if err == nil {
			return common.Claim{Token: token, Holder: holder, Generation: gen, Phase: common.ClaimPhase(phase)}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return common.Claim{}, fmt.Errorf("acquire purchase claim %s: %w", common.MaskToken(token), err)
		}
		claim, found, err := s.inspectRefusedClaim(ctx, token, settle)
		if err != nil {
			return common.Claim{}, err
		}
		if found {
			return claim, nil
		}
		// The row vanished: retry the insert.
	}
	return common.Claim{}, fmt.Errorf("acquire purchase claim %s: the row kept changing under the acquire", common.MaskToken(token))
}

// inspectRefusedClaim reads why the conditional upsert changed nothing: a
// completed claim (found, nil error), a refusal wrapping ErrClaimHeld or
// ErrClaimSettlePending, or found=false when the row is gone or has become takeable.
func (s *PostgresStore) inspectRefusedClaim(ctx context.Context, token string, settle time.Duration) (claim common.Claim, found bool, err error) {
	var phase, orderID string
	var leaseLeft, settleLeft float64
	err = s.db.QueryRow(ctx, inspectClaimSQL, token, settle.Seconds()).Scan(&phase, &orderID, &leaseLeft, &settleLeft)
	if errors.Is(err, pgx.ErrNoRows) {
		return common.Claim{}, false, nil
	}
	if err != nil {
		return common.Claim{}, false, fmt.Errorf("read purchase claim %s: %w", common.MaskToken(token), err)
	}
	switch {
	case phase == string(common.ClaimCompleted):
		return common.Claim{Token: token, Phase: common.ClaimCompleted, OrderID: orderID}, true, nil
	case leaseLeft > 0:
		return common.Claim{}, true, fmt.Errorf("%w: lease expires in %s", common.ErrClaimHeld, secondsToDuration(leaseLeft).Round(time.Millisecond))
	case phase == string(common.ClaimClaimed) || settleLeft <= 0:
		// The lease (and for a sending row the settle window) ran out between the
		// refused upsert and this read: the row is takeable now, so retry the upsert
		// instead of reporting a refusal that no longer holds.
		return common.Claim{}, false, nil
	default:
		return common.Claim{}, true, fmt.Errorf("%w: %s remain", common.ErrClaimSettlePending, secondsToDuration(settleLeft).Round(time.Millisecond))
	}
}

// RenewClaim implements common.PurchaseClaimer.
func (s *PostgresStore) RenewClaim(ctx context.Context, claim common.Claim, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("renew purchase claim %s: ttl must be positive", common.MaskToken(claim.Token))
	}
	return s.execOwned(ctx, "renew", claim, renewClaimSQL, claim.Token, claim.Holder, claim.Generation, ttl.Seconds())
}

// AdvanceClaim implements common.PurchaseClaimer.
func (s *PostgresStore) AdvanceClaim(ctx context.Context, claim common.Claim, phase common.ClaimPhase) error {
	if phase != common.ClaimSending {
		return fmt.Errorf("purchase claim: cannot advance to phase %q", phase)
	}
	return s.execOwned(ctx, "advance", claim, advanceClaimSQL, claim.Token, claim.Holder, claim.Generation)
}

// ReleaseClaim implements common.PurchaseClaimer.
func (s *PostgresStore) ReleaseClaim(ctx context.Context, claim common.Claim, outcome common.ClaimOutcome) error {
	if outcome.OrderID != "" {
		return s.execOwned(ctx, "complete", claim, completeClaimSQL, claim.Token, claim.Holder, claim.Generation, outcome.OrderID)
	}
	var deleted, sendingKept bool
	if err := s.db.QueryRow(ctx, dropClaimSQL, claim.Token, claim.Holder, claim.Generation).Scan(&deleted, &sendingKept); err != nil {
		return fmt.Errorf("release purchase claim %s: %w", common.MaskToken(claim.Token), err)
	}
	if !deleted && !sendingKept {
		return lostClaim(claim)
	}
	return nil
}

// execOwned runs a statement whose WHERE clause matches only the claim's
// (holder, generation); no matched row means the claim was lost.
func (s *PostgresStore) execOwned(ctx context.Context, op string, claim common.Claim, sql string, args ...any) error {
	tag, err := s.db.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("%s purchase claim %s: %w", op, common.MaskToken(claim.Token), err)
	}
	if tag.RowsAffected() == 0 {
		return lostClaim(claim)
	}
	return nil
}

func lostClaim(claim common.Claim) error {
	return fmt.Errorf("%w: token %s", common.ErrClaimLost, common.MaskToken(claim.Token))
}
