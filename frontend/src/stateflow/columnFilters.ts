/**
 * RI Exchange, Active Convertible RI and History per-column filter slices
 * (moved verbatim from state.ts; state.ts re-exports every name).
 */

// ---------------------------------------------------------------------------
// RI Exchange per-column filters (issue #166 follow-up to merged #570).
//
// Scoped to the RI Exchange reshape-recommendations table. Independent of
// the recommendations slice so the two tabs don't fight over column-id
// shape — RI Exchange's columns are reshape-specific (source/target
// instance types, normalized units, utilization %).
// In-memory only; resets on page reload. Persistence is out of scope for
// this PR, same as the recommendations slice.
// ---------------------------------------------------------------------------
export type RiExchangeColumnId =
  | 'source_ri_id' | 'source_instance_type' | 'target_instance_type' | 'reason'
  | 'source_count' | 'target_count' | 'utilization_percent'
  | 'normalized_used' | 'normalized_purchased';

export type RiExchangeColumnFilter =
  | { kind: 'set'; values: string[] }   // categorical — string-form values
  | { kind: 'expr'; expr: string };     // numeric — parsed on apply

export type RiExchangeColumnFilters = Partial<
  Record<RiExchangeColumnId, RiExchangeColumnFilter>
>;

let riExchangeColumnFilters: RiExchangeColumnFilters = {};

export function getRiExchangeColumnFilters(): RiExchangeColumnFilters {
  return { ...riExchangeColumnFilters };
}

export function setRiExchangeColumnFilter(
  column: RiExchangeColumnId,
  filter: RiExchangeColumnFilter | null,
): void {
  if (filter === null) {
    const next = { ...riExchangeColumnFilters };
    delete next[column];
    riExchangeColumnFilters = next;
    return;
  }
  riExchangeColumnFilters = {
    ...riExchangeColumnFilters,
    [column]: filter,
  };
}

export function clearAllRiExchangeColumnFilters(): void {
  riExchangeColumnFilters = {};
}

// ---------------------------------------------------------------------------
// Active Convertible RIs per-column filters (issue #1414).
//
// Mirrors the reshape-recommendations filter slice above but scoped to the
// Active Convertible RIs table (ri-exchange-instances-list). Column IDs map
// directly to ConvertibleRI / RIUtilization API fields.
// ---------------------------------------------------------------------------

export type ActiveRiColumnId =
  | 'instance_type' | 'availability_zone' | 'offering_type'
  | 'instance_count' | 'utilization_pct';

export type ActiveRiColumnFilter =
  | { kind: 'set'; values: string[] }
  | { kind: 'expr'; expr: string };

export type ActiveRiColumnFilters = Partial<Record<ActiveRiColumnId, ActiveRiColumnFilter>>;

let activeRiColumnFilters: ActiveRiColumnFilters = {};

export function getActiveRiColumnFilters(): ActiveRiColumnFilters {
  return { ...activeRiColumnFilters };
}

export function setActiveRiColumnFilter(
  column: ActiveRiColumnId,
  filter: ActiveRiColumnFilter | null,
): void {
  if (filter === null) {
    const next = { ...activeRiColumnFilters };
    delete next[column];
    activeRiColumnFilters = next;
    return;
  }
  activeRiColumnFilters = { ...activeRiColumnFilters, [column]: filter };
}

export function clearAllActiveRiColumnFilters(): void {
  activeRiColumnFilters = {};
}

// ---------------------------------------------------------------------------
// History per-column filters (issue #166 follow-up).
//
// The History page renders two tables: Purchase History (completed-and-final
// rows) and Approval Queue (pending/in-flight rows). Their column shapes
// overlap only partially (Account / Payment / Monthly Cost / Created By
// appear only on the queue; Resource Type / Region appear only on Purchase
// History), so each table gets its own closed column-id enum and its own
// in-memory filter slice. Both reuse `applyColumnFilters` from
// lib/column-filters.ts via the same `kind: 'set' | 'expr'` shape used by
// the recommendations table -- no new lib-level abstractions needed.
//
// In-memory only on this iteration; persistence (localStorage) is a
// follow-up tracked under the same umbrella as the recommendations
// equivalent.
// ---------------------------------------------------------------------------

export type PurchaseHistoryColumnId =
  | 'provider' | 'service' | 'resource_type' | 'region' | 'term'
  | 'count' | 'upfront_cost' | 'savings';

export type ApprovalQueueColumnId =
  | 'provider' | 'account' | 'service' | 'term' | 'payment' | 'created_by'
  | 'count' | 'monthly_cost' | 'upfront_cost' | 'savings';

export type HistoryColumnFilter =
  | { kind: 'set'; values: string[] }
  | { kind: 'expr'; expr: string };

export type PurchaseHistoryColumnFilters = Partial<
  Record<PurchaseHistoryColumnId, HistoryColumnFilter>
>;
export type ApprovalQueueColumnFilters = Partial<
  Record<ApprovalQueueColumnId, HistoryColumnFilter>
>;

let purchaseHistoryColumnFilters: PurchaseHistoryColumnFilters = {};
let approvalQueueColumnFilters: ApprovalQueueColumnFilters = {};

export function getPurchaseHistoryColumnFilters(): PurchaseHistoryColumnFilters {
  return { ...purchaseHistoryColumnFilters };
}

export function setPurchaseHistoryColumnFilter(
  column: PurchaseHistoryColumnId,
  filter: HistoryColumnFilter | null,
): void {
  if (filter === null) {
    const next = { ...purchaseHistoryColumnFilters };
    delete next[column];
    purchaseHistoryColumnFilters = next;
    return;
  }
  purchaseHistoryColumnFilters = {
    ...purchaseHistoryColumnFilters,
    [column]: filter,
  };
}

export function clearAllPurchaseHistoryColumnFilters(): void {
  purchaseHistoryColumnFilters = {};
}

export function getApprovalQueueColumnFilters(): ApprovalQueueColumnFilters {
  return { ...approvalQueueColumnFilters };
}

export function setApprovalQueueColumnFilter(
  column: ApprovalQueueColumnId,
  filter: HistoryColumnFilter | null,
): void {
  if (filter === null) {
    const next = { ...approvalQueueColumnFilters };
    delete next[column];
    approvalQueueColumnFilters = next;
    return;
  }
  approvalQueueColumnFilters = {
    ...approvalQueueColumnFilters,
    [column]: filter,
  };
}

export function clearAllApprovalQueueColumnFilters(): void {
  approvalQueueColumnFilters = {};
}
