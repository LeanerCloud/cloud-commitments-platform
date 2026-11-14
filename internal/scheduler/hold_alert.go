package scheduler

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/google/uuid"
)

// ambientHoldID is the nil-UUID key of the ambient (unregistered) Azure
// identity in the persisted hold; it is not an account an operator can act on.
var ambientHoldID = uuid.Nil.String()

// expiredAzureHolds returns the Azure accounts whose pricing hold has expired,
// as persisted in the freshness row. The row carries them until the account
// re-prices, so it is the dedupe state: an alert fires only for ids that were
// not already in it before this collection (issue #1900). The map is never nil
// on success; a read error returns a nil map so callers can fail closed.
func (s *Scheduler) expiredAzureHolds(ctx context.Context) (map[string]bool, error) {
	fresh, err := s.config.GetRecommendationsFreshness(ctx)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	if fresh == nil || fresh.LastCollectionPartial == nil {
		return ids, nil
	}
	for _, p := range fresh.LastCollectionPartial.Providers {
		if p.Provider != "azure" {
			continue
		}
		for _, id := range p.ExpiredHoldAccountIDs {
			if id != ambientHoldID {
				ids[id] = true
			}
		}
	}
	return ids, nil
}

// alertNewlyExpiredHolds emails the operator about accounts that passed the
// pricing hold bound in the collection just persisted. A nil before means the
// pre-persist read failed: no alert is sent, since every carried id would look
// new. A failed send keeps the ids in s.alertRetry so a later cycle resends
// them while they are still expired; the retry state is per process, so a
// restart before the retry loses it (accepted over adding a table).
func (s *Scheduler) alertNewlyExpiredHolds(ctx context.Context, before map[string]bool) {
	if before == nil {
		return
	}
	current, err := s.expiredAzureHolds(ctx)
	if err != nil {
		logging.Errorf("Could not read expired Azure pricing holds, skipping alert: %v", err)
		return
	}
	s.alertRetryMu.Lock()
	defer s.alertRetryMu.Unlock()
	var fresh []string
	for id := range current {
		if !before[id] || s.alertRetry[id] {
			fresh = append(fresh, id)
		}
	}
	s.alertRetry = nil
	if len(fresh) == 0 {
		return
	}
	slices.Sort(fresh)
	subject := fmt.Sprintf("Azure pricing unavailable: stale recommendations removed for %d account(s)", len(fresh))
	msg := fmt.Sprintf("Azure prices have been unavailable for more than %s. Stale recommendations were removed for these accounts: %s",
		config.PricingHoldMaxAge, strings.Join(fresh, ", "))
	if err := s.email.SendNotification(ctx, subject, msg); err != nil {
		logging.Errorf("Failed to send Azure pricing hold alert, will retry next cycle: %v", err)
		s.alertRetry = map[string]bool{}
		for _, id := range fresh {
			s.alertRetry[id] = true
		}
	}
}
