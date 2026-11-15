package purchase

import (
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// mapServiceType maps a service string to common.ServiceType. Both the
// canonical hyphenated slugs (compute, relational-db, cache, search,
// data-warehouse) and the legacy AWS-only slugs (ec2, rds, elasticache,
// opensearch, redshift, memorydb) are recognized; everything else passes
// through verbatim. Savings Plans slugs are normalized by mapSavingsPlansSlug.
func (m *Manager) mapServiceType(service string) common.ServiceType {
	if svc, ok := mapSavingsPlansSlug(service); ok {
		return svc
	}
	if svc, ok := mapServiceSlug(service); ok {
		return svc
	}
	return common.ServiceType(service)
}

var errUnknownOfferingClass = errors.New("unknown EC2 RI offering class")

// resolveEC2OfferingClass mirrors the pinned EC2 client's resolveOfferingClassType,
// where "" means convertible, so history records the class the client buys.
func resolveEC2OfferingClass(s string) (ec2types.OfferingClassType, error) {
	switch s {
	case "", string(ec2types.OfferingClassTypeConvertible):
		return ec2types.OfferingClassTypeConvertible, nil
	case string(ec2types.OfferingClassTypeStandard):
		return ec2types.OfferingClassTypeStandard, nil
	default:
		return "", fmt.Errorf("%w %q: must be %q or %q", errUnknownOfferingClass, s,
			ec2types.OfferingClassTypeConvertible, ec2types.OfferingClassTypeStandard)
	}
}

// purchaseClientService is the service whose client buys a recommendation of
// service. Azure Redis recommendations are labeled ServiceCache, but only the
// managedredis client (ServiceMemoryDB) buys RedisCache reservations; the
// Azure cache client refuses every purchase (cloud-commitments-go#694, #1801).
// Labels in history and suppressions keep the recommendation's own service.
func purchaseClientService(providerName string, service common.ServiceType) common.ServiceType {
	if providerName == string(common.ProviderAzure) && service == common.ServiceCache {
		return common.ServiceMemoryDB
	}
	return service
}

// mapServiceSlug returns the common.ServiceType for a canonical or legacy
// service slug.
//
// Canonical slugs (compute, relational-db, nosql, cache, search,
// data-warehouse) map to the canonical ServiceType constants. Legacy
// AWS-only slugs (ec2, rds, elasticache, opensearch, redshift, memorydb)
// map to the AWS-legacy constants for backward compat with rec rows
// persisted before the canonical normalisation. nosql is Azure Cosmos DB
// (#613); it used to reach the Azure client only through mapServiceType's
// verbatim fallback.
//
// The split matters because Azure and GCP providers' GetServiceClient
// switches accept canonical-only; passing the AWS-legacy constants for a
// canonical rec falls through to default and returns "unsupported
// service: <legacy>". AWS provider accepts both legacy and canonical
// forms (see providers/aws/provider.go), so the legacy slugs below are
// still safe for AWS rec rows. Bug:
// https://github.com/LeanerCloud/CUDly/issues/626 (tracked in the monorepo).
//
// Pulled out of mapServiceType to keep that function under the gocyclo
// budget (same pattern as mapSavingsPlansSlug).
func mapServiceSlug(service string) (common.ServiceType, bool) {
	slugs := map[string]common.ServiceType{
		// Canonical slugs.
		"compute":        common.ServiceCompute,
		"relational-db":  common.ServiceRelationalDB,
		"nosql":          common.ServiceNoSQL,
		"cache":          common.ServiceCache,
		"search":         common.ServiceSearch,
		"data-warehouse": common.ServiceDataWarehouse,
	}
	if svc, ok := slugs[service]; ok {
		return svc, true
	}
	svc, ok := legacyAWSServiceSlugs[service]
	return svc, ok
}

// legacyAWSServiceSlugs are the AWS-only slugs. A new alias must also be added
// to config.awsLegacyServiceSlugs, else suppressions miss it (#1420); a test
// ranges over this map to enforce that.
var legacyAWSServiceSlugs = map[string]common.ServiceType{
	"ec2":         common.ServiceEC2,
	"rds":         common.ServiceRDS,
	"elasticache": common.ServiceElastiCache,
	"opensearch":  common.ServiceOpenSearch,
	"redshift":    common.ServiceRedshift,
	"memorydb":    common.ServiceMemoryDB,
}

// mapSavingsPlansSlug normalizes both the canonical hyphenated SP slugs and
// the dash-free spellings the frontend has historically sent into the
// matching common.ServiceType. The map covers the legacy umbrella plus the
// four per-plan-type slugs in both spellings — pulled out of mapServiceType
// to keep that switch under the gocyclo budget.
//
// TODO(#95): PR #94 (merged 2026-04-30) renamed "savings-plans" ->
// "savingsplans". purchase_executions rows have a ~6-month retention window,
// so the earliest safe drop date is 2026-10-30. Until then the
// "savings-plans"-spelled aliases below must stay so Lambda-scheduled
// executions persisted before PR #94 still map correctly on retry/approval.
// To drop: verify zero rows with
//
//	SELECT id FROM purchase_executions
//	WHERE recommendations::text LIKE '%"service":"savings-plans"%'
//	LIMIT 1;
//
// then remove every "savings-plans*" key from this map and the matching
// case in pkg/common/service_details_codec.go: newDetailsForService, and
// flip the coverage_extra_test.go case (line ~60) to assert
// ServiceType("savings-plans") (default-arm pass-through). The
// "savingsplans-*" keys are forward-compat and should stay.
//
// A new per-plan-type spelling must also be added to
// config.awsSavingsPlansSpellings, else a suppression stored under it misses
// recs (#1422); a test ranges over savingsPlansSlugs to enforce that.
func mapSavingsPlansSlug(service string) (common.ServiceType, bool) {
	svc, ok := savingsPlansSlugs[service]
	return svc, ok
}

var savingsPlansSlugs = map[string]common.ServiceType{
	"savings-plans":             common.ServiceSavingsPlansAll,
	"savingsplans":              common.ServiceSavingsPlansAll,
	"savings-plans-compute":     common.ServiceSavingsPlansCompute,
	"savingsplans-compute":      common.ServiceSavingsPlansCompute,
	"savings-plans-ec2instance": common.ServiceSavingsPlansEC2Instance,
	"savingsplans-ec2instance":  common.ServiceSavingsPlansEC2Instance,
	"savings-plans-sagemaker":   common.ServiceSavingsPlansSageMaker,
	"savingsplans-sagemaker":    common.ServiceSavingsPlansSageMaker,
	"savings-plans-database":    common.ServiceSavingsPlansDatabase,
	"savingsplans-database":     common.ServiceSavingsPlansDatabase,
}

// applyEngineFallback backfills the Engine field on DB / Cache Details
// from the persisted RecommendationRecord.Engine column when the decoded
// Details left Engine empty. Legacy rows (persisted before #453) carry
// an empty Details payload but a populated Engine column — without this
// backfill, a Postgres RDS rec would silently mis-purchase as whatever
// engine buildOfferingFilters defaults to. For non-DB/Cache Details
// types the call is a no-op. Pointer-typed Details get mutated in
// place; callers pass the pointer that's about to be assigned into
// recommendation.Details so the caller's variable sees the update.
func applyEngineFallback(details common.ServiceDetails, engine string) {
	if engine == "" {
		return
	}
	switch d := details.(type) {
	case *common.DatabaseDetails:
		if d.Engine == "" {
			d.Engine = engine
		}
	case *common.CacheDetails:
		if d.Engine == "" {
			d.Engine = engine
		}
	}
}
