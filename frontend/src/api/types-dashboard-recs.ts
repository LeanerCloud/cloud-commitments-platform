import type { Provider } from './types';

// Dashboard types
export interface DashboardSummary {
  potential_monthly_savings: number;
  total_recommendations: number;
  active_commitments: number;
  committed_monthly: number;
  current_coverage: number;
  target_coverage: number;
  ytd_savings: number;
  by_service: Record<string, { potential_savings: number; current_savings: number }>;
}

export interface UpcomingPurchase {
  execution_id: string;
  plan_id: string;
  plan_name: string;
  scheduled_date: string;
  provider: string;
  // Set only for a bound plan: its distinct enabled group providers, since
  // provider is then "multiple" when the groups differ.
  providers?: string[];
  service: string;
  step_number: number;
  total_steps: number;
  estimated_savings: number;
  // false when the step carries no recommendations; estimated_savings is
  // then a placeholder, not data (platform#609). Optional so fixtures that
  // predate the field keep type-checking; only an explicit false is bare.
  has_recommendations?: boolean;
  // created_by_user_id is the UUID of the user who scheduled the
  // execution, propagated by the backend so the dashboard's "Cancel"
  // button can apply the same creator-scope ownership gate the Plans
  // page uses (issue #950). Optional because legacy / scheduler-tick
  // rows ship NULL here; the gate treats undefined as "not the current
  // user" and hides the button for non-update-any callers.
  created_by_user_id?: string;
}

// GET /api/dashboard/upcoming wraps the rows in an object (#1189). The Go
// handler builds the slice with `var upcoming []UpcomingPurchase`, so an
// empty result serializes as `purchases: null`, not `[]`.
export interface UpcomingPurchasesResponse {
  purchases: UpcomingPurchase[] | null;
}

// Recommendation types
export interface Recommendation {
  id: string;
  provider: string;
  service: string;
  region: string;
  resource_type: string;
  engine?: string;
  // Zonal EC2 reservation AZ name; absent for regional recs (issue #792).
  availability_zone?: string;
  // Opaque ServiceDetails payload from the backend (json.RawMessage).
  // The frontend treats this as a pass-through blob: it is stashed when
  // the GET /api/recommendations response is parsed and forwarded unchanged
  // in the POST /api/purchases/execute body so the backend can reconstruct
  // the correct typed *Details pointer (Platform, Tenancy, Scope, Engine
  // etc.) for each cloud service client. Absent on pre-#597 cached rows;
  // those fall back to zero-valued defaults on the backend. See #597, #453.
  details?: unknown;
  count: number;
  // recommended_count is the pre-scaling count this rec carried before the
  // bulk-purchase Capacity % slider scaled it down. Stamped onto the scaled
  // copy at submit time so the backend can verify capacity_percent against the
  // scaled count rather than trusting a decorative audit field (#647). Absent
  // on un-scaled / full-capacity / legacy recs, in which case the backend
  // skips the consistency check for that rec.
  recommended_count?: number;
  term: number;
  payment: string;
  upfront_cost: number;
  // null when the provider API did not return a monthly recurring breakdown.
  monthly_cost: number | null;
  savings: number;
  // Canonical on-demand monthly baseline straight from the cloud provider
  // (Azure CostWithNoReservedInstances, AWS EstimatedMonthlyOnDemandCost).
  // Optional/null when the provider didn't return a baseline; in that case
  // the frontend reconstructs on-demand from monthly_cost + savings +
  // amortized_upfront. When populated it's preferred — see #274.
  on_demand_cost?: number | null;
  selected: boolean;
  purchased: boolean;
  purchase_id?: string;
  error?: string;
  cloud_account_id?: string;
  // usage_history carries the last 7 daily RI-coverage percentages (0-100,
  // oldest-to-newest). Absent/null when the provider did not populate it
  // (non-AWS providers or pre-#239 cached rows).
  usage_history?: number[] | null;
  // vcpu / memory_gb surface the compute size of the recommended instance
  // type so the Capacity column can render "<vcpu> vCPU / <memory_gb> GB"
  // (#219). Emitted top-level by the backend (buildRecommendationsResponse
  // decodes the nested ComputeDetails and stamps them) only for compute recs
  // with a known size; absent for non-compute services, legacy rows, or
  // unknown sizes. formatCapacity renders absent/0 as "—".
  vcpu?: number | null;
  memory_gb?: number | null;
}

export interface RecommendationFilters {
  provider?: Provider | '';
  service?: string;
  region?: string;
  /**
   * Minimum absolute dollar savings per month. Sent to the API as
   * `min_savings_usd`. 0 or absent means no floor.
   *
   * IMPORTANT: this is a DOLLAR amount, not a percentage. The CLI equivalent
   * is `--min-savings-pct` which filters by percentage -- these are two
   * distinct concepts. Use `minSavingsPct` to filter by percentage.
   */
  minSavingsUsd?: number;
  /**
   * Minimum effective savings percentage (0-100 scale). Sent to the API as
   * `min_savings_pct`. 0 or absent means no floor.
   *
   * IMPORTANT: this is a PERCENTAGE (0-100), not a dollar amount. The CLI
   * `--min-savings-pct` flag uses the same semantics. Use `minSavingsUsd`
   * to filter by an absolute dollar floor.
   */
  minSavingsPct?: number;
  account_ids?: string[];
}
