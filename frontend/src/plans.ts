/**
 * Plans module for CUDly
 */


import * as api from './api';
import * as state from './state';
import { escapeHtml, escapeHtmlAttr } from './utils';
import { showToast } from './toast';
import { createIcon } from './icons';
import type { SavePlanData } from './types';
import type { ApiError } from './api/types';
import { populateTermSelect, populatePaymentSelect, isValidCombination, normalizePaymentValue } from './commitmentOptions';
import { openModal, closeModal, modalCloseButtonHtml } from './modal';
import { canAccess } from './permissions';
import { suggestedPurchaseStartDate } from './lib/calendar-date';
import { serviceLabel } from './lib/service-labels';
import { describeGroup, groupSelection } from './lib/plan-groups';
import { mountPlanEditorPanel } from './plan-editor-panel';
import { renderPlanFiltersNotice } from './plan-filters-notice';
import { clearPlanDraft } from './lib/plan-form-draft';
import { plansState } from './plansflow/plansState';
import { BackendPlan, LEGACY_PLAN_SERVICE_IDS, extractPlanInfo, rampRadioValue } from './plansflow/planModel';
import { loadPlannedPurchases } from './plansflow/plannedLoad';
import { loadPlans } from './plansflow/planCards';
import { schedulePlanDraftSave } from './plansflow/planDraft';

// showSelectionGroups lists the groups of a mixed selection in place of the
// service fields, which describe one group only. An empty list restores them.
function showSelectionGroups(labels: readonly string[]): void {
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
function setPlanFieldsLocked(service: boolean, coverage: boolean): void {
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

// Install-once guard for setupRampScheduleHandlers. The elements it binds are
// static modal singletons that are never replaced, so adding listeners on
// every modal open stacks duplicate handlers. The flag ensures we bind once;
// reset to false only in tests via resetRampHandlersForTest().
let rampHandlersInstalled = false;

let disposePlanEditor: (() => void) | undefined;
function openPlanForm(modal: HTMLElement, restartsFromToday = false): void {
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

export async function editPlan(planId: string): Promise<boolean> {
  plansState.editPlanFailureStatus = undefined;
  try {
    const backendPlan = await api.getPlan(planId) as unknown as BackendPlan;

    // Extract info from the backend plan format
    const info = extractPlanInfo(backendPlan);
    const rampSchedule = backendPlan.ramp_schedule || { type: 'immediate', percent_per_step: 100, step_interval_days: 0 };

    const rampValue = rampRadioValue(rampSchedule);

    // Get payment option from services and normalize for provider.
    // H-5: do not pre-select 'no-upfront' when the payment field is absent.
    // An absent payment field leaves the select empty so the user must
    // explicitly choose rather than silently inheriting a fabricated default
    // that could change the plan's payment type on re-save.
    const firstService = Object.values(backendPlan.services || {})[0];
    const rawPayment = firstService?.payment || null;
    const payment = rawPayment !== null ? normalizePaymentValue(rawPayment, info.provider ?? '') : '';

    const titleEl = document.getElementById('plan-modal-title');
    if (titleEl) titleEl.textContent = 'Edit Purchase Plan';
    plansState.pendingPlanRecommendations = [];
    plansState.editingBoundPlan = backendPlan.ramp_schedule?.allocation_mode === 'bound_groups';
    setPlanFieldsLocked(plansState.editingBoundPlan, plansState.editingBoundPlan);
    showSelectionGroups(plansState.editingBoundPlan ? Object.values(backendPlan.services ?? {}).map((svc) => describeGroup(svc, svc.bound_count ?? 0)) : []);
    plansState.planModalSession += 1;

    // Edit can be the first modal opened. Install the handlers before any field is filled: the first install rebuilds term/payment (#1536).
    setupRampScheduleHandlers();

    (document.getElementById('plan-id') as HTMLInputElement).value = backendPlan.id;
    (document.getElementById('plan-name') as HTMLInputElement).value = backendPlan.name;
    (document.getElementById('plan-description') as HTMLTextAreaElement).value = '';

    // Set provider and service first. When provider is absent from the API
    // response, leave the select at its default empty/first option rather
    // than fabricating 'aws' (H-4: never default provider silently).
    const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement;
    providerSelect.value = info.provider ?? '';
    const planService = LEGACY_PLAN_SERVICE_IDS[`${info.provider}/${info.service}`] ?? info.service;
    const serviceSelect = document.getElementById('plan-service') as HTMLSelectElement;
    serviceSelect.value = planService;
    if (!plansState.editingBoundPlan && (serviceSelect.selectedIndex === -1 || serviceSelect.value === '')) {
      serviceSelect.selectedIndex = -1;
      showToast({
        message: `This plan uses a service that is no longer supported (${info.provider ?? 'unknown provider'}/${info.service}). Recreate the plan, or choose a supported service before saving.`,
        kind: 'error',
      });
    }

    // Update term/payment options based on provider/service
    const termSelect = document.getElementById('plan-term') as HTMLSelectElement;
    const paymentSelect = document.getElementById('plan-payment') as HTMLSelectElement;
    populateTermSelect(termSelect, info.provider ?? '', planService);
    populatePaymentSelect(paymentSelect, info.provider ?? '', planService);

    // Set term only when present; absent term leaves the select unset so
    // the user must explicitly choose rather than silently inheriting a
    // fabricated 3yr default (H-4).
    termSelect.value = info.term !== null ? String(info.term) : '';

    paymentSelect.value = payment;

    // Set coverage only when present; absent coverage leaves the input
    // blank so the user sees the field is missing (H-4).
    (document.getElementById('plan-coverage') as HTMLInputElement).value = info.coverage !== null ? String(info.coverage) : '';
    (document.getElementById('plan-auto-purchase') as HTMLInputElement).checked = backendPlan.auto_purchase;
    const capInput = document.getElementById('plan-max-step-commitment') as HTMLInputElement | null;
    if (capInput) capInput.value = backendPlan.max_step_commitment == null ? '' : String(backendPlan.max_step_commitment);
    (document.getElementById('plan-notify-days') as HTMLInputElement).value = String(backendPlan.notification_days_before || 3);
    (document.getElementById('plan-enabled') as HTMLInputElement).checked = backendPlan.enabled;

    const rampRadio = document.querySelector<HTMLInputElement>(`input[name="ramp-schedule"][value="${rampValue}"]`);
    if (rampRadio) rampRadio.checked = true;

    setCustomRampConfigVisible(rampValue === 'custom');

    if (rampValue === 'custom') {
      (document.getElementById('ramp-step-percent') as HTMLInputElement).value = String(rampSchedule.percent_per_step || 20);
      (document.getElementById('ramp-interval-days') as HTMLInputElement).value = String(rampSchedule.step_interval_days || 7);
    }

    plansState.planAccountsLoad = setupPlanAccountsSection(backendPlan.id);
    // Wire live range validation on all five numeric inputs (#702).
    wirePlanRangeInputs();
    const planModal = document.getElementById('plan-modal');
    if (planModal) openPlanForm(planModal, true);
    renderPlanFiltersNotice(document.getElementById('plan-filters-notice'), backendPlan.services);
    return true;
  } catch (error) {
    console.error('Failed to load plan:', error);
    // A missing/inaccessible plan comes back as 404 (issue #1403): the
    // scheduled-purchase row outlived its plan (deleted, or the caller's
    // account scope changed). Surface an actionable message instead of the
    // generic "Failed to load plan details", and signal failure so the
    // caller can reconcile the stale row. Other errors keep the generic
    // message (network blip, transient 5xx) since the plan may still exist.
    const status = (error as { status?: number }).status;
    plansState.editPlanFailureStatus = status;
    const message = status === 404
      ? 'This plan is no longer available. It may have been deleted.'
      : 'Failed to load plan details';
    showToast({ message, kind: 'error' });
    return false;
  }
}

// savingPlan is true while a save request is in flight. refreshPlanSaveButtonState
// reads it so the account gate stays the single owner of the Save button.
let savingPlan = false;

// duplicatePlanId returns the id of the plan that blocked a create, from the
// backend's plan_name_taken 409, or null for any other error.
function duplicatePlanId(error: unknown): string | null {
  const apiError = error as ApiError;
  if (apiError.status !== 409 || apiError.details?.['code'] !== 'plan_name_taken') return null;
  const id = apiError.details?.['existing_plan_id'];
  return typeof id === 'string' && id !== '' ? id : null;
}

/**
 * Save plan (create or update)
 */
export async function savePlan(e: Event): Promise<void> {
  e.preventDefault();
  // A double click or Enter+click must not send a second create (issue #1734).
  if (savingPlan) return;

  const planId = (document.getElementById('plan-id') as HTMLInputElement).value;
  const rampScheduleRadio = document.querySelector<HTMLInputElement>('input[name="ramp-schedule"]:checked');
  const rampSchedule = rampScheduleRadio?.value || 'immediate';

  // A bound plan's service fields are read-only and the backend refuses them on
  // edit (#1746), so they are neither validated nor sent.
  const bound = planId !== '' && plansState.editingBoundPlan;
  // The selection defines a from-selection plan's service, term and payment,
  // so only a plan with flat service fields validates the (editable) selects.
  const flatFields = !bound && plansState.pendingPlanRecommendations.length === 0;
  const service = (document.getElementById('plan-service') as HTMLSelectElement).value;
  if (flatFields && !service) {
    showToast({ message: 'Service is required: choose a supported service before saving the plan', kind: 'error' });
    return;
  }

  // Parse and validate integer fields up front. Use Number() not parseInt so
  // fractions like "2.5" fail Number.isInteger() rather than silently truncating
  // to 2. Mirrors the strict parse pattern from handleAddPurchases and settings.ts
  // validatePurchasingSettings (feedback_strict_int_parse, finding 11-M1).
  const rawTerm = Number((document.getElementById('plan-term') as HTMLSelectElement).value);
  const coverageText = (document.getElementById('plan-coverage') as HTMLInputElement).value.trim();
  // Number('') is 0, so an emptied field must not reach the range check as 0.
  const rawCoverage = coverageText === '' ? NaN : Number(coverageText);
  const rawNotifyDays = Number((document.getElementById('plan-notify-days') as HTMLInputElement).value);

  if (flatFields && (!Number.isFinite(rawTerm) || !Number.isInteger(rawTerm) || rawTerm < 1)) {
    showToast({ message: 'Term must be a valid whole number of years', kind: 'error' });
    return;
  }
  if (!bound && (!Number.isFinite(rawCoverage) || !Number.isInteger(rawCoverage) || rawCoverage < 0 || rawCoverage > 100)) {
    showToast({ message: 'Target Coverage must be a whole number between 0 and 100', kind: 'error' });
    return;
  }
  if (!Number.isFinite(rawNotifyDays) || !Number.isInteger(rawNotifyDays) || rawNotifyDays < 1 || rawNotifyDays > 30) {
    showToast({ message: 'Notification Days must be a whole number between 1 and 30', kind: 'error' });
    return;
  }

  const autoPurchase = (document.getElementById('plan-auto-purchase') as HTMLInputElement).checked;
  let maxStepCommitment: number | null;
  const capInput = document.getElementById('plan-max-step-commitment') as HTMLInputElement | null;
  try {
    maxStepCommitment = parsePlanStepCap(capInput?.value.trim() ?? '', autoPurchase);
  } catch (error) {
    // The cap lives in the collapsed Advanced settings; reveal it so the error has a target.
    const advanced = document.getElementById('plan-advanced-settings') as HTMLDetailsElement | null;
    if (advanced) advanced.open = true;
    capInput?.focus();
    showToast({ message: (error as Error).message, kind: 'error' });
    return;
  }

  const plan: SavePlanData = {
    name: (document.getElementById('plan-name') as HTMLInputElement).value,
    ramp_schedule: rampSchedule,
    auto_purchase: autoPurchase,
    max_step_commitment: maxStepCommitment,
    notification_days_before: rawNotifyDays,
    enabled: (document.getElementById('plan-enabled') as HTMLInputElement).checked
  };

  if (rampSchedule === 'custom') {
    const rawStepPercent = Number((document.getElementById('ramp-step-percent') as HTMLInputElement).value);
    const rawIntervalDays = Number((document.getElementById('ramp-interval-days') as HTMLInputElement).value);
    if (!Number.isFinite(rawStepPercent) || !Number.isInteger(rawStepPercent) || rawStepPercent < 1 || rawStepPercent > 100) {
      showToast({ message: 'Ramp Step Percent must be a whole number between 1 and 100', kind: 'error' });
      return;
    }
    if (!Number.isFinite(rawIntervalDays) || !Number.isInteger(rawIntervalDays) || rawIntervalDays < 1 || rawIntervalDays > 365) {
      showToast({ message: 'Ramp Interval Days must be a whole number between 1 and 365', kind: 'error' });
      return;
    }
    plan.custom_step_percent = rawStepPercent;
    plan.custom_interval_days = rawIntervalDays;
  }

  // Use the snapshot stamped at Plan-button click time (#273 CR follow-up).
  // Reading state.getVisibleRecommendations() / getSelectedRecommendation
  // IDs() here would re-derive the target at Save time — racing Refresh,
  // filter changes, and deselections that happen while the modal is open.
  // openCreatePlanModal(snapshot) freezes the Plan target the moment the
  // user clicked "Plan from N selected"; we read it back here.
  // openNewPlanModal() clears the snapshot for the New-Plan-from-scratch
  // path (no pre-resolved target — plan submits without `recommendations`).
  if (!bound && !flatFields) {
    plan.groups = groupSelection(plansState.pendingPlanRecommendations, rawCoverage);
  } else if (flatFields) {
    plan.provider = (document.getElementById('plan-provider') as HTMLSelectElement).value;
    plan.service = service;
    plan.term = rawTerm;
    plan.payment = (document.getElementById('plan-payment') as HTMLSelectElement).value;
    plan.target_coverage = rawCoverage;
  }

  // Universal-plans fix: read the selected account chips and reject submit
  // when the list is empty. The Save button is also disabled in the same
  // condition via refreshPlanSaveButtonState() so this branch is mostly a
  // belt-and-suspenders against scripted form submission; the toast keeps
  // the failure mode loud either way.
  const accountIdsField = document.getElementById('plan-account-ids') as HTMLInputElement | null;
  const accountIds = accountIdsField?.value ? accountIdsField.value.split(',').filter(Boolean) : [];
  if (accountIds.length === 0) {
    showToast({
      message: 'Target accounts is required: pick at least one account before saving the plan.',
      kind: 'error',
    });
    return;
  }
  plan.target_accounts = accountIds;

  savingPlan = true;
  refreshPlanSaveButtonState();
  try {
    if (planId) {
      await api.updatePlan(planId, plan as unknown as api.CreatePlanRequest);
      // Update flow: push the selected account list via the dedicated endpoint.
      await api.setPlanAccounts(planId, accountIds);
    } else {
      await api.createPlan(plan as unknown as api.CreatePlanRequest);
      // Create flow: backend inserted plan_accounts atomically from
      // target_accounts in the POST body (see internal/api/handler_plans.go
      // createPlan). No follow-up account-write needed.
    }

    closePlanModal();
    await loadPlans();
    showToast({ message: planId ? 'Plan updated successfully' : 'Plan created successfully', kind: 'success', timeout: 5_000 });
  } catch (error) {
    console.error('Failed to save plan:', error);
    const err = error as Error;
    const existingId = duplicatePlanId(error);
    showToast({
      message: `Failed to save plan: ${err.message}`,
      kind: 'error',
      actions: existingId === null ? undefined : [{
        label: 'Open existing plan',
        // Close first: it clears the Plan-from-selection snapshot, so the
        // existing plan's editor does not inherit this form's target.
        onClick: () => { closePlanModal(); void editPlan(existingId); },
      }],
    });
  } finally {
    savingPlan = false;
    refreshPlanSaveButtonState();
  }
}

// refreshPlanSaveButtonState toggles the Save button's disabled state based
// on whether at least one Target Account is selected. Universal plans (rows
// in purchase_plans with no plan_accounts row) are no longer allowed by the
// API; surfacing the failure in the disabled state is friendlier than
// letting the user fill in every other field and get rejected at submit.
export function refreshPlanSaveButtonState(): void {
  const form = document.getElementById('plan-form') as HTMLFormElement | null;
  if (!form) return;
  const submitBtn = Array.from(form.elements).find((element): element is HTMLButtonElement => element instanceof HTMLButtonElement && element.type === 'submit');
  if (!submitBtn) return;
  const hasAccounts = plansState.planSelectedAccounts.length > 0;
  submitBtn.disabled = !hasAccounts || savingPlan;
  submitBtn.title = savingPlan ? 'Saving…' : hasAccounts ? '' : 'Select at least one Target Account to save the plan';
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
 * Render selected account chips in the plan modal
 */
export function renderPlanAccountChips(): void {
  const container = document.getElementById('plan-accounts-selected');
  if (!container) return;
  container.textContent = '';
  plansState.planSelectedAccounts.forEach(acct => {
    const label = `${acct.name} (${acct.external_id})`;
    const chip = document.createElement('span');
    chip.className = 'account-chip';
    // A pill that never wraps mid-name: a long name is truncated with an
    // ellipsis and shown in full on hover.
    const name = document.createElement('span');
    name.className = 'account-chip-name';
    name.textContent = label;
    name.title = label;
    chip.appendChild(name);

    const removeBtn = document.createElement('button');
    removeBtn.type = 'button';
    removeBtn.className = 'account-chip-remove';
    removeBtn.setAttribute('aria-label', `Remove ${label}`);
    removeBtn.title = 'Remove';
    removeBtn.appendChild(createIcon('x'));
    removeBtn.addEventListener('click', () => {
      const removedAt = plansState.planSelectedAccounts.findIndex(a => a.id === acct.id);
      plansState.planSelectedAccounts = plansState.planSelectedAccounts.filter(a => a.id !== acct.id);
      renderPlanAccountChips();
      updatePlanAccountIdsField();
      schedulePlanDraftSave();
      focusAfterChipRemoval(removedAt);
    });
    chip.appendChild(removeBtn);
    container.appendChild(chip);
  });
}

/**
 * Re-rendering destroys the focused remove button, so keyboard users would
 * lose their place. Focus the next chip, else the previous one, else the search box.
 */
export function focusAfterChipRemoval(removedAt: number): void {
  const buttons = document.querySelectorAll<HTMLElement>('#plan-accounts-selected .account-chip-remove');
  const target = buttons.item(removedAt) ?? buttons.item(buttons.length - 1);
  if (target) {
    target.focus();
    return;
  }
  document.getElementById('plan-account-search')?.focus();
}

/**
 * Update hidden plan-account-ids field
 */
export function updatePlanAccountIdsField(): void {
  const field = document.getElementById('plan-account-ids') as HTMLInputElement | null;
  if (field) field.value = plansState.planSelectedAccounts.map(a => a.id).join(',');
  // Recalc Save-button disabled state every time the account list changes
  // so the user gets immediate feedback when they remove the last chip.
  refreshPlanSaveButtonState();
}

let planAccountSearchTimer: ReturnType<typeof setTimeout> | null = null;

/**
 * Handle plan account search input
 */
export async function handlePlanAccountSearch(value: string): Promise<void> {
  const suggestions = document.getElementById('plan-account-suggestions');
  if (!suggestions) return;

  if (!value.trim()) {
    suggestions.classList.add('hidden');
    return;
  }

  try {
    const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement | null;
    const provider = (planSpansProviders() ? undefined : providerSelect?.value) as api.Provider | undefined;
    // Minimal-disclosure list (view:recommendations) so plan-account search
    // works for Standard / Read-Only users; the full view:accounts list 403s
    // for them. See issues #949/#951.
    const accounts = await api.listAccountsMinimal({ search: value, ...(provider ? { provider } : {}) });
    suggestions.textContent = '';
    if (accounts.length === 0) {
      suggestions.classList.add('hidden');
      return;
    }
    accounts.forEach(a => {
      if (plansState.planSelectedAccounts.some(s => s.id === a.id)) return;
      const item = document.createElement('div');
      item.className = 'account-suggestion-item';
      item.textContent = `${a.name} (${a.external_id})`;
      item.addEventListener('click', () => {
        plansState.planSelectedAccounts.push({ id: a.id, name: a.name, external_id: a.external_id });
        renderPlanAccountChips();
        updatePlanAccountIdsField();
        schedulePlanDraftSave();
        suggestions.classList.add('hidden');
        (document.getElementById('plan-account-search') as HTMLInputElement).value = '';
      });
      suggestions.appendChild(item);
    });
    suggestions.classList.remove('hidden');
  } catch {
    suggestions.classList.add('hidden');
  }
}

// planSpansProviders is true when the plan may hold groups of several providers
// (a bound plan, or a selection of several groups, whose service fields the form
// does not show), so its account picker is not restricted to the provider select (#1746).
export function planSpansProviders(): boolean {
  return plansState.editingBoundPlan || groupSelection(plansState.pendingPlanRecommendations, 0).length > 1;
}

/**
 * Set up plan accounts section in the modal
 */
export async function setupPlanAccountsSection(planId?: string): Promise<void> {
  plansState.planSelectedAccounts = [];

  const spansProviders = planSpansProviders();
  const planProvider = spansProviders ? undefined : (document.getElementById('plan-provider') as HTMLSelectElement | null)?.value;

  if (planId) {
    try {
      const existingAccounts = await api.listPlanAccounts(planId);
      // Filter out any account whose provider does not match the current plan
      // provider. This prevents stale cross-provider assignments from silently
      // surviving a provider switch on an existing plan.
      plansState.planSelectedAccounts = existingAccounts
        .filter(a => !planProvider || a.provider === planProvider)
        .map(a => ({ id: a.id, name: a.name, external_id: a.external_id }));
    } catch {
      // Non-critical — section just starts empty
    }
  }

  renderPlanAccountChips();
  updatePlanAccountIdsField();

  const searchInput = document.getElementById('plan-account-search') as HTMLInputElement | null;
  if (searchInput) {
    // Disable the search input until a provider is selected.
    searchInput.disabled = !planProvider && !spansProviders;

    // Remove previous listeners by replacing node
    const newInput = searchInput.cloneNode(true) as HTMLInputElement;
    searchInput.parentNode?.replaceChild(newInput, searchInput);
    newInput.addEventListener('input', () => {
      if (planAccountSearchTimer) clearTimeout(planAccountSearchTimer);
      planAccountSearchTimer = setTimeout(() => {
        void handlePlanAccountSearch(newInput.value);
      }, 300);
    });
  }
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
 * Fetch the account with the given internal UUID and add it as a pre-selected
 * chip in the plan modal accounts section. Runs after setupPlanAccountsSection
 * has reset the chip list for the create flow. Silently no-ops on failure so
 * the user can still pick the account manually. (#770)
 *
 * @param accountId - Internal UUID of the account to prefill.
 * @param session   - planModalSession value captured at call time. If the
 *                    modal is closed and reopened before this promise resolves,
 *                    the counter will have advanced and the stale result is
 *                    discarded to prevent wrong-modal pollution. (#770 CR)
 */
export async function prefillAccountChips(accountIds: readonly string[], session: number): Promise<void> {
  try {
    // Resolve via the minimal-disclosure list (view:recommendations) instead of
    // GET /api/accounts/:id (view:accounts) so the target prefills for
    // Standard / Read-Only users too — the per-id endpoint 403s for them, which
    // previously left the target empty and made Save Plan silently no-op once
    // the empty-target guard rejected submit. See issues #949/#951.
    const accounts = (await api.listAccountsMinimal()).filter((a) => accountIds.includes(a.id));
    // Session guard: discard the result if the modal was closed and reopened
    // while this promise was in-flight. planModalSession is incremented on
    // each new modal open, so a mismatch means this callback is stale.
    if (session !== plansState.planModalSession) return;
    // Guard: only add if the chip is not already present (e.g. a concurrent
    // edit flow somehow set it) and the account record is usable.
    if (accounts.length < accountIds.length) {
      showToast({ message: 'Some selected recommendations belong to accounts you cannot list. Add those accounts under Target accounts before saving.', kind: 'warning' });
    }
    for (const account of accounts) {
      if (account.id && !plansState.planSelectedAccounts.some(a => a.id === account.id)) {
        plansState.planSelectedAccounts.push({ id: account.id, name: account.name, external_id: account.external_id });
      }
    }
    renderPlanAccountChips();
    updatePlanAccountIdsField();
  } catch {
    // Non-critical: the user can still search and add the account manually.
  }
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
 * Generate a plan name based on the selected ramp schedule
 */
function generatePlanName(rampSchedule: string, customStepPercent?: number, customIntervalDays?: number): string {
  const service = (document.getElementById('plan-service') as HTMLSelectElement)?.value || 'EC2';
  const serviceName = serviceLabel(service);

  switch (rampSchedule) {
    case 'immediate':
      return `${serviceName} Full Coverage Purchase`;
    case 'weekly-25pct':
      return `${serviceName} Weekly 25% Ramp-up (4 weeks)`;
    case 'monthly-10pct':
      return `${serviceName} Monthly 10% Ramp-up (10 months)`;
    case 'custom':
      if (customStepPercent && customIntervalDays) {
        // Same count as the backend (buildCustomRampSchedule) and the schedule
        // preview: whole steps only, at least one. 30% is 3 steps, not 4 (#1185).
        const totalSteps = Math.max(1, Math.floor(100 / customStepPercent));
        const intervalLabel = customIntervalDays === 7 ? 'weekly' :
                              customIntervalDays === 30 ? 'monthly' :
                              `every ${customIntervalDays} days`;
        return `${serviceName} Custom ${customStepPercent}% ${intervalLabel} (${totalSteps} step${totalSteps === 1 ? '' : 's'})`;
      }
      return `${serviceName} Custom Ramp-up Plan`;
    default:
      return `${serviceName} Purchase Plan`;
  }
}

/**
 * Update plan name field based on current ramp schedule selection
 */
function updatePlanNameFromSchedule(): void {
  const planNameInput = document.getElementById('plan-name') as HTMLInputElement;
  const planIdInput = document.getElementById('plan-id') as HTMLInputElement;

  // Only auto-generate name for new plans (not editing existing ones)
  if (planIdInput?.value) return;

  const rampScheduleRadio = document.querySelector<HTMLInputElement>('input[name="ramp-schedule"]:checked');
  const rampSchedule = rampScheduleRadio?.value || 'immediate';

  let customStepPercent: number | undefined;
  let customIntervalDays: number | undefined;

  if (rampSchedule === 'custom') {
    customStepPercent = parseInt((document.getElementById('ramp-step-percent') as HTMLInputElement)?.value || '20', 10);
    customIntervalDays = parseInt((document.getElementById('ramp-interval-days') as HTMLInputElement)?.value || '7', 10);
  }

  if (planNameInput) {
    planNameInput.value = generatePlanName(rampSchedule, customStepPercent, customIntervalDays);
  }
}

/**
 * Wire live range + integer-only validation on a plan numeric input.
 *
 * Registers an `input` handler that:
 *   - reject non-integer values via the regex `^\d+$` (blocks scientific
 *     notation such as `1e+30` and decimal fractions)
 *   - show a sibling `.field-error` span when the value is out of [min, max]
 *     or non-integer, and hide it when the value is valid or the field is empty
 *   - set / clear `aria-invalid` for screen-reader accessibility
 *
 * It never rewrites the value: Save blurs the field first, so a clamp would
 * save a number the user did not type (#1534). The Save handlers refuse instead.
 *
 * Idempotent: the `data-range-wired` attribute guards against re-registering
 * duplicate listeners when the modal is closed and reopened. The error span
 * is created once on the first call and reused thereafter. Explicit `min` /
 * `max` parameters are used instead of reading HTML attributes so callers are
 * the source of truth and the helper stays self-contained.
 */
function wireRangeInput(inputId: string, min: number, max: number): void {
  const input = document.getElementById(inputId) as HTMLInputElement | null;
  if (!input) return;

  // Idempotency guard: skip registration if already wired during a prior
  // modal open. The error span, aria-describedby, and listeners persist
  // across opens because the modal node stays in the DOM.
  if (input.dataset['rangeWired']) {
    // Re-trigger validation so stale error UI is reconciled on modal reopen.
    input.dispatchEvent(new Event('input'));
    return;
  }
  input.dataset['rangeWired'] = '1';

  const errorId = `${inputId}-range-error`;
  let errorEl = document.getElementById(errorId);
  if (!errorEl) {
    errorEl = document.createElement('small');
    errorEl.id = errorId;
    errorEl.className = 'field-error hidden';
    errorEl.setAttribute('role', 'status');
    errorEl.setAttribute('aria-live', 'polite');
    input.insertAdjacentElement('afterend', errorEl);
    const existing = input.getAttribute('aria-describedby');
    input.setAttribute(
      'aria-describedby',
      existing ? `${existing} ${errorId}` : errorId,
    );
  }
  const error = errorEl;
  const message = `Must be a whole number between ${min} and ${max}`;
  const integerPattern = /^\d+$/;

  const check = (): void => {
    const raw = input.value.trim();
    if (raw === '') {
      input.removeAttribute('aria-invalid');
      error.classList.add('hidden');
      return;
    }
    if (!integerPattern.test(raw)) {
      input.setAttribute('aria-invalid', 'true');
      error.textContent = message;
      error.classList.remove('hidden');
      return;
    }
    const parsed = parseInt(raw, 10);
    if (parsed < min || parsed > max) {
      input.setAttribute('aria-invalid', 'true');
      error.textContent = message;
      error.classList.remove('hidden');
    } else {
      input.removeAttribute('aria-invalid');
      error.classList.add('hidden');
    }
  };

  input.addEventListener('input', check);
}

/**
 * Wire live range validation on all five plan-creation number inputs.
 * Called every time the plan modal opens so validation is active for both
 * the create and edit flows.
 */
function wirePlanRangeInputs(): void {
  wireRangeInput('plan-coverage', 0, 100);
  wireRangeInput('ramp-step-percent', 1, 100);
  wireRangeInput('ramp-interval-days', 1, 365);
  wireRangeInput('plan-notify-days', 1, 30);
  const cap = document.getElementById('plan-max-step-commitment') as HTMLInputElement | null;
  const auto = document.getElementById('plan-auto-purchase') as HTMLInputElement | null;
  if (cap && auto) {
    cap.required = auto.checked;
    if (!auto.dataset['capWired']) {
      auto.dataset['capWired'] = '1';
      auto.addEventListener('change', () => { cap.required = auto.checked; });
    }
  }
  void refreshPlanAutomationState();
}

function parsePlanStepCap(raw: string, automatic: boolean): number | null {
  if (raw === '') {
    if (automatic) throw new Error('Maximum commitment per step is required for Auto-Purchase');
    return null;
  }
  const value = Number(raw);
  const [whole = '', fraction = ''] = raw.split('.');
  const normalized = `${whole.replace(/^0+(?=\d)/, '')}.${fraction.padEnd(2, '0')}`;
  const [wireWhole = '', wireFraction = ''] = String(value).split('.');
  const wireAmount = `${wireWhole}.${wireFraction.padEnd(2, '0')}`;
  // Match the backend's decimal-text validation without multiplying binary floats by 100.
  if (!/^\d+(?:\.\d{1,2})?$/.test(raw) || !Number.isFinite(value) || value < 0.01 || value > 1e15 || wireAmount !== normalized) {
    throw new Error('Maximum commitment per step must be an exact amount from 0.01 to 1000000000000000 with at most two decimal places');
  }
  return value;
}

async function refreshPlanAutomationState(): Promise<void> {
  const request = ++plansState.planAutomationRead;
  const banner = document.getElementById('plan-auto-purchase-state');
  if (!banner) return;
  banner.textContent = 'Global Auto-Purchase state is unavailable.';
  if (!canAccess('view', 'config')) return;
  try {
    const config = await api.getConfig();
    if (request !== plansState.planAutomationRead) return;
    const enabled = config.global?.auto_purchase_enabled;
    if (enabled === true) banner.textContent = 'Global Auto-Purchase is enabled. Automatic plans still require a per-step cap and purchase permission.';
    if (enabled === false) banner.textContent = 'Global Auto-Purchase is off. Automatic plans will not execute until it is enabled.';
  } catch {
    if (request === plansState.planAutomationRead) banner.textContent = 'Global Auto-Purchase state could not be loaded.';
  }
}

/**
 * Set up event handlers for ramp schedule changes.
 * Guarded by rampHandlersInstalled so re-opening the plan modal does not
 * stack duplicate listeners on the static modal elements (H3, feedback_event_listener_dedup).
 */
function setupRampScheduleHandlers(): void {
  if (rampHandlersInstalled) return;
  rampHandlersInstalled = true;

  // Listen to ramp schedule radio changes
  document.querySelectorAll<HTMLInputElement>('input[name="ramp-schedule"]').forEach(radio => {
    radio.addEventListener('change', () => {
      // Update custom config fields based on selected preset
      updateCustomConfigFromPreset(radio.value);

      updatePlanNameFromSchedule();

      // Show/hide custom config
      setCustomRampConfigVisible(radio.value === 'custom');
    });
  });

  // Listen to custom schedule field changes
  const stepPercentInput = document.getElementById('ramp-step-percent');
  const intervalDaysInput = document.getElementById('ramp-interval-days');

  stepPercentInput?.addEventListener('input', updatePlanNameFromSchedule);
  intervalDaysInput?.addEventListener('input', updatePlanNameFromSchedule);

  // Listen to provider/service changes to update payment/term options
  const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement | null;
  const serviceSelect = document.getElementById('plan-service') as HTMLSelectElement | null;
  const termSelect = document.getElementById('plan-term') as HTMLSelectElement | null;
  const paymentSelect = document.getElementById('plan-payment') as HTMLSelectElement | null;

  providerSelect?.addEventListener('change', () => {
    updateCommitmentOptions();
    updatePlanNameFromSchedule();
    // Clear all selected accounts when the provider changes. Selected account
    // entries only carry id/name/external_id (no provider field), so we cannot
    // filter by provider directly. Clearing on change is the safe default:
    // an account valid for the old provider is almost never valid for the new
    // one, and it prevents a cross-provider assignment from silently reaching
    // the backend validator.
    plansState.planSelectedAccounts = [];
    renderPlanAccountChips();
    updatePlanAccountIdsField();
    // Clear and hide any open account suggestion dropdown so stale suggestions
    // from the previous provider cannot be clicked and add a mismatched account.
    const suggestions = document.getElementById('plan-account-suggestions') as HTMLElement | null;
    if (suggestions) {
      suggestions.textContent = '';
      suggestions.classList.add('hidden');
    }
    // Re-enable/disable the account search input to match the new provider state.
    const accountSearchInput = document.getElementById('plan-account-search') as HTMLInputElement | null;
    if (accountSearchInput) {
      accountSearchInput.value = '';
      accountSearchInput.disabled = !providerSelect.value;
    }
  });

  serviceSelect?.addEventListener('change', () => {
    updateCommitmentOptions();
    updatePlanNameFromSchedule();
  });

  termSelect?.addEventListener('change', () => {
    updatePaymentOptionsForTerm();
  });

  paymentSelect?.addEventListener('change', () => {
    updateTermOptionsForPayment();
  });

  // Initialize options based on default provider/service
  updateCommitmentOptions();
}

/**
 * Update term and payment options based on current provider/service selection
 */
function updateCommitmentOptions(): void {
  const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement | null;
  const serviceSelect = document.getElementById('plan-service') as HTMLSelectElement | null;
  const termSelect = document.getElementById('plan-term') as HTMLSelectElement | null;
  const paymentSelect = document.getElementById('plan-payment') as HTMLSelectElement | null;

  if (!providerSelect || !serviceSelect || !termSelect || !paymentSelect) return;

  const provider = providerSelect.value;
  const service = serviceSelect.value;

  // Populate both selects with provider/service specific options
  populateTermSelect(termSelect, provider, service);
  populatePaymentSelect(paymentSelect, provider, service);

  // Validate current selection
  validateAndFixCombination();
}

/**
 * Update payment options based on selected term
 */
function updatePaymentOptionsForTerm(): void {
  const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement | null;
  const serviceSelect = document.getElementById('plan-service') as HTMLSelectElement | null;
  const termSelect = document.getElementById('plan-term') as HTMLSelectElement | null;
  const paymentSelect = document.getElementById('plan-payment') as HTMLSelectElement | null;

  if (!providerSelect || !termSelect || !paymentSelect) return;

  const provider = providerSelect.value;
  const service = serviceSelect?.value;
  const term = parseInt(termSelect.value, 10);

  populatePaymentSelect(paymentSelect, provider, service, term);
}

/**
 * Update term options based on selected payment
 */
function updateTermOptionsForPayment(): void {
  const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement | null;
  const serviceSelect = document.getElementById('plan-service') as HTMLSelectElement | null;
  const termSelect = document.getElementById('plan-term') as HTMLSelectElement | null;
  const paymentSelect = document.getElementById('plan-payment') as HTMLSelectElement | null;

  if (!providerSelect || !termSelect || !paymentSelect) return;

  const provider = providerSelect.value;
  const service = serviceSelect?.value;
  const payment = paymentSelect.value;

  populateTermSelect(termSelect, provider, service, payment);
}

/**
 * Validate and fix invalid term/payment combinations
 */
function validateAndFixCombination(): void {
  const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement | null;
  const serviceSelect = document.getElementById('plan-service') as HTMLSelectElement | null;
  const termSelect = document.getElementById('plan-term') as HTMLSelectElement | null;
  const paymentSelect = document.getElementById('plan-payment') as HTMLSelectElement | null;

  if (!providerSelect || !termSelect || !paymentSelect) return;

  const provider = providerSelect.value;
  const service = serviceSelect?.value;
  const term = parseInt(termSelect.value, 10);
  const payment = paymentSelect.value;

  // Check if current combination is valid
  if (!isValidCombination(provider, service, term, payment)) {
    // Invalid combination - update payment to first valid option
    updatePaymentOptionsForTerm();
  }
}

function syncCustomRampConfigToSelection(): void {
  const checked = document.querySelector<HTMLInputElement>('input[name="ramp-schedule"]:checked');
  setCustomRampConfigVisible(checked?.value === 'custom');
}

/**
 * Show or hide the custom ramp config. A hidden input is disabled so its value,
 * which a preset fills with 0 days, is skipped by native validation instead of
 * blocking Save on a control the user cannot reach (#1536).
 */
function setCustomRampConfigVisible(visible: boolean): void {
  document.getElementById('custom-ramp-config')?.classList.toggle('hidden', !visible);
  for (const id of ['ramp-step-percent', 'ramp-interval-days']) {
    const input = document.getElementById(id) as HTMLInputElement | null;
    if (input) input.disabled = !visible;
  }
}

/**
 * Update custom config fields based on the selected ramp schedule preset
 */
function updateCustomConfigFromPreset(rampSchedule: string): void {
  const stepPercentInput = document.getElementById('ramp-step-percent') as HTMLInputElement;
  const intervalDaysInput = document.getElementById('ramp-interval-days') as HTMLInputElement;

  if (!stepPercentInput || !intervalDaysInput) return;

  switch (rampSchedule) {
    case 'immediate':
      stepPercentInput.value = '100';
      intervalDaysInput.value = '0';
      break;
    case 'weekly-25pct':
      stepPercentInput.value = '25';
      intervalDaysInput.value = '7';
      break;
    case 'monthly-10pct':
      stepPercentInput.value = '10';
      intervalDaysInput.value = '30';
      break;
    case 'custom':
      // Don't change values when switching to custom - let user modify
      break;
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

/**
 * Reset the ramp-handlers install-once guard.
 * Exported for unit tests only: each test rebuilds the DOM so the static
 * modal elements are fresh; without this reset the guard prevents listener
 * re-attachment and tests that open the modal multiple times break.
 * Must NOT be called in production code.
 */
export function _resetRampHandlersForTest(): void {
  rampHandlersInstalled = false;
}

export { loadPlans } from './plansflow/planCards';
export { restorePlanDraft } from './plansflow/planDraft';
