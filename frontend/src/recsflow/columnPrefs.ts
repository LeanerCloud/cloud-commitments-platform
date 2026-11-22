import * as state from '../state';
import { recsState } from './recsState';
import { type ColumnDef, COLUMN_DEFS } from './columnDefs';

//
// Column visibility — localStorage persistence (issue #318)
//

// TOGGLEABLE_COLUMNS — the subset of COLUMN_DEFS whose visibility can be toggled.
// Provider, Account, Service, and Resource Type are "cell identity anchors" on
// multi-variant summary rows and are always visible in v1.
export const TOGGLEABLE_COLUMNS: readonly ColumnDef[] = COLUMN_DEFS.filter(
  (c) => !(['provider', 'account', 'service', 'resource_type'] as string[]).includes(c.key),
);

export const TOGGLEABLE_COLUMN_KEYS = new Set<state.RecommendationsColumnId>(
  TOGGLEABLE_COLUMNS.map((c) => c.key),
);

const COLUMN_VISIBILITY_LS_KEY = 'cudly.recs.columnVisibility.v1';

const COLUMN_VISIBILITY_SCHEMA_VERSION = 1;

interface ColumnVisibilitySchema {
  schemaVersion: number;
  hidden: string[];
}

// Columns hidden until the user picks their own set in the Columns menu.
// With all sixteen columns the table is ~1700px wide, so at a 1440px
// window Monthly Savings and everything after it sat behind the sideways
// scroll. These five are the least needed to choose a commitment: capacity
// restates the resource type, monthly cost and on-demand monthly are the
// inputs behind Monthly Savings, Effective % is savings over on-demand, and
// the 7-day coverage sparkline is AWS-only. All stay one click away.
export const DEFAULT_HIDDEN_COLUMNS: ReadonlySet<state.RecommendationsColumnId> = new Set<state.RecommendationsColumnId>([
  'capacity', 'monthly_cost', 'on_demand_monthly', 'effective_savings_pct', 'usage_history',
]);

/**
 * Load hidden columns from localStorage. A saved choice (even "show all")
 * wins; with nothing saved, or an unreadable entry, the defaults above
 * apply. Exported for tests.
 */
export function loadColumnVisibility(): Set<state.RecommendationsColumnId> {
  try {
    const raw = localStorage.getItem(COLUMN_VISIBILITY_LS_KEY);
    if (!raw) return new Set(DEFAULT_HIDDEN_COLUMNS);
    const parsed = JSON.parse(raw) as Partial<ColumnVisibilitySchema>;
    if (parsed.schemaVersion !== COLUMN_VISIBILITY_SCHEMA_VERSION) return new Set(DEFAULT_HIDDEN_COLUMNS);
    if (!Array.isArray(parsed.hidden)) return new Set(DEFAULT_HIDDEN_COLUMNS);
    // Whitelist: only accept known toggleable column keys so stale/unknown
    // values from future versions are silently dropped.
    const valid = parsed.hidden.filter(
      (k): k is state.RecommendationsColumnId => TOGGLEABLE_COLUMN_KEYS.has(k as state.RecommendationsColumnId),
    );
    return new Set(valid);
  } catch {
    return new Set(DEFAULT_HIDDEN_COLUMNS);
  }
}

/** Persist hidden columns to localStorage. Exported for tests. */
export function saveColumnVisibility(hidden: ReadonlySet<state.RecommendationsColumnId>): void {
  try {
    const payload: ColumnVisibilitySchema = {
      schemaVersion: COLUMN_VISIBILITY_SCHEMA_VERSION,
      hidden: Array.from(hidden),
    };
    localStorage.setItem(COLUMN_VISIBILITY_LS_KEY, JSON.stringify(payload));
  } catch {
    // Private-browsing / quota-exceeded — non-fatal.
  }
}

//
// Column filters — localStorage persistence (issue #163)
//

const COLUMN_FILTERS_LS_KEY = 'cudly.recs.columnFilters.v1';

const COLUMN_FILTERS_SCHEMA_VERSION = 1;

// Full set of valid column ids, used as an allowlist when loading from
// localStorage so stale or hand-edited keys are silently dropped.
const VALID_COLUMN_IDS = new Set<state.RecommendationsColumnId>(
  COLUMN_DEFS.filter((c) => c.kind !== 'visual').map((c) => c.key),
);

interface ColumnFiltersSchema {
  schemaVersion: number;
  filters: Record<string, state.RecommendationsColumnFilter>;
}

/** Load column filter state from localStorage. Returns empty object on any error. Exported for tests. */
export function loadColumnFilters(): state.RecommendationsColumnFilters {
  try {
    const raw = localStorage.getItem(COLUMN_FILTERS_LS_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw) as Partial<ColumnFiltersSchema>;
    if (parsed.schemaVersion !== COLUMN_FILTERS_SCHEMA_VERSION) return {};
    if (!parsed.filters || typeof parsed.filters !== 'object' || Array.isArray(parsed.filters)) return {};
    const result: state.RecommendationsColumnFilters = {};
    for (const [key, value] of Object.entries(parsed.filters)) {
      // Drop unknown column ids (stale after a column rename or deletion).
      if (!VALID_COLUMN_IDS.has(key as state.RecommendationsColumnId)) continue;
      // Validate filter shape: must be kind:'set' with string[] or kind:'expr' with string.
      if (value && value.kind === 'set' && Array.isArray(value.values)
          && value.values.every((v) => typeof v === 'string')) {
        result[key as state.RecommendationsColumnId] = { kind: 'set', values: value.values };
      } else if (value && value.kind === 'expr' && typeof value.expr === 'string' && value.expr !== '') {
        result[key as state.RecommendationsColumnId] = { kind: 'expr', expr: value.expr };
      }
      // Anything else (malformed, hand-edited) is silently dropped.
    }
    return result;
  } catch {
    return {};
  }
}

/** Persist column filter state to localStorage. Exported for tests. */
export function saveColumnFilters(filters: state.RecommendationsColumnFilters): void {
  try {
    const payload: ColumnFiltersSchema = {
      schemaVersion: COLUMN_FILTERS_SCHEMA_VERSION,
      filters: filters as Record<string, state.RecommendationsColumnFilter>,
    };
    localStorage.setItem(COLUMN_FILTERS_LS_KEY, JSON.stringify(payload));
  } catch {
    // Private-browsing / quota-exceeded — non-fatal.
  }
}

/**
 * Reset column-filter seeded state. Exported for tests only — not part of
 * the public API. Call in beforeEach to ensure tests don't share seeding state.
 */
export function resetColumnFiltersState(): void {
  recsState.columnFiltersSeeded = false;
  state.clearAllRecommendationsColumnFilters();
}

/**
 * Reset column-visibility seeded state. Exported for tests only — not part of
 * the public API. Call in beforeEach to ensure tests don't share seeding state.
 */
export function resetColumnVisibilityState(): void {
  recsState.columnVisibilitySeeded = false;
  state.setHiddenColumns(new Set());
}

/** Returns the subset of COLUMN_DEFS that are currently visible. */
export function visibleColumns(): readonly ColumnDef[] {
  const hidden = state.getHiddenColumns();
  if (hidden.size === 0) return COLUMN_DEFS;
  return COLUMN_DEFS.filter((c) => !hidden.has(c.key));
}
