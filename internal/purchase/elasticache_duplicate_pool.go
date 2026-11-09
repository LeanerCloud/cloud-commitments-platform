package purchase

import (
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

const elastiCacheDuplicatePoolRefusal = "refusing to dispatch: this purchase selects the same ElastiCache pool (region, node type, engine) twice for one account, and AWS would treat the second as already covered by the first, so remove the duplicate recommendation and start a new purchase, or ask an operator if this is a plan step"

// hasDuplicateElastiCachePool reports whether two selected AWS ElastiCache recs share an account,
// region, node type and engine. Concurrent per-rec checks cannot see each other's purchase, so both buy.
// Term, payment and count are not in the key: the go library's pool key omits them too.
func hasDuplicateElastiCachePool(exec *config.PurchaseExecution) bool {
	seen := map[string]bool{}
	for _, i := range selectedIndices(exec.Recommendations) {
		key, ok := elastiCachePoolKey(exec, exec.Recommendations[i])
		if !ok {
			continue
		}
		if seen[key] {
			return true
		}
		seen[key] = true
	}
	return false
}

// elastiCachePoolKey returns the pool a rec buys into, or false for a rec the library refuses anyway.
func elastiCachePoolKey(exec *config.PurchaseExecution, rec config.RecommendationRecord) (string, bool) {
	service, _ := mapServiceSlug(rec.Service)
	if !isAWSElastiCache(rec.Provider, service) || rec.Count <= 0 {
		return "", false
	}
	engine := elastiCacheBuyEngine(rec)
	if engine == "" {
		return "", false
	}
	account := ""
	switch {
	case rec.CloudAccountID != nil:
		account = *rec.CloudAccountID
	case exec.CloudAccountID != nil:
		account = *exec.CloudAccountID
	}
	return strings.Join([]string{account, rec.Region, rec.ResourceType, engine}, "|"), true
}

// elastiCacheBuyEngine is the engine the buy path sends, trimmed then normalized; empty when the
// details do not decode into cache details.
func elastiCacheBuyEngine(rec config.RecommendationRecord) string {
	details, err := common.DecodeServiceDetailsFor(rec.Service, rec.Details)
	if err != nil {
		return ""
	}
	cache, ok := details.(*common.CacheDetails)
	if !ok {
		return ""
	}
	applyEngineFallback(cache, rec.Engine)
	return common.NormalizeEngineName(strings.TrimSpace(cache.Engine))
}
