import * as api from '../api';
import { showToast } from '../toast';
import { getValidPaymentOptions, getValidTermOptions, getPaymentLabel, getCommitmentConfig } from '../commitmentOptions';
import { openModal, closeModal } from '../modal';
import { loadRecommendations } from '../recommendations';
import { legacySettingsServiceTarget } from '../lib/settings-service-ids';
import { closeOverrideModal, loadOverridesPanel } from '../settings';
import { inheritCurrentlyLabel, paymentLabel, termLabel } from './settings-fields';
import type { AccountProvider } from './settings-state';

/**
 * Issue #196 — after any mutation to an `account_service_overrides` row
 * (create, edit, reset/delete), refresh the cached recommendations list so
 * the read-time filters (`enabled`, include/exclude lists) and the
 * dashboard's coverage cap reflect the new state without a manual reload.
 *
 * Errors are swallowed: the override mutation that just succeeded shouldn't
 * be reported as a failure because the secondary refresh fails. The recs
 * page re-fetches on the next nav event, so a stale list is recoverable.
 */
export async function refreshRecommendationsAfterOverrideChange(): Promise<void> {
  try {
    await loadRecommendations();
  } catch (err) {
    console.warn('Failed to refresh recommendations after override change:', err);
  }
}

// Cached global defaults surfaced by loadGlobalSettings. Used by the override
// modal (issue #112) to show "Inherit (currently: X)" labels so the user can
// see what they'd be overriding without switching tabs.
export let cachedGlobalDefaults: { term: number; payment: string; coverage: number } = {
  term: 3,
  payment: 'no-upfront',
  coverage: 80,
};

/**
 * Override the cached global defaults used for the "Inherit (currently: X)"
 * labels. Test-only; production code updates the cache via loadGlobalSettings.
 */
export function setGlobalDefaultsForTest(defaults: { term: number; payment: string; coverage: number }): void {
  cachedGlobalDefaults = { ...defaults };
}

export function setCachedGlobalDefaults(defaults: { term: number; payment: string; coverage: number }): void {
  cachedGlobalDefaults = defaults;
}

// AWS services that can carry a per-account override. Mirrors the AWS-only
// entries in SERVICE_FIELDS. Issue #109 adds AZURE_OVERRIDE_SERVICES and
// GCP_OVERRIDE_SERVICES below; getOverrideServicesForProvider dispatches to
// the right list so the create modal is provider-aware.
// (issue #104) since the backend payment/term semantics differ per provider.
const AWS_OVERRIDE_SERVICES: ReadonlyArray<{ value: string; label: string }> = [
  { value: 'ec2',                       label: 'EC2 (Reserved Instances)' },
  { value: 'rds',                       label: 'RDS' },
  { value: 'elasticache',               label: 'ElastiCache' },
  { value: 'opensearch',                label: 'OpenSearch' },
  { value: 'redshift',                  label: 'Redshift' },
  // Issue #22 follow-up: per-plan-type SP slugs aligned with
  // SERVICE_FIELDS so per-account overrides target the same rows the
  // global Settings cards write to. The legacy `savingsplans` and PR
  // #71 `sagemaker` values were removed because they no longer
  // correspond to live ServiceConfig rows after migration 000040.
  { value: 'savings-plans-compute',     label: 'Compute Savings Plans' },
  { value: 'savings-plans-ec2instance', label: 'EC2 Instance Savings Plans' },
  { value: 'savings-plans-sagemaker',   label: 'SageMaker Savings Plans' },
  { value: 'savings-plans-database',    label: 'Database Savings Plans' },
];

// Human-readable labels for Azure and GCP services used in the override
// service dropdown. AWS labels are carried by AWS_OVERRIDE_SERVICES above.
// Values are the service ids recommendations carry (issue #613), the same
// ones SERVICE_FIELDS writes.
const AZURE_OVERRIDE_SERVICES: ReadonlyArray<{ value: string; label: string }> = [
  { value: 'compute',       label: 'Virtual Machines' },
  { value: 'relational-db', label: 'SQL Database' },
  { value: 'nosql',         label: 'Cosmos DB' },
  { value: 'cache',         label: 'Cache for Redis' },
  { value: 'search',        label: 'AI Search' },
];

const GCP_OVERRIDE_SERVICES: ReadonlyArray<{ value: string; label: string }> = [
  { value: 'compute',       label: 'Compute Engine' },
  { value: 'relational-db', label: 'Cloud SQL' },
  { value: 'cache',         label: 'Memorystore' },
];

/** Return the service list for the given provider's override modal. */
export function getOverrideServicesForProvider(provider: string): ReadonlyArray<{ value: string; label: string }> {
  if (provider === 'azure') return AZURE_OVERRIDE_SERVICES;
  if (provider === 'gcp')   return GCP_OVERRIDE_SERVICES;
  return AWS_OVERRIDE_SERVICES;
}

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
  inheritOpt.textContent = inheritCurrentlyLabel(paymentLabel(cachedGlobalDefaults.payment));
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
  inheritOpt.textContent = inheritCurrentlyLabel(termLabel(String(cachedGlobalDefaults.term)));
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

/**
 * Load and render the service overrides panel for an account.
 *
 * Empty state (no overrides yet for this account) auto-opens the create
 * modal \u2014 the previous "No service overrides set." text was a dead-end
 * because there was no UI affordance to create one. See issue #104.
 *
 * Populated state: render the existing table + an "Add override" button at
 * the top so users can add another override for a different service.
 *
 * Issue #109: Azure and GCP accounts now also get the "Add override" button
 * and full inline editing (term, payment, coverage, enabled) on existing rows.
 * Provider-specific payment options are derived via getCommitmentConfig so
 * GCP shows "Monthly only" and Azure shows "Upfront / Monthly".
 */
/**
 * Open the per-account overrides modal (issue #122). Replaces the inline
 * expandable panel that used to render below the accounts table — the
 * panels stacked without per-row attachment, so two open panels gave the
 * user no way to tell which override row belonged to which account.
 *
 * The modal title binds explicitly to the account; the body uses the same
 * `loadOverridesPanel` rendering function as before, so all CRUD wiring
 * (Reset button, inline payment select, Add override button, empty-state
 * auto-open of the create modal) carries forward unchanged.
 */
export function openAccountOverridesModal(account: api.CloudAccount): void {
  const modal = document.getElementById('account-overrides-modal');
  if (!modal) return;
  const titleEl = document.getElementById('account-overrides-modal-title');
  if (titleEl) {
    titleEl.textContent = `Service overrides for ${account.name} (${account.external_id})`;
  }
  const body = document.getElementById('account-overrides-modal-body');
  if (!body) return;
  // openModal moves focus inside, traps Tab and closes on Escape (#731).
  // onClose runs on every close path, Escape included.
  openModal(modal, { onClose: resetAccountOverridesModal });
  void loadOverridesPanel(account.id, body, account.provider);
}

export function closeAccountOverridesModal(): void {
  const modal = document.getElementById('account-overrides-modal');
  if (modal) closeModal(modal);
  // Idempotent; covers a close of a modal that was never opened via openModal.
  resetAccountOverridesModal();
}

function resetAccountOverridesModal(): void {
  // Close the inner create modal too if it's still open. The user can't
  // visually reach the outer's close affordances while the inner is up
  // (inner backdrop covers them), but a programmatic close should leave a
  // clean slate, not an orphaned inner modal whose Save would target a
  // hidden parent.
  closeOverrideModal();
  // Drop the body content so the next open doesn't briefly flash stale
  // data from a previous account before loadOverridesPanel populates it.
  const body = document.getElementById('account-overrides-modal-body');
  if (body) body.replaceChildren();
}

// Issue #613: overrides saved under an old Azure/GCP service id never match a
// recommendation. Say so on the row and name the id to re-create it under.
export function appendLegacyOverrideHint(cell: HTMLElement, o: api.AccountServiceOverride): void {
  const target = legacySettingsServiceTarget(o.provider, o.service);
  if (target === null) return;
  const hint = document.createElement('div');
  hint.className = 'help-text override-legacy-hint';
  hint.textContent = `Old service id: never applied. Re-create it as ${o.provider}/${target}, then delete this row.`;
  cell.appendChild(hint);
}
