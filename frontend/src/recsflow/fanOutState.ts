import {
  isPaymentSupported,
  type Payment as CompatPayment,
  type Provider as CompatProvider,
} from '../lib/purchase-compatibility';
import { executeButtonLabel } from '../commitmentSummary';
import type { AccountServiceOverride } from '../api/accounts';
import type { LocalRecommendation } from '../types';
import { recsState } from './recsState';
import { getAccountName } from './shared';
import { normalizeBulkPayment, type BulkPurchaseToolbarState } from './bulkPrefs';
import { pricedCellVariant } from './groups';

// Buckets are single-account by construction (issue #333), so recs[0] names
// the bucket's account.
export function fanOutBucketAccountLabel(b: FanOutBucket): string {
  const accountId = b.recs[0]?.cloud_account_id;
  return accountId ? getAccountName(accountId) : 'Unattributed';
}

// FanOutBucket groups one batch of recs under a single (provider,
// service, term, payment, capacity) choice. A multi-bucket Purchase
// fires one executePurchase POST per bucket.
//
// `service` is the canonical bucket slug — equal to the rec's service
// for non-SP buckets, or `SAVINGS_PLANS_BUCKET_KEY` for any bucket
// containing one or more savings-plans-* recs (issue #132). Per-rec
// service slugs are preserved on `recs[].service` and round-trip into
// the executePurchase POST body — the backend uses each rec's own
// service for suppression and audit records.
//
// `paymentSource` (issue #111) records WHERE this bucket's payment
// default came from:
//   - 'override': all recs share one cloud_account_id, that account
//     has an AccountServiceOverride matching the bucket's
//     (provider, service), and the override's payment is supported by
//     the (provider, service, term) cell. The bucket section renders
//     a small "(from account override)" note next to the Payment
//     dropdown.
//   - 'toolbar': fallback — multi-account bucket, no override, override
//     has no payment, or the override's payment is unsupported for
//     this cell. Bucket inherits the bulk-toolbar Payment, today's
//     pre-#111 behavior.
//
// The user can change the per-bucket Payment via the dropdown
// rendered in the modal; the `change` handler updates `payment` (and
// keeps `paymentSource` so the source note doesn't lie about origin).
//
// Every bucket is single-account (or all-unattributed): the bucket key
// includes cloud_account_id (issue #333).
export interface FanOutBucket {
  provider: CompatProvider;
  service: string;
  term: 1 | 3;
  payment: 'all-upfront' | 'partial-upfront' | 'no-upfront' | 'monthly';
  capacityPercent: number;
  recs: LocalRecommendation[]; // scaled by capacityPercent
  paymentSource: 'override' | 'toolbar';
  /**
   * Identity within one modal open (issue #757), unique across opens.
   * getFanOutBuckets() hands out copies, so the submit path needs this to
   * map a POST result back to its bucket and section after a partial
   * failure, and to tell whether that modal is still the open one.
   */
  id?: string;
}

// The renderer promises "This bucket will be skipped" from exactly this
// check (renderFanOutBucketSection); submit and totals must agree with it
// (issue #1904) so a bucket the UI marks skipped is never posted.
export function isSubmittableBucket(b: FanOutBucket): boolean {
  if (b.recs.length === 0) return false;
  return b.recs.every((rec) => {
    const actualPayment = normalizeBulkPayment(rec.payment);
    return actualPayment === b.payment
      && isPaymentSupported(rec.provider as CompatProvider, rec.service, rec.term as 1 | 3, b.payment)
      && pricedCellVariant(rec, b.term, b.payment, b.capacityPercent) !== null;
  });
}

export function getFanOutBuckets(): FanOutBucket[] | null {
  if (!recsState.currentFanOutBuckets) return null;
  return recsState.currentFanOutBuckets.filter(isSubmittableBucket).map((b) => ({ ...b }));
}

export function clearFanOutBuckets(): void {
  recsState.currentFanOutBuckets = null;
}

export function getExecuteMode(): '' | 'direct' {
  return recsState.currentExecuteMode;
}

export function clearExecuteMode(): void {
  recsState.currentExecuteMode = '';
}

// Resets the mode and the submit button label together so the label never
// promises a different action than the radio (issue #754).
export function resetExecuteModeAndLabel(): void {
  recsState.currentExecuteMode = '';
  const executeBtn = document.getElementById('execute-purchase-btn') as HTMLButtonElement | null;
  if (executeBtn && executeBtn.dataset['submitting'] !== 'true') {
    executeBtn.textContent = executeButtonLabel(false);
  }
}

// resolveBucketPaymentSeed picks the default Payment value for a
// bucket per issue #111 sub-option (ii):
//   - When all recs in the bucket share one non-empty cloud_account_id
//     AND that account has a saved AccountServiceOverride matching
//     `(provider, recs[0].service)` AND the override's `payment` is a
//     non-empty value supported by the (provider, service, term) cell:
//     seed from override.
//   - Otherwise: seed from the toolbar payment (today's behavior).
//
// IMPORTANT: the override-lookup uses `recs[0].service` (the per-rec
// service slug), NOT `bucket.service`. That's a future-proofing choice
// for the post-#132 SP-canonical-bucket-key landing in PR #180 — when
// `bucket.service` becomes the canonical `'savings-plans'` for a
// mixed-plan-type SP bucket, the override is still keyed on the
// per-plan-type slug (`savings-plans-compute`, etc.), so this lookup
// stays correct under either bucket-key encoding.
//
// Multi-account fallback: if recs span 2+ distinct cloud_account_id
// values, no single account's override applies cleanly. Falling back
// to toolbar avoids surprising the user. TODO(#111-followup): consider
// per-rec seeding inside a multi-account bucket — would need either
// per-rec dropdowns or a "split this bucket by account" UX.
export function resolveBucketPaymentSeed(
  recs: LocalRecommendation[],
  toolbar: BulkPurchaseToolbarState,
  overridesByAccount: Map<string, AccountServiceOverride[]>,
): { payment: BulkPurchaseToolbarState['payment']; source: 'override' | 'toolbar' } {
  if (recs.length === 0) return { payment: toolbar.payment, source: 'toolbar' };
  const r0 = recs[0]!;
  const provider = r0.provider as CompatProvider;
  const term = r0.term as 1 | 3;

  // Single-account check: every rec must carry the same non-empty cloud_account_id.
  const accountIDs = new Set<string>();
  for (const r of recs) {
    if (!r.cloud_account_id) {
      // Any rec missing an account id skips the override lookup; fall
      // through to the rec.payment seed below.
      accountIDs.clear();
      break;
    }
    accountIDs.add(r.cloud_account_id);
  }
  if (accountIDs.size === 1) {
    const accountID = recs[0]!.cloud_account_id!;
    const overrides = overridesByAccount.get(accountID);
    if (overrides) {
      // Match on the per-rec service (NOT bucket.service) — see the comment
      // above for the SP-canonical-bucket-key future-proofing rationale.
      const match = overrides.find(
        (o) => o.provider === provider && o.service === r0.service,
      );
      const overridePayment = normalizeBulkPayment(match?.payment);
      if (
        overridePayment
        // Defensive: only honour the override when the (provider, service,
        // term, payment) combo is actually supported. A stale or hand-saved
        // override pointing at an unsupported payment for this term shouldn't
        // poison the dropdown — fall through to rec.payment seed.
        && isPaymentSupported(provider, r0.service, term, overridePayment)
      ) {
        return {
          payment: overridePayment,
          source: 'override',
        };
      }
    }
  }

  // Issue #699: since `payment` is now part of the bucket key, every bucket
  // is payment-uniform (all recs share the same rec.payment). Seed from
  // recs[0].payment when it's a supported value for this (provider, service,
  // term) cell instead of blindly falling back to toolbar.payment
  // (the global default payment). Multi-account buckets, missing-payment recs, and
  // unsupported payment values still fall through to toolbar.
  //
  // Normalize first so upstream synonym forms ('upfront', 'monthly') map to
  // the canonical BulkPurchasePayment values before the support check.
  const recPayment = normalizeBulkPayment(r0.payment);
  if (
    recPayment
    && isPaymentSupported(provider, r0.service, term, recPayment as CompatPayment)
  ) {
    return { payment: recPayment, source: 'toolbar' };
  }

  return { payment: toolbar.payment, source: 'toolbar' };
}
