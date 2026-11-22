import * as api from '../api';
import { loadDashboard } from '../dashboard';
import { clearPurchaseModalRecommendations, clearFanOutBuckets, fanOutBucketAccountLabel, type FanOutBucket } from '../recommendations';
import { closePurchaseModal } from '../plans';
import { showToast } from '../toast';
import { formatPaymentAdjustmentNotice } from '../commitmentOptions';
import { openArcheraOfferModal } from '../archera';
import { showFanOutRetry, purchaseModalReplaced, type FanOutBucketOutcome } from '../fanout-retry';
import { duplicateState, describeDuplicateSubmit } from './shared';

/**
 * fanOutBucketLabel renders a short human identifier for a fan-out bucket so a
 * partial-failure toast can name which buckets created pending executions
 * (issue #642). Uses the provider / account / service @ capacity% tuple, laid
 * out like the bucket header the user just saw in the modal, so the
 * orphaned-but-actionable pending requests are identifiable rather than hidden
 * behind a bare count. Buckets are per account (#333), so without the account
 * a two-account split of one service read as the same label twice (#434). The
 * account uses the header's label (name, else ID, else 'Unattributed'); the
 * toast renders it via textContent.
 */
function fanOutBucketLabel(b: FanOutBucket): string {
  const provider = (b.provider || '').toString().toUpperCase();
  const service = b.service || 'commitment';
  const cap = Number.isFinite(b.capacityPercent) ? `@${b.capacityPercent}%` : '';
  return [provider, fanOutBucketAccountLabel(b), `${service}${cap}`].filter(Boolean).join(' / ');
}

/**
 * Per-bucket outcome for the retry view (issue #757). Uses the same
 * success rule as the toast: a fulfilled POST whose approval email failed
 * is a failure.
 */
function fanOutOutcomes(
  buckets: FanOutBucket[],
  results: PromiseSettledResult<api.PurchaseResult>[],
): FanOutBucketOutcome[] {
  return results.map((r, i) => {
    const bucket = buckets[i]!;
    if (r.status === 'rejected') {
      return { bucket, ok: false, detail: r.reason instanceof Error ? r.reason.message : String(r.reason) };
    }
    if (r.value.email_sent === false || r.value.status === 'failed') {
      return { bucket, ok: false, detail: r.value.email_reason || 'approval email did not send' };
    }
    if (r.value.duplicate) {
      return { bucket, ok: true, detail: `joined existing request ${r.value.execution_id.slice(0, 8)}; ${duplicateState(r.value.status)}` };
    }
    const to = r.value.approval_recipient ? ` from ${r.value.approval_recipient}` : '';
    return { bucket, ok: true, detail: `awaiting approval${to}` };
  });
}

export async function submitFanOutBuckets(buckets: FanOutBucket[]): Promise<void> {
  // Fire all POSTs in parallel via allSettled so one failure doesn't
  // cascade. Each bucket's recs are already scaled by its capacity %;
  // the POST body records capacity_percent for audit. Spread the full
  // server-provided rec so `details`, `engine`, `cloud_account_id`, and
  // any future additions flow through unchanged. Only `payment`,
  // `monthly_cost`, `selected`, and `purchased` are overridden: `payment`
  // comes from the bucket-level payment.
  // `monthly_cost` is coerced to null for absent values, and the
  // purchase-intent flags are forced to their canonical values. Passing
  // `details` ensures non-default platforms (Windows EC2, dedicated tenancy,
  // AZ-scoped RIs, non-default-engine RDS/Cache) reach the backend correctly
  // (issue #597).
  const promises = buckets.map((b) =>
    api.executePurchase(
      b.recs.map((r) => ({
        ...r,
        monthly_cost: r.monthly_cost ?? null,
        payment: b.payment,
        selected: true,
        purchased: false,
      })),
      b.capacityPercent,
    ),
  );
  const results = await Promise.allSettled(promises);

  // Reclassify business-level email failures: a fulfilled POST that returns
  // email_sent === false or status === 'failed' is not a true success —
  // the approval email never went out (CR pass on PR #294 Finding 2).
  const fulfilled = results.filter(
    (r): r is PromiseFulfilledResult<api.PurchaseResult> => r.status === 'fulfilled',
  );
  const submissionFailures = fulfilled.filter(
    (r) => r.value.email_sent === false || r.value.status === 'failed',
  );
  const succeeded = fulfilled.length - submissionFailures.length;
  const failed = results.length - succeeded;
  // Issue #757: on any failure keep the modal open with per-bucket status
  // and a retry limited to the failed buckets, instead of closing it and
  // making the user reselect rows (and risk resubmitting the successes).
  // If the user closed the modal while the POSTs were in flight, report by
  // toast as before. If they already opened another purchase modal, leave
  // that one alone: it is not the one these results belong to.
  // (Checked before showFanOutRetry, which narrows the modal's buckets.)
  const replaced = purchaseModalReplaced(buckets);
  const retrying = failed > 0 && showFanOutRetry(fanOutOutcomes(buckets, results));
  if (!retrying && !replaced) {
    closePurchaseModal();
    clearFanOutBuckets();
    clearPurchaseModalRecommendations();
  }

  // Disclose payment-option coercions across every bucket that reached the
  // backend (#1503). Collected from all fulfilled responses, not just the
  // truly-succeeded ones: a bucket whose approval email failed to send still
  // created a pending execution carrying the coerced billing schedule, so the
  // user needs to know before they approve it from History.
  const fanOutNotice = formatPaymentAdjustmentNotice(
    fulfilled.flatMap((r) => r.value.payment_adjustments ?? []),
  );
  if (fanOutNotice) {
    // Sticky as before, except in retry mode, where it would cover the
    // modal's Retry button; it then expires on the default timeout.
    showToast({ message: fanOutNotice, kind: 'warning', ...(retrying ? {} : { timeout: null }) });
  }

  if (failed === 0) {
    // Collect the unique approval-recipient set from truly-succeeded responses
    // only (email_sent !== false and status !== 'failed') so the toast doesn't
    // name a recipient whose email never arrived. Multi-bucket purchases can
    // route to different approvers; dedupe so the toast is compact.
    const recipients = new Set<string>();
    const joined: string[] = [];
    let fresh = 0;
    for (const r of fulfilled) {
      if (r.value.duplicate) {
        joined.push(describeDuplicateSubmit(r.value));
        continue;
      }
      fresh++;
      if (
        r.value.email_sent !== false &&
        r.value.status !== 'failed' &&
        r.value.approval_recipient
      ) {
        recipients.add(r.value.approval_recipient);
      }
    }
    for (const message of joined) {
      showToast({ message, kind: 'info', timeout: 15_000 });
    }
    if (fresh > 0) {
      const noun = fresh === 1 ? 'purchase' : 'purchases';
      let message: string;
      if (recipients.size === 0) {
        message = `${fresh} ${noun} submitted — check your email to approve each.`;
      } else if (recipients.size === 1) {
        message = `${fresh} ${noun} sent for approval to ${[...recipients][0]}.`;
      } else {
        message = `${fresh} ${noun} sent for approval to ${recipients.size} recipients (${[...recipients].sort().join(', ')}).`;
      }
      showToast({
        message,
        kind: 'success',
        timeout: 15_000,
      });
    }
  } else if (!retrying) {
    // Issue #757: skipped while the modal stays open for a retry. The modal
    // then shows every bucket's status and reason in full, and a sticky toast
    // would sit on top of its Retry button.
    // Prefix each failure with its bucket's account so the user knows what to
    // retry when one account's bucket fails and another's succeeds (issue #333).
    const failureMsgs = results
      .flatMap((r, i) => {
        let reason: string;
        if (r.status === 'rejected') {
          reason = r.reason instanceof Error ? r.reason.message : String(r.reason);
        } else if (r.value.email_sent === false || r.value.status === 'failed') {
          reason = r.value.email_reason || 'approval email did not send';
        } else {
          return [];
        }
        return [`${fanOutBucketAccountLabel(buckets[i]!)}: ${reason}`];
      })
      .slice(0, 3)
      .join('; ');
    // #642: on a partial fan-out failure the succeeded buckets created real
    // pending executions an approver can still act on. Name them so the user
    // knows which requests are live (and which to re-submit) rather than
    // leaving them as silent orphans behind a bare count.
    let submittedNote = '';
    if (succeeded > 0 && succeeded < results.length) {
      const submittedLabels: string[] = [];
      const joinedLabels: string[] = [];
      results.forEach((r, i) => {
        const ok =
          r.status === 'fulfilled' &&
          r.value.email_sent !== false &&
          r.value.status !== 'failed';
        if (!ok || !buckets[i]) return;
        if (r.value.duplicate) {
          joinedLabels.push(
            `${fanOutBucketLabel(buckets[i])} (existing ${r.value.execution_id.slice(0, 8)}: ${duplicateState(r.value.status).replace(/^(it was|it is|it) /, '')})`,
          );
        } else {
          submittedLabels.push(fanOutBucketLabel(buckets[i]));
        }
      });
      if (joinedLabels.length > 0) {
        submittedNote += ` Joined existing: ${joinedLabels.join(', ')}.`;
      }
      if (submittedLabels.length > 0) {
        submittedNote += ` Submitted (awaiting approval): ${submittedLabels.slice(0, 5).join(', ')}${
          submittedLabels.length > 5 ? ` +${submittedLabels.length - 5} more` : ''
        }.`;
      }
    }
    showToast({
      message: `${succeeded} of ${results.length} submitted · ${failed} failed: ${failureMsgs}${failed > 3 ? ' (…)' : ''}${submittedNote}`,
      kind: failed === results.length ? 'error' : 'warning',
      timeout: null,
    });
  }
  // Offer Archera Insurance when at least one bucket's approval submission
  // succeeded (issue #499 follow-up). Fires right after the user approves
  // the pre-purchase confirmation and the calls resolve, not after the
  // async email-link execution completes. Skipping the all-fail path keeps
  // the modal from layering on top of an error toast for a user who has
  // nothing to insure yet.
  // Not while a purchase modal is open (issue #757): the retry view, or a
  // newer modal the user opened before these POSTs settled. The offer would
  // stack on top of it. In retry mode it fires once a retry closes the modal.
  if (succeeded > 0 && !retrying && !replaced) {
    openArcheraOfferModal();
  }

  await loadDashboard();
}
