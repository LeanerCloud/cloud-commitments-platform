/**
 * Plans module for CUDly
 */


import * as api from './api';
import * as state from './state';
import { escapeHtml, escapeHtmlAttr } from './utils';
import { showToast } from './toast';
import { populateTermSelect, populatePaymentSelect, normalizePaymentValue } from './commitmentOptions';
import { openModal, closeModal, modalCloseButtonHtml } from './modal';
import { suggestedPurchaseStartDate } from './lib/calendar-date';
import { describeGroup, groupSelection } from './lib/plan-groups';
import { mountPlanEditorPanel } from './plan-editor-panel';
import { renderPlanFiltersNotice } from './plan-filters-notice';
import { clearPlanDraft } from './lib/plan-form-draft';
import { plansState } from './plansflow/plansState';
import { loadPlannedPurchases } from './plansflow/plannedLoad';
import { loadPlans } from './plansflow/planCards';
import { schedulePlanDraftSave } from './plansflow/planDraft';
import { setupPlanAccountsSection, prefillAccountChips } from './plansflow/planAccounts';
import { updatePlanNameFromSchedule, wireRangeInput, wirePlanRangeInputs } from './plansflow/planRanges';
import { setupRampScheduleHandlers, syncCustomRampConfigToSelection } from './plansflow/rampSchedule';

// showSelectionGroups lists the groups of a mixed selection in place of the
// service fields, which describe one group only. An empty list restores them.
export function showSelectionGroups(labels: readonly string[]): void {
  const list = document.getElementById('plan-selection-groups');
  const intro = document.getElementById('plan-selection-groups-intro');
  const fields = document.getElementById('plan-service-fields');
  if (!list || !fields) return;
  list.replaceChildren(...labels.map((textContent) => Object.assign(document.createElement('li'), { textContent })));
  list.hidden = labels.length === 0;
  if (intro) intro.hidden = labels.length === 0;
  fields.hidden = labels.length > 0;
}

// setPlanFieldsLocked makes the modal's service fields read-only. A plan built
// from a selection locks provider, service, term and payment (the selection
// defines them); editing a bound plan also locks coverage.
export function setPlanFieldsLocked(service: boolean, coverage: boolean): void {
  for (const id of ['plan-provider', 'plan-service', 'plan-term', 'plan-payment']) {
    const el = document.getElementById(id) as HTMLSelectElement | null;
    if (!el) continue;
    el.disabled = service;
    if (service) el.title = 'Set by the selected recommendations'; else el.removeAttribute('title');
  }
  const coverageInput = document.getElementById('plan-coverage') as HTMLInputElement | null;
  if (!coverageInput) return;
  coverageInput.readOnly = coverage;
  if (coverage) coverageInput.title = 'Set by the selected recommendations'; else coverageInput.removeAttribute('title');
}

let disposePlanEditor: (() => void) | undefined;
export function openPlanForm(modal: HTMLElement, restartsFromToday = false): void {
  plansState.planModalEpoch++;
  renderPlanFiltersNotice(modal.querySelector<HTMLElement>('#plan-filters-notice'), undefined);
  disposePlanEditor?.(); disposePlanEditor = undefined;
  const host = modal.querySelector<HTMLElement>('#plan-editor-panel'), form = modal.querySelector<HTMLFormElement>('#plan-form');
  const advanced = modal.querySelector<HTMLDetailsElement>('#plan-advanced-settings');
  if (advanced) advanced.open = false;
  const revealInvalid = (event: Event): void => {
    if (advanced?.contains(event.target as Node)) advanced.open = true;
  };
  form?.addEventListener('invalid', revealInvalid, true);
  const disposePanel = host && form ? mountPlanEditorPanel(host, form, { restartsFromToday }) : undefined;
  disposePlanEditor = () => {
    try { disposePanel?.(); }
    finally { form?.removeEventListener('invalid', revealInvalid, true); }
  };
  openModal(modal, { initialFocus: '#plan-name', onClose: () => { disposePlanEditor?.(); disposePlanEditor = undefined; } });
}

/**
 * Close plan modal
 */
export function closePlanModal(): void {
  plansState.planAutomationRead += 1;
  plansState.planModalEpoch++;
  // Save, Cancel and every other close end the edit: nothing left to restore.
  if (plansState.planDraftTimer) clearTimeout(plansState.planDraftTimer);
  plansState.planDraftTimer = null;
  clearPlanDraft();
  const planModal = document.getElementById('plan-modal');
  if (planModal) closeModal(planModal);
  // Invalidate the resolved-target snapshot stamped by openCreatePlanModal
  // so a subsequent flow doesn't accidentally inherit it. The snapshot
  // ties the plan to a specific button-click moment; once the modal
  // closes — by Save, Cancel, or any other path — that moment is over.
  plansState.pendingPlanRecommendations = [];
  plansState.editingBoundPlan = false;
  setPlanFieldsLocked(false, false);
  showSelectionGroups([]);
  plansState.planModalSession += 1; // a late account lookup must not fill the next modal
}

/**
 * Prefill the Purchase Configuration section (provider / service / term /
 * payment) from a representative selected commitment. Called after
 * form.reset() so the defaults are already in place; each field is still
 * editable. (#770)
 *
 * The caller passes the first commitment as the representative only when the
 * whole selection shares provider, service, term and payment (one plan group);
 * a mixed selection lists its groups instead. (#898, #1746)
 */
function prefillPurchaseConfigFromCommitment(rec: api.Recommendation): void {
  const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement | null;
  const serviceSelect = document.getElementById('plan-service') as HTMLSelectElement | null;
  const termSelect = document.getElementById('plan-term') as HTMLSelectElement | null;
  const paymentSelect = document.getElementById('plan-payment') as HTMLSelectElement | null;

  if (!providerSelect || !serviceSelect || !termSelect || !paymentSelect) return;

  const provider = rec.provider ?? '';
  const service = rec.service ?? '';

  if (provider) providerSelect.value = provider;
  if (service) serviceSelect.value = service;

  // Repopulate term/payment options for the chosen provider+service, then
  // apply the commitment's own values so the dropdowns are consistent.
  if (provider && service) {
    populateTermSelect(termSelect, provider, service);
    populatePaymentSelect(paymentSelect, provider, service);
  }

  if (rec.term != null) termSelect.value = String(rec.term);
  const normalizedPayment = rec.payment ? normalizePaymentValue(rec.payment, provider) : '';
  if (normalizedPayment) paymentSelect.value = normalizedPayment;
}

/**
 * Open create plan modal with selected recommendations.
 *
 * When the user has no selection (issue #17 reproducer: filter
 * active, no checkboxes ticked), fall through to the plain new-plan
 * flow instead of silently noop-ing behind a toast the user may
 * miss. Same UX as the dedicated "New Plan" button — the modal
 * always opens, and the user fills in provider/service from scratch.
 */
export function openCreatePlanModal(snapshot?: readonly api.Recommendation[]): void {
  // Stamp the resolved-target snapshot from the caller so savePlan can
  // consume it without re-deriving from global state at Save time. The
  // Bottom Action Box passes the result of resolvePurchaseTarget(); a
  // missing arg falls back to the legacy behaviour (no captured target,
  // savePlan submits a plan without `recommendations`). See #273 CR.
  plansState.pendingPlanRecommendations = snapshot ? [...snapshot] : [];

  const titleEl = document.getElementById('plan-modal-title');
  const hasSelection = plansState.pendingPlanRecommendations.length > 0;
  if (titleEl) {
    titleEl.textContent = hasSelection ? 'Create purchase plan' : 'New purchase plan';
  }
  (document.getElementById('plan-id') as HTMLInputElement).value = '';
  (document.getElementById('plan-form') as HTMLFormElement | null)?.reset();

  // One plan group (every selected commitment shares provider, service, term
  // and payment): prefill and lock the Purchase Configuration from the first
  // commitment. A mixed selection lists its groups instead. (#770, #898, #1746)
  const groups = groupSelection(plansState.pendingPlanRecommendations, 0);
  if (groups.length === 1) {
    prefillPurchaseConfigFromCommitment(plansState.pendingPlanRecommendations[0]!);
  }
  showSelectionGroups(groups.length > 1 ? groups.map((g) => describeGroup(g, g.recommendation_ids.length)) : []);
  plansState.editingBoundPlan = false;
  setPlanFieldsLocked(hasSelection, false);

  // Set up ramp schedule change handlers for dynamic plan name
  setupRampScheduleHandlers();
  syncCustomRampConfigToSelection();

  // Wire live range validation on all five numeric inputs (#702).
  wirePlanRangeInputs();

  // Generate initial plan name
  updatePlanNameFromSchedule();

  // Stamp a new session so any in-flight prefillAccountChips promise
  // from a prior modal open can detect it belongs to a stale session and
  // discard its result without mutating planSelectedAccounts. (#770 CR)
  plansState.planModalSession += 1;

  // setupPlanAccountsSection clears planSelectedAccounts and re-renders.
  // When every selected commitment carries the SAME cloud_account_id, we look
  // up that account after the section has reset and add it as a pre-selected
  // chip. Homogeneity of provider/service/term/payment (enforced by the
  // "Plan from N selected" gate) does not imply a single account, so a
  // multi-account selection leaves the chip empty for the user to fill. (#898)
  plansState.planAccountsLoad = setupPlanAccountsSection();
  // Every selected recommendation's account must be a target account, or the
  // backend refuses the bound plan, so all of them are pre-selected (#1746).
  const selectedAccountIds = [...new Set(plansState.pendingPlanRecommendations.map((r) => r.cloud_account_id).filter((id): id is string => !!id))];
  if (selectedAccountIds.length > 0) {
    void prefillAccountChips(selectedAccountIds, plansState.planModalSession);
  }

  const planModal = document.getElementById('plan-modal');
  if (planModal) {
    openPlanForm(planModal);
  }
}

/**
 * Open new plan modal (without pre-selected recommendations)
 */
export function openNewPlanModal(): void {
  // No pre-resolved target — this is the "New Plan from scratch" path.
  // Clear any stale snapshot from a prior openCreatePlanModal call so a
  // subsequent savePlan doesn't accidentally inherit a previous flow's
  // recs. See #273 CR.
  plansState.pendingPlanRecommendations = [];
  plansState.editingBoundPlan = false;
  setPlanFieldsLocked(false, false);
  showSelectionGroups([]);
  plansState.planModalSession += 1;

  const titleEl = document.getElementById('plan-modal-title');
  if (titleEl) titleEl.textContent = 'New purchase plan';
  (document.getElementById('plan-id') as HTMLInputElement).value = '';
  (document.getElementById('plan-form') as HTMLFormElement | null)?.reset();

  // Set up ramp schedule change handlers for dynamic plan name
  setupRampScheduleHandlers();
  syncCustomRampConfigToSelection();

  // Wire live range validation on all five numeric inputs (#702).
  wirePlanRangeInputs();

  // Generate initial plan name
  updatePlanNameFromSchedule();

  plansState.planAccountsLoad = setupPlanAccountsSection();

  const planModal = document.getElementById('plan-modal');
  if (planModal) {
    openPlanForm(planModal);
  }
}

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
