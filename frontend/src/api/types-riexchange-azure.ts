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
