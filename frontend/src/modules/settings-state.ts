import * as api from '../api';

export type AccountProvider = 'aws' | 'azure' | 'gcp';

// Per-service field definitions — used for loading and saving service configs.
export const SERVICE_FIELDS = [
  { provider: 'aws', service: 'ec2',          termId: 'aws-ec2-term',          paymentId: 'aws-ec2-payment' },
  { provider: 'aws', service: 'rds',          termId: 'aws-rds-term',          paymentId: 'aws-rds-payment' },
  { provider: 'aws', service: 'elasticache',  termId: 'aws-elasticache-term',  paymentId: 'aws-elasticache-payment' },
  { provider: 'aws', service: 'opensearch',   termId: 'aws-opensearch-term',   paymentId: 'aws-opensearch-payment' },
  { provider: 'aws', service: 'redshift',     termId: 'aws-redshift-term',     paymentId: 'aws-redshift-payment' },
  // Issue #22 follow-up: AWS Savings Plans split into four per-plan-type
  // cards (Compute / EC2 Instance / SageMaker / Database). The earlier
  // umbrella `savingsplans` and PR #71 `sagemaker` SERVICE_FIELDS entries
  // are replaced by these four. Migration 000040_split_savingsplans
  // copies the umbrella row's term/payment into all four new rows and
  // (when present) overrides the SageMaker slot from PR #71's sagemaker
  // row. Lambda is intentionally NOT a separate card — Lambda has no
  // standalone SP product; its commitments roll up into Compute SP.
  // Issue #136: SP cards carry per-card coverageId and enabledId so users
  // can set divergent coverage and toggle each plan type independently.
  { provider: 'aws', service: 'savings-plans-compute',     termId: 'aws-savings-plans-compute-term',     paymentId: 'aws-savings-plans-compute-payment',     coverageId: 'aws-savings-plans-compute-coverage',     enabledId: 'aws-savings-plans-compute-enabled' },
  { provider: 'aws', service: 'savings-plans-ec2instance', termId: 'aws-savings-plans-ec2instance-term', paymentId: 'aws-savings-plans-ec2instance-payment', coverageId: 'aws-savings-plans-ec2instance-coverage', enabledId: 'aws-savings-plans-ec2instance-enabled' },
  { provider: 'aws', service: 'savings-plans-sagemaker',   termId: 'aws-savings-plans-sagemaker-term',   paymentId: 'aws-savings-plans-sagemaker-payment',   coverageId: 'aws-savings-plans-sagemaker-coverage',   enabledId: 'aws-savings-plans-sagemaker-enabled' },
  { provider: 'aws', service: 'savings-plans-database',    termId: 'aws-savings-plans-database-term',    paymentId: 'aws-savings-plans-database-payment',    coverageId: 'aws-savings-plans-database-coverage',    enabledId: 'aws-savings-plans-database-enabled' },
  // Issue #613: Azure/GCP rows use the service ids recommendations carry
  // (lib/settings-service-ids.ts); the card DOM ids keep their old names.
  { provider: 'azure', service: 'compute',       termId: 'azure-vm-term',         paymentId: 'azure-vm-payment' },
  { provider: 'azure', service: 'relational-db', termId: 'azure-sql-term',        paymentId: 'azure-sql-payment' },
  { provider: 'azure', service: 'nosql',         termId: 'azure-cosmosdb-term',   paymentId: 'azure-cosmosdb-payment' },
  { provider: 'azure', service: 'cache',         termId: 'azure-redis-term',      paymentId: 'azure-redis-payment' },
  { provider: 'azure', service: 'search',        termId: 'azure-search-term',     paymentId: 'azure-search-payment' },
  { provider: 'gcp',   service: 'compute',       termId: 'gcp-compute-term',      paymentId: null },
  { provider: 'gcp',   service: 'relational-db', termId: 'gcp-sql-term',          paymentId: null },
  { provider: 'gcp',   service: 'cache',         termId: 'gcp-memorystore-term',  paymentId: null },
] as const;

// Fields that are persisted to the backend via saveGlobalSettings.
// Used for dirty tracking and unsaved-changes detection.
export const TRACKED_FIELDS = [
  'provider-aws', 'provider-azure', 'provider-gcp',
  'setting-notification-email', 'setting-auto-collect',
  'setting-collection-schedule', 'setting-notification-days',
  'setting-default-term', 'setting-default-payment', 'setting-default-coverage',
  // Per-provider grace-period inputs
  'setting-grace-aws', 'setting-grace-azure', 'setting-grace-gcp',
  // Recommendations cycle params
  'setting-recs-stale-hours', 'setting-recs-lookback-days',
  // 4-eyes approval mode (issue #1005)
  'setting-require-different-approver',
  'setting-experimental-gcp-plan-term',
  'setting-ec2-offering-class',
  // Per-service fields
  ...SERVICE_FIELDS.map(f => f.termId),
  ...SERVICE_FIELDS.filter(f => f.paymentId !== null).map(f => f.paymentId as string),
  // Per-product SP fields (issue #136)
  ...SERVICE_FIELDS.filter(f => 'coverageId' in f).map(f => (f as { coverageId: string }).coverageId),
  ...SERVICE_FIELDS.filter(f => 'enabledId' in f).map(f => (f as { enabledId: string }).enabledId),
];

// Snapshot of field values at last save (or initial load).
export const savedSnapshot: Record<string, string> = {};

// DOM fields behind each global Config key, so the global PUT can carry only
// the keys whose fields changed since load/save (#539). The backend merges
// omitted keys over the stored row.
export const GLOBAL_CONFIG_FIELDS: ReadonlyArray<readonly [keyof api.Config, readonly string[]]> = [
  ['enabled_providers', ['provider-aws', 'provider-azure', 'provider-gcp']],
  ['notification_email', ['setting-notification-email']],
  ['auto_collect', ['setting-auto-collect']],
  ['collection_schedule', ['setting-collection-schedule']],
  ['default_term', ['setting-default-term']],
  ['default_payment', ['setting-default-payment']],
  ['default_coverage', ['setting-default-coverage']],
  ['notification_days_before', ['setting-notification-days']],
  ['grace_period_days', ['setting-grace-aws', 'setting-grace-azure', 'setting-grace-gcp']],
  ['recommendations_cache_stale_hours', ['setting-recs-stale-hours']],
  ['recommendations_lookback_days', ['setting-recs-lookback-days']],
  ['offering_class', ['setting-ec2-offering-class']],
  ['require_different_approver', ['setting-require-different-approver']],
  ['experimental_gcp_plan_term', ['setting-experimental-gcp-plan-term']],
];
