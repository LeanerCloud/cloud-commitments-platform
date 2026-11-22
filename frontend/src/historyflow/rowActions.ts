import type { HistoryPurchase } from '../types';
import { showToast } from '../toast';
import { historyState } from './historyState';
import { loadHistory } from './load';

const busyHistoryExecutions = new Set<string>();

// sameRowActions returns the set of row-action buttons (Approve, Cancel,
// future siblings) in the same table cell as `btn`. Used by the Approve
// and Cancel click handlers to disable BOTH actions for the in-flight
// row while the API request is pending — issue #286 + CR pass on
// PR #299: clicking Approve disabled only Approve, leaving the
// adjacent Cancel button live; a quick double-click could fire
// conflicting requests on the same row before the reload completes.
//
// Falls back to `[btn]` when the button has no parent <td> (test
// fixtures may render buttons without a wrapping cell).
function sameRowActions(btn: HTMLButtonElement): HTMLButtonElement[] {
  const cell = btn.closest('td') || btn.parentElement;
  if (!cell) return [btn];
  return Array.from(
    cell.querySelectorAll<HTMLButtonElement>(
      '.history-approve-btn, .history-cancel-btn, .history-retry-btn, .history-revoke-btn, .history-marketplace-sell-btn, .history-marketplace-cancel-btn',
    ),
  );
}

/** The loaded History or Approval queue row for an execution ID. */
export function findHistoryRow(id: string): HistoryPurchase | undefined {
  return historyState.lastPurchases.find(p => p.purchase_id === id) ?? historyState.lastPendingForQueue.find(p => p.purchase_id === id);
}

export async function runHistoryRowAction(
  btn: HTMLButtonElement,
  id: string,
  action: (markMutationSucceeded: () => void) => Promise<void>,
): Promise<void> {
  if (btn.disabled || busyHistoryExecutions.has(id)) return;
  const buttons = sameRowActions(btn).filter(button => !button.disabled);
  busyHistoryExecutions.add(id);
  buttons.forEach(button => { button.disabled = true; });
  let mutationSucceeded = false;
  try {
    await action(() => {
      mutationSucceeded = true;
      historyState.historyMutationEpoch++;
      historyState.lastPurchases = historyState.lastPurchases.filter(purchase => purchase.purchase_id !== id);
      historyState.lastPendingForQueue = historyState.lastPendingForQueue.filter(purchase => purchase.purchase_id !== id);
    });
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    showToast({
      kind: mutationSucceeded ? 'warning' : 'error',
      message: mutationSucceeded
        ? `Purchase action succeeded, but the display could not update: ${message}`
        : `Failed to complete purchase action: ${message}`,
    });
  } finally {
    try {
      if (mutationSucceeded) {
        await loadHistory();
      } else {
        buttons.forEach(button => { button.disabled = false; });
        if (btn.isConnected && document.activeElement === document.body) btn.focus();
      }
    } finally {
      busyHistoryExecutions.delete(id);
    }
  }
}
