/**
 * Plans module for CUDly
 */


import * as state from './state';
import { loadPlans } from './plansflow/planCards';
import { schedulePlanDraftSave } from './plansflow/planDraft';

// Unsubscribe handles kept at module scope so setupPlanHandlers stays
// idempotent: calling it more than once (e.g. in tests or after HMR)
// replaces the old subscriptions rather than stacking duplicates.
let _unsubProvider: (() => void) | null = null;
let _unsubAccount: (() => void) | null = null;
let _onVisible: (() => void) | null = null;

// Recommendations move while the tab sits in the background, and the step
// estimates follow them, so refetch the list when the page comes back.
function reloadWhenPlansTabVisible(): void {
  if (document.visibilityState !== 'visible') return;
  if (!document.getElementById('plans-tab')?.classList.contains('active')) return;
  void loadPlans();
}

/**
 * Setup plan form event handlers (provider-aware service dropdown).
 *
 * Provider/account filter source-of-truth is the global topbar (state.ts);
 * subscribe so the plans list re-renders when the user changes filters at
 * the page level.
 */
export function setupPlanHandlers(): void {
  // Drop any previous subscriptions before re-registering so we don't
  // accumulate duplicate loadPlans() calls on each invocation.
  _unsubProvider?.();
  _unsubAccount?.();
  _unsubProvider = state.subscribeProvider(() => void loadPlans());
  _unsubAccount = state.subscribeAccount(() => void loadPlans());
  if (_onVisible) document.removeEventListener('visibilitychange', _onVisible);
  _onVisible = reloadWhenPlansTabVisible;
  document.addEventListener('visibilitychange', _onVisible);

  const planForm = document.getElementById('plan-form');
  planForm?.addEventListener('input', schedulePlanDraftSave);
  planForm?.addEventListener('change', schedulePlanDraftSave);

  const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement | null;
  const serviceSelect = document.getElementById('plan-service') as HTMLSelectElement | null;

  if (providerSelect && serviceSelect) {
    // Update service dropdown visibility when provider changes
    providerSelect.addEventListener('change', () => {
      updateServiceDropdownForProvider(providerSelect.value);
    });

    // Initialize with current provider value
    updateServiceDropdownForProvider(providerSelect.value);
  }
}

/**
 * Update service dropdown to show only services for selected provider
 */
function updateServiceDropdownForProvider(provider: string): void {
  const serviceSelect = document.getElementById('plan-service') as HTMLSelectElement | null;
  if (!serviceSelect) return;

  // Show/hide optgroups based on selected provider.
  //
  // Toggle the `hidden` class (same class the HTML starts with) so the
  // DOM state tracks a single source of truth — previously we flipped
  // `style.display`, which doesn't clear the pre-existing `hidden`
  // class, so Azure/GCP stayed hidden forever even when selected.
  //
  // Also flip `optgroup.disabled`: Chrome has a long-standing quirk
  // where a `display: none` <optgroup> still renders its <option>s as
  // selectable, so users could end up with provider=Azure + ec2. The
  // `disabled` attribute disables selection in every browser.
  const optgroups = serviceSelect.querySelectorAll('optgroup');
  let firstVisibleOptionValue = '';

  optgroups.forEach(optgroup => {
    // Match on the explicit data-provider attribute, not a substring of the
    // human-readable label (issue #241): the AWS Savings Plans optgroup is
    // labeled "Savings Plans", which does not contain "aws", so the old
    // label.includes() check hid and disabled it for the entire session.
    const shouldShow = optgroup.dataset['provider'] === provider.toLowerCase();
    optgroup.classList.toggle('hidden', !shouldShow);
    optgroup.disabled = !shouldShow;

    // Track first visible option value to auto-select
    if (shouldShow && !firstVisibleOptionValue) {
      const firstOption = optgroup.querySelector('option');
      if (firstOption) {
        firstVisibleOptionValue = firstOption.value;
      }
    }
  });

  // If current selection is hidden, select first visible option
  const currentOption = serviceSelect.options[serviceSelect.selectedIndex];
  const parentOptgroup = currentOption?.parentElement;
  const isHidden = parentOptgroup instanceof HTMLOptGroupElement && parentOptgroup.classList.contains('hidden');
  if (isHidden && firstVisibleOptionValue) {
    serviceSelect.value = firstVisibleOptionValue;
    // Trigger change event to update term/payment options
    serviceSelect.dispatchEvent(new Event('change'));
  }
}

export { loadPlans } from './plansflow/planCards';
export { restorePlanDraft } from './plansflow/planDraft';
export { _resetRampHandlersForTest } from './plansflow/rampSchedule';
export { savePlan } from './plansflow/planSave';
export { closePlanModal, openCreatePlanModal, openNewPlanModal } from './plansflow/planModal';
export { closePurchaseModal, openAddPurchasesModal } from './plansflow/addPurchases';
