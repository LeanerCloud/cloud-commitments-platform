import * as api from '../../api';
import { showToast } from '../../toast';
import {
  getValidPaymentOptions,
  getValidTermOptions,
  getPaymentLabel,
  getCommitmentConfig,
} from '../../commitmentOptions';
import { inheritCurrentlyLabel, paymentLabel, termLabel } from '../settings-fields';
import type { AccountProvider } from '../settings-state';
import { overridesState } from './state';
import { refreshRecommendationsAfterOverrideChange } from './refresh';
import { loadOverridesPanel } from '../settings-overrides';

/**
 * Build the per-row Payment <select> for the overrides panel. Issue #23.
 *
 * - "Inherit (default)" keeps the override silent on payment so the global
 *   default applies. We only emit it as the chosen value when the override
 *   currently has no payment set; once a payment is set, switching back
 *   would require deleting/recreating the override (out of scope for #23).
 * - The change handler PUTs only `{ payment }` — the backend's
 *   overlayOverrideScalars (handler_accounts_overrides.go) treats unset request fields
 *   as "leave alone", so other override fields (term, coverage, etc.) are
 *   preserved.
 */
export function buildPaymentOverrideSelect(
  accountId: string,
  override: api.AccountServiceOverride,
  panel: HTMLElement,
): HTMLSelectElement {
  const select = document.createElement('select');
  select.className = 'override-payment-select';
  select.setAttribute(
    'aria-label',
    `Payment option for ${override.provider}/${override.service} override`,
  );

  const inheritOpt = document.createElement('option');
  inheritOpt.value = '';
  // Issue #112: show the current global payment so the user knows what
  // "Inherit" means without switching to the Purchasing tab.
  inheritOpt.textContent = inheritCurrentlyLabel(paymentLabel(overridesState.cachedGlobalDefaults.payment));
  select.appendChild(inheritOpt);

  // Filter the payment dropdown to only the (term, payment) combinations
  // the provider/service actually supports. e.g. RDS rejects 3yr no-upfront;
  // Azure has upfront/monthly only; GCP has monthly only. Per #107.
  // Falls back to the full provider payment list when term is missing (override
  // row was saved without a term) — the global default's term will resolve at
  // recommendation time, and we can't pre-validate without it.
  const overrideTerm = override.term ?? 0;
  const providerPayments = getCommitmentConfig(override.provider).payments;
  const validPayments = overrideTerm > 0
    ? getValidPaymentOptions(override.provider, override.service, overrideTerm)
    : providerPayments.map(p => ({ value: p.value, label: p.label }));

  for (const { value, label } of validPayments) {
    const opt = document.createElement('option');
    opt.value = value;
    opt.textContent = label;
    select.appendChild(opt);
  }

  const initial = override.payment ?? '';

  // If the row's CURRENT payment isn't in the valid set (e.g., the row was
  // saved via direct API curl before this filter shipped, or AWS tightened
  // the rules), still render the current value so the row isn't silently
  // mutated — but mark it as a problem so the user notices.
  const currentIsValid = initial === '' || validPayments.some(p => p.value === initial);
  if (!currentIsValid) {
    const stale = document.createElement('option');
    stale.value = initial;
    stale.textContent = `${getPaymentLabel(initial)} (invalid for term ${overrideTerm}y — Delete the override and recreate)`;
    stale.dataset['stale'] = 'true';
    select.appendChild(stale);
  }

  select.value = initial;
  select.dataset['previous'] = initial;

  // If a payment is already set, "Inherit" is not a clean transition (the
  // backend has no clear-field channel — it would require resetting the
  // whole override). Disable the option so the user can still pick another
  // payment value but can't accidentally pick a no-op state.
  if (initial !== '') {
    inheritOpt.disabled = true;
    inheritOpt.title = 'Use Delete to remove the override entirely (clears all fields including payment)';
  }

  select.addEventListener('change', () => {
    void handlePaymentOverrideChange(accountId, override, select, panel);
  });

  return select;
}

async function handlePaymentOverrideChange(
  accountId: string,
  override: api.AccountServiceOverride,
  select: HTMLSelectElement,
  panel: HTMLElement,
): Promise<void> {
  const previous = select.dataset['previous'] ?? '';
  const next = select.value;
  if (next === previous) return;
  if (next === '') {
    // Reverting to Inherit while a payment is set is blocked above; defensive
    // no-op here keeps types narrow if the option becomes selectable later.
    select.value = previous;
    return;
  }
  select.disabled = true;
  try {
    await api.saveAccountServiceOverride(accountId, override.provider, override.service, {
      payment: next,
    });
    select.dataset['previous'] = next;
    showToast({
      message: `Payment override updated for ${override.provider}/${override.service}.`,
      kind: 'success',
    });
    await loadOverridesPanel(accountId, panel, override.provider as AccountProvider);
    await refreshRecommendationsAfterOverrideChange();
  } catch (err) {
    select.value = previous;
    showToast({
      message: `Failed to update payment override: ${(err as Error).message}`,
      kind: 'error',
    });
  } finally {
    select.disabled = false;
  }
}

/**
 * Build the per-row Term <select> for the overrides panel. Issue #110.
 *
 * Mirrors buildPaymentOverrideSelect (#23) verbatim: Inherit option +
 * provider/service-specific valid options + sparse PUT on change. The
 * options are filtered by the row's CURRENT payment via
 * `getValidTermOptions('aws', service, payment)`, so an RDS row with
 * payment=no-upfront does not list term=3 (the one hard AWS rule per
 * commitmentOptions.ts).
 *
 * When `term` is already set on the row, the Inherit option is disabled
 * for the same reason as Payment's Inherit: the backend has no
 * clear-field channel, so reverting to "no term" would require Delete.
 */
export function buildTermOverrideSelect(
  accountId: string,
  override: api.AccountServiceOverride,
  panel: HTMLElement,
): HTMLSelectElement {
  const select = document.createElement('select');
  select.className = 'override-term-select';
  select.setAttribute(
    'aria-label',
    `Term for ${override.provider}/${override.service} override`,
  );

  const inheritOpt = document.createElement('option');
  inheritOpt.value = '';
  // Issue #112: show the current global term so the user knows what
  // "Inherit" means without switching to the Purchasing tab.
  inheritOpt.textContent = inheritCurrentlyLabel(termLabel(String(overridesState.cachedGlobalDefaults.term)));
  select.appendChild(inheritOpt);

  // Filter the term dropdown to the (term, payment) combinations AWS
  // supports for THIS service given the row's CURRENT payment. e.g. an
  // RDS row with payment=no-upfront cannot upgrade to term=3.
  // When payment is unset, getValidTermOptions falls back to the
  // service's full term list (the underlying `getValidTermOptions`
  // returns all terms whose invalidCombinations don't match the given
  // payment; with payment='' nothing matches, so all terms are valid).
  const currentPayment = override.payment ?? '';
  const validTerms = getValidTermOptions(override.provider, override.service, currentPayment);

  for (const { value, label } of validTerms) {
    const opt = document.createElement('option');
    opt.value = String(value);
    opt.textContent = label;
    select.appendChild(opt);
  }

  const initial = override.term !== undefined && override.term > 0 ? String(override.term) : '';

  // If the row's CURRENT term isn't in the valid set (e.g., the row was
  // saved before the filter shipped, or AWS tightened the rules), still
  // render the current value so the row isn't silently mutated, but
  // mark it as a problem.
  const currentIsValid = initial === '' || validTerms.some(t => String(t.value) === initial);
  if (!currentIsValid) {
    const stale = document.createElement('option');
    stale.value = initial;
    stale.textContent = `${initial}yr (invalid for current payment — Delete the override and recreate)`;
    stale.dataset['stale'] = 'true';
    select.appendChild(stale);
  }

  select.value = initial;
  select.dataset['previous'] = initial;

  // If a term is already set, "Inherit" is not a clean transition (the
  // backend has no clear-field channel). Same rationale as Payment.
  if (initial !== '') {
    inheritOpt.disabled = true;
    inheritOpt.title = 'Use Delete to remove the override entirely (clears all fields including term)';
  }

  select.addEventListener('change', () => {
    void handleTermOverrideChange(accountId, override, select, panel);
  });

  return select;
}

async function handleTermOverrideChange(
  accountId: string,
  override: api.AccountServiceOverride,
  select: HTMLSelectElement,
  panel: HTMLElement,
): Promise<void> {
  const previous = select.dataset['previous'] ?? '';
  const next = select.value;
  if (next === previous) return;
  if (next === '') {
    // Reverting to Inherit while a term is set is blocked above; defensive
    // no-op here keeps types narrow if the option becomes selectable later.
    select.value = previous;
    return;
  }
  const termNum = parseInt(next, 10);
  if (Number.isNaN(termNum)) {
    select.value = previous;
    return;
  }
  select.disabled = true;
  try {
    await api.saveAccountServiceOverride(accountId, override.provider, override.service, {
      term: termNum,
    });
    select.dataset['previous'] = next;
    showToast({
      message: `Term override updated for ${override.provider}/${override.service}.`,
      kind: 'success',
    });
    await loadOverridesPanel(accountId, panel, override.provider as AccountProvider);
    await refreshRecommendationsAfterOverrideChange();
  } catch (err) {
    select.value = previous;
    showToast({
      message: `Failed to update term override: ${(err as Error).message}`,
      kind: 'error',
    });
  } finally {
    select.disabled = false;
  }
}
