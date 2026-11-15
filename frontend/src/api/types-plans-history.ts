import type { Provider, ServiceConfig } from './types';

// PlanFilters are the query parameters accepted by the GET /api/plans endpoint.
export interface PlanFilters {
  account_ids?: string[];
}

// Plan types
export interface PlanRampSchedule {
  type: string;
  percent_per_step: number;
  step_interval_days: number;
  current_step: number;
  total_steps: number;
  start_date?: string;
}

export interface Plan {
  id: string;
  name: string;
  enabled: boolean;
  auto_purchase: boolean;
  max_step_commitment?: number | null;
  notification_days_before: number;
  services: Record<string, ServiceConfig>;
  ramp_schedule: PlanRampSchedule;
  created_at: string;
  updated_at: string;
  next_execution_date?: string;
  last_execution_date?: string;
  // unassigned is true for legacy plans that have zero plan_accounts rows.
  // Such plans are surfaced under an "Unassigned" section in the Plans UI
  // so operators can find and re-scope them (issue #973).
  unassigned?: boolean;
  // Health score/factors are computed at read time on GET /plans (never
  // persisted) by computePlanHealth server-side (issue #340 follow-up).
  // Optional so an older cached response served during a partial deploy
  // still renders the card cleanly -- the badge just doesn't appear.
  // Explicitly null when the backend could not compute a score; clients
  // must render that as "unknown" rather than substituting a default.
  health_score?: number | null;
  health_factors?: PlanHealthFactor[];
}

// PlanHealthFactor is one penalty subtracted from a plan's health score
// (issue #340 follow-up). `code` is a fixed vocabulary from the backend
// (see internal/api/plan_health.go); `note` is a human-readable reason
// suitable for the health-badge tooltip.
export interface PlanHealthFactor {
  code: string;
  penalty: number;
  note: string;
}

export interface PlanGroupRequest {
  provider: string;
  service: string;
  term?: number;
  payment?: string;
  coverage?: number;
  max_step_commitment?: number;
  recommendation_ids: string[];
}

export interface CreatePlanRequest {
  name: string;
  enabled: boolean;
  auto_purchase: boolean;
  max_step_commitment?: number | null;
  notification_days_before: number;
  services: Record<string, ServiceConfig>;
  ramp_schedule: PlanRampSchedule;
  // Required server-side (universal-plans fix): a plan must be tied to at
  // least one cloud account. The frontend bundles the selected account IDs
  // here so the backend receives plan creation + account assignment in a
  // single request and can validate atomically.
  target_accounts: string[];
}

// History types
export interface PurchaseHistory {
  account_id: string;
  purchase_id: string;
  timestamp: string;
  provider: string;
  service: string;
  region: string;
  resource_type: string;
  count: number;
  term: number;
  payment: string;
  upfront_cost: number | null;
  monthly_cost: number | null;
  estimated_savings: number;
  plan_id?: string;
  plan_name?: string;
  ramp_step?: number;
  cloud_account_id?: string;
}

export interface HistoryFilters {
  start?: string;
  end?: string;
  provider?: Provider;
  planId?: string;
  account_ids?: string[];
  /** next_cursor from the previous page, to fetch the rows after it. */
  cursor?: string;
}
