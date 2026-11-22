import * as api from '../api';
import { getPurchaseModalRecommendations, getFanOutBuckets } from '../recommendations';

/**
 * describeDuplicateSubmit words the outcome of a submit the backend collapsed
 * onto an earlier execution (duplicate=true, #758). It names that execution
 * and its real state, so a direct-mode submit that joined a still-pending
 * approval never claims it executed immediately (#759).
 */
export function duplicateState(status: string): string {
  // Exactly the statuses the duplicate lookup can return
  // (internal/config/store_postgres.go); anything else gets a neutral line so
  // a backend change cannot produce a false claim.
  switch (status) {
    case 'pending':
    case 'notified':
      return 'it is still awaiting approval';
    case 'approved':
    case 'running':
      return 'it is being executed';
    case 'completed':
      return 'it was already executed';
    case 'partially_completed':
      return 'it was partially executed';
    default:
      return `its status is ${status}`;
  }
}

export function describeDuplicateSubmit(result: api.PurchaseResult): string {
  const id = result.execution_id.slice(0, 8);
  return `This matches an existing execution ${id}, so no new request was created; ${duplicateState(result.status)}. Check Purchase History.`;
}
export function setExecutePurchaseSubmitting(
  executeBtn: HTMLButtonElement | null,
  submitting: boolean,
  label: string,
): void {
  if (!executeBtn) return;

  executeBtn.textContent = label;
  if (submitting) {
    executeBtn.dataset['submitting'] = 'true';
    executeBtn.disabled = true;
    executeBtn.title = 'Purchase submission in progress';
    return;
  }

  delete executeBtn.dataset['submitting'];
  const fanOutBuckets = getFanOutBuckets();
  const unavailable = fanOutBuckets !== null
    ? fanOutBuckets.length === 0
    : getPurchaseModalRecommendations().length === 0;
  executeBtn.disabled = unavailable;
  executeBtn.title = unavailable
    ? fanOutBuckets !== null ? 'No compatible buckets to submit' : 'Select at least one purchase'
    : '';
}
