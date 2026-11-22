/**
 * History module for CUDly
 */


import * as state from './state';
import { toLocalDateInputValue } from './lib/local-date';
import { historyState } from './historyflow/historyState';
import { syncAmortizeCheckbox } from './historyflow/amortize';
import { renderHistoryList } from './historyflow/historyList';
import { renderApprovalQueue } from './historyflow/approvalQueue';
import { loadHistory, loadMoreHistory } from './historyflow/load';

/**
 * Setup history filter event handlers.
 *
 * Provider/account filters are global (sourced from state.ts via the
 * topbar chips). The history-section's own controls are just the date range
 * (a change reloads) and the Refresh button; those stay here.
 */
export function setupHistoryHandlers(): void {
  for (const id of ['history-start', 'history-end']) {
    document.getElementById(id)?.addEventListener('change', () => void loadHistory());
  }
  document.getElementById('history-load-more-btn')?.addEventListener('click', () => void loadMoreHistory());
  state.subscribeProvider(() => void loadHistory());
  state.subscribeAccount(() => void loadHistory());
  // Re-render both tables when the amortize toggle flips (issue #1112).
  state.subscribeAmortizeUpfront(() => {
    renderHistoryList(historyState.lastPurchases);
    renderApprovalQueue(historyState.lastPurchases);
    syncAmortizeCheckbox('history-amortize-checkbox');
    syncAmortizeCheckbox('approval-queue-amortize-checkbox');
  });
}

/**
 * Initialize history date range.
 *
 * Defaults to 90 days, the same window as the Savings History card on the
 * same page (`#savings-period` in index.html), so both open to one span
 * (issue #1681).
 */
export function initHistoryDateRange(): void {
  const end = new Date();
  const start = new Date();
  start.setDate(start.getDate() - 90);

  const startInput = document.getElementById('history-start') as HTMLInputElement | null;
  const endInput = document.getElementById('history-end') as HTMLInputElement | null;

  if (startInput && !startInput.value) {
    startInput.value = toLocalDateInputValue(start);
  }
  if (endInput && !endInput.value) {
    endInput.value = toLocalDateInputValue(end);
  }
}

export { readDeepLinkExecutionID, applyExecutionDeepLink } from './historyflow/deepLink';
export { purchaseHistoryNumericCellValue, applyPurchaseHistoryColumnFilters } from './historyflow/historyColumns';
export { applyApprovalQueueColumnFilters, renderApprovalQueue } from './historyflow/approvalQueue';
export { viewPlanHistory, loadHistory, loadMoreHistory } from './historyflow/load';
