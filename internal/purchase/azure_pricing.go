package purchase

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/google/uuid"
)

var ErrStaleAzurePricing = errors.New("stale Azure recommendation pricing; refresh recommendations and submit a new purchase")

func (m *Manager) loadCurrentAzurePricing(ctx context.Context) ([]config.RecommendationRecord, error) {
	rows, err := m.config.ListStoredRecommendations(ctx, config.RecommendationFilter{Provider: "azure", RequireAzurePricingMigration: true})
	if err != nil {
		if errors.Is(err, config.ErrAzurePricingNotReady) {
			return nil, fmt.Errorf("%w: %w", ErrStaleAzurePricing, err)
		}
		return nil, fmt.Errorf("load current Azure recommendation pricing: %w", err)
	}
	return rows, nil
}

// staleAzurePricingRefusal refuses an execution whose Azure rows no longer carry
// the approved price, or whose account has a pricing hold marker (held, or
// expired with a row that eviction did not delete). The collection state is read
// before the rows: a re-pricing collection commits both in one tx, so a marker
// read before the commit still refuses and one read after it is followed by the
// new rows, which fail the price match.
func (m *Manager) staleAzurePricingRefusal(ctx context.Context, exec *config.PurchaseExecution) error {
	var azure []config.RecommendationRecord
	for i := range exec.Recommendations {
		rec := &exec.Recommendations[i]
		if rec.Provider == "azure" {
			azure = append(azure, *rec)
		}
	}
	if len(azure) == 0 {
		return nil
	}
	fresh, err := m.loadAzureFreshness(ctx)
	if err != nil {
		return err
	}
	rows, err := m.loadCurrentAzurePricing(ctx)
	if err != nil {
		return err
	}
	matched, err := matchStoredAzure(rows, azure)
	if err != nil {
		return err
	}
	return azureHoldRefusal(fresh, azure, matched)
}

// matchStoredAzure returns the stored row of each rec, or refuses when one no
// longer carries the approved price.
func matchStoredAzure(rows, azure []config.RecommendationRecord) ([]config.RecommendationRecord, error) {
	byID := make(map[string]config.RecommendationRecord, len(rows))
	for i := range rows {
		byID[rows[i].ID] = rows[i]
	}
	matched := make([]config.RecommendationRecord, 0, len(azure))
	for i := range azure {
		rec := &azure[i]
		stored, ok := byID[rec.ID]
		if rec.ID == "" || !ok || !azurePricingMatches(*rec, stored) {
			return nil, fmt.Errorf("%w: Azure recommendation %q no longer has the approved price", ErrStaleAzurePricing, rec.ID)
		}
		matched = append(matched, stored)
	}
	return matched, nil
}

// loadAzureFreshness fails closed when the collection state cannot be read.
func (m *Manager) loadAzureFreshness(ctx context.Context) (*config.RecommendationsFreshness, error) {
	fresh, err := m.config.GetRecommendationsFreshness(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read the latest collection state to confirm Azure prices are current: %w", ErrStaleAzurePricing, err)
	}
	if fresh == nil {
		return nil, fmt.Errorf("%w: the latest collection state is missing, so Azure prices cannot be confirmed current", ErrStaleAzurePricing)
	}
	return fresh, nil
}

// azureHoldRefusal refuses Azure recs whose account carries a pricing hold
// marker: held through a Retail Prices outage, or expired. Those rows are the
// previous cycles', so they match the approved execution while their price may be
// days old; an expired account's row is one eviction skipped (locked by another
// tx) or one not yet deleted. stored are the rows the recs were matched to.
func azureHoldRefusal(fresh *config.RecommendationsFreshness, azure, stored []config.RecommendationRecord) error {
	if fresh.LastCollectionPartial == nil {
		return nil
	}
	for _, sweep := range fresh.LastCollectionPartial.Providers {
		if sweep.Provider != "azure" {
			continue
		}
		if err := heldSweepRefusal(sweep, azure); err != nil {
			return err
		}
		if err := expiredSweepRefusal(sweep, stored); err != nil {
			return err
		}
	}
	return nil
}

// heldSweepRefusal: a rec without an account (an unresolved ambient identity)
// cannot be named in the hold, so any current Azure hold refuses it.
func heldSweepRefusal(sweep config.PartialProviderSweep, azure []config.RecommendationRecord) error {
	if sweep.Reasons[config.PartialReasonPricingUnavailable] == 0 {
		return nil
	}
	for i := range azure {
		if azureAccountHeld(azure[i].CloudAccountID, sweep.HeldAccountIDs) {
			return fmt.Errorf("%w: Azure prices for recommendation %q could not be refreshed (price catalog unavailable); "+
				"refresh recommendations after prices recover and submit a new purchase", ErrStaleAzurePricing, azure[i].ID)
		}
	}
	return nil
}

// expiredSweepRefusal matches the stored row's account, not the exec rec's (the
// price was matched against the stored row), exactly: an ambient row is refused
// only by the nil UUID key. It does not need pricing_unavailable, which the
// marker drops once nothing is held.
func expiredSweepRefusal(sweep config.PartialProviderSweep, stored []config.RecommendationRecord) error {
	for i := range stored {
		if azureKeyIn(sweep.ExpiredHoldAccountIDs, stored[i].CloudAccountID) {
			return fmt.Errorf("%w: Azure prices for recommendation %q are older than the %s pricing hold limit and could not be refreshed; "+
				"refresh recommendations after prices recover and submit a new purchase", ErrStaleAzurePricing, stored[i].ID, config.PricingHoldMaxAge)
		}
	}
	return nil
}

// azureAccountKey mirrors the account_key of a stored row (nil UUID for none).
// The row's account comes from the JSON payload and the marker's ids from the
// lowercase account_key column, so both sides are compared case-insensitively.
func azureAccountKey(accountID *string) string {
	if accountID == nil || strings.TrimSpace(*accountID) == "" {
		return uuid.Nil.String()
	}
	return strings.ToLower(strings.TrimSpace(*accountID))
}

func azureKeyIn(ids []string, accountID *string) bool {
	key := azureAccountKey(accountID)
	return slices.ContainsFunc(ids, func(id string) bool { return strings.ToLower(strings.TrimSpace(id)) == key })
}

func azureAccountHeld(accountID *string, held []string) bool {
	return accountID == nil || strings.TrimSpace(*accountID) == "" || azureKeyIn(held, accountID)
}

func azurePricingMatches(rec, stored config.RecommendationRecord) bool {
	if rec.Count <= 0 || stored.Count <= 0 {
		return false
	}
	total := stored.UpfrontCost
	if stored.MonthlyCost != nil {
		total += *stored.MonthlyCost * float64(stored.Term) * 12
	}
	if total <= 0 || !azureCostEqual(total, total, 1) {
		return false
	}
	ratio := float64(rec.Count) / float64(stored.Count)
	if !azureCostEqual(rec.UpfrontCost, stored.UpfrontCost, ratio) {
		return false
	}
	if rec.MonthlyCost == nil || stored.MonthlyCost == nil {
		return rec.MonthlyCost == nil && stored.MonthlyCost == nil
	}
	return azureCostEqual(*rec.MonthlyCost, *stored.MonthlyCost, ratio)
}

func azureCostEqual(a, b, ratio float64) bool {
	if a < 0 || b < 0 || math.IsNaN(a) || math.IsNaN(b) || math.IsInf(a, 0) || math.IsInf(b, 0) {
		return false
	}
	b *= ratio
	if math.IsInf(b, 0) {
		return false
	}
	// Nonnegative IEEE-754 bit patterns are ordered; allow only rounding drift.
	x, y := math.Float64bits(math.Abs(a)), math.Float64bits(math.Abs(b))
	if x > y {
		return x-y <= 8
	}
	return y-x <= 8
}
