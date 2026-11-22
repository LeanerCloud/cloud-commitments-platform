import * as api from '../api';
import { showToast } from '../toast';
import { createIcon } from '../icons';
import { groupSelection } from '../lib/plan-groups';
import { plansState } from './plansState';
import { schedulePlanDraftSave } from './planDraft';
import { refreshPlanSaveButtonState } from '../plans';

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
function focusAfterChipRemoval(removedAt: number): void {
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
async function handlePlanAccountSearch(value: string): Promise<void> {
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
function planSpansProviders(): boolean {
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
