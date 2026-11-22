import { showToast } from '../toast';
import {
  isPaymentSupported,
  isSavingsPlanService,
  paymentOptionsFor,
  SAVINGS_PLANS_BUCKET_KEY,
  type Provider as CompatProvider,
} from '../lib/purchase-compatibility';
import type { LocalRecommendation } from '../types';
import { openModal } from '../modal';
import { fetchOverridesForAccounts } from '../lib/overrides';
import { clearPurchaseModalRecommendations, recsState } from './recsState';
import { getAccountName } from './shared';
import { type BulkPurchasePayment, normalizeBulkPayment, type BulkPurchaseToolbarState } from './bulkPrefs';
import { pricedCellVariant } from './groups';
import { type FanOutBucket, clearFanOutBuckets, resetExecuteModeAndLabel, resolveBucketPaymentSeed } from './fanOutState';
import { renderFanOutSummary } from './fanOutSummary';
import { renderFanOutBucketSection } from './fanOutSection';

const OVERRIDE_WARN_MAX_IDS = 5;

let overrideWarningToast: { dismiss: () => void } | null = null;

// Sticky (timeout: null): a payment mismatch on a purchase path must not
// vanish after 30s. Both modals dismiss it from their onClose (every close
// path); the next modal open also replaces it so warnings never stack.
export function dismissOverrideWarning(): void {
  overrideWarningToast?.dismiss();
  overrideWarningToast = null;
}

export function warnOverrideFetchFailed(failedIds: readonly string[]): void {
  dismissOverrideWarning();
  if (failedIds.length === 0) return;
  const shown = failedIds.slice(0, OVERRIDE_WARN_MAX_IDS).map(getAccountName).join(', ');
  const more = failedIds.length > OVERRIDE_WARN_MAX_IDS
    ? ` +${failedIds.length - OVERRIDE_WARN_MAX_IDS} more`
    : '';
  overrideWarningToast = showToast({
    message: `Account payment overrides could not be loaded for ${failedIds.length} account(s) (${shown}${more}); check each row's payment before buying.`,
    kind: 'warning',
    timeout: null,
  });
}

export async function openFanOutModal(
  bucketEntries: Array<[string, LocalRecommendation[]]>,
  toolbar: BulkPurchaseToolbarState,
): Promise<void> {
  const openToken = ++recsState.purchaseModalOpenToken;
  resetExecuteModeAndLabel();
  // Pre-fetch service-overrides for every distinct account referenced by
  // any rec in any bucket; overridesByAccount seeds the bucket-level
  // payment (issue #111). One fetch per distinct accountID; cached for the lifetime of this
  // openFanOutModal call. A failed fetch does not block the modal: the
  // bucket seeds from the first rec's payment or the toolbar, and one sticky
  // warning toast tells the user.
  const allAccountIDs = new Set<string>();
  for (const [, recs] of bucketEntries) {
    for (const r of recs) {
      if (r.cloud_account_id) allAccountIDs.add(r.cloud_account_id);
    }
  }
  const { byAccount: overridesByAccount, failedIds } = await fetchOverridesForAccounts(allAccountIDs);
  if (openToken !== recsState.purchaseModalOpenToken) return;
  warnOverrideFetchFailed(failedIds);

  const buckets: FanOutBucket[] = bucketEntries
    .filter(([_key, recs]) => recs.length > 0)
    .map(([_key, recs], index) => {
      const r = recs[0]!;
      const seed = resolveBucketPaymentSeed(recs, toolbar, overridesByAccount);
      // SP buckets carry the canonical bucket key as `service` so the
      // section header can render the mixed-plan-type label; the per-
      // rec slugs on recs[].service are what the backend sees.
      const bucketService = isSavingsPlanService(r.service) ? SAVINGS_PLANS_BUCKET_KEY : r.service;

      let resolvedRecs = recs;
      let resolvedPayment = seed.payment;
      let resolvedPaymentSource: 'override' | 'toolbar' = seed.source;
      const candidates: BulkPurchasePayment[] = [
        seed.payment,
        ...recs
          .map((rec) => normalizeBulkPayment(rec.payment))
          .filter((payment): payment is BulkPurchasePayment => payment !== null),
        toolbar.payment,
        ...paymentOptionsFor(r.provider as CompatProvider, r.service, r.term as 1 | 3)
          .map((payment) => normalizeBulkPayment(payment))
          .filter((payment): payment is BulkPurchasePayment => payment !== null),
      ].filter((payment, index, all) =>
        all.indexOf(payment) === index
        && isPaymentSupported(r.provider as CompatProvider, r.service, r.term as 1 | 3, payment),
      );
      for (const candidate of candidates) {
        const resolved = recs.map((rec) => pricedCellVariant(rec, r.term as 1 | 3, candidate, toolbar.capacity));
        if (resolved.every((value) => value !== null)) {
          resolvedRecs = resolved.map((value) => value!);
          resolvedPayment = candidate;
          resolvedPaymentSource = candidate === seed.payment ? seed.source : 'toolbar';
          break;
        }
      }

      return {
        provider: r.provider as CompatProvider,
        service: bucketService,
        // Each bucket is now term-uniform (key includes term), so we read
        // the term from the bucket itself rather than from the dropped
        // toolbar override.
        term: r.term as 1 | 3,
        payment: resolvedPayment,
        paymentSource: resolvedPaymentSource,
        capacityPercent: toolbar.capacity,
        recs: resolvedRecs,
        // Unique per open, so results that settle after this modal closed
        // cannot match the sections of a newer one.
        id: `bucket-${openToken}-${index}`,
      };
    });
  recsState.currentFanOutBuckets = buckets;

  const container = document.getElementById('purchase-details');
  const modal = document.getElementById('purchase-modal');
  if (!container || !modal) return;

  // Build the modal body via createElement so the innerHTML hook
  // doesn't flag any template-literal HTML.
  while (container.firstChild) container.removeChild(container.firstChild);

  const summary = document.createElement('div');
  summary.id = 'fanout-summary';
  summary.className = 'form-section fanout-summary';
  renderFanOutSummary(summary, buckets);
  container.appendChild(summary);

  for (const b of buckets) {
    container.appendChild(renderFanOutBucketSection(b));
  }

  // onClose covers every close path (Escape included, issue #331): closing
  // this modal without submitting must always discard both fan-out and
  // single-row purchase state, or a later single-bucket open can submit
  // these stale buckets instead of the newly displayed recommendation.
  openModal(modal, {
    onClose: () => {
      clearPurchaseModalRecommendations();
      clearFanOutBuckets();
      resetExecuteModeAndLabel();
      dismissOverrideWarning();
    },
  });
}
