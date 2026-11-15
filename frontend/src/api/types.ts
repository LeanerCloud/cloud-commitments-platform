/**
 * Type definitions for CUDly API
 */

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
export * from './types-config';
export * from './types-purchases';
export * from './types-riexchange-azure';
