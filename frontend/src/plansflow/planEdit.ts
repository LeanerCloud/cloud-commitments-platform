import * as api from '../api';
import { showToast } from '../toast';
import { populateTermSelect, populatePaymentSelect, normalizePaymentValue } from '../commitmentOptions';
import { describeGroup } from '../lib/plan-groups';
import { renderPlanFiltersNotice } from '../plan-filters-notice';
import { plansState } from './plansState';
import { BackendPlan, LEGACY_PLAN_SERVICE_IDS, extractPlanInfo, rampRadioValue } from './planModel';
import { setupPlanAccountsSection } from './planAccounts';
import { wirePlanRangeInputs } from './planRanges';
import { setupRampScheduleHandlers, setCustomRampConfigVisible } from './rampSchedule';
import { showSelectionGroups, setPlanFieldsLocked, openPlanForm } from '../plans';

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
