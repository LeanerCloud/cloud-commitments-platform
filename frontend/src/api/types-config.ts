import type { PaymentOption, Provider } from './types';

// Config types
export interface Config {
  enabled_providers: Provider[];
  notification_email?: string;
  auto_collect: boolean;
  collection_schedule: string;
  notification_days_before: number;
  approval_required?: boolean;
  default_term: number;
  default_payment: PaymentOption;
  default_coverage: number;
  default_ramp_schedule?: string;
  // Per-provider grace-period window (days) during which recently-
  // purchased capacity is suppressed from the Recommendations view.
  // Keys: 'aws' / 'azure' / 'gcp'. A missing key defaults to 7 on
  // the backend. Explicit 0 disables the feature for that provider.
  grace_period_days?: Record<string, number>;
  // EC2 Reserved Instance offering class. "convertible" (default) allows
  // future exchanges for different families/sizes/OS; "standard" is
  // ~5% cheaper but locked to the exact instance type for the full term.
  offering_class?: 'convertible' | 'standard';
  ri_exchange_enabled?: boolean;
  ri_exchange_mode?: string;
  ri_exchange_utilization_threshold?: number;
  ri_exchange_max_per_exchange_usd?: number;
  ri_exchange_max_daily_usd?: number;
  ri_exchange_lookback_days?: number;
  // Age (hours) after which the recommendations cache triggers a background
  // stale-while-revalidate refresh. 0 disables automatic background refresh;
  // the cron scheduler and the manual Refresh button still work regardless.
  // Valid range: 0–8760 (up to one year). Default: 24.
  recommendations_cache_stale_hours?: number;
  // AWS Cost Explorer lookback window (days) for fresh recommendations.
  // Must be 7, 30, or 60 (AWS LookbackPeriodInDays enum). Default: 7.
  // GCP CUD Recommender has no equivalent parameter; applies to AWS only.
  recommendations_lookback_days?: number;
  // Global kill-switch for the commitment-laddering feature (issue #1336).
  // Default false. When true, per-account LadderConfig.enabled settings
  // determine whether the engine runs for that account.
  laddering_enabled?: boolean;
  // When true, a purchase execution cannot be approved by the same user who
  // created it -- a different person with approval rights must do so.
  // SOX / SOC2 segregation-of-duties control. Default: false.
  require_different_approver?: boolean;
  // EXPERIMENTAL, default false: take GCP Compute CUD terms from the
  // recommendation's root plan instead of defaulting to 1yr.
  experimental_gcp_plan_term?: boolean;
}

export interface ServiceConfig {
  provider: string;
  service: string;
  enabled: boolean;
  term: number;
  payment: string;
  coverage: number;
  ramp_schedule?: string;
  include_engines?: string[];
  exclude_engines?: string[];
  include_regions?: string[];
  exclude_regions?: string[];
  include_types?: string[];
  exclude_types?: string[];
  /** Minimum instance/node count a recommendation must carry to be surfaced
   *  (GUI equivalent of the CLI --min-count flag). 0 / undefined disables the
   *  filter. */
  min_count?: number;
  /** Read-only row stamp the server sets on every save (issue #8). */
  updated_at?: string;
}

/** Response from GET /api/info (unauthenticated). Only safe-to-expose fields. */
export interface PublicInfo {
  version: string;
  admin_exists: boolean;
}

/** Response from GET /api/info/deployment (AuthUser). Sensitive identifiers
 *  that must not be visible to unauthenticated callers (#633). */
export interface DeploymentInfo {
  /** AWS Console deep-link to the Secrets Manager secret holding the API key. */
  api_key_secret_url?: string;
  /** AWS account ID of the CUDly Lambda (from STS GetCallerIdentity). Present
   *  only on AWS-hosted deployments; omitted on Azure/GCP or when STS is
   *  unreachable. Used by formatAccountLabel to distinguish genuine
   *  ambient-credential executions from orphan rows (issue #608). */
  deployment_aws_account_id?: string;
}
