package purchase

import (
	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
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
