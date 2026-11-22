import { showToast } from '../toast';
import {
  isSavingsPlanService,
  SAVINGS_PLANS_BUCKET_KEY,
} from '../lib/purchase-compatibility';
import type { LocalRecommendation } from '../types';
import { getAccountName } from './shared';
import { normalizeBulkPayment, isBucketPaymentCompatible, loadBulkPurchaseState } from './bulkPrefs';
import { scaleRecForCapacity } from './groups';
import { openFanOutModal } from './fanOutModal';
import { openPurchaseModal } from './purchaseModal';

const SP_DROP_MAX_ROWS = 5;

let spDropToast: { dismiss: () => void } | null = null;

function spRowLabel(r: LocalRecommendation): string {
  const acct = r.cloud_account_id ? ` (${getAccountName(r.cloud_account_id)})` : '';
  return `${r.service} ${r.resource_type ?? ''} ${r.region ?? ''}`.replace(/\s+/g, ' ').trim() + acct;
}

// A Savings Plans row whose sized hourly commitment is below $0.01/hr is
// refused by the backend for the whole request, which would also block the
// RI rows in its bucket. Drop it before sending and say so, never silently.
function warnDroppedSavingsPlans(dropped: LocalRecommendation[], capacity: number): void {
  if (dropped.length === 0) return;
  const shown = dropped.slice(0, SP_DROP_MAX_ROWS).map(spRowLabel).join(', ');
  const more = dropped.length > SP_DROP_MAX_ROWS ? ` +${dropped.length - SP_DROP_MAX_ROWS} more` : '';
  spDropToast = showToast({
    message: `Savings Plans rows left out: at ${capacity}% capacity their hourly commitment falls below the $0.01/hr minimum (or carries no hourly commitment to size): ${shown}${more}. Raise Capacity to include them.`,
    kind: 'warning',
    timeout: null,
  });
}

export function handleBulkPurchaseClick(recommendations: LocalRecommendation[]): void {
  const tb = loadBulkPurchaseState();
  if (recommendations.length === 0) {
    showToast({ message: 'No recommendations to purchase.', kind: 'warning' });
    return;
  }

  spDropToast?.dismiss();
  spDropToast = null;

  // Scale by capacity %; drop rows whose scaled count floors to 0. Savings
  // Plans rows keep Count 1 and are sized by the backend from capacity_percent.
  const scaled: LocalRecommendation[] = [];
  const droppedSavingsPlans: LocalRecommendation[] = [];
  for (const r of recommendations) {
    const s = scaleRecForCapacity(r, tb.capacity);
    if (s) scaled.push(s);
    else if (isSavingsPlanService(r.service)) droppedSavingsPlans.push(r);
  }
  warnDroppedSavingsPlans(droppedSavingsPlans, tb.capacity);
  if (scaled.length === 0) {
    if (droppedSavingsPlans.length > 0) return;
    showToast({
      message: `Capacity ${tb.capacity}% produces no whole-number purchases from the current selection. Try a higher %.`,
      kind: 'warning',
    });
    return;
  }

  // Bucket by (provider, service, term, payment, cloud_account_id). Bundle B
  // added `term` to the key so multi-term selections fan out into separate
  // buckets. Issue #699 adds `payment` for the same reason: recs with
  // identical (provider, service, term) but different per-rec payment
  // values must also land in separate buckets so each bucket is payment-
  // uniform and resolveBucketPaymentSeed can seed from recs[0].payment
  // rather than falling back to the toolbar default (the global default payment).
  //
  // Issue #132: SP recs (savings-plans-{compute,ec2instance,sagemaker,
  // database}) collapse into a single bucket per (provider, term) so an
  // operator who used to bulk-buy SP pre-PR-#123 (when there was one
  // 'savings-plans' service) keeps the one-click experience. Each rec
  // retains its real per-plan-type service slug — only the bucket key
  // is canonicalized via SAVINGS_PLANS_BUCKET_KEY. The backend
  // executePurchase loops per rec and uses rec.service for the
  // suppression and audit records, so a mixed-SP POST behaves
  // identically to four separate POSTs except that there's only one
  // approval token / email.
  //
  // Issue #333: `cloud_account_id` joins the key so a bucket -- and
  // therefore the single executePurchase POST body built from it -- never
  // spans more than one cloud account. internal/purchase/execution.go's
  // SingleCloudAccountIDFromRecs (enforced both at the API boundary in
  // validateExecutePurchaseRecommendations and by the executor) rejects a
  // request whose selected recs target more than one account, or mix
  // attributed and unattributed recs, with HTTP 400 (issue #1902). Recs
  // with no cloud_account_id (the ambient single-account deployment)
  // normalize to '' so they still bucket together, but never alongside an
  // attributed account's recs.
  const buckets = new Map<string, LocalRecommendation[]>();
  for (const r of scaled) {
    const bucketService = isSavingsPlanService(r.service) ? SAVINGS_PLANS_BUCKET_KEY : r.service;
    const key = `${r.provider}|${bucketService}|${r.term}|${normalizeBulkPayment(r.payment) ?? ''}|${r.cloud_account_id ?? ''}`;
    const existing = buckets.get(key);
    if (existing) existing.push(r);
    else buckets.set(key, [r]);
  }
  const bucketEntries = Array.from(buckets.entries());

  // Per-bucket compatibility check using the bucket's own term. For a
  // mixed-SP bucket we check every rec's service — if ANY rec's
  // (provider, service, term, payment) is unsupported, the whole bucket
  // is flagged incompatible. SP plan types share the same compatibility
  // rules today (no SP variant rejects no-upfront the way RDS 3yr does),
  // so this is a defensive belt-and-suspenders check rather than a
  // common case.
  const incompatible = bucketEntries.filter(([_key, recs]) => !isBucketPaymentCompatible(recs, tb.payment));

  if (bucketEntries.length > 1 || incompatible.length > 0) {
    // Multi-bucket / incompatible path: open the fan-out modal so the
    // user can review per-bucket details before submitting one
    // executePurchase call per bucket in parallel.
    // openFanOutModal is async (issue #111: it pre-fetches per-account
    // service overrides to seed each bucket's Payment default); the
    // returned promise is fire-and-forget — the modal is the surface
    // the user interacts with.
    void openFanOutModal(bucketEntries, tb);
    return;
  }

  // Single-bucket happy path: open the preview modal + submit via the
  // existing approval-request flow. The modal's "Send for approval" button
  // (wired in app.ts) picks up the recs via getPurchaseModalRecommendations.
  // openPurchaseModal is async (issue #111 (iii): per-rec override
  // prefetch); fire-and-forget — the modal is the user's surface.
  void openPurchaseModal(scaled, tb.capacity);
}
