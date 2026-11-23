package scheduler

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// suppressionKey is the 6-tuple used to group suppressions. Within a group,
// each row's variant (availability zone, platform, tenancy; issue #816)
// decides whether it applies to a given rec (PurchaseSuppression.AppliesTo).
type suppressionKey struct {
	accountID, provider, service, region, resourceType, engine string
}

// suppressionAgg aggregates the active suppression rows that apply to one
// rec: cumulative count, earliest expiry (drives the badge's
// "Xd remaining"), and the execution whose suppression contributed
// the most (drives the badge deep-link).
type suppressionAgg struct {
	earliestExpiresAt       time.Time
	primaryExecutionCreated time.Time
	primaryExecutionID      string
	suppressedCount         int
	primaryExecutionContrib int
}

// add folds one suppression row into the aggregate.
func (a *suppressionAgg) add(sup *config.PurchaseSuppression) {
	a.suppressedCount += sup.SuppressedCount
	if sup.ExpiresAt.Before(a.earliestExpiresAt) {
		a.earliestExpiresAt = sup.ExpiresAt
	}
	// "Most contribution" selects the biggest single suppression
	// row; ties go to the most-recently-created row so a fresh
	// cancel of an older peer doesn't leave the badge pointing at
	// a stale execution.
	if sup.SuppressedCount > a.primaryExecutionContrib ||
		(sup.SuppressedCount == a.primaryExecutionContrib && sup.CreatedAt.After(a.primaryExecutionCreated)) {
		a.primaryExecutionID = sup.ExecutionID
		a.primaryExecutionCreated = sup.CreatedAt
		a.primaryExecutionContrib = sup.SuppressedCount
	}
}

// applySuppressions subtracts active purchase_suppressions from each
// rec's count and annotates the rec with the SuppressedCount /
// SuppressionExpiresAt / PrimarySuppressionExecutionID fields that
// drive the "recently purchased" badge on the frontend. Recs whose
// remaining count hits 0 are dropped entirely.
//
// A read failure returns no recs alongside the error, so a caller that
// dropped the error would still show nothing rather than the un-suppressed
// list (issue #111).
func (s *Scheduler) applySuppressions(ctx context.Context, recs []config.RecommendationRecord) ([]config.RecommendationRecord, error) {
	sups, err := s.config.ListActiveSuppressions(ctx)
	if err != nil {
		return nil, err
	}
	if len(sups) == 0 {
		return recs, nil
	}
	index := aggregateSuppressions(sups)
	return applySuppressionIndex(recs, index), nil
}

// aggregateSuppressions groups a raw suppression list by 6-tuple. The
// per-rec aggregate is built in applySuppressionIndex, because which rows
// of a group apply depends on the rec's variant.
func aggregateSuppressions(sups []config.PurchaseSuppression) map[suppressionKey][]config.PurchaseSuppression {
	index := make(map[suppressionKey][]config.PurchaseSuppression, len(sups))
	for i := range sups {
		sup := sups[i]
		k := suppressionKey{
			accountID: sup.AccountID, provider: sup.Provider,
			service: config.CanonicalSuppressionService(sup.Provider, sup.Service),
			region:  sup.Region, resourceType: sup.ResourceType,
		}
		k.engine = config.CanonicalSuppressionEngine(k.provider, k.service, sup.Engine)
		index[k] = append(index[k], sup)
	}
	return index
}

// matchingSuppressions aggregates the rows of rec's 6-tuple group that apply
// to rec's variant: rows for the same availability zone, platform and
// tenancy, plus legacy rows with no variant (a wildcard over the 6-tuple).
// A rec whose Details cannot be read matches every row of its group, the
// pre-#816 behavior. Returns nil when no row applies.
func matchingSuppressions(rec *config.RecommendationRecord, group []config.PurchaseSuppression) *suppressionAgg {
	var recVariant *config.SuppressionVariant
	if v, err := rec.SuppressionVariant(); err == nil {
		recVariant = &v
	}
	var a *suppressionAgg
	for i := range group {
		if !group[i].AppliesTo(recVariant) {
			continue
		}
		if a == nil {
			a = &suppressionAgg{earliestExpiresAt: group[i].ExpiresAt}
		}
		a.add(&group[i])
	}
	return a
}

// applySuppressionIndex walks recs, subtracts the count of the
// suppressions that apply to each rec, drops recs that go to 0 or below,
// and annotates the survivors with the badge fields.
func applySuppressionIndex(recs []config.RecommendationRecord, index map[suppressionKey][]config.PurchaseSuppression) []config.RecommendationRecord {
	// Allocate a fresh backing array so callers that hold a reference to
	// the original recs slice do not see mutations (05-M1).
	out := make([]config.RecommendationRecord, 0, len(recs))
	for i := range recs {
		rec := recs[i]
		accountID := ""
		if rec.CloudAccountID != nil {
			accountID = *rec.CloudAccountID
		}
		k := suppressionKey{
			accountID: accountID, provider: rec.Provider,
			service: config.CanonicalSuppressionService(rec.Provider, rec.Service),
			region:  rec.Region, resourceType: rec.ResourceType,
		}
		k.engine = config.CanonicalSuppressionEngine(k.provider, k.service, rec.Engine)
		a := matchingSuppressions(&rec, index[k])
		if a == nil {
			out = append(out, rec)
			continue
		}
		rec.Count -= a.suppressedCount
		if rec.Count <= 0 {
			continue // fully covered by recent purchases; hide
		}
		rec.SuppressedCount = a.suppressedCount
		expiry := a.earliestExpiresAt
		rec.SuppressionExpiresAt = &expiry
		primary := a.primaryExecutionID
		rec.PrimarySuppressionExecutionID = &primary
		out = append(out, rec)
	}
	return out
}
