/**
 * Type definitions for CUDly API
 */

import type { PaymentOption, Provider } from './types-core';
import type { Recommendation } from './types-dashboard-recs';

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

// Purchase types

/**
 * PaymentAdjustment is one payment-option coercion notice returned by the
 * purchase-execute endpoint (#1503 follow-up). Mirrors the backend
 * PaymentAdjustment struct (internal/api/validation.go) field for field.
 *
 * Named (rather than inlined into PurchaseResult) so the presentation helper
 * that turns these into user-facing copy
 * (commitmentOptions.ts:formatPaymentAdjustmentNotice) can be typed and unit
 * tested against the same shape the API actually returns.
 */
export interface PaymentAdjustment {
  rec_index: number;
  provider: string;
  service: string;
  requested_payment_option: string;
  applied_payment_option: string;
  reason: string;
}

export interface PurchaseResult {
  execution_id: string;
  status: string;
  recommendation_count?: number;
  total_upfront_cost?: number;
  estimated_savings?: number;
  message?: string;
  // Whether the approval email was actually sent. When false, email_reason
  // carries a human-readable explanation (e.g. "no notification email set in
  // Settings → General") so the UI can tell the user to approve/cancel from
  // History instead of waiting for an inbox.
  email_sent?: boolean;
  email_reason?: string;
  // Resolved To address that received the approval email. Surfaced so the
  // post-submit toast can name the approver ("Approval request sent to
  // alice@acme.com") per the CR pass on PR #294 / issue #288. Absent when
  // recipient resolution itself failed (no approvers configured).
  approval_recipient?: string;
  // True when the request was handled via the direct-execute path (issue
  // #289). Absent (undefined) on the standard approval-required flow.
  direct_execute?: boolean;
  // True when this submit collapsed onto an earlier execution with the same
  // creator and recommendations (#644, #758). execution_id and status then
  // describe that earlier row, whatever mode this submit used.
  duplicate?: boolean;
  // Per-rec payment-option coercion notices (#1503 follow-up). Present only
  // when the backend normalized a requested payment option onto a token that
  // bills on a DIFFERENT schedule (e.g. Azure "partial-upfront" -> "monthly"):
  // each entry names what was requested, what was actually applied, and why,
  // so the UI can tell the user instead of silently changing the billing
  // schedule. Absent (never an empty array) when every option was already
  // canonical, and also when the backend only respelled a token into the
  // provider's own vocabulary for the same schedule (Azure "all-upfront" ->
  // "upfront") — that changes nothing the user pays, so warning about it
  // would just teach them to dismiss the notice that matters.
  payment_adjustments?: PaymentAdjustment[];
  results?: Array<{
    recommendation_id: string;
    status: string;
    error?: string;
  }>;
}

// PurchaseDetails matches the shape produced by backend
// buildPurchaseDetailsResponse (internal/api/handler_purchases_details.go).
// `recommendations` carries the per-rec snapshot that was captured at
// approval-request time so callers can render the deal without
// re-querying provider APIs. plan_id / plan_name / scheduled_date are
// only present when the execution belongs to a plan (direct-execute
// purchases from the recommendations page have plan_id="" and
// plan_name="").
export interface PurchaseDetails {
  execution_id: string;
  status: string;
  plan_id?: string;
  plan_name?: string;
  step_number?: number;
  scheduled_date?: string;
  total_upfront_cost: number;
  estimated_savings: number;
  recommendations: Recommendation[];
  notification_sent?: string;
  completed_at?: string;
  error?: string;
}

export interface PlannedPurchasesResponse {
  purchases: PlannedPurchase[];
}

export interface PlannedPurchase {
  id: string;
  plan_id: string;
  plan_name: string;
  scheduled_date: string;
  provider: Provider;
  // Set only for a bound plan: its distinct enabled group providers (see UpcomingPurchase).
  providers?: string[];
  service: string;
  resource_type: string;
  region: string;
  count: number;
  term: number;
  payment: string;
  estimated_savings: number;
  upfront_cost: number;
  // False for a step that carries no recommendations: count, resource_type,
  // upfront_cost and estimated_savings are then placeholders, not data
  // (platform#609).
  has_recommendations: boolean;
  // True when the figures come from the current matching recommendations, not
  // an attached step; the purchase recomputes them. estimate_note says why a
  // step has none. Both are omitted for callers without view:recommendations.
  estimate?: boolean;
  estimate_note?: string;
  status: 'pending' | 'notified' | 'paused' | 'running' | 'completed' | 'failed';
  step_number: number;
  total_steps: number;
  // created_by_user_id is the UUID of the user who scheduled the purchase.
  // The row action buttons are gated on creator-scope ownership (issue #950):
  // a non-creator without update-any:purchases sees no actionable buttons.
  // Omitted (undefined) for legacy rows with a NULL creator.
  created_by_user_id?: string;
}

// RI Exchange Types
export interface ConvertibleRI {
  reserved_instance_id: string;
  instance_type: string;
  availability_zone: string;
  instance_count: number;
  start: string;
  end: string;
  offering_type: string;
  fixed_price: number;
  usage_price: number;
  state: string;
  normalization_factor: number;
  // The AWS account the RI belongs to; absent when the API could not
  // resolve it (issue #1692).
  account_id?: string;
}

// ExchangeableAzureRI is one Azure VM reservation eligible for the
// cross-SKU/cross-region exchange flow, returned by
// GET /api/ri-exchange/azure-instances. Mirrors the Go
// azurecompute.ExchangeableReservation struct (issue #871).
export interface ExchangeableAzureRI {
  reservation_order_id: string;
  reservation_id: string;
  sku: string;
  quantity: number;
  region?: string;
  term?: string;
  expiry_date?: string;
  instance_flexibility: string;
  display_name?: string;
  /** ARM scope billed for this reservation, e.g. "/subscriptions/{id}" (issue #81). */
  billing_scope_id?: string;
}

// Azure RI exchange requests (issue #81). Mirror AzureExchangeSourceBody /
// AzureExchangeTargetBody in internal/api/handler_ri_exchange_azure_body.go. term is
// the ISO 8601 reservation term ("P1Y", "P3Y", "P5Y").
export interface AzureExchangeSource {
  reservation_id: string;
  quantity: number;
}

export interface AzureExchangeTarget {
  sku: string;
  location: string;
  term: string;
  quantity: number;
}

export interface AzureCompatibleOfferingsRequest {
  subscription_id: string;
  sources: AzureExchangeSource[];
  targets: AzureExchangeTarget[];
}

/** Mandatory guardrails: the backend refuses a fresh quote above the cap or in another currency. */
export interface AzureExecuteExchangeRequest extends AzureCompatibleOfferingsRequest {
  max_payment_due: string;
  currency: string;
}

// Amounts are null when Azure did not report them; never treat that as 0.
export interface AzureCompatibleOffering {
  sku: string;
  location: string;
  term: string;
  quantity: number;
  billing_currency_total: number | null;
  currency_code?: string;
}

export interface AzureExchangePreview {
  session_id: string;
  net_payable: number | null;
  net_payable_currency?: string;
  refunds_total: number | null;
  purchases_total: number | null;
  policy_errors?: string[];
}

export interface AzureCompatibleOfferingsResponse {
  offerings: AzureCompatibleOffering[] | null;
  preview: AzureExchangePreview | null;
}

export interface AzureExecuteExchangeResponse {
  session_id: string;
  status: string;
  net_payable: number | null;
  net_payable_currency?: string;
  refunds_total: number | null;
  purchases_total: number | null;
}

export interface RIUtilization {
  reserved_instance_id: string;
  utilization_percent: number;
  purchased_hours: number;
  total_actual_hours: number;
  unused_hours: number;
}

export interface OfferingOption {
  instance_type: string;
  offering_id: string;
  effective_monthly_cost: number;
  // ISO-4217 currency effective_monthly_cost is denominated in. Omitted
  // by the backend when unknown (pkg/exchange.OfferingOption).
  currency_code?: string;
}

// TargetOffering is one valid exchange target returned by
// GET /api/ri-exchange/target-offerings. The offering_id is the AWS
// ReservedInstancesOfferingId UUID that must be submitted in the
// targets[] exchange request.
export interface TargetOffering {
  offering_id: string;
  instance_type: string;
  offering_type: string;
  product_description: string;
  duration: number;
  fixed_price: number;
  usage_price: number;
  currency_code: string;
  scope: string;
  normalization_factor: number;
}

export interface ReshapeRecommendation {
  source_ri_id: string;
  source_instance_type: string;
  source_count: number;
  target_instance_type: string;
  target_count: number;
  alternative_targets?: OfferingOption[];
  utilization_percent: number;
  normalized_used: number;
  normalized_purchased: number;
  reason: string;
}

// ExchangeTarget is one entry in a multi-target exchange request.
// Matches the Go struct pkg/exchange.TargetConfig.
export interface ExchangeTarget {
  offering_id: string;
  count: number;
}

// ExchangeQuoteRequest shape: callers may supply either the new
// `targets[]` array (preferred) or the legacy singleton fields
// (`target_offering_id` + `target_count`). When `targets[]` is
// present, the singleton fields are ignored server-side. The
// backend's validateExecuteExchangeBody accepts either shape since
// commit 5eb274690.
export interface ExchangeQuoteRequest {
  ri_ids: string[];
  targets?: ExchangeTarget[];
  target_offering_id?: string;
  target_count?: number;
  region?: string;
}

export interface ExchangeQuoteSummary {
  IsValidExchange: boolean;
  ValidationFailureReason: string;
  CurrencyCode: string;
  PaymentDueRaw: string;
  SourceHourlyPriceRaw: string;
  SourceRemainingUpfrontRaw: string;
  SourceRemainingTotalRaw: string;
  TargetHourlyPriceRaw: string;
  TargetRemainingUpfrontRaw: string;
  TargetRemainingTotalRaw: string;
  OutputReservedInstancesExp?: string;
  // Region the quote was priced in; the backend always sets it. Execute
  // must send this same region back (ExchangeExecuteRequest.region).
  Region: string;
}

// ExchangeExecuteRequest: same `targets[]` vs legacy singleton
// semantics as ExchangeQuoteRequest. `max_payment_due_usd` is a
// TOTAL cap across all targets — AWS returns a single aggregated
// PaymentDue so spend-cap checking becomes a total when `targets[]`
// has multiple entries.
export interface ExchangeExecuteRequest {
  ri_ids: string[];
  targets?: ExchangeTarget[];
  target_offering_id?: string;
  target_count?: number;
  max_payment_due_usd: string;
  // Required: the backend rejects an execute without a region rather than
  // defaulting one, since exchanges are irreversible. Pass the quote's Region.
  region: string;
}

export interface ExchangeResult {
  exchange_id: string;
  // The execute response's quote carries no Region (only the quote endpoint does).
  quote: Omit<ExchangeQuoteSummary, "Region">;
}

// RI Exchange Config Types
export interface RIExchangeConfig {
  auto_exchange_enabled: boolean;
  mode: "manual" | "auto";
  utilization_threshold: number;
  max_payment_per_exchange_usd: number;
  max_payment_daily_usd: number;
  lookback_days: number;
}

export interface RIExchangeHistoryRecord {
  id: string;
  account_id: string;
  exchange_id: string;
  region: string;
  source_ri_ids: string[];
  source_instance_type: string;
  source_count: number;
  target_offering_id: string;
  target_instance_type: string;
  target_count: number;
  payment_due: string;
  status: string;
  mode: string;
  error?: string;
  created_at: string;
  updated_at: string;
  completed_at?: string;
  expires_at?: string;
  /** UUID of the session user who submitted this exchange (issue #300). */
  created_by_user_id?: string;
  /** Email of the session user who approved via the dashboard Approve button (issue #300). */
  approved_by?: string;
}

/** A direct (interactive) RI exchange, listed read-only in History (#1883). */
export interface RIExchangeDirectExecution {
  id: string;
  provider: string;
  status: string;
  region: string;
  cloud_account_id: string | null;
  provider_account_id: string | null;
  source_ids: string[];
  targets: unknown[];
  max_payment_due: string;
  max_payment_currency: string;
  net_payable: string | null;
  net_payable_currency: string;
  error_summary: string;
  executed_by: string;
  created_at: string;
  completed_at: string | null;
  resolved_status: string;
  resolved_at: string | null;
}

// Inventory & Coverage types (issue #340 deferred sub-task — Active commitments)
export interface InventoryCommitment {
  id: string;
  provider: string;
  account_id: string;
  account_name?: string;
  service: string;
  resource_type?: string;
  region: string;
  count: number;
  term_years: number;
  payment_option?: string;
  start_date: string;
  end_date: string;
  upfront_cost: number | null;
  /** null when the provider API did not return a monthly recurring breakdown; "$X.XX" when populated, "—" when null. */
  monthly_cost: number | null;
  estimated_savings: number;
  /**
   * Always "active" today — the backend filters expired rows before
   * responding. The field stays in the shape so a future "expiring
   * soon" sub-state can land without a breaking API change.
   */
  status: string;
}

// Coverage breakdown types (issue #754)

/**
 * One service row within a provider's coverage section.
 * coverage_pct is null when both covered_monthly and on_demand_monthly
 * are zero (no usage detected) -- do not coerce null to 0.
 */
export interface CoverageServiceRow {
  service: string;
  covered_monthly: number | null;
  on_demand_monthly: number;
  coverage_pct: number | null;
}

/**
 * Per-provider coverage section returned by GET /api/inventory/coverage.
 * services is null when the provider has no usage data. Render "No usage
 * detected" rather than an empty table in that case.
 */
export interface ProviderCoverageSection {
  provider: string;
  services: CoverageServiceRow[] | null;
  overall_coverage_pct: number | null;
}

/** Envelope returned by GET /api/inventory/coverage. */
export interface CoverageBreakdownResponse {
  providers: ProviderCoverageSection[];
}

// ---------------------------------------------------------------------------
// Archera comparison (GET /api/insurance/*). Mirrors internal/archera/dto.go
// field for field; money is an exact decimal string or null (unknown, never 0).
// ---------------------------------------------------------------------------

export interface InsuranceStatus {
  configured: boolean;
  missing: string[];
}

export interface ArcheraProductSupport {
  status: string;
  source?: string;
  evidence?: string;
  underwriting_allowance: string;
  customer_eligibility: string;
}

export interface ArcheraFinancials {
  monthly_730h_commitment_cost_total: string | null;
  monthly_730h_cloud_provider_cost: string | null;
  monthly_730h_archera_premium: string | null;
  monthly_730h_gross_savings: string | null;
  monthly_730h_net_savings: string | null;
  monthly_730h_covered_on_demand_cost: string | null;
}

export interface ArcheraTotals extends ArcheraFinancials {
  upfront_one_time_cost: string | null;
}

export interface ArcheraOfferDelta {
  monthly_730h_net_savings: string | null;
  upfront_one_time_cost: string | null;
  discount_rate: string | null;
  breakeven_days: string | null;
}

export interface ArcheraHypotheticalDelta {
  monthly_730h_net_savings: string | null;
  monthly_730h_commitment_cost: string | null;
  upfront_one_time_cost: string | null;
}

export interface ArcheraLineItem {
  line_item_id: string;
  reason: string;
  actual_term: string | null;
  actual_payment_option: string | null;
  actual_commitment_type: string | null;
}

export interface ArcheraHypothetical {
  contract_term: string | null;
  payment_option: string;
  totals: ArcheraTotals;
  delta_vs_current: ArcheraHypotheticalDelta;
  line_items: ArcheraLineItem[];
}

export interface ArcheraOffer {
  offer_id: string;
  is_current: boolean;
  provider: string;
  commitment_type: string;
  region: string | null;
  contract_term: string | null;
  payment_option: string | null;
  lease_attached: boolean;
  lease_menu_item_id: string | null;
  archera_offer_name?: string | null;
  archera_offer_name_note?: string;
  archera_product_support: ArcheraProductSupport;
  discount_rate: string | null;
  breakeven_days: string | null;
  monthly: ArcheraFinancials;
  upfront_one_time_cost: string | null;
  delta_vs_current: ArcheraOfferDelta;
}

export interface ArcheraRow {
  line_item_id: string;
  current: ArcheraOffer;
  candidates: ArcheraOffer[];
}

export interface ArcheraComparison {
  title: string;
  plan_id: string;
  fetched_at: string;
  currency: string | null;
  currency_note: string;
  premium_included: boolean;
  basis_note: string;
  delta_basis_note: string;
  current: ArcheraTotals;
  hypotheticals: ArcheraHypothetical[];
  rows: ArcheraRow[];
  non_gating_disclosure: string;
  sponsorship_disclosure: string;
}

export * from './types-core';
export * from './types-identity';
export * from './types-keys-savings';
export * from './types-dashboard-recs';
export * from './types-plans-history';
