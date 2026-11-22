import * as api from '../api';
import { showToast } from '../toast';
import type { SavePlanData } from '../types';
import type { ApiError } from '../api/types';
import { groupSelection } from '../lib/plan-groups';
import { plansState } from './plansState';
import { loadPlans } from './planCards';
import { parsePlanStepCap } from './planRanges';
import { editPlan } from './planEdit';
import { closePlanModal } from './planModal';

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
      // The two writes are separate requests (the owner kept the legacy Save in
      // #1057), so a failure here means the plan row is already saved.
      try {
        await api.setPlanAccounts(planId, accountIds);
      } catch (accountsError) {
        console.error('Plan saved but account update failed:', accountsError);
        showToast({
          message: `The plan settings were saved, but the account change failed: ${(accountsError as Error).message}. Review the target accounts and save again.`,
          kind: 'error',
        });
        await loadPlans();
        return;
      }
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
