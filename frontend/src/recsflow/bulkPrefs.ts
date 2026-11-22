import {
  isPaymentSupported,
  type Provider as CompatProvider,
} from '../lib/purchase-compatibility';
import type { LocalRecommendation } from '../types';
import { recsState } from './recsState';

// BULK_PURCHASE_LS_KEY holds the persisted Term/Payment/Capacity values
// so the toolbar remembers the user's last choice across page reloads.
// Versioned so we can migrate the shape in future without blowing up on
// a stale cached blob.
const BULK_PURCHASE_LS_KEY = 'cudly.recommendations.bulkPurchase.v1';

// BulkPurchaseToolbarState used to carry a `term` field that overrode each
// row's recommended term at API-call time. Bundle B drops it: each rec is
// purchased with its own per-row term (see term-aware bucketing in
// handleBulkPurchaseClick). Issue #282 drops the global Payment dropdown
// from the toolbar: the `payment` field is kept internally (seeded from
// GlobalConfig or 'no-upfront') so the fan-out modal's override/fallback
// logic continues to work, but it is no longer exposed in the UI or
// persisted to localStorage. loadBulkPurchaseState explicitly picks known
// fields so any legacy `term` or `payment` from older localStorage values
// is silently ignored on read — no migration shim needed.
export type BulkPurchasePayment = 'all-upfront' | 'partial-upfront' | 'no-upfront' | 'monthly';

// Normalize payment synonyms that upstream rows may carry (e.g. Azure's
// 'upfront') to the canonical BulkPurchasePayment forms used throughout the
// bucket-key and toolbar machinery.  Returns null for unknown or absent values
// so callers can fall back safely.
//
// Mappings:
//   'upfront'       -> 'all-upfront'  (Azure canonical synonym)
//   'all-upfront'   -> 'all-upfront'  (AWS / pass-through)
//   'partial-upfront' -> 'partial-upfront' (pass-through)
//   'no-upfront'    -> 'no-upfront'   (pass-through)
//   'monthly'       -> 'monthly'      (GCP canonical; kept as-is)
//   anything else / undefined -> null
export function normalizeBulkPayment(payment: string | undefined): BulkPurchasePayment | null {
  switch (payment) {
    case 'upfront':
    case 'all-upfront':
      return 'all-upfront';
    case 'partial-upfront':
      return 'partial-upfront';
    case 'no-upfront':
      return 'no-upfront';
    case 'monthly':
      return 'monthly';
    default:
      return null;
  }
}

// Centralized bucket-level payment compatibility check. A bucket is
// compatible iff EVERY rec in it has a supported (provider, service,
// term, payment) combination. Used by the bulk-buy fan-out path to
// flag buckets the user has built but won't be allowed to submit.
// A rec's own supported payment also qualifies: the single-bucket modal seeds
// from it (resolvePerRecPaymentSeed), so a GCP rec priced monthly must not be
// forced into fan-out by the toolbar default.
export function isBucketPaymentCompatible(
  recs: readonly LocalRecommendation[],
  payment: BulkPurchasePayment,
): boolean {
  return recs.every((r) => {
    const provider = r.provider as CompatProvider;
    const term = r.term as 1 | 3;
    const own = normalizeBulkPayment(r.payment);
    return isPaymentSupported(provider, r.service, term, payment)
      || (own !== null && isPaymentSupported(provider, r.service, term, own));
  });
}

export interface BulkPurchaseToolbarState {
  payment: BulkPurchasePayment;
  capacity: number; // 1..100
}

// payment follows the global default (no-upfront until the config loads).
function defaultBulkPurchaseState(): BulkPurchaseToolbarState {
  return { payment: recsState.cachedGlobalDefaultPayment as BulkPurchasePayment, capacity: 100 };
}

export function loadBulkPurchaseState(): BulkPurchaseToolbarState {
  try {
    const raw = localStorage.getItem(BULK_PURCHASE_LS_KEY);
    if (!raw) return defaultBulkPurchaseState();
    const parsed = JSON.parse(raw) as Partial<BulkPurchaseToolbarState> & { term?: unknown };
    // Explicit field-pick rather than spread-and-omit — avoids leaking a
    // legacy `term` or `payment` from older localStorage values at runtime.
    // Payment is seeded from GlobalConfig only (issue #282 drops the toolbar
    // dropdown; the field is internal-only, not persisted).
    // Integers only, like every other numeric parse here: a hand-edited
    // 33.33 would otherwise reach Math.floor scaling and the audited
    // capacity_percent as a value no UI control can produce (#136).
    const capacity = Number(parsed.capacity);
    return {
      payment: recsState.cachedGlobalDefaultPayment as BulkPurchasePayment,
      capacity: Number.isInteger(capacity) && capacity > 0 ? Math.min(100, capacity) : 100,
    };
  } catch {
    return defaultBulkPurchaseState();
  }
}

export function saveBulkPurchaseState(s: BulkPurchaseToolbarState): void {
  try {
    // Only persist capacity — payment is dropped from the toolbar (issue #282)
    // and is now session-only, seeded from GlobalConfig.
    localStorage.setItem(BULK_PURCHASE_LS_KEY, JSON.stringify({ capacity: s.capacity }));
  } catch {
    // Private-browsing / quota-exceeded — non-fatal, just lose the
    // sticky choice. The bottom box still works in-session.
  }
}
