import * as api from '../api';
import { buildCommitmentSummary, withIntro } from '../commitmentSummary';
import { formatDate } from '../utils';
import { showToast } from '../toast';
import { confirmDialog } from '../confirmDialog';
import type { PlannedPurchase } from '../api';
import { plansState } from './plansState';
import { loadPlannedPurchases } from '../plans';
import { loadPlans } from '../plans';
import { editPlan } from '../plans';

// Purchase ids whose "Run now" confirm or request is open, so a second click
// on the row button cannot stack a second dialog or send a second request.
const runNowInFlight = new Set<string>();

function runNowBody(target: PlannedPurchase | undefined): string | HTMLElement {
  if (!target) return 'This immediately executes the purchase.';
  const summary = buildCommitmentSummary([{
    service: target.service,
    provider: target.provider,
    region: target.region,
    resourceType: target.resource_type,
    count: target.count,
    term: target.term,
    payment: target.payment,
    upfront: target.upfront_cost,
    // Planned purchases carry no recurring-cost breakdown.
    monthly: null,
  }]);
  return withIntro(`This immediately buys the commitment below for plan "${target.plan_name}", scheduled for ${formatDate(target.scheduled_date)}. It cannot be undone.`, summary);
}

/** Resolves false when nothing ran and the list needs no reload. */
async function runPlannedPurchaseNow(purchaseId: string): Promise<boolean> {
  if (runNowInFlight.has(purchaseId)) return true;
  runNowInFlight.add(purchaseId);
  try {
    const target = plansState.lastLoadedPurchases.find(p => p.id === purchaseId);
    if (target?.has_recommendations === false) {
      showToast({ message: 'This step has no recommendations attached, so there is nothing to run.', kind: 'info', timeout: 5_000 });
      return false;
    }
    const ok = await confirmDialog({
      title: target ? `Run step ${target.step_number}/${target.total_steps} of "${target.plan_name}" now?` : 'Run purchase now?',
      body: runNowBody(target),
      confirmLabel: 'Run now',
      destructive: true,
    });
    if (!ok) return true;
    await api.runPlannedPurchase(purchaseId);
    showToast({ message: 'Purchase executed successfully', kind: 'success', timeout: 5_000 });
    return true;
  } finally {
    runNowInFlight.delete(purchaseId);
  }
}

// The reload replaces the row the confirm dialog restored focus to, so
// focus would fall to <body>; re-target the same control, else the heading.
function restorePlannedPurchaseFocus(action: string, purchaseId: string): void {
  const rows = document.querySelectorAll<HTMLElement>('#planned-purchases-list [data-action][data-id]');
  const control = Array.from(rows).find(el => el.dataset['action'] === action && el.dataset['id'] === purchaseId);
  if (control) {
    control.focus();
    return;
  }
  const heading = document.querySelector<HTMLElement>('#planned-purchases-header h2');
  if (heading) {
    heading.setAttribute('tabindex', '-1');
    heading.focus();
  }
}

/**
 * Handle planned purchase action
 */
export async function handlePlannedPurchaseAction(action: string, purchaseId: string, planId = ''): Promise<void> {
  try {
    let declinedConfirm = false;
    switch (action) {
      case 'run':
        // An empty step only toasts; a reload would drop keyboard focus.
        if (!(await runPlannedPurchaseNow(purchaseId))) return;
        break;
      case 'pause':
        // Pause is reversible and scoped to a single execution: the plan stays
        // enabled (unlike Disable plan) and the row stays listed with a Paused
        // badge (unlike the old silent-removal behaviour).
        await api.pausePlannedPurchase(purchaseId);
        showToast({ message: 'Purchase paused', kind: 'success', timeout: 5_000 });
        break;
      case 'resume':
        await api.resumePlannedPurchase(purchaseId);
        showToast({ message: 'Purchase resumed', kind: 'success', timeout: 5_000 });
        break;
      case 'edit':
        // Open edit modal for the parent plan using plan_id, not the purchase id.
        // The purchase row's data-plan-id attribute carries the plan FK (#773).
        if (!planId) {
          console.warn('edit action ignored: missing plan id');
          return;
        }
        // If the plan can't be loaded (deleted or no longer accessible while
        // the row was on screen), reconcile the list so the orphaned row is
        // dropped instead of leaving a dead Edit button (issue #1403).
        if (!(await editPlan(planId))) {
          await loadPlannedPurchases();
        }
        return;
      case 'disable': {
        // Use styled async dialog (11-L2) instead of blocking browser confirm().
        const disableOk = await confirmDialog({
          title: 'Cancel this scheduled purchase?',
          body: 'This scheduled purchase will be canceled, and its plan will be disabled so no further purchases are scheduled from it. You can re-enable the plan later from the Plans list.',
          confirmLabel: 'Cancel purchase and disable plan',
          cancelLabel: 'Keep it',
          destructive: true,
        });
        if (disableOk) {
          await api.deletePlannedPurchase(purchaseId);
          // Reload full plans list since we disabled a plan
          await loadPlans();
          return;
        }
        declinedConfirm = true;
        break;
      }
    }
    await loadPlannedPurchases();
    if (declinedConfirm) restorePlannedPurchaseFocus(action, purchaseId);
  } catch (error) {
    console.error(`Failed to ${action} planned purchase:`, error);
    const err = error as Error;
    showToast({ message: `Failed to ${action} purchase: ${err.message}`, kind: 'error' });
  }
}
