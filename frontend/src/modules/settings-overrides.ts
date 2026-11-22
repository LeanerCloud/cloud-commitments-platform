import * as api from '../api';
import { confirmDialog } from '../confirmDialog';
import { friendlyError } from '../api/errors';
import { showToast } from '../toast';
import {
  isValidCombination,
  getValidPaymentOptions,
  getValidTermOptions,
  getPaymentLabel,
  getCommitmentConfig,
} from '../commitmentOptions';
import { openModal, closeModal } from '../modal';
import { showSkeletonText } from '../lib/skeleton';
import { legacySettingsServiceTarget } from '../lib/settings-service-ids';
import { buildLastUpdatedStamp } from '../lib/last-updated';
import { byId, inheritCurrentlyLabel, paymentLabel, setInputValue, termLabel } from './settings-fields';
import type { AccountProvider } from './settings-state';
import { overridesState } from './settings-overridesflow/state';
import type { OverrideModalContext } from './settings-overridesflow/state';
import { refreshRecommendationsAfterOverrideChange } from './settings-overridesflow/refresh';
import { getOverrideServicesForProvider } from './settings-overridesflow/services';
import { applyOverrideBulkMode, syncBulkServiceConflicts } from './settings-overridesflow/bulk';

export { setGlobalDefaultsForTest, setCachedGlobalDefaults } from './settings-overridesflow/state';

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
function buildPaymentOverrideSelect(
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
function buildTermOverrideSelect(
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
function buildCoverageOverrideInput(
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
function buildEnabledOverrideCheckbox(
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
function appendLegacyOverrideHint(cell: HTMLElement, o: api.AccountServiceOverride): void {
  const target = legacySettingsServiceTarget(o.provider, o.service);
  if (target === null) return;
  const hint = document.createElement('div');
  hint.className = 'help-text override-legacy-hint';
  hint.textContent = `Old service id: never applied. Re-create it as ${o.provider}/${target}, then delete this row.`;
  cell.appendChild(hint);
}

export async function loadOverridesPanel(accountId: string, panel: HTMLElement, provider: AccountProvider): Promise<void> {
  showSkeletonText(panel, 2); // issue #186: was a plain "Loading…" line
  try {
    const overrides = await api.listAccountServiceOverrides(accountId);
    panel.textContent = '';

    // Issue #109: all providers can now create overrides.
    const canCreate = true;

    if (!overrides || overrides.length === 0) {
      const msg = document.createElement('p');
      msg.className = 'help-text';
      msg.textContent = 'No service overrides yet for this account.';
      panel.appendChild(msg);
      if (canCreate) {
        // No overrides yet \u2014 auto-open the modal so the user lands directly
        // on the create flow instead of staring at empty-state text. The
        // button stays in the panel as a fallback if the user cancels and
        // wants to retry without re-expanding the row.
        const addBtn = document.createElement('button');
        addBtn.type = 'button';
        addBtn.className = 'btn btn-small';
        addBtn.textContent = 'Add override';
        addBtn.addEventListener('click', () => {
          openOverrideModal(accountId, provider, overrides ?? [], panel);
        });
        panel.appendChild(addBtn);
        openOverrideModal(accountId, provider, [], panel);
      }
      return;
    }

    if (canCreate) {
      const addBtn = document.createElement('button');
      addBtn.type = 'button';
      addBtn.className = 'btn btn-small';
      addBtn.textContent = 'Add override';
      addBtn.addEventListener('click', () => {
        openOverrideModal(accountId, provider, overrides, panel);
      });
      panel.appendChild(addBtn);
    }

    const table = document.createElement('table');
    table.className = 'overrides-table';
    const thead = table.createTHead();
    const hrow = thead.insertRow();
    // Column order: Service | Term | Payment | Coverage | Enabled | <action>
    // Enabled is second-to-last (immediately before the action cell) so the
    // descriptive columns keep the layout users learned from PR #72/#23.
    // Issue #110.
    ['Service', 'Term', 'Payment', 'Dashboard coverage', 'Enabled', ''].forEach(h => {
      const th = document.createElement('th');
      th.textContent = h;
      hrow.appendChild(th);
    });
    const tbody = table.createTBody();
    overrides.forEach(o => {
      const tr = tbody.insertRow();
      // Reflect the row's enabled state visually from the initial render so
      // disabled overrides are obvious without waiting for a toggle event.
      if (o.enabled === false) {
        tr.classList.add('override-disabled');
      }

      const serviceTd = tr.insertCell();
      serviceTd.textContent = `${o.provider}/${o.service}`;
      // When this override last changed (issue #8), under the service name so
      // the column order users learned (#110) stays put.
      const stamp = buildLastUpdatedStamp(o.updated_at);
      if (stamp) serviceTd.append(stamp);
      // Old Azure/GCP service id (#613): warn below the stamp.
      appendLegacyOverrideHint(serviceTd, o);

      // Term cell: editable <select> for all providers (issue #109 extends
      // the AWS-only #110 inline editing to Azure/GCP).
      const termTd = tr.insertCell();
      termTd.appendChild(buildTermOverrideSelect(accountId, o, panel));

      // Payment cell: editable <select> for all providers. GCP's payment
      // dropdown only shows "Monthly"; Azure shows upfront/monthly. The
      // underlying getValidPaymentOptions already handles per-provider
      // filtering, so the same builder works for all three clouds.
      const paymentTd = tr.insertCell();
      paymentTd.appendChild(buildPaymentOverrideSelect(accountId, o, panel));

      // Coverage cell: editable numeric <input> for all providers (issue #109).
      const coverageTd = tr.insertCell();
      coverageTd.appendChild(buildCoverageOverrideInput(accountId, o, panel));

      // Enabled cell: per-row checkbox toggling account_service_overrides.enabled.
      // Editable for all providers. Issue `#110`, extended to Azure/GCP in `#109`.
      const enabledTd = tr.insertCell();
      enabledTd.appendChild(buildEnabledOverrideCheckbox(accountId, o, panel, tr));

      const actionTd = tr.insertCell();
      const resetBtn = document.createElement('button');
      resetBtn.type = 'button';
      resetBtn.className = 'btn btn-small btn-danger';
      // Button + dialog wording aligned with the actual data semantics
      // (DELETE on account_service_overrides) per #114. The previous
      // "Reset … will be replaced" copy implied the row stuck around
      // with new values; in fact the row goes away entirely and the
      // engine reads the global default as a side effect of its absence.
      resetBtn.textContent = 'Delete';
      resetBtn.addEventListener('click', async () => {
        const ok = await confirmDialog({
          title: 'Delete override?',
          body: `Delete the ${o.provider}/${o.service} override? This account's recommendations will revert to the global default. The override row and any per-service values you set will be removed.`,
          confirmLabel: 'Delete override',
          destructive: true,
        });
        if (!ok) return;
        // Snapshot all fields before deleting so the Undo action can re-PUT
        // them if the user clicks the toast button within the 5-second window.
        const snapshot: api.AccountServiceOverrideRequest = {
          enabled: o.enabled,
          term: o.term,
          payment: o.payment,
          coverage: o.coverage,
          ramp_schedule: o.ramp_schedule,
          include_engines: o.include_engines,
          exclude_engines: o.exclude_engines,
          include_regions: o.include_regions,
          exclude_regions: o.exclude_regions,
          include_types: o.include_types,
          exclude_types: o.exclude_types,
        };
        try {
          await api.deleteAccountServiceOverride(accountId, o.provider, o.service);
        } catch (err) {
          showToast({ message: `Failed to delete override: ${(err as Error).message}`, kind: 'error' });
          return;
        }
        // The server has confirmed the delete. Show the 10-second undo toast
        // IMMEDIATELY, before the panel reload + recommendations refresh below.
        // Previously the toast was awaited behind both of those calls, so a
        // slow recommendations refresh (a full page-worth of API calls) delayed
        // the Undo affordance long enough that users reported seeing no toast
        // at all (issue #1415). The snapshot-restore Undo is valid regardless
        // of the UI refresh, so it must not be gated behind it. Keeping the
        // failed-delete branch above also stops a refresh error from being
        // misreported as "Failed to delete override" when the delete succeeded.
        //
        // The action closure captures `snapshot` and re-PUTs it if the user
        // clicks Undo before the toast expires. Each Delete click replaces any
        // prior toast handle so a rapid double-click cannot stack multiple
        // Undo buttons.
        showToast({
          message: `Override for ${o.provider}/${o.service} deleted.`,
          kind: 'info',
          // 10 s (was 5 s, issue #728) so there is time to read the message
          // and reach Undo; the countdown also pauses on hover/focus.
          timeout: 10_000,
          actions: [{
            label: 'Undo',
            onClick: () => {
              void (async () => {
                try {
                  await api.saveAccountServiceOverride(accountId, o.provider, o.service, snapshot);
                  await loadOverridesPanel(accountId, panel, provider);
                  await refreshRecommendationsAfterOverrideChange();
                } catch (undoErr) {
                  showToast({ message: `Failed to restore override: ${(undoErr as Error).message}`, kind: 'error' });
                }
              })();
            },
          }],
        });
        await loadOverridesPanel(accountId, panel, provider);
        await refreshRecommendationsAfterOverrideChange();
      });
      actionTd.appendChild(resetBtn);
    });
    panel.appendChild(table);
  } catch (err) {
    panel.textContent = `Failed to load overrides: ${(err as Error).message}`;
  }
}

let overridePaymentOptionsController: AbortController | null = null;

/**
 * Open the create-override modal for a specific account.
 *
 * `existingOverrides` is used to filter the service dropdown so users
 * can't pick a service that already has an override for this account
 * (the backend would UPSERT, silently overwriting the existing values —
 * users should use the panel's Reset + re-create flow for that, not the
 * Add flow).
 *
 * Issue #104.
 */
export function openOverrideModal(
  accountId: string,
  provider: string,
  existingOverrides: api.AccountServiceOverride[],
  panel: HTMLElement,
): void {
  const modal = document.getElementById('override-modal');
  if (!modal) return;

  // Populate available-services list before writing context so bulk mode can
  // reference it from the start.
  const used = new Set(
    existingOverrides
      .filter(o => o.provider === provider)
      .map(o => o.service),
  );
  const available = getOverrideServicesForProvider(provider).filter(s => !used.has(s.value));

  overridesState.overrideModalContext = { accountId, provider, panel, availableServices: available };

  setInputValue('override-account-id', accountId);
  setInputValue('override-provider', provider);

  // Reset form fields to defaults each open so a previous cancelled
  // session doesn't bleed values forward.
  setInputValue('override-term', '');
  setInputValue('override-payment', '');
  setInputValue('override-coverage', '');
  const errEl = document.getElementById('override-form-error');
  if (errEl) errEl.textContent = '';

  // Issue #112: update the "Inherit" option on the term select to show the
  // current global default so the user knows what they'd be inheriting.
  const termInheritOpt = document.querySelector<HTMLOptionElement>('#override-term option[value=""]');
  if (termInheritOpt) {
    termInheritOpt.textContent = inheritCurrentlyLabel(termLabel(String(overridesState.cachedGlobalDefaults.term)));
  }

  // Issue #112: update the coverage placeholder to show the current global
  // default. Uses .placeholder (plain text assignment), not innerHTML -- safe.
  const coverageInput = document.getElementById('override-coverage') as HTMLInputElement | null;
  if (coverageInput) {
    coverageInput.placeholder = inheritCurrentlyLabel(`${overridesState.cachedGlobalDefaults.coverage}%`);
  }

  // Reset bulk-mode toggle to off so the modal always opens in single-service
  // mode (issue #119).
  const bulkToggle = document.getElementById('override-bulk-toggle') as HTMLInputElement | null;
  if (bulkToggle) bulkToggle.checked = false;
  applyOverrideBulkMode(false, available);

  // Populate the single-service dropdown with provider-specific services,
  // excluding any that already have an override for this account. Issue #109
  // extends this to Azure and GCP (previously AWS-only per issue #104 follow-up).
  const select = document.getElementById('override-service') as HTMLSelectElement | null;
  if (select) {
    select.replaceChildren();
    if (available.length === 0) {
      const opt = document.createElement('option');
      opt.value = '';
      opt.textContent = 'All services already have overrides';
      opt.disabled = true;
      select.appendChild(opt);
      const submitBtn = document.getElementById('override-submit-btn') as HTMLButtonElement | null;
      if (submitBtn) submitBtn.disabled = true;
    } else {
      for (const { value, label } of available) {
        const opt = document.createElement('option');
        opt.value = value;
        opt.textContent = label;
        select.appendChild(opt);
      }
      const submitBtn = document.getElementById('override-submit-btn') as HTMLButtonElement | null;
      if (submitBtn) submitBtn.disabled = false;
    }
  }

  // Wire service/term changes to repopulate payment options. AWS rejects
  // some (service, term, payment) tuples (e.g., RDS 3yr no-upfront) — the
  // global Settings cards already hide invalid combinations; the override
  // modal must do the same so we can't silently save an invalid combo.
  // Per #107.
  overridePaymentOptionsController?.abort();
  overridePaymentOptionsController = new AbortController();
  syncOverridePaymentOptions(provider);
  // After payment options update, re-evaluate bulk conflict gating (issue #119).
  const onChange = () => {
    syncOverridePaymentOptions(provider);
    syncBulkServiceConflicts(provider);
  };
  select?.addEventListener('change', onChange, { signal: overridePaymentOptionsController.signal });
  const termSel = document.getElementById('override-term') as HTMLSelectElement | null;
  termSel?.addEventListener('change', onChange, { signal: overridePaymentOptionsController.signal });
  const paymentSel = document.getElementById('override-payment') as HTMLSelectElement | null;
  paymentSel?.addEventListener('change', onChange, { signal: overridePaymentOptionsController.signal });

  // Wire the bulk-mode toggle (issue #119). AbortController cleans it up on close.
  bulkToggle?.addEventListener(
    'change',
    () => {
      const ctx = overridesState.overrideModalContext;
      applyOverrideBulkMode(bulkToggle.checked, ctx?.availableServices ?? []);
    },
    { signal: overridePaymentOptionsController.signal },
  );

  // Focus moves into the dialog and Escape closes it (#731). When this opens
  // on top of the account overrides modal, closing it hands focus back there.
  openModal(modal, { onClose: resetOverrideModalState });
}

function resetOverrideModalState(): void {
  overridesState.overrideModalContext = null;
  overridePaymentOptionsController?.abort();
  overridePaymentOptionsController = null;
  // Reset bulk toggle so next open always starts in single-service mode.
  const bulkToggle = document.getElementById('override-bulk-toggle') as HTMLInputElement | null;
  if (bulkToggle) bulkToggle.checked = false;
  applyOverrideBulkMode(false, []);
}

// syncOverridePaymentOptions filters the override-payment <select> down to
// the (service, term) combinations AWS supports. Called whenever the user
// changes service or term in the modal. Per #107.
function syncOverridePaymentOptions(provider: string): void {
  const serviceSel = document.getElementById('override-service') as HTMLSelectElement | null;
  const termSel = document.getElementById('override-term') as HTMLSelectElement | null;
  const paymentSel = document.getElementById('override-payment') as HTMLSelectElement | null;
  if (!serviceSel || !termSel || !paymentSel) return;

  const service = serviceSel.value;
  const term = parseInt(termSel.value, 10);

  const previous = paymentSel.value;
  paymentSel.replaceChildren();

  // Always include the "Inherit (currently: X)" option — corresponds to
  // leaving the field unset on the sparse PUT (issue #112).
  const inheritOpt = document.createElement('option');
  inheritOpt.value = '';
  inheritOpt.textContent = inheritCurrentlyLabel(paymentLabel(overridesState.cachedGlobalDefaults.payment));
  paymentSel.appendChild(inheritOpt);

  // When service or term is unset, can't run the validity check yet —
  // show the full provider payment list so the user can choose; the
  // submit-side guard will catch any invalid combo on Save.
  const provPayments = getCommitmentConfig(provider).payments;
  const candidates = (service && Number.isFinite(term) && term > 0)
    ? getValidPaymentOptions(provider, service, term).map(p => ({ value: p.value, label: p.label }))
    : provPayments.map(p => ({ value: p.value, label: p.label }));

  for (const { value, label } of candidates) {
    const opt = document.createElement('option');
    opt.value = value;
    opt.textContent = label;
    paymentSel.appendChild(opt);
  }

  // Preserve the previous selection if it's still valid; otherwise snap
  // back to "Inherit". Snapping (rather than forcing a new value) avoids
  // a confusing silent change.
  const previousStillValid = previous === '' || candidates.some(c => c.value === previous);
  paymentSel.value = previousStillValid ? previous : '';
}

export function closeOverrideModal(): void {
  const modal = document.getElementById('override-modal');
  if (modal) closeModal(modal);
  // Idempotent; covers a close of a modal that was never opened via openModal.
  resetOverrideModalState();
}

/**
 * Submit the create-override form. Sends a sparse PUT — fields left blank
 * (term/payment/coverage = "Inherit") are omitted so the backend's
 * overlayOverrideScalars treats them as "unset → inherit global default".
 */
export async function submitOverrideForm(e: Event): Promise<void> {
  e.preventDefault();
  const ctx = overridesState.overrideModalContext;
  if (!ctx) return;

  const termRaw = (byId<HTMLSelectElement>('override-term')?.value ?? '').trim();
  const paymentRaw = (byId<HTMLSelectElement>('override-payment')?.value ?? '').trim();
  const coverageRaw = (byId<HTMLInputElement>('override-coverage')?.value ?? '').trim();

  const req: api.AccountServiceOverrideRequest = {};
  if (termRaw !== '') req.term = parseInt(termRaw, 10);
  if (paymentRaw !== '') req.payment = paymentRaw;
  if (coverageRaw !== '') {
    const n = Number(coverageRaw);
    if (!Number.isFinite(n) || !Number.isInteger(n) || n < 0 || n > 100) {
      const errEl = document.getElementById('override-form-error');
      if (errEl) errEl.textContent = 'Dashboard coverage must be a whole number between 0 and 100.';
      return;
    }
    req.coverage = n;
  }

  if (Object.keys(req).length === 0) {
    // The backend would happily store an all-null override row, but it'd
    // be a no-op semantically — no field overrides anything. Block at the
    // UI to avoid silent confusion.
    const errEl = document.getElementById('override-form-error');
    if (errEl) errEl.textContent = 'Set at least one of Term, Payment, or Dashboard coverage.';
    return;
  }

  // Determine which services to save to.
  const bulkToggle = document.getElementById('override-bulk-toggle') as HTMLInputElement | null;
  const isBulk = bulkToggle?.checked ?? false;

  if (isBulk) {
    await submitBulkOverrideForm(ctx, req);
    return;
  }

  // Single-service path (original behaviour).
  const service = (byId<HTMLSelectElement>('override-service')?.value ?? '').trim();
  if (!service) return;

  // Submit-side validation per #107: when both term and payment are
  // explicitly set, refuse to save invalid (service, term, payment)
  // combinations even if a stale select option somehow leaked through
  // (e.g., browser back-button restoring a stale dropdown state).
  // Mirror of the global Settings → Purchasing card's checkCommitmentOptionCombo.
  if (req.term != null && req.payment) {
    if (!isValidCombination(ctx.provider, service, req.term, req.payment)) {
      const errEl = document.getElementById('override-form-error');
      if (errEl) {
        errEl.textContent = `${ctx.provider}/${service} does not support ${req.term}-year ${req.payment}. Pick a different term or payment.`;
      }
      return;
    }
  }

  const submitBtn = document.getElementById('override-submit-btn') as HTMLButtonElement | null;
  if (submitBtn) submitBtn.disabled = true;
  try {
    await api.saveAccountServiceOverride(ctx.accountId, ctx.provider, service, req);
    showToast({
      message: `Override created for ${ctx.provider}/${service}.`,
      kind: 'success',
    });
    closeOverrideModal();
    await loadOverridesPanel(ctx.accountId, ctx.panel, ctx.provider as AccountProvider);
    await refreshRecommendationsAfterOverrideChange();
  } catch (err) {
    const errEl = document.getElementById('override-form-error');
    if (errEl) errEl.textContent = `Failed to create override: ${(err as Error).message}`;
  } finally {
    if (submitBtn) submitBtn.disabled = false;
  }
}

/**
 * Fan out save calls for all checked services (issue #119).
 *
 * Uses Promise.allSettled so a failure on one service does not abort the
 * others. Shows one aggregated toast on completion summarising how many
 * succeeded and which failed.
 */
async function submitBulkOverrideForm(
  ctx: OverrideModalContext,
  req: api.AccountServiceOverrideRequest,
): Promise<void> {
  const listDiv = document.getElementById('override-bulk-services-list');
  const checked = Array.from(
    listDiv?.querySelectorAll<HTMLInputElement>('input[type="checkbox"]:checked') ?? [],
  ).map(cb => cb.value);

  if (checked.length === 0) {
    const errEl = document.getElementById('override-form-error');
    if (errEl) errEl.textContent = 'Select at least one service.';
    return;
  }

  // Validate (provider, service, term, payment) combinations before fanning out.
  if (req.term != null && req.payment) {
    const invalid = checked.filter(
      service => !isValidCombination(ctx.provider, service, req.term!, req.payment!),
    );
    if (invalid.length > 0) {
      const errEl = document.getElementById('override-form-error');
      if (errEl) {
        errEl.textContent = `${invalid.join(', ')}: does not support ${req.term}-year ${req.payment}. Pick a different term or payment.`;
      }
      return;
    }
  }

  const submitBtn = document.getElementById('override-submit-btn') as HTMLButtonElement | null;
  if (submitBtn) submitBtn.disabled = true;
  const errEl = document.getElementById('override-form-error');
  if (errEl) errEl.textContent = '';

  const results = await Promise.allSettled(
    checked.map(service => api.saveAccountServiceOverride(ctx.accountId, ctx.provider, service, req)),
  );

  const failed = checked.filter((_, i) => results[i]?.status === 'rejected');
  const succeeded = checked.length - failed.length;

  if (failed.length === 0) {
    showToast({
      message: `Created ${succeeded} override${succeeded !== 1 ? 's' : ''}.`,
      kind: 'success',
    });
    closeOverrideModal();
    await loadOverridesPanel(ctx.accountId, ctx.panel, ctx.provider as AccountProvider);
    await refreshRecommendationsAfterOverrideChange();
  } else if (succeeded === 0) {
    if (submitBtn) submitBtn.disabled = false;
    if (errEl) {
      // Show the server's reason (e.g. a validation error) instead of
      // always blaming the connection (issue #748).
      const firstFailure = results.find((r): r is PromiseRejectedResult => r.status === 'rejected');
      errEl.textContent = `All ${failed.length} save${failed.length !== 1 ? 's' : ''} failed: ${friendlyError(firstFailure?.reason)}`;
    }
  } else {
    // Partial success: close and reload, surface the failures in a toast.
    showToast({
      message: `Created ${succeeded} override${succeeded !== 1 ? 's' : ''}; failed for: ${failed.join(', ')}.`,
      kind: 'warning',
    });
    closeOverrideModal();
    await loadOverridesPanel(ctx.accountId, ctx.panel, ctx.provider as AccountProvider);
    await refreshRecommendationsAfterOverrideChange();
  }
}
