import * as api from '../../api';
import { showToast } from '../../toast';
import type { AccountProvider } from '../settings-state';
import { refreshRecommendationsAfterOverrideChange } from './refresh';
import { loadOverridesPanel } from './panel';

/**
 * Build the per-row Coverage <input> for the overrides panel. Issue #110.
 *
 * Numeric input bound to the row's coverage % (0–100, integer). PUT
 * fires on the 'change' event (blur or Enter), not 'input', so we don't
 * issue one PUT per keystroke. Out-of-range or non-numeric input
 * reverts to the previous value with an error toast.
 *
 * Empty value when no coverage is set: placeholder shows "Inherit".
 * Clearing a previously-set value reverts (same no-clear-field
 * rationale as Term and Payment).
 */
export function buildCoverageOverrideInput(
  accountId: string,
  override: api.AccountServiceOverride,
  panel: HTMLElement,
): HTMLInputElement {
  const input = document.createElement('input');
  input.type = 'number';
  input.min = '0';
  input.max = '100';
  input.step = '1';
  input.className = 'override-coverage-input';
  input.setAttribute(
    'aria-label',
    `Dashboard coverage for ${override.provider}/${override.service} override`,
  );

  const initial = override.coverage !== undefined ? String(override.coverage) : '';
  input.value = initial;
  input.dataset['previous'] = initial;
  if (initial === '') {
    input.placeholder = 'Inherit';
  }

  input.addEventListener('change', () => {
    void handleCoverageOverrideChange(accountId, override, input, panel);
  });

  return input;
}

async function handleCoverageOverrideChange(
  accountId: string,
  override: api.AccountServiceOverride,
  input: HTMLInputElement,
  panel: HTMLElement,
): Promise<void> {
  const previous = input.dataset['previous'] ?? '';
  const raw = input.value.trim();
  if (raw === previous) return;
  if (raw === '') {
    // No clear-field channel — clearing a coverage value would leave the
    // override row in a no-op state. Revert and tell the user.
    input.value = previous;
    showToast({
      message: 'Use Delete to remove the override entirely (clears all fields including coverage).',
      kind: 'info',
    });
    return;
  }
  const n = Number(raw);
  if (!Number.isFinite(n) || !Number.isInteger(n) || n < 0 || n > 100) {
    input.value = previous;
    showToast({
      message: 'Coverage must be an integer between 0 and 100.',
      kind: 'error',
    });
    return;
  }
  input.disabled = true;
  try {
    await api.saveAccountServiceOverride(accountId, override.provider, override.service, {
      coverage: n,
    });
    input.dataset['previous'] = String(n);
    showToast({
      message: `Coverage override updated for ${override.provider}/${override.service}.`,
      kind: 'success',
    });
    await loadOverridesPanel(accountId, panel, override.provider as AccountProvider);
    await refreshRecommendationsAfterOverrideChange();
  } catch (err) {
    input.value = previous;
    showToast({
      message: `Failed to update coverage override: ${(err as Error).message}`,
      kind: 'error',
    });
  } finally {
    input.disabled = false;
  }
}

/**
 * Build the per-row Enabled checkbox for the overrides panel. Issue #110.
 *
 * Toggles the `enabled` column on `account_service_overrides`. When
 * unchecked, the override row exists but does not influence
 * recommendations (the backend's read path filters by `enabled`). The
 * UI dims the row to make the inactive state obvious.
 *
 * Default state: an override with `enabled === undefined` (legacy rows,
 * or rows never explicitly toggled) is treated as ACTIVE. Only explicit
 * `enabled: false` renders as unchecked.
 *
 * Non-AWS rows render the checkbox disabled with an explanatory tooltip
 * to preserve column alignment between provider types — Azure/GCP
 * editing is tracked separately under the provider-aware modal work.
 */
export function buildEnabledOverrideCheckbox(
  accountId: string,
  override: api.AccountServiceOverride,
  panel: HTMLElement,
  row: HTMLTableRowElement,
): HTMLInputElement {
  const input = document.createElement('input');
  input.type = 'checkbox';
  input.className = 'override-enabled-toggle';
  input.checked = override.enabled !== false;
  input.dataset['previous'] = input.checked ? 'true' : 'false';
  input.setAttribute(
    'aria-label',
    `Enable ${override.provider}/${override.service} override`,
  );

  input.addEventListener('change', () => {
    void handleEnabledOverrideChange(accountId, override, input, panel, row);
  });

  return input;
}

async function handleEnabledOverrideChange(
  accountId: string,
  override: api.AccountServiceOverride,
  input: HTMLInputElement,
  panel: HTMLElement,
  row: HTMLTableRowElement,
): Promise<void> {
  const next = input.checked;
  const previousStr = input.dataset['previous'] ?? 'true';
  const previous = previousStr === 'true';
  if (next === previous) return;
  input.disabled = true;
  // Optimistically reflect the new state in the row's visual treatment
  // so the user sees feedback before the network round-trip completes;
  // we revert on error below.
  row.classList.toggle('override-disabled', !next);
  try {
    await api.saveAccountServiceOverride(accountId, override.provider, override.service, {
      enabled: next,
    });
    input.dataset['previous'] = next ? 'true' : 'false';
    showToast({
      message: `${override.provider}/${override.service} override ${next ? 'enabled' : 'disabled'}.`,
      kind: 'success',
    });
    await loadOverridesPanel(accountId, panel, override.provider as AccountProvider);
    await refreshRecommendationsAfterOverrideChange();
  } catch (err) {
    input.checked = previous;
    row.classList.toggle('override-disabled', !previous);
    showToast({
      message: `Failed to update enabled state: ${(err as Error).message}`,
      kind: 'error',
    });
  } finally {
    input.disabled = false;
  }
}
