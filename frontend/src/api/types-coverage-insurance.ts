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

// ---------------------------------------------------------------------------
// GET /insurance/recommendations/{id} (ArcheraRecommendationComparison).
// Money is an exact decimal string or null (unknown, never zero); currency is
// always null (not inferred); status "unavailable" carries no figures.
// ---------------------------------------------------------------------------

export interface ArcheraRecommendationOffer {
  offer_id: string;
  provider: string;
  commitment_type: string;
  hourly_selected_commitment: string | null;
  native_duration_seconds: string | null;
  native_plan_type: string | null;
  native_instance_family: string | null;
  effective_contract_term: string | null;
  payment_option: string | null;
  lease_attached: boolean;
  monthly: ArcheraFinancials;
  upfront_one_time_cost: string | null;
}

export interface ArcheraRecommendationAlternative {
  offer_id: string;
  status: string;
  reason: string;
  native_identity_status: string;
  figures: ArcheraRecommendationOffer | null;
}

export interface ArcheraRecommendationComparison {
  recommendation_id: string;
  status: string;
  reason: string;
  amount_provenance: string;
  economic_status: string;
  fetched_at: string;
  currency: null;
  currency_note: string;
  comparable_delta: null;
  premium_included: boolean;
  current: ArcheraRecommendationOffer | null;
  candidates: ArcheraRecommendationAlternative[];
  non_gating_disclosure: string;
  sponsorship_disclosure: string;
}
