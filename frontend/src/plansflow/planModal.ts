import * as api from '../api';
import { populateTermSelect, populatePaymentSelect, normalizePaymentValue } from '../commitmentOptions';
import { openModal, closeModal } from '../modal';
import { describeGroup, groupSelection } from '../lib/plan-groups';
import { mountPlanEditorPanel } from '../plan-editor-panel';
import { renderPlanFiltersNotice } from '../plan-filters-notice';
import { clearPlanDraft } from '../lib/plan-form-draft';
import { plansState } from './plansState';
import { setupPlanAccountsSection, prefillAccountChips } from './planAccounts';
import { updatePlanNameFromSchedule, wirePlanRangeInputs } from './planRanges';
import { setupRampScheduleHandlers, syncCustomRampConfigToSelection } from './rampSchedule';

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
