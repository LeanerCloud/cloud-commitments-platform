import * as state from '../state';
import { showToast } from '../toast';
import { applyPlanForm, clearPlanDraft, loadPlanDraft, readPlanForm, savePlanDraft } from '../lib/plan-form-draft';
import { plansState } from './plansState';
import { renderPlanAccountChips, updatePlanAccountIdsField } from '../plans';
import { editPlan } from '../plans';
import { closePlanModal, openNewPlanModal } from '../plans';

const PLAN_DRAFT_DEBOUNCE_MS = 300;

// Session copy of the open plan form (#1735). Plan-from-selection forms are skipped:
// their target snapshot is not persisted.
export function schedulePlanDraftSave(): void {
  plansState.planModalEpoch++; // the user edited the form
  if (plansState.planDraftTimer) clearTimeout(plansState.planDraftTimer);
  plansState.planDraftTimer = setTimeout(() => {
    plansState.planDraftTimer = null;
    const form = document.getElementById('plan-form') as HTMLFormElement | null;
    const userId = state.getCurrentUser()?.id;
    if (!form || !userId || plansState.pendingPlanRecommendations.length > 0) return;
    savePlanDraft({
      userId,
      planId: (document.getElementById('plan-id') as HTMLInputElement).value,
      ...readPlanForm(form),
      accounts: plansState.planSelectedAccounts.map(a => ({ ...a })),
    });
  }, PLAN_DRAFT_DEBOUNCE_MS);
}

// Reopen the plan form with the draft a session expiry interrupted (only for its own user).
export async function restorePlanDraft(stillOnPlans: () => boolean = () => true): Promise<boolean> {
  try {
    return await restorePlanDraftUnsafe(stillOnPlans);
  } catch (error) {
    // Restoring is a convenience: it must never break init or keep a draft that cannot be applied.
    console.warn('Failed to restore the plan draft:', error);
    clearPlanDraft();
    return false;
  }
}

const PLAN_DRAFT_ACCOUNTS_WAIT_MS = 5_000;

async function restorePlanDraftUnsafe(stillOnPlans: () => boolean): Promise<boolean> {
  const draft = loadPlanDraft();
  if (!draft || !stillOnPlans()) return false;
  if (draft.userId !== state.getCurrentUser()?.id) {
    clearPlanDraft();
    return false;
  }
  const epoch = plansState.planModalEpoch;
  if (draft.planId) {
    if (!(await editPlan(draft.planId))) {
      // Only a definitely gone or forbidden plan ends the draft; transient failures retry next load.
      if (plansState.editPlanFailureStatus === 404 || plansState.editPlanFailureStatus === 403) clearPlanDraft();
      return false;
    }
  } else {
    openNewPlanModal();
  }
  await Promise.race([plansState.planAccountsLoad, new Promise(resolve => setTimeout(resolve, PLAN_DRAFT_ACCOUNTS_WAIT_MS))]);
  // Exactly one open (ours) since the start: otherwise the user opened, closed or typed meanwhile.
  const mine = plansState.planModalEpoch === epoch + 1;
  const form = document.getElementById('plan-form') as HTMLFormElement | null;
  if (!mine || !form || !stillOnPlans()) {
    if (mine) closePlanModal();
    clearPlanDraft();
    return false;
  }
  applyPlanForm(form, draft);
  plansState.planSelectedAccounts = draft.accounts.map(a => ({ ...a }));
  renderPlanAccountChips();
  updatePlanAccountIdsField();
  showToast({ message: 'Restored your unsaved plan draft. Review it and save.', kind: 'info', timeout: 8_000 });
  return true;
}
