package purchase

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// Fixed refusal notes (#1863 D11). Both are stored verbatim and are on the
// stored-error allowlist (errnotes.go), so neither may carry provider text.
const (
	elastiCacheDuplicateCheckFailedNote = "ElastiCache duplicate check could not complete; no purchase was submitted; retry later, and if it persists check the account's ElastiCache reservations in AWS"
	elastiCacheRecentReservationNote    = "a recent ElastiCache reservation for this region, node type and a compatible engine already covers all or part of this purchase (a Redis reservation can also cover Valkey); no purchase was submitted, to avoid a duplicate; retry after 24 hours, or pick a different node type or region if more capacity is really needed"
)

// elastiCacheReservationIDPrefix mirrors the unexported prefix the elasticache
// client gives a token-derived ReservedCacheNodeId (client.go:162). The parity
// test drives the real client, so a rule change there fails it.
const elastiCacheReservationIDPrefix = "elasticache-id-"

// isAWSElastiCache gates the check: Azure cache and AWS memorydb recs must
// never reach it. Both service constants are accepted because the platform
// stores the legacy "elasticache" slug next to the canonical "cache".
func isAWSElastiCache(providerName string, service common.ServiceType) bool {
	return common.ProviderType(providerName) == common.ProviderAWS &&
		(service == common.ServiceCache || service == common.ServiceElastiCache)
}

// elastiCacheOwnReservationIDs returns the reservation IDs this execution's
// selected AWS ElastiCache recs will create, from the same per-rec tokens the
// purchase path passes to the provider. A listed reservation with one of these
// IDs came from this lineage, not from another purchase.
// Two same-pool recs of one execution cannot see each other here; hasDuplicateElastiCachePool
// refuses that selection before submission.
func elastiCacheOwnReservationIDs(exec *config.PurchaseExecution, selected []int) map[string]bool {
	ids := make(map[string]bool)
	for _, i := range selected {
		rec := exec.Recommendations[i]
		service, _ := mapServiceSlug(rec.Service)
		if !isAWSElastiCache(rec.Provider, service) {
			continue
		}
		ids[common.IdempotentReservationID(elastiCacheReservationIDPrefix, common.DeriveIdempotencyToken(idempotencyLineageKey(exec), i))] = true
	}
	return ids
}

// elastiCacheRecentDedupeClient lists without this execution's own reservations.
// It must not define FilterRecommendations: recfilter then takes the generic
// engine-aware map path, which is the one that understands cache nodes.
type elastiCacheRecentDedupeClient struct {
	provider.ServiceClient
	own map[string]bool
	// self is this rec's own reservation ID; sawSelf records that the raw
	// listing held it, so the purchase call can adopt it instead of being refused.
	self    string
	sawSelf *bool
}

func (c elastiCacheRecentDedupeClient) GetExistingCommitments(ctx context.Context) ([]common.Commitment, error) {
	all, err := c.ServiceClient.GetExistingCommitments(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if c.self != "" && all[i].CommitmentID == c.self {
			*c.sawSelf = true
		}
	}
	return slices.DeleteFunc(slices.Clone(all), func(cm common.Commitment) bool { return c.own[cm.CommitmentID] }), nil
}

// elastiCacheDedupeCopy returns the rec recfilter matches against: the library
// trims the engine when it buys, recfilter does not, so an untrimmed engine
// would buy yet be invisible to the check. Details is a pointer, so the
// CacheDetails struct is copied too and the rec that is bought is never changed.
func elastiCacheDedupeCopy(rec common.Recommendation) common.Recommendation {
	if details, ok := rec.Details.(*common.CacheDetails); ok && details != nil {
		d := *details
		d.Engine = strings.TrimSpace(d.Engine)
		rec.Details = &d
	}
	return rec
}

// elastiCacheRefusalFor maps the duplicate checker's outcome to a refusal. The
// order is pinned: this rec's own reservation listed always wins (a retry must
// reach the library guard, which adopts it), then a failed check, then any
// reduction of the rec (D3), then an impossible result count.
func elastiCacheRefusalFor(passed, filtered []common.Recommendation, err error, sawSelf bool, rec common.Recommendation) error {
	switch {
	case sawSelf:
		return nil
	case err != nil:
		logging.Errorf("ElastiCache duplicate check for %s in %s failed: %v", rec.ResourceType, rec.Region, err)
		return beforeSubmission(errors.New(elastiCacheDuplicateCheckFailedNote))
	case len(filtered) > 0 || (len(passed) > 0 && passed[0].Count != rec.Count):
		return beforeSubmission(errors.New(elastiCacheRecentReservationNote))
	case len(passed) != 1:
		logging.Errorf("ElastiCache duplicate check for %s in %s returned %d passed recs, want 1", rec.ResourceType, rec.Region, len(passed))
		return beforeSubmission(errors.New(elastiCacheDuplicateCheckFailedNote))
	}
	return nil
}

// elastiCacheRecentRefusal refuses an AWS ElastiCache rec when a recent
// reservation in the client's region already covers all or part of it, and fails
// closed when the reservations cannot be listed. It returns nil for every other
// rec, for a rec with Count <= 0 (the library's own validation refuses it) and
// when this rec's own reservation (self) is already listed. A refusal is a
// beforeSubmission error: the purchase call is never made.
func elastiCacheRecentRefusal(ctx context.Context, client provider.ServiceClient, rec common.Recommendation, ownIDs map[string]bool, self string) error {
	if !isAWSElastiCache(string(rec.Provider), rec.Service) || rec.Count <= 0 {
		return nil
	}
	sawSelf := false
	passed, filtered, err := recfilter.NewDuplicateChecker(0).AdjustRecommendationsForExisting(
		ctx, []common.Recommendation{elastiCacheDedupeCopy(rec)}, elastiCacheRecentDedupeClient{ServiceClient: client, own: ownIDs, self: self, sawSelf: &sawSelf})
	return elastiCacheRefusalFor(passed, filtered, err, sawSelf, rec)
}

// purchaseOwnNames carries the commitment names this execution's selected recs
// will create, per provider, so a retry is told apart from another lineage's
// purchase. A nil pointer reads as no own names.
type purchaseOwnNames struct {
	gcp, elastiCache map[string]bool
}

func newPurchaseOwnNames(exec *config.PurchaseExecution, selected []int) *purchaseOwnNames {
	return &purchaseOwnNames{
		gcp:         gcpOwnCommitmentNames(exec, selected),
		elastiCache: elastiCacheOwnReservationIDs(exec, selected),
	}
}

func (o *purchaseOwnNames) gcpNames() map[string]bool {
	if o == nil {
		return nil
	}
	return o.gcp
}

func (o *purchaseOwnNames) elastiCacheNames() map[string]bool {
	if o == nil {
		return nil
	}
	return o.elastiCache
}

// ownElastiCacheID is the reservation ID this rec's purchase call would create,
// or "" when the call carries no idempotency token.
func ownElastiCacheID(opts common.PurchaseOptions) string {
	if opts.IdempotencyToken == "" {
		return ""
	}
	return common.IdempotentReservationID(elastiCacheReservationIDPrefix, opts.IdempotencyToken)
}
