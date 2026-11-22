import * as state from '../state';
import type { CostPeriod } from '../state';

//
// COLUMN_DEFS — single source of truth for the recommendations table columns.
//
// Order here matches the rendered column order (left to right), excluding the
// leading checkbox column which is always visible and never sortable/filterable.
//
// Both the table header (<th> generation) and the data rows (<td> generation)
// derive from this array so adding a new column only requires one edit here.
//
// `kind` drives the column-filter popover: 'numeric' → text input with
// comparison operators; 'categorical' → checkbox list of distinct values.
//
// `label` is the canonical (monthly-period) header. The 3 period-varying
// cost columns (`savings`, `monthly_cost`, `on_demand_monthly`) are handled
// separately by `getColumnLabel` per-period, so their entry here is only
// used as the data-attribute / fallback label, not the rendered <th>.
//
export interface ColumnDef {
  key: state.RecommendationsColumnId;
  label: string;
  kind: 'numeric' | 'categorical' | 'visual';
  // Issue #480: direction applied on the first click of a previously-
  // unsorted column. Text columns and most numerics get 'asc' (A→Z, low →
  // high) per platform convention. Two exceptions stay 'desc': `savings`
  // (savings tables open "biggest wins first") and `on_demand_monthly`
  // (per QA, current behaviour reads correctly there). Subsequent
  // clicks on the active column still toggle desc <-> asc regardless of
  // this default.
  defaultSortDirection?: 'asc' | 'desc';
  // sortable defaults to true. Set to false for visual-only columns
  // (e.g. the usage_history sparkline) that have no meaningful sort order.
  // Visual columns also suppress the column-filter button.
  sortable?: boolean;
}

export const COLUMN_DEFS: readonly ColumnDef[] = [
  { key: 'provider',              label: 'Provider',          kind: 'categorical' },
  { key: 'account',               label: 'Account',           kind: 'categorical' },
  { key: 'service',               label: 'Service',           kind: 'categorical' },
  { key: 'resource_type',         label: 'Resource Type',     kind: 'categorical' },
  { key: 'capacity',              label: 'Capacity',          kind: 'categorical' },
  { key: 'region',                label: 'Region',            kind: 'categorical' },
  { key: 'count',                 label: 'Count',             kind: 'numeric'     },
  { key: 'term',                  label: 'Term',              kind: 'categorical' },
  { key: 'payment',               label: 'Payment',           kind: 'categorical' },
  { key: 'savings',               label: 'Monthly Savings',   kind: 'numeric',     defaultSortDirection: 'desc' },
  { key: 'upfront_cost',          label: 'Upfront Cost',      kind: 'numeric'     },
  { key: 'monthly_cost',          label: 'Monthly Cost',      kind: 'numeric'     },
  { key: 'on_demand_monthly',     label: 'On-Demand Monthly', kind: 'numeric',     defaultSortDirection: 'desc' },
  { key: 'effective_savings_pct', label: 'Effective %',       kind: 'numeric'     },
  // usage_history is a visual-only sparkline column (closes #239 Part 2).
  // sortable:false suppresses the sort header and column-filter button.
  { key: 'usage_history',         label: 'Coverage (7d)',     kind: 'visual', sortable: false },
];

// Static labels for columns whose header is period-invariant. Derived from
// COLUMN_DEFS so the source-of-truth still drives what's displayed; the
// 3 period-varying cost columns are filtered out and handled by
// getColumnLabel's switch instead.
const PERIOD_VARYING_COLUMNS = new Set<string>(['savings', 'monthly_cost', 'on_demand_monthly']);

const STATIC_COLUMN_LABELS: Record<string, string> = Object.fromEntries(
  COLUMN_DEFS
    .filter((c) => !PERIOD_VARYING_COLUMNS.has(c.key))
    .map((c) => [c.key, c.label]),
);

/**
 * Return the human-readable column header for `column` given the active
 * `period`. Cost-bearing columns (`savings`, `monthly_cost`,
 * `on_demand_monthly`) update their label to reflect the active period;
 * all other columns are period-invariant.
 */
export function getColumnLabel(column: string, period: CostPeriod): string {
  switch (column) {
    case 'savings': {
      const suffixes: Record<CostPeriod, string> = {
        hourly: 'Savings / hr', daily: 'Savings / day', monthly: 'Monthly Savings', yearly: 'Savings / yr',
      };
      return suffixes[period];
    }
    case 'monthly_cost': {
      const suffixes: Record<CostPeriod, string> = {
        hourly: 'Cost / hr', daily: 'Cost / day', monthly: 'Monthly Cost', yearly: 'Cost / yr',
      };
      return suffixes[period];
    }
    case 'on_demand_monthly': {
      // The base value is the reconstructed monthly on-demand baseline (#322);
      // scale the label to the same period as the displayed cell value so the
      // header stays consistent with the column's contents.
      const suffixes: Record<CostPeriod, string> = {
        hourly: 'On-Demand / hr', daily: 'On-Demand / day', monthly: 'On-Demand Monthly', yearly: 'On-Demand / yr',
      };
      return suffixes[period];
    }
    default:
      return STATIC_COLUMN_LABELS[column] ?? column;
  }
}

// Backward-compatible alias used by popover aria-labels and filter buttons.
// These always show the current-period label.
export function columnLabel(column: string): string {
  return getColumnLabel(column, state.getCostPeriod());
}

//
// Column-filter popover (portal pattern)
//
// The popover element lives appended to document.body so it survives
// renderRecommendationsList's table re-render (which does container.innerHTML
// = buildListMarkup, destroying anything inside <th>). Module-scope state
// tracks which column id is currently open; the trigger DOM node is re-found
// by `[data-column="..."]` on every render (anchor re-bind).
//
// The popover STRUCTURE is built once on open; STATE (.checked / .value) is
// re-synced on every anchor re-bind from the latest column-filter state, EXCEPT
// when the input is document.activeElement (mid-typing protection).
//

// Derived from COLUMN_DEFS — numeric columns get a text-input filter; categoricals
// get a checkbox-list filter.  Kept as a Set for O(1) membership tests.
export const NUMERIC_COLUMNS: ReadonlySet<state.RecommendationsColumnId> = new Set(
  COLUMN_DEFS.filter((c) => c.kind === 'numeric').map((c) => c.key),
);
