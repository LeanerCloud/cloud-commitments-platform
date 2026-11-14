package scheduler

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// expiredAzureHolds returns the Azure accounts whose pricing hold has expired,
// as persisted in the freshness row. The row carries them until the account
// re-prices, so it is the dedupe state: an alert fires only for ids that were
// not already in it before this collection (issue #1900).
func (s *Scheduler) expiredAzureHolds(ctx context.Context) map[string]bool {
	fresh, err := s.config.GetRecommendationsFreshness(ctx)
	if err != nil || fresh == nil || fresh.LastCollectionPartial == nil {
		if err != nil {
			logging.Warnf("Could not read expired Azure pricing holds: %v", err)
		}
		return nil
	}
	ids := map[string]bool{}
	for _, p := range fresh.LastCollectionPartial.Providers {
		if p.Provider != "azure" {
			continue
		}
		for _, id := range p.ExpiredHoldAccountIDs {
			ids[id] = true
		}
	}
	return ids
}

// alertNewlyExpiredHolds emails the operator about accounts that passed the
// pricing hold bound in the collection just persisted. A failed send is logged
// and not retried: the account stays in the carried state, so it will not
// alert again.
func (s *Scheduler) alertNewlyExpiredHolds(ctx context.Context, before map[string]bool) {
	var fresh []string
	for id := range s.expiredAzureHolds(ctx) {
		if !before[id] {
			fresh = append(fresh, id)
		}
	}
	if len(fresh) == 0 {
		return
	}
	slices.Sort(fresh)
	subject := fmt.Sprintf("Azure pricing unavailable: stale recommendations removed for %d account(s)", len(fresh))
	msg := fmt.Sprintf("Azure prices have been unavailable for more than %s. Stale recommendations were removed for these accounts: %s",
		config.PricingHoldMaxAge, strings.Join(fresh, ", "))
	if err := s.email.SendNotification(ctx, subject, msg); err != nil {
		logging.Errorf("Failed to send Azure pricing hold alert: %v", err)
	}
}
