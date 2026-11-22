import * as state from '../state';
import { NO_VALUE } from '../utils';
import type { HistoryPurchase } from '../types';
import { formatTermShort } from '../lib/table-format';
import { serviceLabel } from '../lib/service-labels';
import { applyColumnFilters } from '../lib/column-filters';
import { isRealTerm } from '../lib/purchase-row-guards';
import type { PurchaseHistoryColumnId } from '../state';
import { boughtNothing, roundForDisplay } from './cells';

// ---------------------------------------------------------------------------
// Per-column filter wiring for the Purchase History table.
//
// The Status chip-row above stays as-is — it's an enum-driven filter with no
// natural fit for the generic set/expr column-filter shape, and the chip-row
// is the more discoverable affordance for status anyway. Column filters here
// cover the row attributes (provider/service/type/region/term/count/upfront/
// savings) — Status is intentionally excluded.
//
// Numeric extractors round to 0 decimal places to match formatCurrency's
// default (CURRENCY_DEFAULT_DIGITS), so a user typing the visible "$123"
// matches the row that displays that exact value (issue #484 contract on
// the recommendations table).
// ---------------------------------------------------------------------------

export const PURCHASE_HISTORY_NUMERIC_COLUMNS: ReadonlySet<PurchaseHistoryColumnId> = new Set([
  'count', 'upfront_cost', 'savings',
]);

function purchaseHistoryCategoricalCellValue(
  p: HistoryPurchase,
  col: PurchaseHistoryColumnId,
): string {
  switch (col) {
    case 'provider':       return p.provider ?? '';
    case 'service':        return p.service ?? '';
    case 'resource_type':  return p.resource_type ?? '';
    case 'region':         return p.region ?? '';
    case 'term':           return p.term == null ? '' : String(p.term);
    case 'count':
    case 'upfront_cost':
    case 'savings':        return '';
  }
}

export function purchaseHistoryNumericCellValue(
  p: HistoryPurchase,
  col: PurchaseHistoryColumnId,
): number {
  switch (col) {
    case 'count':         return p.count ?? 0;
    case 'upfront_cost':  return p.upfront_cost ?? Number.NaN;
    case 'savings':       return boughtNothing(p) ? 0 : p.estimated_savings ?? Number.NaN;
    case 'provider':
    case 'service':
    case 'resource_type':
    case 'region':
    case 'term':          return Number.NaN;
  }
}

export function applyPurchaseHistoryColumnFilters(
  purchases: readonly HistoryPurchase[],
  filters: state.PurchaseHistoryColumnFilters,
): HistoryPurchase[] {
  return applyColumnFilters<HistoryPurchase, PurchaseHistoryColumnId>(
    purchases,
    filters,
    {
      categorical: purchaseHistoryCategoricalCellValue,
      numeric: (p, col) => roundForDisplay(purchaseHistoryNumericCellValue(p, col)),
    },
  );
}

export const PURCHASE_HISTORY_LABELS: Record<PurchaseHistoryColumnId, string> = {
  provider: 'Provider',
  service: 'Service',
  resource_type: 'Type',
  region: 'Region',
  term: 'Term',
  count: 'Count',
  upfront_cost: 'Upfront Cost',
  savings: 'Monthly Savings',
};

export function purchaseHistoryDistinctValues(
  purchases: readonly HistoryPurchase[],
  column: PurchaseHistoryColumnId,
): string[] {
  const seen = new Set<string>();
  for (const p of purchases) {
    seen.add(purchaseHistoryCategoricalCellValue(p, column));
  }
  return Array.from(seen).sort((a, b) => {
    if (a === '' && b !== '') return -1;
    if (a !== '' && b === '') return 1;
    return a.localeCompare(b);
  });
}

export function purchaseHistoryDisplayLabel(
  column: PurchaseHistoryColumnId,
  value: string,
): string {
  if (value === '') return '(empty)';
  if (column === 'term') {
    // Match the cell: a 0 term renders as a dash, never "0 Years" (#181).
    const n = Number(value);
    return isRealTerm(n) ? formatTermShort(n) : Number.isFinite(n) ? NO_VALUE : value;
  }
  if (column === 'service') return serviceLabel(value);
  return value;
}

// Per-table namespaces for the shared filter popover (issue #281): both
// tables render buttons with the same bare column ids, so the popover keys
// and resolves its anchor by (scope, column).
export const PURCHASE_HISTORY_FILTER_SCOPE = 'history';
