import * as api from '../api';
import { loadDashboard } from '../dashboard';
import { getPurchaseModalRecommendations, getPurchaseModalSession, clearPurchaseModalRecommendations, getFanOutBuckets, getExecuteMode, clearExecuteMode, getAccountName } from '../recommendations';
import { closePurchaseModal } from '../plans';
import { showToast } from '../toast';
import { formatPaymentAdjustmentNotice } from '../commitmentOptions';
import { confirmDialog } from '../confirmDialog';
import { buildCommitmentSummary, commitmentLine, commitmentTotals, executeButtonLabel, executeConfirmLabel, withIntro } from '../commitmentSummary';
import { openArcheraOfferModal } from '../archera';
import { describeDuplicateSubmit, setExecutePurchaseSubmitting } from './shared';
import { handleFanOutExecute } from './fanout';

/** Strict Capacity % parse: a whole number 1-100, never coerced. */
export function parseCapacityPercent(raw: string): number | { error: string } {
  const text = raw.trim();
  const n = Number(text);
  if (text === '' || !Number.isInteger(n) || n < 1 || n > 100) {
    return { error: 'Capacity % must be a whole number from 1 to 100.' };
  }
  return n;
}
/**
 * Handle execute purchase button click. Routes to the single-bucket
 * path when getPurchaseModalRecommendations has content, or to the
 * multi-bucket fan-out path when the fan-out modal set buckets.
 */
export async function handleExecutePurchase(): Promise<void> {
  const fanOutBuckets = getFanOutBuckets();
  if (fanOutBuckets && fanOutBuckets.length > 0) {
    await handleFanOutExecute(fanOutBuckets);
    return;
  }
  // fanOutBuckets !== null means fan-out mode is active but every bucket
  // was skipped (unpriced/unavailable) -- distinct from null (fan-out was
  // never opened). Refuse rather than silently falling through to
  // getPurchaseModalRecommendations()'s single-row state, which could be
  // stale leftovers from a different selection (issue #331). In practice
  // the Execute button is already disabled in this state; this is
  // defense-in-depth, not a workaround for a reachable click.
  if (fanOutBuckets !== null) {
    showToast({ message: 'No recommendations selected for purchase.', kind: 'warning' });
    return;
  }

  const localRecs = getPurchaseModalRecommendations();
  const modalSession = getPurchaseModalSession();
  if (localRecs.length === 0) {
    showToast({ message: 'No recommendations selected for purchase.', kind: 'warning' });
    return;
  }

  // Refuse before the confirm dialog and the POST when any rec has no payment
  // option. Defaulting here would silently pick the most cash-intensive
  // schedule on a money path (issue #431).
  const recsWithPayment = localRecs.filter(
    (r): r is typeof r & { payment: string } => !!r.payment?.trim(),
  );
  const missingCount = localRecs.length - recsWithPayment.length;
  if (missingCount > 0) {
    showToast({
      message: `Cannot submit: ${missingCount} of ${localRecs.length} selected recommendation${localRecs.length === 1 ? '' : 's'} ${missingCount === 1 ? 'has' : 'have'} no payment option. Choose a payment option for each row and try again.`,
      kind: 'error',
    });
    return;
  }

  // Read the sticky toolbar Capacity % so the execution record carries the
  // user's intent. Parsed strictly and before the confirm dialog: 0, blank or
  // fractional input used to coerce to 100 or truncate, submitting something
  // other than what was typed. Absent toolbar (test harness) means 100.
  const capacityInput = document.getElementById('bulk-purchase-capacity') as HTMLInputElement | null;
  const capacity = capacityInput ? parseCapacityPercent(capacityInput.value) : 100;
  if (typeof capacity !== 'number') {
    showToast({ message: capacity.error, kind: 'error' });
    return;
  }
  const capacityPercent = capacity;

  // Read the execute mode set by the modal toggle (issue #289).
  // "direct" means the session has execute-any/execute-own and chose to
  // bypass approval; "" is the default approval-required path.
  const executeMode = getExecuteMode();
  const isDirect = executeMode === 'direct';

  // Disable the button BEFORE awaiting the confirm dialog and the network
  // call so a double-click or rapid re-click can't fire a second POST and
  // mint a duplicate pending execution (#644). The button is re-enabled on
  // cancel below and in the finally block once the request settles.
  const executeBtn = document.getElementById('execute-purchase-btn') as HTMLButtonElement | null;
  setExecutePurchaseSubmitting(executeBtn, true, 'Sending...');

  // Confirmation dialog varies by mode:
  //   - Approval path: low-friction, non-destructive.
  //   - Direct-execute path: red destructive dialog with cost callout and
  //     immediate-charge callout; no cancellation promise (issue #251).
  const summaryLines = recsWithPayment.map((r) => commitmentLine(
    r,
    r.cloud_account_id ? { account: getAccountName(r.cloud_account_id) } : {},
  ));
  const totals = commitmentTotals(summaryLines);
  const summary = buildCommitmentSummary(
    summaryLines,
    isDirect ? {} : { upfrontLabel: 'Upfront on approval' },
  );
  const ok = isDirect
    ? await confirmDialog({
        title: `Execute ${localRecs.length} purchase${localRecs.length === 1 ? '' : 's'} now?`,
        body: withIntro('This charges the amount shown under "Charged today" immediately and bypasses the approval step. The monthly amount, if any, is billed for the whole term.', summary),
        confirmLabel: executeConfirmLabel(true, totals.upfront),
        destructive: true,
      })
    : await confirmDialog({
        title: `Send ${localRecs.length} purchase${localRecs.length === 1 ? '' : 's'} for approval?`,
        body: withIntro('This emails an approval request to the configured approver. Nothing is charged until the approver clicks the link in that email.', summary),
        confirmLabel: executeConfirmLabel(false, totals.upfront),
        destructive: false,
      });

  if (!ok) {
    setExecutePurchaseSubmitting(executeBtn, false, executeButtonLabel(getExecuteMode() === 'direct'));
    return;
  }

  // Build the POST body recs by spreading the server-provided rec so that
  // all fields (including `details`, `engine`, `cloud_account_id`, and any
  // future additions) flow through unchanged. Only `payment`, `selected`,
  // and `purchased` are overridden: `payment` uses the user-edited value
  // (issue #111), and `selected`/`purchased` are forced to the canonical
  // purchase-intent values. A missing `payment` was refused above, so it is
  // never defaulted here. Passing `details` ensures
  // Windows EC2, dedicated-tenancy, AZ-scoped RIs, and non-default-engine
  // RDS/Cache recs reach the backend with the correct ServiceDetails payload
  // instead of falling back to Linux/regional/default (issue #597).
  const apiRecs: api.Recommendation[] = recsWithPayment.map((r) => ({
    ...r,
    monthly_cost: r.monthly_cost ?? null,
    payment: r.payment,
    selected: true,
    purchased: false,
  }));

  try {
    const result = await api.executePurchase(apiRecs, capacityPercent, executeMode || undefined);
    closePurchaseModal();
    clearPurchaseModalRecommendations();
    clearExecuteMode();

    if (result.duplicate) {
      showToast({
        message: describeDuplicateSubmit(result),
        kind: 'info',
        timeout: 15_000,
      });
    } else if (isDirect) {
      // Direct-execute: purchase is already committed; inform the user.
      showToast({
        message: `Purchase executed immediately (id ${result.execution_id.slice(0, 8)}). Check Purchase History for the result.`,
        kind: 'success',
        timeout: 15_000,
      });
    } else if (result.email_sent === false) {
      // The backend now surfaces email-send status so the toast can be honest
      // about what the user should do next. When email_sent is undefined we
      // fall back to the old "check your email" message for backward compat
      // with any pre-deploy caller that hasn't picked up the new field yet.
      const reason = result.email_reason || 'reason unavailable';
      showToast({
        message: `Purchase queued as pending (id ${result.execution_id.slice(0, 8)}) but the approval email did not send: ${reason}. Approve or cancel it from the Purchase History tab.`,
        kind: 'warning',
        timeout: null,
      });
    } else {
      // Name the approver in the success toast so the user can confirm WHO
      // received the request (CR pass on PR #294 / issue #288). The backend
      // surfaces `approval_recipient` from `resolveApprovalRecipients`; the
      // field may be absent on older deploys or when only the global notify
      // mailbox is configured — fall back to the generic line in that case.
      const recipient = result.approval_recipient;
      showToast({
        message: recipient
          ? `Approval request sent to ${recipient}.`
          : 'Purchase submitted - check your email to approve.',
        kind: 'success',
        timeout: 10_000,
      });
    }
    // Disclose any payment-option coercion the backend applied (#1503). This
    // is a SEPARATE, non-expiring warning toast rather than an addition to the
    // success copy above: the billing schedule changing is the one thing the
    // user did not ask for, and on Azure it cannot be changed after purchase,
    // so it must not scroll away inside a success message.
    const paymentNotice = formatPaymentAdjustmentNotice(result.payment_adjustments);
    if (paymentNotice) {
      showToast({ message: paymentNotice, kind: 'warning', timeout: null });
    }

    // Offer Archera Insurance immediately after the user approves the
    // pre-purchase confirmation and the approval-submission call succeeds
    // (issue #499 follow-up). Firing here, rather than after the async
    // email-link execution completes, surfaces the optional coverage at
    // the moment the user has committed their intent. Gated on the
    // executePurchase success path so a failed submission shows no offer.
    openArcheraOfferModal();
    await loadDashboard();
  } catch (error) {
    const err = error as Error;
    const verb = isDirect ? 'execute' : 'send for approval';
    showToast({ message: `Failed to ${verb} purchase: ${err.message}`, kind: 'error' });
    // A recorded execution, a 409 after the claim or any 5xx may follow a real
    // purchase: close the modal so one more click can't start another. Only
    // act on the modal session that sent this request.
    if (attemptMayHaveRun(err) && getPurchaseModalSession() === modalSession) {
      closePurchaseModal();
      clearPurchaseModalRecommendations();
      clearExecuteMode();
    }
  } finally {
    setExecutePurchaseSubmitting(executeBtn, false, executeButtonLabel(getExecuteMode() === 'direct'));
  }
}

// attemptMayHaveRun is false only for transport failures and 4xx refusals
// other than 409, which the server answers before any execution.
function attemptMayHaveRun(err: unknown): boolean {
  const e = err as { status?: number; details?: { execution_id?: unknown } };
  const status = e.status ?? 0;
  return Boolean(e.details?.execution_id) || status >= 500 || status === 409;
}
