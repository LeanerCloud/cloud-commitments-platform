package config

import "time"

// BuildSuppressions converts the recs on an executePurchase request into
// purchase_suppressions rows, one per unique 6-tuple (account, provider,
// service, region, resource_type, engine). Counts for duplicate tuples
// within the same request are summed (defensive — the UI should collapse
// them client-side, but the backend doesn't trust that). Providers with
// a grace period of 0 (feature disabled) contribute no rows.
func BuildSuppressions(recs []RecommendationRecord, executionID string, cfg *GlobalConfig, now time.Time) []PurchaseSuppression {
	// Aggregate count per 6-tuple before writing suppression rows so
	// the UNIQUE(execution_id, ...6-tuple) constraint can't fire from
	// a request that repeated the same tuple.
	type key struct {
		accountID, provider, service, region, resourceType, engine string
	}
	agg := map[key]int{}
	order := []key{}
	for _rvc := range recs {
		rec := recs[_rvc]
		if rec.Count <= 0 {
			continue
		}
		accountID := ""
		if rec.CloudAccountID != nil {
			accountID = *rec.CloudAccountID
		}
		k := key{
			accountID:    accountID,
			provider:     rec.Provider,
			service:      rec.Service,
			region:       rec.Region,
			resourceType: rec.ResourceType,
			engine:       rec.Engine,
		}
		if _, seen := agg[k]; !seen {
			order = append(order, k)
		}
		agg[k] += rec.Count
	}

	out := make([]PurchaseSuppression, 0, len(order))
	for _, k := range order {
		graceDays := DefaultGracePeriodDays
		if cfg != nil {
			graceDays = cfg.GracePeriodFor(k.provider)
		}
		if graceDays <= 0 {
			continue // feature disabled for this provider
		}
		out = append(out, PurchaseSuppression{
			ExecutionID:     executionID,
			AccountID:       k.accountID,
			Provider:        k.provider,
			Service:         k.service,
			Region:          k.region,
			ResourceType:    k.resourceType,
			Engine:          k.engine,
			SuppressedCount: agg[k],
			ExpiresAt:       now.Add(time.Duration(graceDays) * 24 * time.Hour),
		})
	}
	return out
}
