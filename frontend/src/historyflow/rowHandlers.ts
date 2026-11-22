import * as api from '../api';
import { formatCurrency } from '../utils';
import { historyConfirmBody } from '../history-confirm-body';
import type { RevokePurchaseResult, RevokeQuote } from '../api';
import { confirmDialog } from '../confirmDialog';
import { approvalDetailsUnavailableMessage, buildApprovalDetailsBody, isPermanentDetailsError } from '../approval-details';
import { showToast } from '../toast';
import { historyState } from './historyState';
import { REVOCATION_PENDING_CONFIRMATION_COPY, AWS_MARKETPLACE_FEE_PERCENT, AWS_MARKETPLACE_NET_FACTOR, AWS_MARKETPLACE_BUYER_DISCOUNT_PERCENT, AWS_MARKETPLACE_BUYER_DISCOUNT } from './constants';
import { findHistoryRow, runHistoryRowAction } from './rowActions';

// wireRowActionHandlers binds the Approve / Cancel / Retry click handlers
// against the buttons inside `container`. Scoped to the container (NOT
// the document) so multiple mounted lists (Purchase History table +
// Approval queue card) don't cross-fire: clicking the queue's Approve
// button binds and dispatches against the queue's button instance only.
//
// All three handlers terminate with a `loadHistory()` reload on success,
// which re-renders BOTH the history table and the queue card from the
// same fetched dataset. That means a successful approve from the queue
// card removes the row from BOTH views in one shot.
export function wireRowActionHandlers(container: HTMLElement): void {
  // Wire the inline Approve button on pending rows the current session
  // may approve (issue #286). One flow: confirmDialog → POST /approve
  // (no token — bearer-session auth on apiRequest) → reload + toast.
  // Backend may still 409 on a status race (concurrent cancel landed
  // first); the catch surfaces the structured detail.
  container.querySelectorAll<HTMLButtonElement>('.history-approve-btn[data-approve-id]').forEach(btn => {
    const id = btn.dataset['approveId'];
    if (!id) return;
    btn.addEventListener('click', () => runHistoryRowAction(btn, id, async markMutationSucceeded => {
      // Issue #374: show the per-rec details (service / engine /
      // resource / region / count / term + payment / costs) in the
      // modal so the user has informed consent before authorising a
      // financial commitment. Without them, Approve is not offered (#247).
      let detailsBody: HTMLElement;
      try {
        detailsBody = await buildApprovalDetailsBody(id);
      } catch (detailsError) {
        console.error('Failed to load purchase details for approval:', detailsError);
        const permanent = isPermanentDetailsError(detailsError);
        showToast({
          message: permanent ? approvalDetailsUnavailableMessage(detailsError) : `${approvalDetailsUnavailableMessage(detailsError)} Try again.`,
          kind: 'error',
          timeout: null,
        });
        return;
      }
      const ok = await confirmDialog({
        title: 'Approve this pending purchase?',
        body: detailsBody,
        confirmLabel: 'Approve purchase',
        destructive: false,
      });
      if (!ok) return;
      try {
        await api.approvePurchase(id);
        markMutationSucceeded();
      } catch (approveError) {
        console.error('Failed to approve pending purchase:', approveError);
        const err = approveError as Error;
        showToast({ message: `Failed to approve: ${err.message || 'unknown error'}`, kind: 'error', timeout: null });
        return;
      }
      showToast({ message: 'Purchase approved', kind: 'success', timeout: 5_000 });
    }));
  });

  // Wire the inline Cancel button on pending/notified rows the current
  // session may cancel (issue #46). confirmDialog → POST → reload. The
  // backend remains the security boundary; the canCancelPendingRow
  // helper above is a UX gate that hides the button when the call would
  // 403, but a stale cache could still surface a 403 — handle it the
  // same way as any other failure.
  //
  // The cancel POST and the follow-up reload are split into separate
  // try/catch blocks: a successful cancel + failed reload must not show
  // a "Failed to cancel" toast (the purchase IS cancelled), and the
  // user should see success-toast first so they don't think their
  // click was lost while we re-fetch the table.
  container.querySelectorAll<HTMLButtonElement>('.history-cancel-btn[data-cancel-id]').forEach(btn => {
    const id = btn.dataset['cancelId'];
    if (!id) return;
    btn.addEventListener('click', () => runHistoryRowAction(btn, id, async markMutationSucceeded => {
      const ok = await confirmDialog({
        title: 'Cancel this pending purchase?',
        // Issue #728: name the purchase so the right row is confirmed.
        body: historyConfirmBody(findHistoryRow(id), 'This will permanently abort the approval flow. The pending email approval link will stop working. This action cannot be undone.'),
        confirmLabel: 'Cancel purchase',
        destructive: true,
      });
      if (!ok) return;
      try {
        await api.cancelPurchase(id);
        markMutationSucceeded();
      } catch (cancelError) {
        console.error('Failed to cancel pending purchase:', cancelError);
        const err = cancelError as Error;
        showToast({ message: `Failed to cancel: ${err.message || 'unknown error'}`, kind: 'error', timeout: null });
        return;
      }
      showToast({ message: 'Purchase cancelled', kind: 'success', timeout: 5_000 });
    }));
  });

  // Wire the inline Retry button on failed rows the current session
  // may retry (issue #47). Two flows:
  //   * normal:    confirmDialog → POST /retry → reload + toast.
  //   * over-threshold: confirmDialog with stronger warning → POST
  //     with ?force=true → reload + toast.
  // The backend may still 409 with an ops_hint or threshold response;
  // the catch block surfaces the structured detail when present.
  container.querySelectorAll<HTMLButtonElement>('.history-retry-btn[data-retry-id]').forEach(btn => {
    const id = btn.dataset['retryId'];
    if (!id) return;
    btn.addEventListener('click', () => runHistoryRowAction(btn, id, async markMutationSucceeded => {
      const overThreshold = btn.classList.contains('history-retry-over-threshold');
      const ok = await confirmDialog({
        title: overThreshold ? 'Retry past threshold?' : 'Retry this failed purchase?',
        body: historyConfirmBody(findHistoryRow(id), overThreshold
          ? 'The same recommendations have already failed multiple times. Are you sure you want to retry again? This may not succeed.'
          : 'This will create a new purchase execution from the same recommendations. The original failed row will be linked to the new attempt.'),
        confirmLabel: overThreshold ? 'Retry anyway' : 'Retry purchase',
        destructive: false,
      });
      if (!ok) return;
      let retryResult: Awaited<ReturnType<typeof api.retryPurchase>>;
      try {
        retryResult = await api.retryPurchase(id, overThreshold ? { force: true } : undefined);
        markMutationSucceeded();
      } catch (retryError) {
        console.error('Failed to retry purchase:', retryError);
        // Surface structured retry hints from the backend (issue #47):
        //   * ops_hint — operator-actionable reason; takes priority
        //   * retry_attempt_n + threshold — soft-block message
        //   * else — fall back to the raw error message
        const err = retryError as Error & { details?: Record<string, unknown> };
        const opsHint = typeof err.details?.['ops_hint'] === 'string' ? err.details['ops_hint'] : '';
        const retryAttemptN = typeof err.details?.['retry_attempt_n'] === 'number' ? err.details['retry_attempt_n'] : undefined;
        const threshold = typeof err.details?.['threshold'] === 'number' ? err.details['threshold'] : undefined;
        // A Redshift purchase that failed minutes ago may still be settling at AWS (not overridable by force).
        const retryAfterSeconds = typeof err.details?.['retry_after_seconds'] === 'number' ? err.details['retry_after_seconds'] : undefined;
        if (!opsHint && retryAfterSeconds != null) {
          const minutes = Math.max(1, Math.ceil(retryAfterSeconds / 60));
          showToast({
            message: `Retry is not possible yet: AWS may still be settling this purchase. Try again in ${minutes} minute${minutes === 1 ? '' : 's'}.`,
            kind: 'warning',
          });
          return;
        }
        let detailMessage = '';
        if (opsHint) {
          detailMessage = opsHint;
        } else if (retryAttemptN != null && threshold != null) {
          detailMessage = `already retried ${retryAttemptN} times (threshold ${threshold}) — confirm the override prompt to force`;
        }
        const finalMessage = detailMessage || err.message || 'unknown error';
        showToast({ message: `Failed to retry: ${finalMessage}`, kind: 'error', timeout: null });
        return;
      }
      // Gate the toast on the approval email outcome reported by the backend.
      // email_sent===false is an explicit failure signal that overrides any
      // status-based inference; show a warning even when status==='pending'.
      // email_sent===true or a pending/notified status (with email_sent absent)
      // means the approval request is in the queue.
      const emailExplicitlyFailed = retryResult.email_sent === false;
      const emailOk = !emailExplicitlyFailed && (
        retryResult.email_sent === true
        || retryResult.status === 'pending'
        || retryResult.status === 'notified'
      );
      if (emailOk) {
        showToast({ message: 'Purchase request sent for approval', kind: 'success', timeout: 5_000 });
      } else {
        showToast({ message: 'Retry created but approval email failed - check your notification settings', kind: 'warning', timeout: 8_000 });
      }
    }));
  });

  // Wire the inline Revoke button on completed Azure rows within the
  // free-cancel window (issue #290) and on scheduled rows of any provider
  // (#924). quote -> confirmDialog -> POST -> reload.
  // Completed rows fetch the Azure refund quote first so the user consents to
  // a concrete amount, which the backend re-checks before issuing the refund.
  // The backend is the security boundary; canRevokeCompletedRow is a
  // UX gate that hides the button when the call would fail, but a stale
  // cache can still surface a 4xx -- handle it like any other failure.
  container.querySelectorAll<HTMLButtonElement>('.history-revoke-btn[data-revoke-id]').forEach(btn => {
    const id = btn.dataset['revokeId'];
    if (!id) return;
    btn.addEventListener('click', () => runHistoryRowAction(btn, id, async markMutationSucceeded => {
      let quote: RevokeQuote | undefined;
      if (btn.dataset['revokeScheduled'] !== 'true') {
        try {
          quote = await api.getRevokeQuote(id);
        } catch (quoteError) {
          console.error('Failed to get refund quote:', quoteError);
          const err = quoteError as Error;
          showToast({ message: `Failed to get refund quote: ${err.message || 'unknown error'}`, kind: 'error' });
          return;
        }
      }
      // A scheduled row has not reached any cloud yet, so its copy must not
      // promise an Azure return: it may be an AWS or GCP purchase (#924).
      const body = quote
        ? `This will request an Azure reservation return. Azure will refund ${quote.refund_amount.toFixed(2)} ${quote.refund_currency}.`
        : 'This will cancel the scheduled purchase. It has not been submitted yet, so nothing will be charged.';
      const ok = await confirmDialog({
        title: 'Revoke this purchase within the free-cancel window?',
        body: `${body} This action cannot be undone.`,
        confirmLabel: 'Revoke purchase',
        destructive: true,
      });
      if (!ok) return;
      let result: RevokePurchaseResult;
      try {
        result = await api.revokePurchase(id, quote);
        markMutationSucceeded();
      } catch (revokeError) {
        console.error('Failed to revoke purchase:', revokeError);
        const err = revokeError as Error;
        showToast({ message: `Failed to revoke: ${err.message || 'unknown error'}`, kind: 'error', timeout: null });
        return;
      }
      if (result.code === 'REVOCATION_PENDING_CONFIRMATION') {
        // Not proven either way: say so, and do not call it a success (#1522).
        showToast({ message: REVOCATION_PENDING_CONFIRMATION_COPY, kind: 'info' });
        return;
      }
      if (result.code === 'RECONCILE_PENDING') {
        showToast({ message: result.message || 'Refund issued. We will reconcile your audit record shortly.', kind: 'warning' });
        return;
      }
      showToast({ message: 'Purchase revocation submitted', kind: 'success', timeout: 5_000 });
    }));
  });

  // Wire Sell on Marketplace button (issue #292).
  // Flow: pricing/schedule modal (RI summary + default price + 12% fee) →
  // user confirms → createMarketplaceListing. We never skip the pricing
  // modal (CR finding: going straight from confirmDialog to the API call
  // denies the user informed consent about the price and fee).
  container.querySelectorAll<HTMLButtonElement>('.history-marketplace-sell-btn[data-marketplace-sell-id]').forEach(btn => {
    btn.addEventListener('click', async () => {
      const id = btn.dataset['marketplaceSellId'];
      if (!id) return;

      // Look up the purchase record so we can show a meaningful price summary.
      const purchase = historyState.lastPurchases.find(p => p.purchase_id === id);

      if (!purchase || purchase.upfront_cost == null) {
        btn.disabled = true;
        showToast({ message: 'Upfront cost is unknown; marketplace pricing is unavailable.', kind: 'error' });
        return;
      }

      const upfront = purchase.upfront_cost;
      return runHistoryRowAction(btn, id, async markMutationSucceeded => {
        // Build a pricing modal body with RI summary and fee breakdown.
        const bodyEl = document.createElement('div');
        bodyEl.className = 'marketplace-pricing-modal-body';

        if (purchase) {
          // purchase_history.term is stored in YEARS (1 or 3); convert to months
          // before computing the remaining term and residual so the price summary
          // shown to the user reflects real remaining value rather than ~1/3 of it
          // (a 3-year RI was previously treated as 3 months). Mirrors the
          // row.Term * 12 conversion in internal/api/handler_marketplace.go.
          const termYears = typeof purchase.term === 'number' ? purchase.term : Number(purchase.term) || 0;
          const termMonths = termYears > 0 ? termYears * 12 : 0;
          const purchaseMs = new Date(purchase.timestamp).getTime();
          const elapsedMonths = Number.isFinite(purchaseMs)
            ? (Date.now() - purchaseMs) / (1000 * 60 * 60 * 24 * 30.4375)
            : 0;
          // Must match computeRemainingMonths in internal/api/handler_marketplace.go
          // EXACTLY: floor (not round), floored at 1 (not 0), so the number
          // shown here is the same number the backend actually lists at.
          const remainingMonths = Math.max(1, Math.floor(termMonths - elapsedMonths));
          const count = purchase.count > 0 ? purchase.count : 1;
          // Mirror marketplaceResidualPerUnit + resolveMarketplacePriceSchedule's
          // default branch in internal/api/handler_marketplace.go EXACTLY, so
          // this preview can never diverge from what the backend actually lists:
          //   - upfront-only: recurring (monthly) cost is deliberately excluded
          //     because the buyer assumes the recurring obligation post-transfer;
          //   - per instance: upfront_cost is the row total for `count` instances,
          //     but the AWS Marketplace price is per instance, so divide by count;
          //   - prorated: the upfront residual is scaled by remaining/original
          //     term (a 36-month RI at month 6 retains only 30/36 of its value);
          //   - zero when unpriceable: a no-upfront RI (upfront <= 0) or an
          //     unknown term (termMonths <= 0) has no residual to prorate, which
          //     is exactly when the backend now rejects the default schedule
          //     with an error instead of silently listing at $0.
          const perUnitResidual = termMonths > 0 && upfront > 0
            ? (upfront * (remainingMonths / termMonths)) / count
            : 0;
          const listPricePerUnit = perUnitResidual * AWS_MARKETPLACE_BUYER_DISCOUNT;
          const listPriceTotal = listPricePerUnit * count;
          const netProceedsTotal = listPriceTotal * AWS_MARKETPLACE_NET_FACTOR;

          const summaryEl = document.createElement('dl');
          summaryEl.className = 'marketplace-pricing-summary';
          const addRow = (label: string, value: string): void => {
            const dt = document.createElement('dt');
            dt.textContent = label;
            const dd = document.createElement('dd');
            dd.textContent = value;
            summaryEl.appendChild(dt);
            summaryEl.appendChild(dd);
          };
          addRow('RI ID', id);
          addRow('Region', purchase.region || '-');
          addRow('Resource type', purchase.resource_type || '-');
          addRow('Remaining term', remainingMonths === 1 ? '1 month' : `${remainingMonths} months`);
          if (listPricePerUnit > 0) {
            // Show cents, and derive the fee from the two rounded totals, so
            // the three lines always reconcile (fee + net = list price). The
            // dashboard's whole-dollar default rounded each line on its own,
            // e.g. $101 = $12 + $88 for a $100.50 listing (#318).
            const listCents = Math.round(listPriceTotal * 100);
            const netCents = Math.round(netProceedsTotal * 100);
            addRow('Default list price', count > 1
              ? `${formatCurrency(listPricePerUnit, '$', 2)}/unit (${formatCurrency(listCents / 100, '$', 2)} total for ${count} units)`
              : formatCurrency(listCents / 100, '$', 2));
            addRow(`AWS fee (${AWS_MARKETPLACE_FEE_PERCENT}%)`, formatCurrency((listCents - netCents) / 100, '$', 2));
            addRow('Estimated net proceeds', formatCurrency(netCents / 100, '$', 2));
          } else {
            // No default price can be computed (no upfront cost or unknown
            // term) -- listing will be rejected server-side unless a custom
            // price_schedule is supplied. Say so instead of showing a
            // misleading $0 or fabricated price.
            addRow('Default list price', 'unavailable (no upfront cost or unknown term)');
          }
          bodyEl.appendChild(summaryEl);
        }

        const noteEl = document.createElement('p');
        noteEl.className = 'marketplace-pricing-note';
        noteEl.textContent = `AWS charges a ${AWS_MARKETPLACE_FEE_PERCENT}% transaction fee on proceeds. The default schedule prices the listing at ${AWS_MARKETPLACE_BUYER_DISCOUNT_PERCENT}% below remaining value. You can adjust pricing by contacting your administrator or modifying the schedule via the API. This action cannot be undone without cancelling the listing.`;
        bodyEl.appendChild(noteEl);

        const ok = await confirmDialog({
          title: 'List this RI on the AWS Marketplace?',
          body: bodyEl,
          confirmLabel: 'Confirm listing',
          destructive: false,
        });
        if (!ok) return;

        try {
          await api.createMarketplaceListing(id);
          markMutationSucceeded();
        } catch (sellError) {
          console.error('Failed to list RI on Marketplace:', sellError);
          const err = sellError as Error;
          showToast({ message: `Failed to list on Marketplace: ${err.message || 'unknown error'}`, kind: 'error' });
          return;
        }
        showToast({ message: 'RI listed on Marketplace successfully', kind: 'success', timeout: 5_000 });
      });
    });
  });

  // Wire Cancel listing button (issue #292)
  container.querySelectorAll<HTMLButtonElement>('.history-marketplace-cancel-btn[data-marketplace-cancel-id]').forEach(btn => {
    const id = btn.dataset['marketplaceCancelId'];
    if (!id) return;
    btn.addEventListener('click', () => runHistoryRowAction(btn, id, async markMutationSucceeded => {
      const ok = await confirmDialog({
        title: 'Cancel this Marketplace listing?',
        body: 'This will remove the listing from the AWS Marketplace. Any existing buyer negotiations will be cancelled. You can relist the RI at any time.',
        confirmLabel: 'Cancel listing',
        destructive: true,
      });
      if (!ok) return;
      try {
        await api.cancelMarketplaceListing(id);
        markMutationSucceeded();
      } catch (cancelError) {
        console.error('Failed to cancel Marketplace listing:', cancelError);
        const err = cancelError as Error;
        showToast({ message: `Failed to cancel listing: ${err.message || 'unknown error'}`, kind: 'error' });
        return;
      }
      showToast({ message: 'Marketplace listing cancelled', kind: 'success', timeout: 5_000 });
    }));
  });
}
