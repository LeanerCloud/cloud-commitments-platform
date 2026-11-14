package config

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/jackc/pgx/v5"
)

// ErrRIExchangeSourceInFlight marks a reservation refused because another
// exchange still holds one of its source RIs in processing, so that exchange
// may already have happened at AWS (issue #1050).
var ErrRIExchangeSourceInFlight = errors.New("RI exchange source RI is held by an exchange in progress")

// refuseRIExchangeSourceInFlight refuses when another processing row shares a
// source RI. It must run under the daily-spend advisory lock that
// reserveRIExchangeCeiling takes, so two reservations cannot both pass.
// excludeID is the approving row itself. The error names only the shared RIs,
// which the caller already holds; the blocking row's id goes only to the log,
// because other users' exchanges are not revealed (#1428).
func refuseRIExchangeSourceInFlight(ctx context.Context, tx pgx.Tx, excludeID *string, sources []string) error {
	if len(sources) == 0 {
		return nil
	}
	var blocker string
	var shared []string
	err := tx.QueryRow(ctx, `
		SELECT id::text, ARRAY(SELECT unnest(source_ri_ids) INTERSECT SELECT unnest($1::text[]) ORDER BY 1)
		FROM ri_exchange_history
		WHERE status = 'processing' AND source_ri_ids && $1::text[] AND ($2::uuid IS NULL OR id <> $2::uuid)
		ORDER BY created_at, id
		LIMIT 1`, sources, excludeID).Scan(&blocker, &shared)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		logging.Errorf("RI exchange reservation: source RI check failed: %v", err)
		return errors.New("failed to check RI exchange source RIs in progress")
	}
	logging.Warnf("RI exchange reservation refused: source RI(s) %v are held by processing exchange %s", shared, blocker)
	return fmt.Errorf("%w: source RI(s) %s already have an exchange in progress; resolve that exchange first", ErrRIExchangeSourceInFlight, strings.Join(shared, ", "))
}

// refuseRIExchangeSourcesInFlight runs both source-RI guards: the processing
// history rows and the in-flight direct executes (#1829).
func refuseRIExchangeSourcesInFlight(ctx context.Context, tx pgx.Tx, excludeID *string, sources []string) error {
	if err := refuseRIExchangeSourceInFlight(ctx, tx, excludeID, sources); err != nil {
		return err
	}
	return refuseRIExchangeSourceInFlightDirect(ctx, tx, sources)
}

// normaliseRIExchangeSourceIDs lowercases and trims ids, dropping empty and
// duplicate ones.
func normaliseRIExchangeSourceIDs(sources []string) []string {
	seen := make(map[string]struct{}, len(sources))
	out := make([]string, 0, len(sources))
	for _, src := range sources {
		src = strings.ToLower(strings.TrimSpace(src))
		if _, dup := seen[src]; src == "" || dup {
			continue
		}
		seen[src] = struct{}{}
		out = append(out, src)
	}
	return out
}

// refuseRIExchangeSourceInFlightDirect is the sibling guard for direct
// executes: an AWS ri_exchange_direct_executions row still submitted (the
// accept may be committing) or unknown (it may have committed) holds its
// source RIs until an operator resolves it (resolved_at set, #1836). It
// normalises its own input and compares against the stored ids normalised the
// same way, so spelling and padding cannot slip an RI past it.
// Like refuseRIExchangeSourceInFlight it runs under the daily-spend advisory
// lock, fails closed on a lookup error, and logs the blocking row's id rather
// than returning it (#1428).
func refuseRIExchangeSourceInFlightDirect(ctx context.Context, tx pgx.Tx, sources []string) error {
	normalised := normaliseRIExchangeSourceIDs(sources)
	if len(normalised) == 0 {
		return nil
	}
	resolvedFilter := " AND resolved_at IS NULL"
	if ready, err := directResolveSchemaReady(ctx, tx); err != nil {
		logging.Errorf("RI exchange reservation: schema version check failed: %v", err)
		return errors.New("failed to check RI exchange source RIs held by a direct execute")
	} else if !ready {
		resolvedFilter = ""
	}
	var blocker string
	var shared []string
	err := tx.QueryRow(ctx, `
		SELECT id::text, ARRAY(SELECT n FROM unnest(ids) n WHERE n = ANY($1::text[]) ORDER BY n)
		FROM (
			SELECT id, created_at, ARRAY(SELECT lower(btrim(x, E' \t\n\r\f\x0B')) FROM unnest(source_ids) x) AS ids
			FROM ri_exchange_direct_executions
			WHERE provider = 'aws' AND status IN ('submitted', 'unknown')`+resolvedFilter+`
		) d
		WHERE ids && $1::text[]
		ORDER BY created_at, id
		LIMIT 1`, normalised).Scan(&blocker, &shared)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		logging.Errorf("RI exchange reservation: direct execute source RI check failed: %v", err)
		return errors.New("failed to check RI exchange source RIs held by a direct execute")
	}
	logging.Warnf("RI exchange reservation refused: source RI(s) %v are held by direct execute %s", shared, blocker)
	return fmt.Errorf("%w: source RI(s) %s are held by a direct exchange that may have reached AWS; an operator must verify the outcome at AWS and settle the record", ErrRIExchangeSourceInFlight, strings.Join(shared, ", "))
}
