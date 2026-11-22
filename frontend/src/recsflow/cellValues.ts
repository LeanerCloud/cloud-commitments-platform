import * as state from '../state';
import type { CostPeriod } from '../state';
import { CURRENCY_DEFAULT_DIGITS } from '../utils';
import { serviceLabel } from '../lib/service-labels';
import type { LocalRecommendation } from '../types';
import { applyColumnFilters as applyColumnFiltersLib } from '../lib/column-filters';
import { formatPaymentOption, formatTermShort } from '../lib/table-format';
import { recsState } from './recsState';
import { providerDisplayName } from './shared';
import { displaySavingsPct, onDemandMonthly, scaleCost, PERIOD_DECIMALS } from './money';

// sortedRecommendations was removed in issues #225/#226: group-level sorting
// via groupsInSortOrder() supersedes the flat-list sort. The same
// SORTABLE_NUMERIC_COLUMNS / SORTABLE_STRING_COLUMNS maps are reused there.

// parseNumericFilter and ParsedNumericFilter are now in lib/column-filters.ts
// (issue #166 extraction). Imported at the top of this file and re-exported
// so existing consumers that import from recommendations.ts keep working.

// Apply the per-column filters to a rec list. Routes through the shared
// generic applyColumnFilters from lib/column-filters (issue #166/#570).
//
// ANDs all column filters together. Categorical: row passes iff its column
// value (string-form, empty/null mapped to "") is in `values`. Numeric: row
// passes iff parseNumericFilter(expr).predicate accepts the value (skipped if
// parse failed — the popover's inline error tells the user).
//
// Account uses cloud_account_id for matching; Term uses String(r.term).
// All other categorical columns compare on the underlying string field.
//
// Issue #484: numeric predicates compare against the rounded display value so
// exact-match ("123.45") works for rows whose raw value rounds to the typed
// value, and ">N" / "<N" / "N..M" all behave consistently with what the user
// sees in the cell.
export function applyColumnFilters(
  recs: readonly LocalRecommendation[],
  filters: state.RecommendationsColumnFilters,
): LocalRecommendation[] {
  const period = state.getCostPeriod();
  return applyColumnFiltersLib<LocalRecommendation, state.RecommendationsColumnId>(
    recs,
    filters,
    {
      categorical: categoricalCellValue,
      numeric: (r, col) => roundForDisplay(numericCellValue(r, col), displayPrecision(col, period)),
    },
  );
}

// formatCapacity renders the VCPU+MemoryGB pair from a ComputeDetails rec.
// Mirrors the Go-side ComputeDetails.GetDetailDescription format:
// "<vcpu> vCPU / <memoryGB> GB". Returns null when either field is absent or
// zero so callers can render a dash rather than a misleading "0 vCPU / 0 GB".
export function formatCapacity(vcpu: number | null | undefined, memoryGB: number | null | undefined): string | null {
  if (!vcpu || !memoryGB) return null;
  // String(n) trims trailing zeros for whole numbers (16, not 16.0)
  // and preserves fractional precision (0.5), matching Go's %g format.
  return `${vcpu} vCPU / ${String(memoryGB)} GB`;
}

function categoricalCellValue(r: LocalRecommendation, col: state.RecommendationsColumnId): string {
  switch (col) {
    case 'provider':       return r.provider ?? '';
    case 'account':        return r.cloud_account_id ?? '';
    case 'service':        return r.service ?? '';
    case 'resource_type':  return r.resource_type ?? '';
    case 'capacity':       return formatCapacity(r.vcpu, r.memory_gb) ?? '';
    case 'region':         return r.region ?? '';
    case 'term':           return r.term == null ? '' : String(r.term);
    case 'payment':        return r.payment ?? '';
    // Numeric / visual columns shouldn't reach this branch; return empty for type-safety.
    case 'count':
    case 'savings':
    case 'upfront_cost':
    case 'monthly_cost':
    case 'on_demand_monthly':
    case 'effective_savings_pct':
    case 'usage_history':           return '';
  }
}

function numericCellValue(r: LocalRecommendation, col: state.RecommendationsColumnId): number {
  // issue #319: savings and monthly_cost filter predicates operate on the
  // scaled (displayed) value so a "< $1" filter at hourly does the right thing.
  const period = state.getCostPeriod();
  switch (col) {
    case 'count':                return r.count ?? 0;
    // M-2: return NaN for null savings/upfront_cost so numeric filter
    // predicates (e.g. "savings = 0") don't match rows where the field is
    // simply absent rather than genuinely zero. Consistent with monthly_cost
    // and on_demand_monthly which already use NaN for the same reason.
    case 'savings':              return scaleCost(r.savings, period) ?? Number.NaN;
    case 'upfront_cost':         return r.upfront_cost ?? Number.NaN;
    // Return NaN for null monthly_cost so numeric filter predicates (e.g. "= 0")
    // don't match rows where the provider simply didn't report a monthly cost.
    case 'monthly_cost':         return scaleCost(r.monthly_cost, period) ?? Number.NaN;
    // Return NaN for null on_demand_monthly (missing or zero on_demand_cost — see
    // onDemandMonthly() for the contract) so any numeric predicate returns false
    // rather than coincidentally matching 0. Scale to the active period so a
    // numeric filter targets what the user sees.
    case 'on_demand_monthly':    return scaleCost(onDemandMonthly(r), period) ?? Number.NaN;
    // Return NaN for null effective_savings_pct so any numeric predicate
    // returns false rather than coincidentally matching 0. Filter on the
    // displayed value (provider % preferred) so typing the visible number
    // matches the row.
    case 'effective_savings_pct': return displaySavingsPct(r) ?? Number.NaN;
    // Categorical / visual columns shouldn't reach this branch; return NaN so any
    // numeric predicate returns false rather than coincidentally matching 0.
    case 'provider':
    case 'account':
    case 'service':
    case 'resource_type':
    case 'capacity':
    case 'region':
    case 'term':
    case 'payment':
    case 'usage_history': return Number.NaN;
  }
}

// Issue #484: number of decimal places the cell renders with for the active
// period. Filter predicates compare against this rounded value so a user
// typing the value they see (e.g. "123.45") matches the row that visibly
// shows that value, regardless of how many decimals the raw backend value
// has. Mirrors the precision used by formatCostForPeriod / formatCurrency /
// pctText so display and filter logic stay in sync.
//
// For currency columns we deliberately reuse `CURRENCY_DEFAULT_DIGITS` from
// utils.ts rather than hard-coding `0`: that constant is the single source
// of truth for `formatCurrency`'s default fraction digits, so if the
// dashboard ever switches to a 2-decimal default the filter precision will
// follow automatically instead of silently diverging (the bug #484 was
// meant to close).
export function displayPrecision(col: state.RecommendationsColumnId, period: CostPeriod): number {
  switch (col) {
    case 'count':
      return 0;
    case 'effective_savings_pct':
      // Header shows pct.toFixed(1): 1 decimal place.
      return 1;
    case 'savings':
    case 'monthly_cost':
    case 'on_demand_monthly':
      // formatCostForPeriod uses formatCurrency (CURRENCY_DEFAULT_DIGITS)
      // for monthly and toFixed(PERIOD_DECIMALS[period]) otherwise.
      return period === 'monthly' ? CURRENCY_DEFAULT_DIGITS : PERIOD_DECIMALS[period];
    case 'upfront_cost':
      // Always formatted via formatCurrency with default digits.
      return CURRENCY_DEFAULT_DIGITS;
    // Categorical / visual columns never reach the numeric filter path; default
    // is irrelevant but match formatCurrency's default-digit count for safety.
    case 'provider':
    case 'account':
    case 'service':
    case 'resource_type':
    case 'capacity':
    case 'region':
    case 'term':
    case 'payment':
    case 'usage_history':
      return CURRENCY_DEFAULT_DIGITS;
  }
}

// Issue #484: round `n` to `precision` decimals using the same half-up
// behaviour as Number.prototype.toFixed (the rendering path used by
// formatCurrency / formatCostForPeriod / toFixed in pctText). Returns NaN
// unchanged so missing data still fails every predicate (preserves the
// "NaN-as-missing" contract from numericCellValue).
function roundForDisplay(n: number, precision: number): number {
  if (!Number.isFinite(n)) return n;
  return Number(n.toFixed(precision));
}

export function distinctValuesForColumn(
  recs: readonly LocalRecommendation[],
  column: state.RecommendationsColumnId,
  // Values to include regardless of what appears in `recs` (used so the
  // currently-active filter selection for the column being edited remains
  // visible even when cross-column filtering would otherwise hide it).
  alwaysInclude?: ReadonlySet<string>,
): string[] {
  // Numeric columns don't get a checkbox list, but we still call this for
  // categorical columns only.
  const seen = new Set<string>();
  for (const r of recs) {
    seen.add(categoricalCellValue(r, column));
  }
  if (alwaysInclude) {
    for (const v of alwaysInclude) seen.add(v);
  }
  return Array.from(seen).sort((a, b) => {
    if (a === '' && b !== '') return -1; // (empty) first
    if (a !== '' && b === '') return 1;
    return a.localeCompare(b);
  });
}

export function categoricalDisplayLabel(
  column: state.RecommendationsColumnId,
  value: string,
): string {
  if (value === '') return '(empty)';
  if (column === 'account') {
    return recsState.accountNamesCache.get(value) || value;
  }
  if (column === 'term') {
    const n = Number(value);
    return n > 0 ? formatTermShort(n) : value;
  }
  if (column === 'payment') {
    return formatPaymentOption(value);
  }
  if (column === 'provider') {
    return providerDisplayName(value);
  }
  if (column === 'service') {
    return serviceLabel(value);
  }
  return value;
}
