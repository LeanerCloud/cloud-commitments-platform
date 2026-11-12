package api

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// mergeRequestedServices folds the single service a legacy PUT describes into
// the plan's stored services map instead of replacing the whole map (issue
// #934). requested is the output of PlanRequest.buildServiceConfig: nil or
// exactly one "provider/service" entry.
//
//   - No service in the request: the stored map is kept as is.
//   - The request names a service the plan already has (key
//     "provider/service" or the "provider:service" form migration 000040
//     wrote): that entry is updated under its existing key, keeping the
//     per-service filters the legacy form cannot express, and every other
//     entry is kept.
//   - The plan has at most one service and the request names another: the
//     request replaces it, which is how the single-service form switches
//     service.
//   - The plan has several services and the request names none of them: the
//     form cannot say which entry it means, and adding a service could buy
//     something the user never chose, so the update is refused.
func mergeRequestedServices(existing, requested map[string]config.ServiceConfig) (map[string]config.ServiceConfig, error) {
	if len(requested) == 0 {
		return existing, nil
	}
	var reqKey string
	for reqKey = range requested {
		break
	}
	reqSvc := requested[reqKey]

	if key, ok := findServiceKey(existing, reqSvc.Provider, reqSvc.Service); ok {
		merged := maps.Clone(existing)
		merged[key] = overlayServiceConfig(existing[key], reqSvc)
		return merged, nil
	}
	if key, ok := legacySlugKey(existing, reqSvc); ok {
		merged := maps.Clone(existing)
		merged[key] = overlayServiceConfig(existing[key], reqSvc)
		return merged, nil
	}
	if len(existing) == 1 {
		for key := range existing {
			if carriesUnexpressibleSettings(existing[key]) {
				return nil, NewClientError(409, fmt.Sprintf(
					"plan service %s carries filters, a minimum count or a service ramp that switching to %s would drop; "+
						"edit the stored service instead, or create a new plan", key, reqKey))
			}
		}
	}
	if len(existing) <= 1 {
		return requested, nil
	}
	return nil, NewClientError(400, fmt.Sprintf(
		"plan has %d services (%s) and the request names %s, which is not one of them; "+
			"pick one of the plan's services to edit it, or use the plan editor to change the service set",
		len(existing), strings.Join(sortedServiceKeys(existing), ", "), reqKey))
}

// findServiceKey returns the key of the stored entry for provider/service,
// accepting both the "provider/service" form buildServiceConfig writes and
// the "provider:service" form migration 000040 wrote. It matches on the key,
// not the value: 000040 copied the umbrella value object verbatim, so all
// four "aws:savings-plans-<type>" entries carry Service "savings-plans".
func findServiceKey(services map[string]config.ServiceConfig, provider, service string) (string, bool) {
	for _, key := range []string{provider + "/" + service, provider + ":" + service} {
		if _, ok := services[key]; ok {
			return key, true
		}
	}
	return "", false
}

// legacyPlanServiceIDs maps the per-provider ids plans saved before #608
// carry to the canonical slug; it mirrors LEGACY_PLAN_SERVICE_IDS in
// frontend/src/plans.ts, which is why the UI's request names the canonical
// slug for a plan stored under the legacy one.
var legacyPlanServiceIDs = map[string]string{
	"azure/vm":        "compute",
	"azure/sql":       "relational-db",
	"azure/redis":     "cache",
	"gcp/cloudsql":    "relational-db",
	"gcp/memorystore": "cache",
}

// legacySlugKey finds the lone stored entry the request means when it is
// stored under a legacy slug: the plan has exactly one entry and its
// provider/service value, mapped to canonical, is the request's.
func legacySlugKey(existing map[string]config.ServiceConfig, req config.ServiceConfig) (string, bool) {
	if len(existing) != 1 {
		return "", false
	}
	for key := range existing {
		stored := existing[key]
		canonical, ok := legacyPlanServiceIDs[stored.Provider+"/"+stored.Service]
		if !ok {
			canonical = stored.Service
		}
		if stored.Provider == req.Provider && canonical == req.Service {
			return key, true
		}
	}
	return "", false
}

func carriesUnexpressibleSettings(s config.ServiceConfig) bool {
	return s.MinCount > 0 || s.RampSchedule != "" || len(s.IncludeRegions) > 0 || len(s.ExcludeRegions) > 0 ||
		len(s.IncludeEngines) > 0 || len(s.ExcludeEngines) > 0 || len(s.IncludeTypes) > 0 || len(s.ExcludeTypes) > 0
}

// overlayServiceConfig applies the fields the legacy form sends onto the
// stored entry, keeping the ones it cannot express (ramp_schedule,
// include/exclude filters, min_count).
func overlayServiceConfig(stored, req config.ServiceConfig) config.ServiceConfig {
	stored.Provider = req.Provider
	stored.Service = req.Service
	stored.Enabled = req.Enabled
	stored.Term = req.Term
	stored.Payment = req.Payment
	stored.Coverage = req.Coverage
	return stored
}

func sortedServiceKeys(services map[string]config.ServiceConfig) []string {
	return slices.Sorted(maps.Keys(services))
}
