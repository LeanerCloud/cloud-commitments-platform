import * as state from '../state';
import { iconSvg } from '../icons';
import type { LocalRecommendation } from '../types';
import { ownValue } from '../lib/own-property';
import { recsState } from './recsState';
import { displaySavingsPct, onDemandMonthly, scaleCost } from './money';
import { cellSummary } from './ranges';
import { COLUMN_DEFS } from './columnDefs';
import { PAYMENT_ORDER, sortVariantsInCell } from './groups';

// Comparator extractors per column. Numeric columns return numbers
// (subtraction-based sort); string columns return strings (localeCompare-based
// sort). Bundle B extended this with the string columns so every visible data
// column is sortable.
//
// issue #319: `savings` and `monthly_cost` extractors are now period-aware —
// they scale by the active cost period so sort order reflects the displayed
// value. POSITIVE_INFINITY sentinel for null values is preserved; scaling
// Infinity by any finite factor still yields Infinity, so the "unknowns to
// the bottom" behaviour is unchanged across periods.
const SORTABLE_NUMERIC_COLUMNS: Record<string, (r: LocalRecommendation) => number> = {
  savings: (r) => {
    const period = state.getCostPeriod();
    return scaleCost(r.savings, period) ?? Number.POSITIVE_INFINITY;
  },
  upfront_cost: (r) => r.upfront_cost,
  // null monthly_cost means "data not provided by the provider API".
  // Use POSITIVE_INFINITY so unknown rows sort to the bottom in ascending
  // order (de-emphasised) and don't conflate with rows that have an explicit
  // $0 recurring charge (e.g. all-upfront commitments).
  monthly_cost: (r) => {
    const period = state.getCostPeriod();
    return scaleCost(r.monthly_cost, period) ?? Number.POSITIVE_INFINITY;
  },
  // onDemandMonthly returns null when the provider didn't report
  // on_demand_cost (or reported 0). POSITIVE_INFINITY is the null sentinel;
  // groupsInSortOrder keeps nullish rows last in both ascending and descending
  // sorts. The base value is monthly; scale it to the selected display period
  // so sorting matches what the user sees in the column.
  on_demand_monthly: (r) => {
    const period = state.getCostPeriod();
    return scaleCost(onDemandMonthly(r), period) ?? Number.POSITIVE_INFINITY;
  },
  // displaySavingsPct prefers the provider-authoritative % and falls back to
  // effectiveSavingsPct; it returns null for term=0 / on_demand=0 / null
  // monthly_cost when no provider % is present. POSITIVE_INFINITY places null
  // rows at the bottom in ascending order and at the top in descending: the
  // least surprising behaviour for a savings column where "no data" rows
  // should be de-emphasised. Sorting on the displayed value keeps the order
  // consistent with what the column shows.
  effective_savings_pct: (r) => displaySavingsPct(r) ?? Number.POSITIVE_INFINITY,
  count: (r) => r.count,
  term: (r) => r.term,
};

const SORTABLE_STRING_COLUMNS: Record<string, (r: LocalRecommendation) => string> = {
  provider: (r) => r.provider ?? '',
  account: (r) => recsState.accountNamesCache.get(r.cloud_account_id ?? '') ?? r.cloud_account_id ?? '',
  service: (r) => r.service ?? '',
  resource_type: (r) => r.resource_type ?? '',
  region: (r) => r.region ?? '',
  payment: (r) => r.payment ?? '',
};

/**
 * Per-column cell-score helper: collapse a cell's variants to ONE deterministic
 * sort key per (column, cell) pair. Closes #494 - the previous comparator used
 * `Math.max(...va.map(numericKey))` for every numeric column, which collapses
 * to the same value across all multi-variant cells after PR #195's per-(term,
 * payment) fan-out (every cell has both 1yr and 3yr variants → `Math.max` always
 * = 3 for `term`; every cell has at least one null monthly_cost or null pct →
 * `Math.max` always = POSITIVE_INFINITY) and produces apparently-random row
 * order.
 *
 * Strategy: for each affected column, pick the per-cell score that matches what
 * the user sees in the rendered summary row:
 *
 *   - `term`            → summary.termMin (then termMax as inner tiebreaker
 *                         via the returned tuple, encoded as `termMin * 100 +
 *                         termMax` since both are small ints)
 *   - `upfront_cost`    → summary.upfrontMin
 *   - `monthly_cost`    → min over non-null variants (best-case framing);
 *                         POSITIVE_INFINITY only when ALL variants are null
 *   - `effective_savings_pct` → max over non-null variants (highest pct is the
 *                               cell's best pitch); POSITIVE_INFINITY only
 *                               when ALL variants are null
 *   - `payment` (categorical) → PAYMENT_ORDER index of the variant
 *                               `sortVariantsInCell` orders first (lowest term,
 *                               then lowest PAYMENT_ORDER). Replaces the
 *                               previous alphabetic comparator which sorted
 *                               `all-upfront` before `no-upfront`, semantically
 *                               wrong.
 *
 * For all other numeric columns (savings, count, on_demand_monthly) the
 * established `Math.max` semantics are preserved: a savings table sorted by
 * "best per cell" is the established convention and #494 does not flag these.
 * For all other string columns (provider, account, service, resource_type,
 * region) the value is invariant across a cell's variants (it's part of
 * cellKey), so `va[0]` continues to work.
 *
 * Returned number/string is the score the caller should compare. The null
 * sentinel `POSITIVE_INFINITY` is preserved for numeric scores so the existing
 * "always sort all-null cells last" early-return continues to work.
 */
function cellScoreFor(
  column: string,
  variants: LocalRecommendation[],
): number | string {
  // Issue #768: do NOT vary the score by selection state. Passing selectedRecs
  // into the comparator caused rows to reorder on every checkbox toggle because
  // a selected variant's individual value differs from the cell-summary value
  // used for unselected cells. Sort order is always derived from the
  // deterministic cell-summary score below, independent of selection.

  const numericKey = SORTABLE_NUMERIC_COLUMNS[column];
  const stringKey = SORTABLE_STRING_COLUMNS[column];

  if (variants.length === 0) {
    // Defensive: empty cells should not occur post-groupRecsByCell, but if one
    // slips through return a sentinel that sorts last.
    return numericKey ? Number.POSITIVE_INFINITY : '';
  }

  // No selected variant: pick a deterministic per-cell score.
  if (column === 'term') {
    const s = cellSummary(variants);
    // Encode (termMin, termMax) into a single number for sort-key purposes:
    // small positive ints multiplied by 100 keep ordering identical to lexical
    // (termMin, termMax) tuple comparison without needing an array compare.
    return s.termMin * 100 + s.termMax;
  }
  if (column === 'upfront_cost') {
    const s = cellSummary(variants);
    // upfrontMin as the primary key; upfrontMax only matters as a much smaller
    // tiebreaker so divide by a large constant. Clamp the tiebreaker so it
    // can never overwhelm the primary key for realistic upfront ranges.
    return s.upfrontMin + s.upfrontMax / 1e12;
  }
  if (column === 'monthly_cost') {
    const period = state.getCostPeriod();
    // #494 best-case framing: prefer the smallest NON-ZERO recurring cost so
    // that all-upfront variants (monthly_cost=0, which is a real value not a
    // null sentinel) do not tie every cell at 0 and make direction-toggling a
    // no-op. Tiers:
    //   1. any non-zero finite value exists  -> Math.min of those
    //   2. all finite values are 0 (pure all-upfront cell) -> 0
    //   3. no finite value at all (all null)  -> POSITIVE_INFINITY (sink to bottom)
    const nonZero: number[] = [];
    const anyFinite: number[] = [];
    for (const v of variants) {
      const s = scaleCost(v.monthly_cost, period);
      if (s != null) {
        anyFinite.push(s);
        if (s > 0) nonZero.push(s);
      }
    }
    if (nonZero.length) return Math.min(...nonZero);
    if (anyFinite.length) return 0;
    return Number.POSITIVE_INFINITY;
  }
  if (column === 'effective_savings_pct') {
    const finite: number[] = [];
    for (const v of variants) {
      // Prefer the provider-authoritative % (parity with the column render);
      // fall back to the reconstruction for variants without one.
      const pct = displaySavingsPct(v);
      if (pct != null) finite.push(pct);
    }
    // Best-case framing: highest savings pct is the cell's best pitch.
    return finite.length === 0 ? Number.POSITIVE_INFINITY : Math.max(...finite);
  }
  if (column === 'payment' && stringKey) {
    // Re-categorise: sort by the same PAYMENT_ORDER index sortVariantsInCell
    // uses for within-cell ordering, applied to the first variant in that
    // canonical order. Stringified so the existing string-column comparator
    // path handles it.
    const first = sortVariantsInCell(variants)[0];
    return String(ownValue(PAYMENT_ORDER, first?.payment ?? '') ?? 99);
  }

  // All other numeric columns (savings, count, on_demand_monthly): preserve
  // the established Math.max-over-variants semantics. These columns are NOT
  // flagged by #494 and have a long history of working correctly under
  // "best per cell" framing for a savings table.
  if (numericKey) return Math.max(...variants.map(numericKey));

  // All other string columns (provider, account, service, region,
  // resource_type): the value is invariant across a cell's variants because
  // it's part of cellKey. The first variant suffices.
  if (stringKey) return stringKey(variants[0]!);

  // Unsortable column.
  return 0;
}

/**
 * Return cell keys in the active sort order.
 *
 * Closes #494. Replaces the `Math.max(...va.map(numericKey))` collapse with a
 * per-column score (see `cellScoreFor`) that produces a deterministic single
 * value per cell. Adds a stable tiebreaker on `cellKey` so genuinely-tied cells
 * render in a consistent order across renders / browsers.
 */
export function groupsInSortOrder(
  groups: Map<string, LocalRecommendation[]>,
  sort: { column: string; direction: 'asc' | 'desc' },
): string[] {
  const direction = sort.direction === 'asc' ? 1 : -1;

  const keys = Array.from(groups.keys());
  return keys.slice().sort((ka, kb) => {
    const va = groups.get(ka)!;
    const vb = groups.get(kb)!;

    const scoreA = cellScoreFor(sort.column, va);
    const scoreB = cellScoreFor(sort.column, vb);

    if (typeof scoreA === 'number' && typeof scoreB === 'number') {
      // Nulls are encoded as POSITIVE_INFINITY. Always sort them last
      // regardless of direction so "no data" rows are de-emphasised in both
      // asc and desc.
      const aNullish = scoreA === Number.POSITIVE_INFINITY;
      const bNullish = scoreB === Number.POSITIVE_INFINITY;
      if (aNullish !== bNullish) return aNullish ? 1 : -1;
      // Both-nullish case: `Infinity - Infinity` would yield NaN and
      // `NaN !== 0` would short-circuit the cellKey tiebreaker below,
      // leaving two all-null cells in implementation-defined order.
      // Skip the numeric diff when both sides are sentinel-null so the
      // tiebreaker runs (matches the well-defined-order intent of #494).
      if (!aNullish) {
        const diff = (scoreA - scoreB) * direction;
        if (diff !== 0) return diff;
      }
    } else if (typeof scoreA === 'string' && typeof scoreB === 'string') {
      const diff = scoreA.localeCompare(scoreB) * direction;
      if (diff !== 0) return diff;
    }

    // Stable tiebreaker on cellKey, direction-invariant. Genuinely-tied cells
    // render in a deterministic order on every render and across browsers
    // (Array.prototype.sort stability varies historically; localeCompare on a
    // stable key sidesteps it entirely).
    return ka.localeCompare(kb);
  });
}

// Issue #480: per-column default sort direction. Defaults to 'asc' unless
// the COLUMN_DEFS entry overrides it. Looked up by sort-header onActivate
// when transitioning from a different sort column.
export function defaultSortDirectionFor(col: state.RecommendationsColumnId): 'asc' | 'desc' {
  const def = COLUMN_DEFS.find((c) => c.key === col);
  return def?.defaultSortDirection ?? 'asc';
}

export function sortIndicator(column: string, active: string, direction: 'asc' | 'desc'): string {
  if (column !== active) return `<span class="sort-indicator" aria-hidden="true">${iconSvg('chevrons-up-down')}</span>`;
  return direction === 'asc'
    ? `<span class="sort-indicator active" aria-hidden="true">${iconSvg('chevron-up')}</span>`
    : `<span class="sort-indicator active" aria-hidden="true">${iconSvg('chevron-down')}</span>`;
}
