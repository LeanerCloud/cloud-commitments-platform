import * as state from '../state';
import { scaleSavingsPlanForCapacity } from '../sp-capacity';
import {
  isSavingsPlanService,
  paymentOptionsFor,
  type Provider as CompatProvider,
} from '../lib/purchase-compatibility';
import type { LocalRecommendation } from '../types';
import { ownValue } from '../lib/own-property';
import { recsState } from './recsState';
import { effectiveMonthlySavings } from './money';
import { type BulkPurchasePayment, normalizeBulkPayment } from './bulkPrefs';

// cellKey identifies the physical-resource cell a rec belongs to.
// After PR #195's per-(term, payment) fan-out, a single physical resource
// produces up to 6 alternative rec rows (2 terms × 3 payments). The
// `(provider, cloud_account_id, service, region, resource_type, engine)`
// prefix is the cell — recs sharing this prefix are alternatives, not
// additions. Same prefix the scheduler ID encoding uses
// (scheduler.go:856-858, PR #189) but without the (term, payment) suffix.
//
// Used by issue #224's radio enforcement: at most one variant per cell
// can be selected at any time.
export function cellKey(rec: LocalRecommendation): string {
  const key = `${rec.provider}|${rec.cloud_account_id ?? ''}|${rec.service}|${rec.region}|${rec.resource_type}|${rec.engine ?? ''}`;
  // Zonal EC2 recs in different AZs are distinct purchasable cells (issue #792);
  // the suffix is added only for them so every other cell key is unchanged.
  const zonal = rec.availability_zone ? `${key}|${rec.availability_zone}` : key;
  // EC2 recs differing only by platform or tenancy are distinct purchasable cells
  // (issue #806). Verbatim values from details, same source as
  // samePurchaseVariantIdentity; the pair is added only when either is set.
  const { platform, tenancy } = computeIdentity(rec);
  return platform || tenancy ? `${zonal}|${platform}|${tenancy}` : zonal;
}

function computeIdentity(rec: LocalRecommendation): { platform: string; tenancy: string } {
  const details = rec.details;
  if ((rec.service !== 'ec2' && rec.service !== 'compute') || typeof details !== 'object' || details === null || Array.isArray(details)) {
    return { platform: '', tenancy: '' };
  }
  const { platform, tenancy } = details as Record<string, unknown>;
  return {
    platform: typeof platform === 'string' ? platform : '',
    tenancy: typeof tenancy === 'string' ? tenancy : '',
  };
}

// issues #225 + #226: cell-grouping helpers.

/**
 * Group a flat recommendation list by physical-resource cell.
 * Returns a Map preserving insertion order of first-seen cell keys.
 * Exported for tests.
 */
export function groupRecsByCell(recs: readonly LocalRecommendation[]): Map<string, LocalRecommendation[]> {
  const groups = new Map<string, LocalRecommendation[]>();
  for (const r of recs) {
    const k = cellKey(r);
    const g = groups.get(k);
    if (g) {
      g.push(r);
    } else {
      groups.set(k, [r]);
    }
  }
  return groups;
}

// Stable offering selectors from pkg/common/types.go; SP prices and offering
// IDs can legitimately differ across term/payment variants.
function samePurchaseVariantIdentity(a: LocalRecommendation, b: LocalRecommendation): boolean {
  if (a.provider !== 'aws') return true;
  let fields: readonly string[];
  switch (a.service) {
    case 'ec2':
    case 'compute':
      fields = ['platform', 'tenancy', 'scope', 'availability_zone'];
      break;
    case 'rds':
    case 'relational-db':
      fields = ['engine', 'az_config'];
      break;
    case 'elasticache':
    case 'cache':
      fields = ['engine'];
      break;
    default:
      fields = isSavingsPlanService(a.service) ? ['plan_type', 'instance_family', 'region'] : [];
  }
  if (fields.length === 0) return true;
  const left = a.details ?? {};
  const right = b.details ?? {};
  if (typeof left !== 'object' || Array.isArray(left) || typeof right !== 'object' || Array.isArray(right)) return false;
  return fields.every((field) => {
    const value = (left as Record<string, unknown>)[field];
    return (value === undefined || typeof value === 'string') && value === (right as Record<string, unknown>)[field];
  });
}

// Every loaded (term, payment) row of rec's purchase identity, plus rec itself when the
// loaded set lacks it (modal opened on a stale or test-supplied list).
// Reads state.getRecommendations() (not getVisibleRecommendations) so a
// column filter that hides a sibling term/payment row can never make the
// purchase modal think a priced variant doesn't exist (issue #1903).
function loadedCellVariants(rec: LocalRecommendation): LocalRecommendation[] {
  const key = cellKey(rec);
  const variants = (state.getRecommendations() as unknown as LocalRecommendation[])
    .filter((v) => cellKey(v) === key && samePurchaseVariantIdentity(rec, v));
  if (!variants.some((v) => v.id === rec.id)) variants.push(rec);
  return sortVariantsInCell(variants);
}

// The scaled variant the modal would submit for (term, payment), or null when
// no such row was loaded or it scales to zero units at the modal's capacity.
export function pricedCellVariant(
  rec: LocalRecommendation,
  term: 1 | 3,
  payment: BulkPurchasePayment,
  capacityPercent: number,
): LocalRecommendation | null {
  const v = loadedCellVariants(rec).find((c) => c.term === term && normalizeBulkPayment(c.payment) === payment);
  if (!v) return null;
  // `rec` reached the modal already scaled to recsState.currentPurchaseCapacityPercent:
  // openPurchaseModal's only caller passes handleBulkPurchaseClick's scaled
  // rows. Only the rows read from state.getRecommendations() are unscaled, so
  // re-scaling the fallback push would halve count and cost a second time.
  if (v === rec) return v;
  return scaleRecForCapacity(v, capacityPercent);
}

// Distinct terms actually loaded for rec's cell, ascending. Used to build
// the purchase modal's Term <select> so it never offers a term the API
// didn't price (issue #1903).
export function cellTermOptions(rec: LocalRecommendation): Array<1 | 3> {
  const terms = new Set<1 | 3>();
  for (const v of loadedCellVariants(rec)) {
    if (v.term === 1 || v.term === 3) terms.add(v.term);
  }
  return Array.from(terms).sort((a, b) => a - b);
}

// Payment options for rec's cell at `term`, restricted to combinations that
// were loaded and remain priced at the modal capacity (issue #1903).
export function cellPaymentOptions(
  rec: LocalRecommendation,
  term: 1 | 3,
  capacityPercent: number,
): BulkPurchasePayment[] {
  return paymentOptionsFor(rec.provider as CompatProvider, rec.service, term).filter((p) =>
    pricedCellVariant(rec, term, p as BulkPurchasePayment, capacityPercent) !== null,
  ) as BulkPurchasePayment[];
}

// issue #135: SP plan-type row grouping helpers.

/**
 * Canonical key for the SP group that collapses per-plan-type cell rows
 * under a single parent row in the Recommendations table.
 *
 * Groups all AWS savings-plans-* cell rows that share the same
 * (provider, account_id, region) scope. Each such triple produces at most
 * one parent row in the table. Exported for tests.
 */
export function spGroupKey(rec: LocalRecommendation): string {
  return `sp-group|${rec.provider}|${rec.cloud_account_id ?? ''}|${rec.region}`;
}

/**
 * Given the sorted cell-key list and the groups map (output of groupRecsByCell),
 * return a Map of spGroupKey -> cellKey[] for every SP group that contains 2 or
 * more distinct per-plan-type cell keys. Groups with only one cell key are not
 * included (they render as a regular flat/cell row with no SP parent).
 *
 * Preserves the relative order of cell keys within each group as they appear in
 * sortedKeys, so the rendered children respect the active sort order.
 * Exported for tests.
 */
export function groupSpCellKeys(
  sortedKeys: readonly string[],
  groups: ReadonlyMap<string, LocalRecommendation[]>,
): Map<string, string[]> {
  // First pass: collect SP cell keys per scope key, in sort order.
  const byScope = new Map<string, string[]>();
  for (const key of sortedKeys) {
    const recs = groups.get(key);
    if (!recs || recs.length === 0) continue;
    const rep = recs[0]!;
    if (!isSavingsPlanService(rep.service)) continue;
    const sk = spGroupKey(rep);
    const existing = byScope.get(sk);
    if (existing) {
      existing.push(key);
    } else {
      byScope.set(sk, [key]);
    }
  }
  // Second pass: drop singletons (one cell key = no parent row needed).
  const result = new Map<string, string[]>();
  for (const [sk, cellKeys] of byScope) {
    if (cellKeys.length >= 2) {
      result.set(sk, cellKeys);
    }
  }
  return result;
}

// Fixed payment ordering for within-cell variant sort.
export const PAYMENT_ORDER: Record<string, number> = {
  'no-upfront': 0,
  'partial-upfront': 1,
  'all-upfront': 2,
  monthly: 3,
};

/** Sort variants within a cell: term ASC, then payment in fixed order. */
export function sortVariantsInCell(variants: LocalRecommendation[]): LocalRecommendation[] {
  return variants.slice().sort((a, b) => {
    const termDiff = a.term - b.term;
    if (termDiff !== 0) return termDiff;
    const pa = ownValue(PAYMENT_ORDER, a.payment ?? '') ?? 99;
    const pb = ownValue(PAYMENT_ORDER, b.payment ?? '') ?? 99;
    return pa - pb;
  });
}

// pickBestVariantPerCell collapses a list of recs to one rec per cell,
// preferring the variant matching resolved GlobalConfig.DefaultTerm +
// DefaultPayment, then falling back to the highest effective monthly savings.
//
// issue #223: tiebreaker is "matches resolved GlobalConfig.DefaultTerm +
// DefaultPayment". If no variant in a cell matches the configured defaults,
// fall back to the variant with the highest effective monthly savings
// (the original #224 behaviour).
//
// Effective savings (fallback metric) amortizes the upfront cost across
// the term:
//   effective = savings - (upfront_cost / (term * 12))
//
// Two `(savings, upfront_cost, term)` tuples that look identical on raw
// `savings` can score very differently on the amortized number — e.g. a
// 3yr all-upfront commitment with a large lump-sum upfront has a much
// lower effective monthly savings than a no-upfront variant with the
// same headline savings. Picking by amortization picks the variant
// that's actually best for the operator's wallet over the term.
//
// Used by issue #224's `select-all` handler. Sibling issue #223 will
// replace this tiebreaker with "matches resolved GlobalConfig.DefaultTerm
// + DefaultPayment" once that lands; until then, amortized savings is
// the right deterministic default.
// Exported so unit tests can exercise it directly.
export function pickBestVariantPerCell(recs: readonly LocalRecommendation[]): LocalRecommendation[] {
  const groups = new Map<string, LocalRecommendation[]>();
  for (const r of recs) {
    const k = cellKey(r);
    const group = groups.get(k);
    if (group) {
      group.push(r);
    } else {
      groups.set(k, [r]);
    }
  }

  const result: LocalRecommendation[] = [];
  for (const group of groups.values()) {
    // Prefer the variant matching the operator's configured defaults.
    const configMatch = group.find(
      (r) => r.term === recsState.cachedGlobalDefaultTerm && r.payment === recsState.cachedGlobalDefaultPayment,
    );
    if (configMatch) {
      result.push(configMatch);
      continue;
    }
    // No config-matching variant in this cell: fall back to highest effective savings.
    let best = group[0]!;
    for (let i = 1; i < group.length; i++) {
      if (effectiveMonthlySavings(group[i]!) > effectiveMonthlySavings(best)) best = group[i]!;
    }
    result.push(best);
  }
  return result;
}

// Scale one rec's count-dependent fields (count, upfront_cost, monthly_cost,
// savings) to a capacity %, flooring the unit count. Returns null when the
// scaled count floors to 0 — the row contributes nothing at this capacity.
// Extracted from handleBulkPurchaseClick's scaling loop (issue #1903) so
// pricedCellVariant can apply the same scaling to a sibling variant swapped
// in on a Term/Payment change.
export function scaleRecForCapacity(r: LocalRecommendation, capacityPercent: number): LocalRecommendation | null {
  // Savings Plans are sized by hourly commitment, never by count (#1959).
  if (isSavingsPlanService(r.service)) return scaleSavingsPlanForCapacity(r, capacityPercent);
  const newCount = Math.floor((r.count * capacityPercent) / 100);
  if (newCount <= 0) return null;
  const ratio = r.count > 0 ? newCount / r.count : 1;
  return {
    ...r,
    count: newCount,
    // Carry the pre-scaling count so the backend can verify the
    // capacity_percent it records against the scaled count (#647).
    recommended_count: r.count,
    upfront_cost: r.upfront_cost * ratio,
    monthly_cost: r.monthly_cost != null ? r.monthly_cost * ratio : null,
    savings: r.savings * ratio,
    // on_demand_cost is a per-rec total like savings, so it scales too;
    // leaving it unscaled halved the Eff. % at 50% capacity (#136).
    on_demand_cost: r.on_demand_cost != null ? r.on_demand_cost * ratio : r.on_demand_cost,
  };
}
