package config

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// insertRecommendationsBatched performs a batched multi-row upsert of recs
// inside the given transaction: each batch carries an ON CONFLICT DO UPDATE
// clause keyed by the natural key.
func insertRecommendationsBatched(ctx context.Context, tx pgx.Tx, collectedAt time.Time, recs []RecommendationRecord) error {
	for start := 0; start < len(recs); start += recommendationsBatchSize {
		end := start + recommendationsBatchSize
		if end > len(recs) {
			end = len(recs)
		}
		if err := insertRecommendationsBatch(ctx, tx, collectedAt, recs[start:end]); err != nil {
			return err
		}
	}
	return nil
}

// insertRecommendationsBatch inserts a single batch of up to
// recommendationsBatchSize rows. Splits the VALUES placeholder list into
// (collected_at, cloud_account_id, provider, service, region,
// resource_type, engine, payload, upfront_cost, monthly_savings, term,
// payment_option) — 12 columns per row (id defaulted via
// gen_random_uuid() so we send 12 args, no $n for id).
//
// term + payment_option were added as part of the natural-key
// broadening (migration 000032) so per-rec ON CONFLICT can store
// every Azure term × payment variant per SKU instead of collapsing
// onto the highest-savings one.
// engine was added to distinguish MySQL vs Postgres RDS at the same SKU.
func insertRecommendationsBatch(ctx context.Context, tx pgx.Tx, collectedAt time.Time, recs []RecommendationRecord) error {
	if len(recs) == 0 {
		return nil
	}

	const colsPerRow = 12
	args := make([]any, 0, len(recs)*colsPerRow)
	placeholders := make([]string, 0, len(recs))

	for i := range recs {
		rec := recs[i]
		if rec.SourceAccountID != nil && (!utf8.ValidString(*rec.SourceAccountID) || strings.ContainsRune(*rec.SourceAccountID, 0)) {
			return fmt.Errorf("invalid source_account_id encoding")
		}
		payload, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("failed to marshal recommendation %d: %w", i, err)
		}
		base := i * colsPerRow
		placeholders = append(placeholders, fmt.Sprintf(
			"($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10, base+11, base+12,
		))
		args = append(args,
			collectedAt,        // collected_at
			rec.CloudAccountID, // cloud_account_id (nullable)
			rec.Provider,       // provider
			rec.Service,        // service
			rec.Region,         // region
			rec.ResourceType,   // resource_type
			rec.Engine,         // engine
			payload,            // payload (JSONB)
			rec.UpfrontCost,    // upfront_cost
			rec.Savings,        // monthly_savings
			rec.Term,           // term
			rec.Payment,        // payment_option
		)
	}

	query := fmt.Sprintf(`
		INSERT INTO recommendations
		    (collected_at, cloud_account_id, provider, service, region,
		     resource_type, engine, payload, upfront_cost, monthly_savings,
		     term, payment_option)
		VALUES %s
		ON CONFLICT (account_key, provider, service, region, resource_type, engine, term, payment_option, availability_zone, platform, tenancy, az_config)
		DO UPDATE SET
		    payload         = EXCLUDED.payload,
		    upfront_cost    = EXCLUDED.upfront_cost,
		    monthly_savings = EXCLUDED.monthly_savings,
		    collected_at    = EXCLUDED.collected_at,
		    cloud_account_id = EXCLUDED.cloud_account_id
	`, strings.Join(placeholders, ","))

	if _, err := tx.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("failed to insert recommendations batch: %w", err)
	}
	return nil
}
