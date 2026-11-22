/**
 * Plans module for CUDly
 */


import * as api from './api';
import * as state from './state';
import { escapeHtml, escapeHtmlAttr } from './utils';
import { showToast } from './toast';
import { openModal, closeModal, modalCloseButtonHtml } from './modal';
import { suggestedPurchaseStartDate } from './lib/calendar-date';
import { loadPlannedPurchases } from './plansflow/plannedLoad';
import { loadPlans } from './plansflow/planCards';
import { schedulePlanDraftSave } from './plansflow/planDraft';
import { wireRangeInput } from './plansflow/planRanges';

/**
 * Close purchase modal
 */
export function closePurchaseModal(): void {
  const purchaseModal = document.getElementById('purchase-modal');
  if (purchaseModal) closeModal(purchaseModal);
}

/**
 * Open modal to add planned purchases for a plan
 */
export async function openAddPurchasesModal(planId: string, planName: string, notificationDays: number): Promise<void> {
  // Remove existing modal if present
  document.getElementById('add-purchases-modal')?.remove();

  const modal = document.createElement('div');
  modal.id = 'add-purchases-modal';
  modal.setAttribute('role', 'dialog');
  modal.setAttribute('aria-modal', 'true');
  modal.setAttribute('aria-labelledby', 'add-purchases-modal-title');
  modal.innerHTML = `
    <div class="modal-overlay">
      <div class="modal-content">
        <div class="modal-header">
          <h2 id="add-purchases-modal-title">Add Planned Purchases</h2>
          ${modalCloseButtonHtml('add-purchases-cancel')}
        </div>
        <p class="help-text">Schedule additional purchases for <strong>${escapeHtml(planName)}</strong></p>

        <form id="add-purchases-form">
          <input type="hidden" id="add-purchases-plan-id" value="${escapeHtmlAttr(planId)}">

          <label>Number of Purchases:
            <input type="number" id="add-purchases-count" min="1" max="52" value="1" required>
          </label>
          <p class="help-text">How many purchase steps to schedule (based on the plan's ramp schedule settings)</p>

          <label>Start Date:
            <input type="date" id="add-purchases-start-date" aria-describedby="add-purchases-date-help add-purchases-date-warning" required>
          </label>
          <p id="add-purchases-date-help" class="help-text"></p>
          <p id="add-purchases-date-warning" class="help-text" role="status"></p>

          <div id="add-purchases-error" class="error-message hidden"></div>

          <div class="modal-buttons">
            <button type="button" id="add-purchases-cancel">Cancel</button>
            <button type="submit" class="primary">Add Purchases</button>
          </div>
        </form>
      </div>
    </div>
  `;
  document.body.appendChild(modal);

  const startDateInput = document.getElementById('add-purchases-start-date') as HTMLInputElement;
  const now = new Date();
  const validNotice = Number.isInteger(notificationDays) && notificationDays >= 0 && notificationDays <= 30;
  startDateInput.min = earliestStartDate(now);
  if (validNotice) {
    // The API parses date-only values at UTC midnight. The extra calendar
    // day avoids opening just before the notification/veto overlap ends.
    const suggested = suggestedPurchaseStartDate(notificationDays, now);
    startDateInput.value = suggested;
    document.getElementById('add-purchases-date-help')!.textContent = `This plan has a ${notificationDays}-day notification window. Dates use midnight UTC; subsequent purchases follow the plan's interval.`;
    const updateDateWarning = (): void => {
      const selected = Date.parse(`${startDateInput.value}T00:00:00Z`);
      // Mirrors the server's start_date_inside_notice_margin rule (D5 of #631):
      // a date before now + (N+1) days is refused.
      document.getElementById('add-purchases-date-warning')!.textContent = selected < Date.now() + (notificationDays + 1) * 24 * 60 * 60 * 1000
        ? `This date leaves too little notice for recommendations to attach and will be refused. Suggested start date: ${suggested}.`
        : '';
    };
    startDateInput.addEventListener('input', updateDateWarning);
    updateDateWarning();
  } else {
    const error = document.getElementById('add-purchases-error')!;
    error.textContent = 'The plan notification window is missing or invalid. Reload the plan before scheduling purchases.';
    error.classList.remove('hidden');
  }

  // Add event listeners
  document.getElementById('add-purchases-cancel')?.addEventListener('click', closeAddPurchasesModal);
  document.getElementById('add-purchases-form')?.addEventListener('submit', (e) => {
    if (validNotice) void handleAddPurchases(e);
    else e.preventDefault();
  });

  // Inline range validation on the count field: surfaces a
  // "Must be a whole number between 1 and 52" message under the input
  // as the user types, instead of waiting for the API call to reject
  // the value (mirrors the wireRangeInput pattern from #702/#714,
  // closes #771). The modal is built fresh on every open so no
  // data-range-wired guard is needed here.
  wireRangeInput('add-purchases-count', 1, 52);

  // Keep the submit button disabled while the count field is invalid.
  // Checked after every input/blur so the button reflects the live
  // validation state set by wireRangeInput above.
  const countInput = document.getElementById('add-purchases-count') as HTMLInputElement | null;
  const submitBtn = modal.querySelector<HTMLButtonElement>('button[type="submit"]');
  if (countInput && submitBtn) {
    const syncSubmitBtn = (): void => {
      submitBtn.disabled = !validNotice || countInput.getAttribute('aria-invalid') === 'true';
    };
    countInput.addEventListener('input', syncSubmitBtn);
    countInput.addEventListener('blur', syncSubmitBtn);
    syncSubmitBtn();
  }

  // Engage focus trap + Escape handler. The modal element itself is
  // removed from the DOM on close (see closeAddPurchasesModal) instead
  // of just toggling .hidden, so the closeModal call there is what
  // actually triggers focus restoration to the trigger.
  openModal(modal);
}

/**
 * Close add purchases modal — restore focus first (closeModal), then
 * remove the dynamically-injected element from the DOM.
 */
function closeAddPurchasesModal(): void {
  const modal = document.getElementById('add-purchases-modal');
  if (modal) closeModal(modal);
  modal?.remove();
}

/**
 * First accepted start date. The server parses start_date as midnight UTC and
 * requires it after now, so tomorrow (UTC) is the earliest (handler_plans.go, #50).
 */
function earliestStartDate(now: Date): string {
  return new Date(now.getTime() + 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
}

/**
 * Handle form submission for adding planned purchases
 */
async function handleAddPurchases(e: Event): Promise<void> {
  e.preventDefault();
  const errorDiv = document.getElementById('add-purchases-error');
  errorDiv?.classList.add('hidden');

  try {
    const planId = (document.getElementById('add-purchases-plan-id') as HTMLInputElement).value;
    // Use Number() (not parseInt) so fractional input like "2.5" fails
    // Number.isInteger() instead of silently truncating to 2. Mirrors
    // the strict parse pattern from #471/#702 (feedback_strict_int_parse).
    const rawCount = Number((document.getElementById('add-purchases-count') as HTMLInputElement).value);
    if (!Number.isFinite(rawCount) || !Number.isInteger(rawCount) || rawCount < 1 || rawCount > 52) {
      if (errorDiv) {
        errorDiv.textContent = 'Number of Purchases must be a whole number between 1 and 52';
        errorDiv.classList.remove('hidden');
      }
      return;
    }
    const count = rawCount;
    const startDate = (document.getElementById('add-purchases-start-date') as HTMLInputElement).value;
    if (startDate < earliestStartDate(new Date())) {
      if (errorDiv) {
        errorDiv.textContent = 'Start date must be in the future (after today UTC)';
        errorDiv.classList.remove('hidden');
      }
      return;
    }

    await api.createPlannedPurchases(planId, count, startDate);

    closeAddPurchasesModal();
    await loadPlannedPurchases();
    showToast({ message: `Successfully scheduled ${count} purchase${count > 1 ? 's' : ''}`, kind: 'success', timeout: 5_000 });
  } catch (error) {
    const err = error as Error;
    if (errorDiv) {
      errorDiv.textContent = err.message;
      errorDiv.classList.remove('hidden');
    }
  }
}

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
