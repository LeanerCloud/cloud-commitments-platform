import * as api from '../api';
import { serviceLabel } from '../lib/service-labels';

// Backend plan type (as returned from API)
export interface BackendPlan {
  id: string;
  name: string;
  enabled: boolean;
  auto_purchase: boolean;
  max_step_commitment?: number | null;
  notification_days_before: number;
  services?: Record<string, {
    provider: string;
    service: string;
    enabled: boolean;
    term: number;
    payment: string;
    coverage: number;
    include_engines?: string[];
    exclude_engines?: string[];
    include_regions?: string[];
    exclude_regions?: string[];
    include_types?: string[];
    exclude_types?: string[];
    min_count?: number;
    // Recommendations a bound group is tied to (#1746); omitted on other entries.
    bound_count?: number;
  }>;
  ramp_schedule: {
    type: string;
    percent_per_step: number;
    step_interval_days: number;
    current_step: number;
    total_steps: number;
    // "bound_groups" marks a plan bound to selected recommendations (#1746).
    allocation_mode?: string;
  };
  next_execution_date?: string;
  // unassigned is true for legacy plans that have zero plan_accounts rows
  // (issue #973). The backend sets this flag when an account filter is
  // active so the frontend can bucket them under an "Unassigned" section.
  unassigned?: boolean;
  // Health score/factors are computed at read time on GET /plans (never
  // persisted) -- see computePlanHealth in internal/api/plan_health.go
  // (issue #340 follow-up). Optional so an older cached response served
  // during a partial deploy still renders the card cleanly; explicitly
  // null when the backend could not compute the score.
  health_score?: number | null;
  health_factors?: api.PlanHealthFactor[];
}

// Pretty label for a service slug used inside the plan card.
//
// SP slugs are abbreviated ("Compute SP") rather than spelled out
// ("Compute Savings Plans") so a multi-SP plan with 3-4 entries still
// fits in the summary line. Non-SP slugs pass through unchanged so
// existing single-service plans render exactly as before.
// Plans saved before #608 carry the modal's old per-provider option ids, which
// the backend's mapServiceSlug never accepted. Map them to the canonical slug
// so editing such a plan selects the right option instead of a blank one.
export const LEGACY_PLAN_SERVICE_IDS: Readonly<Record<string, string>> = {
  'azure/vm': 'compute',
  'azure/sql': 'relational-db',
  'azure/redis': 'cache',
  'gcp/cloudsql': 'relational-db',
  'gcp/memorystore': 'cache',
};

// Extract provider/service info from plan's services map.
//
// `service` is now a comma-separated list of all services covered by
// the plan, not just the first map entry. Pre-PR #123 a plan only ever
// had one service slug; post-split a plan targeting multiple SP plan
// types has up to four entries (savings-plans-{compute,ec2instance,
// sagemaker,database}) but the old "first entry wins" rendering hid
// all but one. See issue #131 for the bug; this fix shows them all.
//
// `term` and `coverage` continue to come from the first entry — they
// are plan-level today, not per-service, so picking any entry is
// correct. If the model ever differentiates per service, this needs
// to render the same way.
//
// `service` holds the raw slugs (the edit flow selects the service option by
// it); `serviceDisplay` holds the display names for the card (#1683).
type PlanInfo = {
  provider: string | null; service: string; serviceDisplay: string; term: number | null; coverage: number | null;
  // Every distinct provider, term and coverage of a bound plan's groups (#1746); a
  // plan that is not bound has only its first entry's, as before.
  providers: string[]; terms: number[]; coverages: number[]; boundCount: number;
};

// planMatchesProvider keeps a bound plan when any of its groups has the provider;
// other plans match on their first service entry only.
export function planMatchesProvider(plan: BackendPlan, provider: string): boolean {
  return extractPlanInfo(plan).providers.includes(provider);
}

export function extractPlanInfo(plan: BackendPlan): PlanInfo {
  const services = plan.services || {};
  const serviceValues = Object.values(services);
  const firstService = serviceValues[0];
  if (firstService) {
    const bound = plan.ramp_schedule?.allocation_mode === 'bound_groups';
    const distinct = <T,>(values: (T | null | undefined)[]): T[] => [...new Set(values.filter((v): v is T => !!v || v === 0))];
    const shown = bound ? serviceValues : [firstService];
    const serviceList = bound ? distinct(serviceValues.map(s => s.service)) : serviceValues.map(s => s.service || '—');
    const service = serviceList.join(', ');
    const serviceDisplay = serviceList.map(sv => (sv ? serviceLabel(sv, true) : '—')).join(', ');
    // H-4: never fabricate provider/term/coverage when absent.
    // Return null so callers can surface NO_VALUE/'Unknown' in the display
    // layer rather than silently committing to aws/3yr/80% defaults.
    return {
      provider: firstService.provider || null,
      service,
      serviceDisplay,
      term: firstService.term || null,
      coverage: firstService.coverage ?? null,
      providers: distinct(shown.map(s => s.provider)),
      terms: distinct(shown.map(s => s.term)),
      coverages: distinct(shown.map(s => s.coverage)),
      boundCount: bound ? serviceValues.reduce((n, s) => n + (s.bound_count ?? 0), 0) : 0,
    };
  }
  // No services at all: all fields are unknown.
  return { provider: null, service: '—', serviceDisplay: '—', term: null, coverage: null, providers: [], terms: [], coverages: [], boundCount: 0 };
}

// Returns true when the plan's next_execution_date is strictly before today.
export function isPlanOverdue(plan: BackendPlan): boolean {
  if (!plan.next_execution_date) return false;
  const nextDate = new Date(plan.next_execution_date);
  if (isNaN(nextDate.getTime())) return false;
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  return nextDate < today;
}

// Presets mirror config.PresetRampSchedules; any other stored ramp is shown as
// custom so Edit never rewrites it to a different schedule on Save (#1156).
export const PRESET_RAMPS: ReadonlyArray<{ value: string; label: string; type: string; percent: number; intervalDays: number }> = [
  { value: 'weekly-25pct', label: 'Weekly 25%', type: 'weekly', percent: 25, intervalDays: 7 },
  { value: 'monthly-10pct', label: 'Monthly 10%', type: 'monthly', percent: 10, intervalDays: 30 },
];

export function rampRadioValue(ramp: { type?: string; percent_per_step?: number; step_interval_days?: number }): string {
  if (ramp.type === 'immediate') return 'immediate';
  const preset = PRESET_RAMPS.find(p => p.type === ramp.type && p.percent === ramp.percent_per_step && p.intervalDays === ramp.step_interval_days);
  return preset ? preset.value : 'custom';
}
