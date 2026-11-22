import * as state from '../state';
import { formatDate, CURRENCY_DEFAULT_DIGITS } from '../utils';
import type { PlannedPurchase } from '../api';
import { applyColumnFilters as applyColumnFiltersLib } from '../lib/column-filters';
import { rowMatchesProvider } from '../lib/upcoming-scope';
import { formatPaymentOption, formatTermShort } from '../lib/table-format';
import { openColumnFilterPopover, sortedDistinctValues } from '../lib/column-filter-popover';
import { plansState } from './plansState';
import { renderPlannedPurchasesInternal } from '../plans';

// ---------------------------------------------------------------------------
// Per-column filter pipeline (issue #166 follow-up to #570).
//
// Mirrors the canonical recommendations.ts wiring: cell extractors map each
// row + column id to its raw value, numeric values are rounded to the cell's
// display precision so filter predicates match what the user sees.
// ---------------------------------------------------------------------------

function applyPlansColumnFilters(
  purchases: readonly PlannedPurchase[],
  filters: state.PlansColumnFilters,
): PlannedPurchase[] {
  const { provider: providerFilter, ...rest } = filters;
  const narrowed = applyColumnFiltersLib<PlannedPurchase, state.PlansColumnId>(
    purchases,
    rest,
    {
      categorical: categoricalCellValueForPlan,
      numeric: (p, col) => roundForDisplay(numericCellValueForPlan(p, col), displayPrecisionForPlan(col)),
    },
  );
  if (providerFilter?.kind !== 'set') return narrowed;
  const values = providerFilter.values;
  return narrowed.filter(p => values.some(v => rowMatchesProvider(p, v)));
}

function categoricalCellValueForPlan(p: PlannedPurchase, col: state.PlansColumnId): string {
  switch (col) {
    case 'plan_name':      return p.plan_name ?? '';
    // Keyed by local YYYY-MM-DD: same-day steps share one option and keys sort chronologically.
    case 'scheduled_date': return localDayKey(p.scheduled_date);
    case 'provider':       return p.provider ?? '';
    case 'service':        return p.service ?? '';
    case 'resource_type':  return p.resource_type ?? '';
    case 'term':           return p.term == null ? '' : String(p.term);
    case 'payment':        return p.payment ?? '';
    case 'status':         return p.status ?? '';
    // Numeric columns shouldn't reach this branch; return empty for type-safety.
    case 'count':
    case 'upfront_cost':
    case 'estimated_savings':
      return '';
  }
}

function localDayKey(date: string | null | undefined): string {
  if (!date) return '';
  if (/^\d{4}-\d{2}-\d{2}$/.test(date)) return date;
  const d = new Date(date);
  if (isNaN(d.getTime())) return '';
  const pad = (n: number): string => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

function numericCellValueForPlan(p: PlannedPurchase, col: state.PlansColumnId): number {
  switch (col) {
    case 'count':              return p.count ?? 0;
    case 'upfront_cost':       return p.upfront_cost ?? Number.NaN;
    case 'estimated_savings':  return p.estimated_savings ?? Number.NaN;
    case 'plan_name':
    case 'scheduled_date':
    case 'provider':
    case 'service':
    case 'resource_type':
    case 'term':
    case 'payment':
    case 'status':
      return Number.NaN;
  }
}

// Issue #484 parity: filter predicates compare against the rounded display
// value so "exact-match" filters work for rows whose raw value rounds to the
// typed value. The planned-purchases table renders count as integer and
// currency cells via formatCurrency (CURRENCY_DEFAULT_DIGITS = 0).
function displayPrecisionForPlan(col: state.PlansColumnId): number {
  switch (col) {
    case 'count':
      return 0;
    case 'upfront_cost':
    case 'estimated_savings':
      return CURRENCY_DEFAULT_DIGITS;
    case 'plan_name':
    case 'scheduled_date':
    case 'provider':
    case 'service':
    case 'resource_type':
    case 'term':
    case 'payment':
    case 'status':
      return CURRENCY_DEFAULT_DIGITS;
  }
}

function roundForDisplay(n: number, precision: number): number {
  if (!Number.isFinite(n)) return n;
  return Number(n.toFixed(precision));
}

// ---------------------------------------------------------------------------
// Plans column-filter popover. The lifecycle (portal DOM, positioning,
// outside click / Escape, re-sync after re-render) lives in the shared
// lib/column-filter-popover.ts (issue #18); this table only supplies its
// column metadata and state accessors.
// ---------------------------------------------------------------------------

const PLANS_POPOVER_SCOPE = 'plans';

const PLANS_NUMERIC_COLUMNS: ReadonlySet<state.PlansColumnId> = new Set<state.PlansColumnId>([
  'count', 'upfront_cost', 'estimated_savings',
]);

// A bound plan spanning providers has provider "multiple" and lists its real
// providers (#1746); the provider options are those providers, never "multiple".
function plansProviderOptions(purchases: readonly PlannedPurchase[]): string[] {
  return sortedDistinctValues(
    purchases.flatMap(p => (p.providers && p.providers.length > 0 ? p.providers : [p.provider ?? ''])),
    (value) => value,
  );
}

function plansColumnLabel(column: state.PlansColumnId): string {
  switch (column) {
    case 'plan_name':          return 'Plan';
    case 'scheduled_date':     return 'Scheduled Date';
    case 'provider':           return 'Provider';
    case 'service':            return 'Service';
    case 'resource_type':      return 'Resource Type';
    case 'term':               return 'Term';
    case 'payment':            return 'Payment';
    case 'status':             return 'Status';
    case 'count':              return 'Count';
    case 'upfront_cost':       return 'Upfront';
    case 'estimated_savings':  return 'Est. Savings';
  }
}

function plansCategoricalDisplayLabel(column: state.PlansColumnId, value: string): string {
  if (value === '') return '(empty)';
  if (column === 'scheduled_date') return formatDate(value);
  if (column === 'term') {
    const n = Number(value);
    return Number.isFinite(n) && n > 0 ? formatTermShort(n) : value;
  }
  if (column === 'payment') {
    return formatPaymentOption(value);
  }
  if (column === 'provider') {
    return value.toUpperCase();
  }
  return value;
}

function getPlansColumnTriggerButton(column: state.PlansColumnId): HTMLButtonElement | null {
  return document.querySelector<HTMLButtonElement>(
    `#planned-purchases-list th .column-filter-btn[data-column="${column}"]`,
  );
}

export function openPlansColumnPopover(column: state.PlansColumnId, anchor: HTMLElement): void {
  openColumnFilterPopover({
    scope: PLANS_POPOVER_SCOPE,
    column,
    label: plansColumnLabel(column),
    kind: PLANS_NUMERIC_COLUMNS.has(column) ? 'numeric' : 'categorical',
    distinctValues: column === 'provider'
      ? plansProviderOptions(plansState.lastLoadedPurchases)
      : sortedDistinctValues(plansState.lastLoadedPurchases, (p) => categoricalCellValueForPlan(p, column)),
    displayLabel: (value) => plansCategoricalDisplayLabel(column, value),
    getFilter: () => state.getPlansColumnFilters()[column],
    onCommit: (filter) => {
      state.setPlansColumnFilter(column, filter);
      rerenderPlannedPurchases();
    },
    findTrigger: () => getPlansColumnTriggerButton(column),
  }, anchor);
}

function rerenderPlannedPurchases(): void {
  renderPlannedPurchasesInternal();
}
