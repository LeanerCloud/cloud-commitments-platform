import { loadRecommendations } from '../recommendations';

/**
 * Issue #196 — after any mutation to an `account_service_overrides` row
 * (create, edit, reset/delete), refresh the cached recommendations list so
 * the read-time filters (`enabled`, include/exclude lists) and the
 * dashboard's coverage cap reflect the new state without a manual reload.
 *
 * Errors are swallowed: the override mutation that just succeeded shouldn't
 * be reported as a failure because the secondary refresh fails. The recs
 * page re-fetches on the next nav event, so a stale list is recoverable.
 */
export async function refreshRecommendationsAfterOverrideChange(): Promise<void> {
  try {
    await loadRecommendations();
  } catch (err) {
    console.warn('Failed to refresh recommendations after override change:', err);
  }
}

// Cached global defaults surfaced by loadGlobalSettings. Used by the override
// modal (issue #112) to show "Inherit (currently: X)" labels so the user can
// see what they'd be overriding without switching tabs.
export let cachedGlobalDefaults: { term: number; payment: string; coverage: number } = {
  term: 3,
  payment: 'no-upfront',
  coverage: 80,
};

/**
 * Override the cached global defaults used for the "Inherit (currently: X)"
 * labels. Test-only; production code updates the cache via loadGlobalSettings.
 */
export function setGlobalDefaultsForTest(defaults: { term: number; payment: string; coverage: number }): void {
  cachedGlobalDefaults = { ...defaults };
}

export function setCachedGlobalDefaults(defaults: { term: number; payment: string; coverage: number }): void {
  cachedGlobalDefaults = defaults;
}

// AWS services that can carry a per-account override. Mirrors the AWS-only
// entries in SERVICE_FIELDS. Issue #109 adds AZURE_OVERRIDE_SERVICES and
// GCP_OVERRIDE_SERVICES below; getOverrideServicesForProvider dispatches to
// the right list so the create modal is provider-aware.
// (issue #104) since the backend payment/term semantics differ per provider.
const AWS_OVERRIDE_SERVICES: ReadonlyArray<{ value: string; label: string }> = [
  { value: 'ec2',                       label: 'EC2 (Reserved Instances)' },
  { value: 'rds',                       label: 'RDS' },
  { value: 'elasticache',               label: 'ElastiCache' },
  { value: 'opensearch',                label: 'OpenSearch' },
  { value: 'redshift',                  label: 'Redshift' },
  // Issue #22 follow-up: per-plan-type SP slugs aligned with
  // SERVICE_FIELDS so per-account overrides target the same rows the
  // global Settings cards write to. The legacy `savingsplans` and PR
  // #71 `sagemaker` values were removed because they no longer
  // correspond to live ServiceConfig rows after migration 000040.
  { value: 'savings-plans-compute',     label: 'Compute Savings Plans' },
  { value: 'savings-plans-ec2instance', label: 'EC2 Instance Savings Plans' },
  { value: 'savings-plans-sagemaker',   label: 'SageMaker Savings Plans' },
  { value: 'savings-plans-database',    label: 'Database Savings Plans' },
];

// Human-readable labels for Azure and GCP services used in the override
// service dropdown. AWS labels are carried by AWS_OVERRIDE_SERVICES above.
// Values are the service ids recommendations carry (issue #613), the same
// ones SERVICE_FIELDS writes.
const AZURE_OVERRIDE_SERVICES: ReadonlyArray<{ value: string; label: string }> = [
  { value: 'compute',       label: 'Virtual Machines' },
  { value: 'relational-db', label: 'SQL Database' },
  { value: 'nosql',         label: 'Cosmos DB' },
  { value: 'cache',         label: 'Cache for Redis' },
  { value: 'search',        label: 'AI Search' },
];

const GCP_OVERRIDE_SERVICES: ReadonlyArray<{ value: string; label: string }> = [
  { value: 'compute',       label: 'Compute Engine' },
  { value: 'relational-db', label: 'Cloud SQL' },
  { value: 'cache',         label: 'Memorystore' },
];

/** Return the service list for the given provider's override modal. */
export function getOverrideServicesForProvider(provider: string): ReadonlyArray<{ value: string; label: string }> {
  if (provider === 'azure') return AZURE_OVERRIDE_SERVICES;
  if (provider === 'gcp')   return GCP_OVERRIDE_SERVICES;
  return AWS_OVERRIDE_SERVICES;
}
