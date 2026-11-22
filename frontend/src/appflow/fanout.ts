import { getAccountName, type FanOutBucket } from '../recommendations';
import { confirmDialog } from '../confirmDialog';
import { buildCommitmentSummary, commitmentLine, commitmentTotals, executeButtonLabel, executeConfirmLabel, withIntro } from '../commitmentSummary';
import { fanOutRetryLabel } from '../fanout-retry';
import { setExecutePurchaseSubmitting } from './shared';
import { submitFanOutBuckets } from './fanoutSubmit';

/**
 * handleFanOutExecute submits one executePurchase POST per bucket.
 *
 * The backend API is a per-execution endpoint — a multi-bucket purchase
 * is N independent submissions, each with its own approval email. The
 * user already saw "Will send N approval emails" in the modal, so no
 * further confirmation is required here beyond the standard destructive
 * confirmDialog.
 */
export async function handleFanOutExecute(buckets: FanOutBucket[]): Promise<void> {
  // Disable the button BEFORE the confirm dialog and the parallel POSTs so a
  // double-click can't fan out a second wave of duplicate executions (#644).
  // Re-enabled on cancel below and after the calls settle at the end.
  const executeBtn = document.getElementById('execute-purchase-btn') as HTMLButtonElement | null;
  setExecutePurchaseSubmitting(executeBtn, true, `Sending 0/${buckets.length}…`);

  // Same approval-required default as the single-purchase path: each
  // bucket POSTs a request that triggers an approval email; the actual
  // charges fire when each approver clicks the link in their email.
  // State the monthly charge and the term total too (issue #711): for
  // no-upfront and partial-upfront buckets the upfront alone reads $0
  // while the account is billed monthly for one or three years. The
  // totals are combined across every bucket and the button carries the
  // upfront amount, as on the single-purchase confirm (#707). A
  // submittable bucket's recs already carry the bucket's payment option,
  // so their costs are the ones that will be approved.
  const summaryLines = buckets.flatMap((b) => b.recs.map((r) => commitmentLine(
    r,
    { payment: b.payment, ...(r.cloud_account_id ? { account: getAccountName(r.cloud_account_id) } : {}) },
  )));
  const totals = commitmentTotals(summaryLines);
  const ok = await confirmDialog({
    title: `Send ${buckets.length} bulk purchase${buckets.length === 1 ? '' : 's'} for approval?`,
    body: withIntro(
      `This will submit ${buckets.length} separate purchase request${buckets.length === 1 ? '' : 's'} and email ${buckets.length} approval request${buckets.length === 1 ? '' : 's'}. Each must be approved individually before its commitments are charged. Totals below are across all requests.`,
      buildCommitmentSummary(summaryLines, { upfrontLabel: 'Upfront on approval' }),
    ),
    confirmLabel: executeConfirmLabel(false, totals.upfront),
    destructive: false,
  });
  if (!ok) {
    setExecutePurchaseSubmitting(executeBtn, false, executeButtonLabel(false));
    return;
  }

  try {
    await submitFanOutBuckets(buckets);
  } finally {
    // Issue #757: after a partial failure the modal stays open in retry mode.
    setExecutePurchaseSubmitting(executeBtn, false, fanOutRetryLabel() ?? executeButtonLabel(false));
  }
}
