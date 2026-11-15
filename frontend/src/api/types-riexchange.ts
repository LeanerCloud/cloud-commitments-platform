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
