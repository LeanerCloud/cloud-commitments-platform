/**
 * Settings module for CUDly
 */

import * as api from './api';
import { fetchAndPopulateCommitmentOptions } from './commitmentOptions';
import { initFederationPanel } from './federation';
import { confirmDialog } from './confirmDialog';
import { friendlyError } from './api/errors';
import { deleteAccount } from './settings-account-delete';
import { showToast } from './toast';
import { rowIconButton } from './lib/row-icon-button';
import {
  isValidCombination,
  getValidPaymentOptions,
  getCommitmentConfig,
  normalizePaymentValue,
} from './commitmentOptions';
import { openModal, closeModal } from './modal';
import {
  injectServiceFilterControls,
  populateServiceFilterControls,
  renderUpdatedStamp,
  readServiceFilterControls,
  isServiceFilterError,
} from './serviceFilters';
import { initLadderingSettings } from './ladder';
import { showSkeletonText } from './lib/skeleton';
import { canonicalizeServiceConfigs } from './lib/settings-service-ids';
import { renderAwsBastionTrustPolicy, renderAwsTrustPolicy } from './modules/settings-aws-trust';
import { setupDirtyTracking, snapshotAllFields, updateDirtyMarkers } from './modules/settings-dirty';
import {
  applyReadOnlySettings,
  byId,
  copyToClipboard,
  getFieldValue,
  inheritCurrentlyLabel,
  isPermissionDeniedError,
  paymentLabel,
  populateGraceInput,
  readGraceInput,
  setInputChecked,
  setInputValue,
  termLabel,
} from './modules/settings-fields';
import {
  cachedGlobalDefaults,
  closeAccountOverridesModal,
  getOverrideServicesForProvider,
  loadOverridesPanel,
  openAccountOverridesModal,
  refreshRecommendationsAfterOverrideChange,
  setCachedGlobalDefaults,
} from './modules/settings-overrides';
import {
  confirmAndPropagatePayment,
  confirmAndPropagateTerm,
  syncAllServiceCommitmentConstraints,
  syncPaymentConstraintsForService,
} from './modules/settings-propagation';
import { revalidateInlineRanges, wireInlineRangeValidation } from './modules/settings-range-validation';
import {
  GRACE_INPUTS,
  changedServiceFields,
  invalidEditedCardCoverages,
  newServiceConfigDefaults,
  rebaseMultiFieldKeys,
  serviceSaveFailureMessage,
  showPropagatedCoverage,
  showSavedGrace,
} from './modules/settings-save-helpers';
import { GLOBAL_CONFIG_FIELDS, SERVICE_FIELDS, TRACKED_FIELDS, savedSnapshot } from './modules/settings-state';
import type { AccountProvider } from './modules/settings-state';
export { resetConfigCache } from './modules/settings-aws-trust';
export { isUnsavedChanges } from './modules/settings-dirty';
export { isPermissionDeniedError, applyReadOnlySettings, copyToClipboard } from './modules/settings-fields';
export { setGlobalDefaultsForTest, loadOverridesPanel } from './modules/settings-overrides';

let cachedSourceCloud: string | undefined;

// sessionStorage key for the in-progress (unsaved) AWS External ID. Persists
// across modal close/reopen cycles within the same session so the operator
// can copy the value into AWS, close the modal, and come back to the same
// value (issue #126). Cleared on successful save.
const AWS_DRAFT_EXTERNAL_ID_KEY = 'cudly:aws-account-modal:draft-external-id';

/** Options for the account modal (used by the registrations approval flow). */
export interface AccountModalOptions {
  onSave?: (provider: AccountProvider, request: api.CloudAccountRequest) => Promise<void>;
}

// Track which provider's account is being edited (for the modal)
let accountModalProvider: AccountProvider = 'aws';
// Optional callback for the registrations approval flow.
let accountModalOnSave: AccountModalOptions['onSave'] | undefined;

// Cached service configs from last load — used as base when saving to preserve
// non-UI fields when a row is re-keyed. For an existing row the server overlays only
// the UI-editable keys and ignores ramp_schedule; it honours ramp_schedule only on a row's first save.
let loadedServiceConfigs: api.ServiceConfig[] = [];

// Rows loaded from an old Settings id (#613) and not yet written under the
// current one; keyed `provider/service`.
let legacyRekeyedServices = new Set<string>();

// saveInFlight guards saveGlobalSettings against concurrent invocations
// (rapid Save clicks, Enter-in-form, etc.). Toggled in a try/finally and
// mirrored on the Save button's disabled attribute so the UI reflects the
// in-progress state.
let saveInFlight = false;

/**
 * Load and render accounts list for a provider
 */
export async function loadAccountsForProvider(provider: AccountProvider): Promise<void> {
  const container = document.getElementById(`${provider}-accounts-list`);
  if (!container) return;
  try {
    const accounts = await api.listAccounts({ provider });
    renderSelfAccountBanner(container, accounts, provider);
    renderAccountsList(container, accounts, provider);
  } catch {
    container.textContent = 'Failed to load accounts.';
  }
}

/**
 * Show a banner to add CUDly's own host account if not already present.
 * Only shown for the provider matching CUDLY_SOURCE_CLOUD.
 */
async function renderSelfAccountBanner(container: HTMLElement, accounts: api.CloudAccount[], provider: AccountProvider): Promise<void> {
  // Remove any existing banner first (prevents duplicates on re-render)
  container.querySelector('.self-account-banner')?.remove();

  const hasSelf = accounts.some(a => a.is_self);
  if (hasSelf) return;

  // Check if this provider matches the source cloud
  try {
    const cfg = await api.getConfig();
    if (!cfg.source_identity || cfg.source_identity.provider !== provider) return;
    const extId = cfg.source_identity.account_id || cfg.source_identity.subscription_id || cfg.source_identity.project_id;
    if (!extId) return;

    const banner = document.createElement('div');
    banner.className = 'account-row self-account-banner';

    const info = document.createElement('span');
    info.className = 'account-info';
    info.textContent = `CUDly host account (${extId})`;
    banner.appendChild(info);

    const addBtn = document.createElement('button');
    addBtn.type = 'button';
    addBtn.className = 'btn btn-small';
    addBtn.textContent = 'Add';
    addBtn.addEventListener('click', async () => {
      addBtn.disabled = true;
      addBtn.textContent = 'Adding...';
      try {
        await api.createSelfAccount();
        await loadAccountsForProvider(provider);
      } catch (e: unknown) {
        const msg = e instanceof Error ? e.message : String(e);
        if (msg.includes('409')) {
          await loadAccountsForProvider(provider);
        } else {
          addBtn.textContent = 'Failed';
        }
      }
    });
    banner.appendChild(addBtn);

    container.prepend(banner);
  } catch {
    // Config fetch failed — skip silently
  }
}

/**
 * Render accounts list into a container element as a scannable table.
 *
 * Replaces the prior inline-row layout ("Account Name (12345) Edit Test
 * Credentials Overrides Delete" as a single text-flow). The table surfaces
 * each field in its own column, uses a status pill for Active/Disabled,
 * and isolates the destructive Delete action in the right-most column.
 */
type AccountStatusFilter = 'all' | 'active' | 'disabled';

function renderAccountsList(
  container: HTMLElement,
  accounts: api.CloudAccount[],
  provider: AccountProvider,
  filter: AccountStatusFilter = 'all'
): void {
  // Remove prior rendered rows (banner lives in a separate className
  // managed by renderSelfAccountBanner). `.account-overrides-panel` used
  // to be cleaned up here too — it was the inline expandable panel each
  // account carried before #122/#124 moved overrides into a per-account
  // modal. No such elements exist anymore.
  container.querySelectorAll(
    '.accounts-table, .accounts-empty, .status-chip-row'
  ).forEach(el => el.remove());

  if (!accounts || accounts.length === 0) {
    if (!container.querySelector('.self-account-banner')) {
      const empty = document.createElement('p');
      empty.className = 'accounts-empty';
      empty.textContent = 'No accounts configured.';
      container.appendChild(empty);
    }
    return;
  }

  // Status chip filter — mirrors the P3 audit recommendation scoped to
  // states the per-provider table actually exposes (Active/Disabled).
  const activeCount = accounts.filter(a => a.enabled).length;
  const disabledCount = accounts.length - activeCount;
  const chipRow = document.createElement('div');
  chipRow.className = 'status-chip-row';
  chipRow.setAttribute('role', 'tablist');
  chipRow.setAttribute('aria-label', 'Filter accounts by status');
  const chips: Array<{ key: AccountStatusFilter; label: string; count: number }> = [
    { key: 'all', label: 'All', count: accounts.length },
    { key: 'active', label: 'Active', count: activeCount },
    { key: 'disabled', label: 'Disabled', count: disabledCount },
  ];
  chips.forEach(({ key, label, count }) => {
    const chip = document.createElement('button');
    chip.type = 'button';
    chip.className = 'status-chip' + (filter === key ? ' active' : '');
    chip.textContent = `${label} (${count})`;
    chip.setAttribute('role', 'tab');
    chip.setAttribute('aria-selected', String(filter === key));
    chip.addEventListener('click', () => {
      if (filter === key) return;
      renderAccountsList(container, accounts, provider, key);
    });
    chipRow.appendChild(chip);
  });
  container.appendChild(chipRow);

  const visible = accounts.filter(a => {
    if (filter === 'active') return a.enabled;
    if (filter === 'disabled') return !a.enabled;
    return true;
  });

  const table = document.createElement('table');
  table.className = 'accounts-table';
  const thead = document.createElement('thead');
  const headerRow = document.createElement('tr');
  ['Name', 'Account ID', 'Status', 'Actions'].forEach((label) => {
    const th = document.createElement('th');
    th.textContent = label;
    headerRow.appendChild(th);
  });
  thead.appendChild(headerRow);
  table.appendChild(thead);

  const tbody = document.createElement('tbody');

  visible.forEach((account) => {
    const accountLabel = account.name.includes(account.external_id)
      ? account.name
      : `${account.name} (${account.external_id})`;
    const tr = document.createElement('tr');

    const nameTd = document.createElement('td');
    nameTd.textContent = account.name;
    if (account.is_self) {
      const self = document.createElement('span');
      self.className = 'badge badge-info';
      self.textContent = ' Self';
      self.setAttribute('title', 'The cloud account running CUDly itself');
      nameTd.appendChild(self);
    }
    tr.appendChild(nameTd);

    const idTd = document.createElement('td');
    idTd.className = 'monospace';
    idTd.textContent = account.external_id;
    tr.appendChild(idTd);

    const statusTd = document.createElement('td');
    const status = document.createElement('span');
    status.className = account.enabled
      ? 'badge badge-success'
      : 'badge badge-warning';
    status.textContent = account.enabled ? 'Active' : 'Disabled';
    statusTd.appendChild(status);
    tr.appendChild(statusTd);

    const actionsTd = document.createElement('td');
    actionsTd.className = 'account-actions';
    // Compact icon buttons in one non-wrapping row, like the Users rows.
    const actions = document.createElement('div');
    actions.className = 'row-actions';

    const editBtn = rowIconButton({
      icon: 'edit', text: 'Edit', title: 'Edit account', ariaLabel: `Edit ${accountLabel}`,
    });
    editBtn.addEventListener('click', () => openAccountModal(provider, account));
    actions.appendChild(editBtn);

    const testBtn = rowIconButton({
      icon: 'zap', text: 'Test', title: 'Test credentials', ariaLabel: `Test credentials for ${accountLabel}`,
    });
    testBtn.addEventListener('click', () => void testAccount(account.id, accountLabel, testBtn));
    actions.appendChild(testBtn);

    const overridesBtn = rowIconButton({
      icon: 'sliders', text: 'Overrides', title: 'Service overrides', ariaLabel: `Service overrides for ${accountLabel}`,
    });
    overridesBtn.addEventListener('click', () => openAccountOverridesModal(account));
    actions.appendChild(overridesBtn);

    // Destructive Delete: the subtle danger icon variant, set slightly apart
    // from the routine actions. .btn-destructive stays as the hook that
    // identifies the row's delete action.
    const deleteBtn = rowIconButton({
      icon: 'trash-2', text: 'Delete', title: 'Delete account', ariaLabel: `Delete ${accountLabel}`,
      className: 'btn-destructive btn-danger account-delete-btn',
    });
    deleteBtn.addEventListener('click', () => void deleteAccount(account, provider, loadAccountsForProvider));
    actions.appendChild(deleteBtn);

    actionsTd.appendChild(actions);
    tr.appendChild(actionsTd);
    tbody.appendChild(tr);
  });
  table.appendChild(tbody);
  container.appendChild(table);
  // Per-account overrides used to render in inline panels appended below
  // the table; they now live in the account-overrides-modal (issue #122)
  // so the accounts table stays compact and per-account scoping is
  // unambiguous.
}

/**
 * Test account credentials. Surfaces progress + outcome via toast so the
 * signal isn't confined to the button label (which auto-clears in 3s and
 * provides no detail beyond OK/Failed). The button keeps its inline status
 * as a secondary affordance so the user's gaze point — the row they just
 * clicked — still gets feedback.
 */
async function testAccount(accountId: string, accountLabel: string, btn: HTMLButtonElement): Promise<void> {
  // The row action is an icon button: progress and result go to its hidden
  // text, its tooltip and a data-test-result tint; the toasts carry the
  // message itself.
  const labelEl = btn.querySelector<HTMLElement>('.sr-only') ?? btn;
  const original = labelEl.textContent;
  const originalTitle = btn.title;
  const show = (text: string, result?: 'ok' | 'failed'): void => {
    labelEl.textContent = text;
    btn.title = text;
    if (result) btn.dataset['testResult'] = result;
  };
  const restore = (): void => {
    labelEl.textContent = original;
    btn.title = originalTitle;
    delete btn.dataset['testResult'];
    btn.disabled = false;
  };
  btn.disabled = true;
  show('Testing...');
  showToast({ message: `Testing credentials for ${accountLabel}…`, kind: 'info', timeout: 3_000 });
  try {
    const result = await api.testAccountCredentials(accountId);
    show(result.ok ? 'OK' : 'Failed', result.ok ? 'ok' : 'failed');
    if (result.ok) {
      const detail = result.message ? `: ${result.message}` : '';
      showToast({ message: `Credentials OK for ${accountLabel}${detail}`, kind: 'success', timeout: 5_000 });
    } else {
      showToast({
        message: `Credentials failed for ${accountLabel}${result.message ? `: ${result.message}` : ''}`,
        kind: 'error',
        timeout: null,
      });
    }
    setTimeout(restore, 3000);
  } catch (err: unknown) {
    show('Error', 'failed');
    const msg = err instanceof Error ? err.message : String(err);
    showToast({ message: `Failed to test credentials for ${accountLabel}: ${msg}`, kind: 'error', timeout: null });
    setTimeout(restore, 3000);
  }
}

// State carried from openOverrideModal → submitOverrideForm so the form
// handler knows which account/provider/panel it belongs to without
// inspecting the DOM. Cleared on close so it can't leak across opens.
let overrideModalContext: {
  accountId: string;
  provider: string;
  panel: HTMLElement;
  /** Services available for bulk selection (excludes already-overridden). */
  availableServices: ReadonlyArray<{ value: string; label: string }>;
} | null = null;
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

  overrideModalContext = { accountId, provider, panel, availableServices: available };

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
    termInheritOpt.textContent = inheritCurrentlyLabel(termLabel(String(cachedGlobalDefaults.term)));
  }

  // Issue #112: update the coverage placeholder to show the current global
  // default. Uses .placeholder (plain text assignment), not innerHTML -- safe.
  const coverageInput = document.getElementById('override-coverage') as HTMLInputElement | null;
  if (coverageInput) {
    coverageInput.placeholder = inheritCurrentlyLabel(`${cachedGlobalDefaults.coverage}%`);
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
      const ctx = overrideModalContext;
      applyOverrideBulkMode(bulkToggle.checked, ctx?.availableServices ?? []);
    },
    { signal: overridePaymentOptionsController.signal },
  );

  // Focus moves into the dialog and Escape closes it (#731). When this opens
  // on top of the account overrides modal, closing it hands focus back there.
  openModal(modal, { onClose: resetOverrideModalState });
}

/**
 * Switch the override modal between single-service and bulk-service mode.
 *
 * In single mode the normal <select id="override-service"> is shown.
 * In bulk mode it is hidden and a checkbox group is shown instead (issue #119).
 * Uses DOM APIs throughout — no innerHTML — so no XSS risk from service values.
 */
function applyOverrideBulkMode(
  bulk: boolean,
  available: ReadonlyArray<{ value: string; label: string }>,
): void {
  const singleRow = document.getElementById('override-single-service-row');
  const bulkDiv   = document.getElementById('override-bulk-services');
  const listDiv   = document.getElementById('override-bulk-services-list');
  const submitBtn = document.getElementById('override-submit-btn') as HTMLButtonElement | null;

  if (singleRow) singleRow.classList.toggle('hidden', bulk);
  if (bulkDiv)   bulkDiv.classList.toggle('hidden', !bulk);

  if (!bulk) {
    // Returning to single-service mode: recompute disabled state from the
    // single-service <select> so the button is not left stuck disabled after
    // toggling back from bulk mode.
    if (submitBtn) {
      const singleSelect = singleRow?.querySelector('select') as HTMLSelectElement | null;
      submitBtn.disabled = (singleSelect?.value ?? '') === '';
    }
    return;
  }

  // Rebuild the checkbox list using DOM APIs (no innerHTML).
  if (listDiv) {
    listDiv.replaceChildren();
    for (const { value, label } of available) {
      const checkLabel = document.createElement('label');
      checkLabel.className = 'bulk-service-checkbox-label';

      const cb = document.createElement('input');
      cb.type  = 'checkbox';
      cb.name  = 'bulk-service';
      cb.value = value;
      // Re-evaluate submit-button state whenever a checkbox changes.
      cb.addEventListener('change', () => {
        if (submitBtn) {
          const anyChecked = listDiv.querySelectorAll('input[type="checkbox"]:checked').length > 0;
          submitBtn.disabled = !anyChecked;
        }
      });

      const span = document.createElement('span');
      span.textContent = label;

      checkLabel.appendChild(cb);
      checkLabel.appendChild(span);
      listDiv.appendChild(checkLabel);
    }
  }

  // In bulk mode start with submit disabled until at least one box is checked.
  if (submitBtn) submitBtn.disabled = true;

  // Apply conflict gating for the current term/payment selection immediately
  // after the list is built, so checkboxes are already disabled if the chosen
  // combo is invalid for a service before the user interacts (issue #119).
  syncBulkServiceConflicts(overrideModalContext?.provider ?? '');
}

/**
 * Disable (and annotate) bulk-service checkboxes whose service does not
 * support the currently selected (term, payment) combo.  Re-enables them
 * if the combo becomes valid again.  Mirrors the single-select path that
 * uses syncOverridePaymentOptions / isValidCombination.  Issue #119.
 *
 * Only acts when both term and payment are explicitly set; when either is
 * blank ("Inherit") every checkbox stays enabled because no hard restriction
 * can be evaluated.
 */
function syncBulkServiceConflicts(provider: string): void {
  const termRaw = (document.getElementById('override-term') as HTMLSelectElement | null)?.value ?? '';
  const paymentRaw = (document.getElementById('override-payment') as HTMLSelectElement | null)?.value ?? '';

  const listDiv = document.getElementById('override-bulk-services-list');
  if (!listDiv) return;

  const checkboxes = listDiv.querySelectorAll<HTMLInputElement>('input[name="bulk-service"]');
  if (checkboxes.length === 0) return;

  // When term or payment is unset ("Inherit"), no restriction can be evaluated.
  const term = termRaw !== '' ? parseInt(termRaw, 10) : NaN;
  const payment = paymentRaw;
  const canEvaluate = payment !== '' && Number.isFinite(term) && term > 0;

  for (const cb of checkboxes) {
    const service = cb.value;
    const label = cb.closest('label');
    if (canEvaluate && !isValidCombination(provider, service, term, payment)) {
      cb.disabled = true;
      cb.checked  = false;
      if (label) {
        label.title = `${provider}/${service} does not support ${term}-year ${payment}`;
        label.classList.add('bulk-service-checkbox-label--conflict');
      }
    } else {
      cb.disabled = false;
      if (label) {
        label.title = '';
        label.classList.remove('bulk-service-checkbox-label--conflict');
      }
    }
  }

  // Re-evaluate submit-button state after disabling/enabling checkboxes.
  const submitBtn = document.getElementById('override-submit-btn') as HTMLButtonElement | null;
  if (submitBtn) {
    const anyChecked = listDiv.querySelectorAll('input[type="checkbox"]:checked').length > 0;
    submitBtn.disabled = !anyChecked;
  }
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
  inheritOpt.textContent = inheritCurrentlyLabel(paymentLabel(cachedGlobalDefaults.payment));
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

function resetOverrideModalState(): void {
  overrideModalContext = null;
  overridePaymentOptionsController?.abort();
  overridePaymentOptionsController = null;
  // Reset bulk toggle so next open always starts in single-service mode.
  const bulkToggle = document.getElementById('override-bulk-toggle') as HTMLInputElement | null;
  if (bulkToggle) bulkToggle.checked = false;
  applyOverrideBulkMode(false, []);
}

/**
 * Submit the create-override form. Sends a sparse PUT — fields left blank
 * (term/payment/coverage = "Inherit") are omitted so the backend's
 * overlayOverrideScalars treats them as "unset → inherit global default".
 */
async function submitOverrideForm(e: Event): Promise<void> {
  e.preventDefault();
  const ctx = overrideModalContext;
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
  ctx: NonNullable<typeof overrideModalContext>,
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

/**
 * Open account modal for add or edit
 */
export function openAccountModal(provider: AccountProvider, account?: api.CloudAccount, options?: AccountModalOptions): void {
  accountModalProvider = provider;
  accountModalOnSave = options?.onSave;
  const modal = document.getElementById('account-modal');
  if (!modal) return;

  const title = document.getElementById('account-modal-title');
  if (title) title.textContent = account ? 'Edit account' : 'Add account';

  setInputValue('account-id', account?.id ?? '');
  setInputValue('account-provider', provider);
  setInputValue('account-name', account?.name ?? '');
  setInputValue('account-description', account?.description ?? '');
  setInputValue('account-contact-email', account?.contact_email ?? '');
  setInputValue('account-external-id', account?.external_id ?? '');
  setInputChecked('account-enabled', account?.enabled ?? true);
  // Approving a registration auto-enables the account server-side, so the toggle would be a no-op there.
  document.getElementById('account-enabled-row')?.classList.toggle('hidden', !!options?.onSave);

  // Show/hide provider-specific fields
  const awsFields = document.getElementById('account-aws-fields');
  const azureFields = document.getElementById('account-azure-fields');
  const gcpFields = document.getElementById('account-gcp-fields');
  if (awsFields) awsFields.classList.toggle('hidden', provider !== 'aws');
  if (azureFields) azureFields.classList.toggle('hidden', provider !== 'azure');
  if (gcpFields) gcpFields.classList.toggle('hidden', provider !== 'gcp');

  if (provider === 'aws') {
    populateAwsAccountFields(account);
  } else if (provider === 'azure') {
    const azureAuthMode = account?.azure_auth_mode ?? 'workload_identity_federation';
    const azureAuthSelect = document.getElementById('account-azure-auth-mode') as HTMLSelectElement | null;
    if (azureAuthSelect) azureAuthSelect.value = azureAuthMode;
    setInputValue('account-azure-tenant-id', account?.azure_tenant_id ?? '');
    setInputValue('account-azure-client-id', account?.azure_client_id ?? '');
    setInputValue('account-azure-client-secret', '');
    updateAzureAuthModeFields(azureAuthMode);
  } else if (provider === 'gcp') {
    const gcpMode = account?.gcp_auth_mode ?? 'workload_identity_federation';
    const gcpAuthSelect = document.getElementById('account-gcp-auth-mode') as HTMLSelectElement | null;
    if (gcpAuthSelect) gcpAuthSelect.value = gcpMode;
    const gcpEmailEl = document.getElementById('account-gcp-client-email') as HTMLInputElement | null;
    if (gcpEmailEl) gcpEmailEl.value = account?.gcp_client_email ?? '';
    const gcpJsonEl = document.getElementById('account-gcp-service-account-json') as HTMLTextAreaElement | null;
    if (gcpJsonEl) gcpJsonEl.value = '';
    const wifConfig = document.getElementById('account-gcp-wif-config') as HTMLTextAreaElement | null;
    if (wifConfig) wifConfig.value = '';
    updateGCPAuthModeFields(gcpMode);
  }

  openModal(modal);
}

/**
 * Populate AWS-specific fields in the account modal
 */
function generateExternalID(): string {
  // crypto.randomUUID is available in all supported browsers (Chrome
  // 92+, Firefox 95+, Safari 15.4+). Fall back to crypto.getRandomValues
  // (a strict superset of randomUUID's availability) for older runtimes.
  // The External ID is a security primitive that protects against the
  // confused-deputy problem on cross-account sts:AssumeRole, so the
  // fallback MUST also be a CSPRNG — never Math.random() (issue #127).
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  if (typeof crypto !== 'undefined' && typeof crypto.getRandomValues === 'function') {
    const bytes = new Uint8Array(16);
    crypto.getRandomValues(bytes);
    return Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('');
  }
  // Last-resort fallback: a runtime with no Web Crypto at all (jsdom
  // without polyfill, very old webviews). Not security-secret-quality,
  // but "never empty" matters for the form to submit and the backend
  // length/charset validator (issue #128) will then catch obviously-
  // weak inputs. Math.random is intentionally NOT used here — see #127.
  return `cudly-fallback-${Date.now().toString(36)}`;
}

/**
 * Return the AWS External ID for the current modal session.
 *
 * - When editing an existing account: always use the persisted value.
 * - When creating a new account: reuse the same draft value across modal
 *   close/reopen cycles so an operator who copied the ID into their IAM
 *   trust policy and then reopens the modal sees the same value (issue
 *   #126). The draft is keyed in sessionStorage (cleared on tab close)
 *   and is reset on a successful save.
 *
 * Falls back to in-memory generation if sessionStorage is unavailable
 * (e.g., privacy-mode browsers, tests with no Storage shim) — that path
 * preserves the legacy regenerate-on-reopen behaviour but never breaks.
 */
function resolveAwsExternalIdForModal(account?: api.CloudAccount): string {
  if (account?.aws_external_id) return account.aws_external_id;

  let storage: Storage | undefined;
  try {
    storage = typeof sessionStorage !== 'undefined' ? sessionStorage : undefined;
  } catch {
    storage = undefined;
  }

  if (storage) {
    try {
      const existing = storage.getItem(AWS_DRAFT_EXTERNAL_ID_KEY);
      if (existing) return existing;
      const fresh = generateExternalID();
      storage.setItem(AWS_DRAFT_EXTERNAL_ID_KEY, fresh);
      return fresh;
    } catch {
      // Quota / permission errors — fall through to in-memory generation.
    }
  }
  return generateExternalID();
}

/** Clear the in-progress AWS External ID draft. Called after successful save. */
function clearAwsExternalIdDraft(): void {
  try {
    if (typeof sessionStorage !== 'undefined') {
      sessionStorage.removeItem(AWS_DRAFT_EXTERNAL_ID_KEY);
    }
  } catch {
    // sessionStorage unavailable — nothing to clear.
  }
}

function populateAwsAccountFields(account?: api.CloudAccount): void {
  const authMode = (account?.aws_auth_mode ?? 'workload_identity_federation') as string;
  const authModeSelect = document.getElementById('account-aws-auth-mode') as HTMLSelectElement | null;
  if (authModeSelect) authModeSelect.value = authMode;

  setInputValue('account-aws-access-key-id', '');
  setInputValue('account-aws-secret-access-key', '');
  // aws_role_arn is the backend column used by all three auth modes that need
  // a role: role_arn (direct), bastion (target role assumed via bastion creds),
  // and workload_identity_federation (role assumed via OIDC). Only one of
  // these fields is visible at a time (see updateAwsAuthModeFields), and the
  // save path writes whichever one is visible back to req.aws_role_arn. We
  // pre-fill all three with the same stored value so switching modes mid-edit
  // doesn't blank the input that just became visible.
  setInputValue('account-aws-role-arn', account?.aws_role_arn ?? '');
  // External ID is a CUDly-managed shared secret for cross-account role
  // assumption (issue #18). On edit, keep whatever was stored before; on
  // create, generate a value once and persist it in sessionStorage so a
  // close/reopen cycle returns the same value (issue #126). The field
  // itself is marked readonly in index.html so users can't hand-craft
  // values that would defeat the purpose.
  // Bastion mode shares the same draft External ID value as role_arn mode
  // (issue #129) — both are AWS sts:ExternalId values used as a confused-
  // deputy guard. Sharing the draft key keeps a single source of truth so
  // toggling auth modes mid-edit doesn't surface diverging values, and the
  // generator semantics are identical (CSPRNG + sessionStorage persistence
  // until save). The two readonly inputs are wired to the same value.
  const externalID = resolveAwsExternalIdForModal(account);
  setInputValue('account-aws-external-id', externalID);
  setInputValue('account-aws-external-id-bastion', externalID);
  setInputValue('account-aws-bastion-role-arn', account?.aws_role_arn ?? '');
  setInputValue('account-aws-wif-role-arn', account?.aws_role_arn ?? '');
  setInputValue('account-aws-wif-token-file', account?.aws_web_identity_token_file ?? '');
  setInputChecked('account-aws-is-org-root', account?.aws_is_org_root ?? false);

  updateAwsAuthModeFields(authMode, account?.aws_bastion_id);

  // Render the trust-policy snippet asynchronously — we need the CUDly
  // host account ID from getConfig.source_identity (issue #19). Kept
  // void so the modal doesn't block waiting for the network. The bastion
  // variant (issue #129) is also rendered eagerly so it's ready when the
  // operator switches to that mode mid-edit.
  void renderAwsTrustPolicy();
  void renderAwsBastionTrustPolicy();
}

/**
 * Show/hide AWS auth mode sub-fields
 */
function updateAwsAuthModeFields(mode: string, bastionId?: string): void {
  const keysFields = document.getElementById('account-aws-keys-fields');
  const roleFields = document.getElementById('account-aws-role-fields');
  const bastionFields = document.getElementById('account-aws-bastion-fields');
  const wifFields = document.getElementById('account-aws-wif-fields');

  keysFields?.classList.toggle('hidden', mode !== 'access_keys');
  roleFields?.classList.toggle('hidden', mode !== 'role_arn');
  bastionFields?.classList.toggle('hidden', mode !== 'bastion');
  wifFields?.classList.toggle('hidden', mode !== 'workload_identity_federation');

  if (mode === 'bastion') {
    // Refresh the dropdown then re-render the trust-policy snippet so
    // a freshly-selected bastion is reflected in the Principal (#129).
    void populateBastionAccountDropdown(bastionId).then(() => renderAwsBastionTrustPolicy());
  }
}

/**
 * Show/hide Azure auth mode sub-fields. Only client_secret mode has an
 * input block; workload_identity_federation is credential-free (CUDly's
 * OIDC issuer + the App Registration's federated-identity-credential
 * handle authentication) and managed_identity is ambient, so neither
 * needs a visible field block here.
 */
function updateAzureAuthModeFields(mode: string): void {
  const secretFields = document.getElementById('account-azure-secret-fields');
  secretFields?.classList.toggle('hidden', mode !== 'client_secret');
}

/**
 * Show/hide GCP auth mode sub-fields
 */
function updateGCPAuthModeFields(mode: string): void {
  const keyFields = document.getElementById('account-gcp-key-fields');
  const wifFields = document.getElementById('account-gcp-wif-fields');
  keyFields?.classList.toggle('hidden', mode !== 'service_account_key');
  wifFields?.classList.toggle('hidden', mode !== 'workload_identity_federation');
}

/**
 * Populate bastion account dropdown with AWS accounts, optionally pre-selecting one
 */
async function populateBastionAccountDropdown(selectedId?: string): Promise<void> {
  const select = document.getElementById('account-aws-bastion-id') as HTMLSelectElement | null;
  if (!select) return;
  try {
    const accounts = await api.listAccounts({ provider: 'aws' });
    while (select.options.length > 1) select.remove(1);
    accounts.forEach(a => {
      const opt = document.createElement('option');
      opt.value = a.id;
      opt.textContent = `${a.name} (${a.external_id})`;
      select.appendChild(opt);
    });
    if (selectedId) select.value = selectedId;
  } catch {
    // Non-critical
  }
}

/**
 * Close the account modal
 */
function closeAccountModal(): void {
  const modal = document.getElementById('account-modal');
  if (modal) closeModal(modal);
  accountModalOnSave = undefined;
}

/**
 * Build the account request from the modal form
 */
function buildAccountRequest(provider: AccountProvider): api.CloudAccountRequest {
  const req: api.CloudAccountRequest = {
    name: (byId<HTMLInputElement>('account-name')?.value ?? '').trim(),
    description: (byId<HTMLTextAreaElement>('account-description')?.value ?? '').trim() || undefined,
    contact_email: (byId<HTMLInputElement>('account-contact-email')?.value ?? '').trim() || undefined,
    provider,
    external_id: (byId<HTMLInputElement>('account-external-id')?.value ?? '').trim(),
    enabled: byId<HTMLInputElement>('account-enabled')?.checked ?? true,
  };

  if (provider === 'aws') {
    const authMode = byId<HTMLSelectElement>('account-aws-auth-mode')?.value ?? '';
    req.aws_auth_mode = authMode;
    req.aws_is_org_root = byId<HTMLInputElement>('account-aws-is-org-root')?.checked ?? false;
    if (authMode === 'role_arn') {
      req.aws_role_arn = (byId<HTMLInputElement>('account-aws-role-arn')?.value ?? '').trim();
      req.aws_external_id = (byId<HTMLInputElement>('account-aws-external-id')?.value ?? '').trim() || undefined;
    } else if (authMode === 'bastion') {
      req.aws_bastion_id = byId<HTMLSelectElement>('account-aws-bastion-id')?.value ?? '';
      req.aws_role_arn = (byId<HTMLInputElement>('account-aws-bastion-role-arn')?.value ?? '').trim();
      // Collect the External ID for bastion mode too (issue #129) — the
      // bastion's STS client calls AssumeRole into the target role just
      // like role_arn mode, so the same sts:ExternalId guard applies.
      req.aws_external_id = (byId<HTMLInputElement>('account-aws-external-id-bastion')?.value ?? '').trim() || undefined;
    } else if (authMode === 'workload_identity_federation') {
      req.aws_role_arn = byId<HTMLInputElement>('account-aws-wif-role-arn')?.value.trim();
      req.aws_web_identity_token_file = byId<HTMLInputElement>('account-aws-wif-token-file')?.value.trim() || undefined;
    }
  } else if (provider === 'azure') {
    req.azure_subscription_id = req.external_id; // external_id IS the subscription ID for Azure
    req.azure_auth_mode = byId<HTMLSelectElement>('account-azure-auth-mode')?.value || 'client_secret';
    req.azure_tenant_id = (byId<HTMLInputElement>('account-azure-tenant-id')?.value ?? '').trim();
    req.azure_client_id = (byId<HTMLInputElement>('account-azure-client-id')?.value ?? '').trim();
  } else if (provider === 'gcp') {
    req.gcp_project_id = req.external_id; // external_id IS the project ID for GCP
    req.gcp_auth_mode = byId<HTMLSelectElement>('account-gcp-auth-mode')?.value || 'service_account_key';
    req.gcp_client_email = byId<HTMLInputElement>('account-gcp-client-email')?.value.trim() ?? '';
  }

  return req;
}

/**
 * Build and save credentials from the account modal form (if filled)
 */
async function saveAccountCredentialsIfFilled(accountId: string, provider: AccountProvider): Promise<void> {
  if (provider === 'aws') {
    const authMode = byId<HTMLSelectElement>('account-aws-auth-mode')?.value ?? '';
    if (authMode === 'access_keys') {
      const keyId = (byId<HTMLInputElement>('account-aws-access-key-id')?.value ?? '').trim();
      const secretKey = byId<HTMLInputElement>('account-aws-secret-access-key')?.value ?? '';
      if (keyId && secretKey) {
        await api.saveAccountCredentials(accountId, {
          credential_type: 'aws_access_keys',
          payload: { access_key_id: keyId, secret_access_key: secretKey }
        });
      }
    }
  } else if (provider === 'azure') {
    const azureMode = byId<HTMLSelectElement>('account-azure-auth-mode')?.value ?? 'client_secret';
    // managed_identity is ambient; workload_identity_federation is
    // secret-free (CUDly's OIDC issuer + the Azure App Registration's
    // federated-identity-credential handle authentication, no material
    // stored in CUDly). Only client_secret accepts a stored credential.
    if (azureMode === 'client_secret') {
      const secret = byId<HTMLInputElement>('account-azure-client-secret')?.value ?? '';
      if (secret) {
        await api.saveAccountCredentials(accountId, {
          credential_type: 'azure_client_secret',
          payload: { client_secret: secret }
        });
      }
    }
  } else if (provider === 'gcp') {
    const gcpMode = byId<HTMLSelectElement>('account-gcp-auth-mode')?.value ?? 'service_account_key';
    if (gcpMode === 'application_default') {
      // No credential to store
    } else if (gcpMode === 'workload_identity_federation') {
      const config = byId<HTMLTextAreaElement>('account-gcp-wif-config')?.value.trim();
      if (config) {
        let parsed: Record<string, unknown>;
        try {
          parsed = JSON.parse(config) as Record<string, unknown>;
        } catch {
          throw new Error('GCP WIF config is not valid JSON');
        }
        await api.saveAccountCredentials(accountId, { credential_type: 'gcp_workload_identity_config', payload: parsed });
      }
    } else {
      const jsonText = byId<HTMLTextAreaElement>('account-gcp-service-account-json')?.value.trim() ?? '';
      if (jsonText) {
        let parsed: Record<string, unknown>;
        try {
          parsed = JSON.parse(jsonText) as Record<string, unknown>;
        } catch {
          throw new Error('Service account JSON is not valid JSON');
        }
        await api.saveAccountCredentials(accountId, { credential_type: 'gcp_service_account', payload: parsed });
      }
    }
  }
}

/**
 * Handle account form submission
 */
async function handleAccountFormSubmit(e: Event): Promise<void> {
  e.preventDefault();
  const submitBtn = document.querySelector<HTMLButtonElement>('#account-form button[type="submit"]');
  if (submitBtn) submitBtn.disabled = true;
  try {
    await submitAccountForm();
  } finally {
    if (submitBtn) submitBtn.disabled = false;
  }
}

async function submitAccountForm(): Promise<void> {
  const provider = accountModalProvider;
  const req = buildAccountRequest(provider);

  // If a custom onSave callback was provided (e.g., registration approval),
  // delegate to it instead of the normal create/update flow.
  if (accountModalOnSave) {
    try {
      await accountModalOnSave(provider, req);
      closeAccountModal();
    } catch (err) {
      console.error('Custom save failed:', err);
      showToast({ message: `Failed to save: ${(err as Error).message}`, kind: 'error' });
    }
    return;
  }

  const accountId = byId<HTMLInputElement>('account-id')?.value ?? '';

  try {
    let savedId = accountId;
    if (accountId) {
      await api.updateAccount(accountId, req);
    } else {
      const created = await api.createAccount(req);
      savedId = created.id;
    }

    // Account persisted — drop the in-progress AWS External ID draft so
    // the next "Add AWS Account" flow generates a fresh value (issue #126).
    if (provider === 'aws') {
      clearAwsExternalIdDraft();
    }

    // Credential save is best-effort: if it fails we still close the modal
    // (the account was already persisted) and show a targeted warning so the
    // user knows to retry just the credential step.
    try {
      await saveAccountCredentialsIfFilled(savedId, provider);
    } catch (credErr) {
      console.error('Account saved but credentials could not be stored:', credErr);
      closeAccountModal();
      await loadAccountsForProvider(provider);
      showToast({
        message: `Account saved, but credentials could not be stored: ${(credErr as Error).message}. Edit the account to re-enter credentials.`,
        kind: 'warning',
        timeout: null,
      });
      return;
    }

    closeAccountModal();
    await loadAccountsForProvider(provider);
  } catch (err) {
    console.error('Failed to save account:', err);
    showToast({ message: `Failed to save account: ${(err as Error).message}`, kind: 'error' });
  }
}

/**
 * Trigger org account discovery from the management (org root) account
 */
async function handleDiscoverOrgAccounts(): Promise<void> {
  try {
    const result = await api.discoverOrgAccounts();
    showToast({ message: `Org discovery started: ${result.message || result.status}`, kind: 'success' });
    await loadAccountsForProvider('aws');
  } catch (err) {
    console.error('Org discovery failed:', err);
    showToast({ message: `Org discovery failed: ${(err as Error).message}`, kind: 'error' });
  }
}

/**
 * Set up settings event handlers
 */
export function setupSettingsHandlers(signal?: AbortSignal): void {
  // Provider checkbox handlers - toggle visibility of provider settings sections
  const awsCheck = document.getElementById('provider-aws') as HTMLInputElement | null;
  const azureCheck = document.getElementById('provider-azure') as HTMLInputElement | null;
  const gcpCheck = document.getElementById('provider-gcp') as HTMLInputElement | null;

  awsCheck?.addEventListener('change', () => updateProviderSettingsVisibility(), { signal });
  azureCheck?.addEventListener('change', () => updateProviderSettingsVisibility(), { signal });
  gcpCheck?.addEventListener('change', () => updateProviderSettingsVisibility(), { signal });

  // Auto-collect checkbox - toggle schedule visibility
  const autoCollect = document.getElementById('setting-auto-collect') as HTMLInputElement | null;
  autoCollect?.addEventListener('change', () => updateCollectionScheduleVisibility(), { signal });

  // Global defaults — propagate to all services when changed. The actual
  // propagation is gated behind a diff-preview confirmation (see
  // confirmAndPropagateTerm / confirmAndPropagatePayment) so the user
  // understands which per-service rows will be rewritten.
  const defaultTerm = document.getElementById('setting-default-term') as HTMLSelectElement | null;
  const defaultPayment = document.getElementById('setting-default-payment') as HTMLSelectElement | null;
  if (defaultTerm) {
    defaultTerm.dataset['previous'] = defaultTerm.value;
    defaultTerm.addEventListener('focus', () => { defaultTerm.dataset['previous'] = defaultTerm.value; }, { signal });
    defaultTerm.addEventListener('change', () => {
      void confirmAndPropagateTerm(defaultTerm);
    }, { signal });
  }
  if (defaultPayment) {
    defaultPayment.dataset['previous'] = defaultPayment.value;
    defaultPayment.addEventListener('focus', () => { defaultPayment.dataset['previous'] = defaultPayment.value; }, { signal });
    defaultPayment.addEventListener('change', () => {
      void confirmAndPropagatePayment(defaultPayment);
    }, { signal });
  }

  // Wire per-service term changes to the commitment-constraint sync so the
  // payment dropdown narrows to supported combinations as the user picks a
  // term. The initial sync runs after loadGlobalSettings populates the
  // form with persisted values.
  SERVICE_FIELDS.forEach(field => {
    if (field.provider !== 'aws' || !field.paymentId) return;
    const termEl = document.getElementById(field.termId) as HTMLSelectElement | null;
    termEl?.addEventListener('change', () => syncPaymentConstraintsForService(field), { signal });
  });

  // Inline range validation on numeric settings — surfaces a single
  // "Must be a whole number between X and Y" message under the field
  // as the user types, instead of waiting for Save Settings (#465).
  // All fields are integer-only at the backend, so pass requireInteger
  // so the inline check rejects fractional input that the save-time
  // guard would otherwise catch on a second trip.
  //
  // Every numeric input on the Settings page is wired through this
  // helper — the three top-level inputs covered by #471, plus the
  // per-provider grace inputs (originally on a separate readGraceInput
  // save-time-only toast contract) — so the inline-error UX is uniform
  // across Settings.
  wireInlineRangeValidation('setting-notification-days', { signal, requireInteger: true });
  wireInlineRangeValidation('setting-recs-stale-hours', { signal, requireInteger: true, required: true });
  wireInlineRangeValidation('setting-default-coverage', { signal, requireInteger: true, required: true });
  wireInlineRangeValidation('setting-grace-aws', { signal, requireInteger: true, required: true });
  wireInlineRangeValidation('setting-grace-azure', { signal, requireInteger: true, required: true });
  wireInlineRangeValidation('setting-grace-gcp', { signal, requireInteger: true, required: true });
  // Per-SP-plan coverage percentage inputs — must carry the same inline
  // validation as the global default-coverage field so users see range errors
  // on the service cards without waiting for Save. Issue #1411 row 7.13.
  SERVICE_FIELDS
    .filter(f => 'coverageId' in f)
    .forEach(f =>
      wireInlineRangeValidation(
        (f as { coverageId: string }).coverageId,
        { signal, requireInteger: true, required: true },
      )
    );

  // Set up dirty-field tracking
  setupDirtyTracking(signal);

  // Account management buttons
  document.getElementById('add-aws-account-btn')?.addEventListener('click', () => openAccountModal('aws'), { signal });
  document.getElementById('add-azure-account-btn')?.addEventListener('click', () => openAccountModal('azure'), { signal });
  document.getElementById('add-gcp-account-btn')?.addEventListener('click', () => openAccountModal('gcp'), { signal });

  // Org discovery button
  document.getElementById('discover-org-accounts-btn')?.addEventListener('click', () => void handleDiscoverOrgAccounts(), { signal });

  // Account form submit handler
  document.getElementById('account-form')?.addEventListener('submit', (e) => void handleAccountFormSubmit(e), { signal });

  // Account modal close
  document.getElementById('close-account-modal-btn')?.addEventListener('click', closeAccountModal, { signal });

  // AWS auth mode change handler
  const awsAuthMode = document.getElementById('account-aws-auth-mode') as HTMLSelectElement | null;
  awsAuthMode?.addEventListener('change', () => updateAwsAuthModeFields(awsAuthMode.value), { signal });

  // Copy the auto-generated AWS External ID to the clipboard (issue #18).
  document.getElementById('account-aws-external-id-copy')?.addEventListener(
    'click',
    () => copyToClipboard('account-aws-external-id'),
    { signal },
  );

  // Copy the rendered AWS IAM trust policy snippet (issue #19).
  document.getElementById('account-aws-trust-policy-copy')?.addEventListener(
    'click',
    () => copyToClipboard('account-aws-trust-policy'),
    { signal },
  );

  // Bastion-mode counterparts of the External ID copy button + trust-
  // policy copy button (issue #129).
  document.getElementById('account-aws-external-id-bastion-copy')?.addEventListener(
    'click',
    () => copyToClipboard('account-aws-external-id-bastion'),
    { signal },
  );
  document.getElementById('account-aws-trust-policy-bastion-copy')?.addEventListener(
    'click',
    () => copyToClipboard('account-aws-trust-policy-bastion'),
    { signal },
  );

  // Re-render the bastion trust-policy snippet whenever the bastion
  // account dropdown changes — the snippet's Principal is the selected
  // bastion's account root (issue #129).
  document.getElementById('account-aws-bastion-id')?.addEventListener(
    'change',
    () => void renderAwsBastionTrustPolicy(),
    { signal },
  );

  // Azure auth mode change handler
  const azureAuthMode = document.getElementById('account-azure-auth-mode') as HTMLSelectElement | null;
  azureAuthMode?.addEventListener('change', () => updateAzureAuthModeFields(azureAuthMode.value), { signal });

  // GCP auth mode change handler
  const gcpAuthMode = document.getElementById('account-gcp-auth-mode') as HTMLSelectElement | null;
  gcpAuthMode?.addEventListener('change', () => updateGCPAuthModeFields(gcpAuthMode.value), { signal });

  // Close account modal when clicking outside
  const accountModal = document.getElementById('account-modal');
  accountModal?.addEventListener('click', (e) => {
    if (e.target === accountModal) closeAccountModal();
  }, { signal });

  // Override modal — submit, cancel, click-outside-to-dismiss (issue #104).
  document.getElementById('override-form')?.addEventListener(
    'submit',
    (e) => void submitOverrideForm(e),
    { signal },
  );
  document.getElementById('close-override-modal-btn')?.addEventListener(
    'click',
    closeOverrideModal,
    { signal },
  );
  const overrideModal = document.getElementById('override-modal');
  overrideModal?.addEventListener('click', (e) => {
    if (e.target === overrideModal) closeOverrideModal();
  }, { signal });

  // Per-account overrides modal — close + click-outside-to-dismiss (issue #122).
  document.getElementById('close-account-overrides-modal-btn')?.addEventListener(
    'click',
    closeAccountOverridesModal,
    { signal },
  );
  const accountOverridesModal = document.getElementById('account-overrides-modal');
  accountOverridesModal?.addEventListener('click', (e) => {
    if (e.target === accountOverridesModal) closeAccountOverridesModal();
  }, { signal });
}

/**
 * Update visibility of provider settings sections based on checkboxes
 */
function updateProviderSettingsVisibility(): void {
  const providers: Array<{ key: AccountProvider; checkId: string; settingsId: string; blockId: string }> = [
    { key: 'aws',   checkId: 'provider-aws',   settingsId: 'aws-settings',   blockId: 'accounts-aws-block'   },
    { key: 'azure', checkId: 'provider-azure', settingsId: 'azure-settings', blockId: 'accounts-azure-block' },
    { key: 'gcp',   checkId: 'provider-gcp',   settingsId: 'gcp-settings',   blockId: 'accounts-gcp-block'   },
  ];
  for (const p of providers) {
    const checked = (document.getElementById(p.checkId) as HTMLInputElement | null)?.checked ?? false;
    // Per-provider settings section (credentials + service defaults): hide when disabled
    document.getElementById(p.settingsId)?.classList.toggle('hidden', !checked);
    // Per-provider accounts block in Accounts section: dim (not hide) when disabled
    const block = document.getElementById(p.blockId);
    if (block) {
      const wasDisabled = block.classList.contains('provider-disabled');
      block.classList.toggle('provider-disabled', !checked);
      // Re-enabling: refresh the account list from the backend
      if (wasDisabled && checked) {
        void loadAccountsForProvider(p.key);
      }
    }
  }
}

/**
 * Update visibility of collection schedule row based on auto-collect checkbox
 */
function updateCollectionScheduleVisibility(): void {
  const autoCollect = document.getElementById('setting-auto-collect') as HTMLInputElement | null;
  const scheduleRow = document.getElementById('collection-schedule-row');

  if (scheduleRow) {
    scheduleRow.classList.toggle('hidden', !autoCollect?.checked);
  }
}

/**
 * Load global settings
 */
export async function loadGlobalSettings(): Promise<void> {
  const loadingEl = document.getElementById('settings-loading');
  const formEl = document.getElementById('global-settings-form');
  const errorEl = document.getElementById('settings-error');

  if (loadingEl) {
    loadingEl.classList.remove('hidden');
    // Issue #186: skeleton instead of the static "Loading settings..." text.
    showSkeletonText(loadingEl, 6);
  }
  if (formEl) formEl.classList.add('hidden');
  if (errorEl) errorEl.classList.add('hidden');

  // Overlay dynamically-probed AWS commitment rules before we render the
  // form, so the first paint already respects server data. Failures fall
  // back to hardcoded rules silently — we never block Settings on this.
  await fetchAndPopulateCommitmentOptions();

  try {
    const data = await api.getConfig();

    if (data.global) {
      const providers = data.global.enabled_providers || [];
      const awsCheck = document.getElementById('provider-aws') as HTMLInputElement | null;
      const azureCheck = document.getElementById('provider-azure') as HTMLInputElement | null;
      const gcpCheck = document.getElementById('provider-gcp') as HTMLInputElement | null;

      if (awsCheck) awsCheck.checked = providers.includes('aws');
      if (azureCheck) azureCheck.checked = providers.includes('azure');
      if (gcpCheck) gcpCheck.checked = providers.includes('gcp');

      const emailInput = document.getElementById('setting-notification-email') as HTMLInputElement | null;
      if (emailInput) emailInput.value = data.global.notification_email || '';

      const autoCollect = document.getElementById('setting-auto-collect') as HTMLInputElement | null;
      if (autoCollect) autoCollect.checked = data.global.auto_collect !== false;

      const collectionSchedule = document.getElementById('setting-collection-schedule') as HTMLSelectElement | null;
      if (collectionSchedule) collectionSchedule.value = data.global.collection_schedule || 'daily';

      const termSelect = document.getElementById('setting-default-term') as HTMLSelectElement | null;
      if (termSelect) termSelect.value = String(data.global.default_term || 3);

      const paymentSelect = document.getElementById('setting-default-payment') as HTMLSelectElement | null;
      if (paymentSelect) paymentSelect.value = data.global.default_payment || 'no-upfront';

      const coverageInput = document.getElementById('setting-default-coverage') as HTMLInputElement | null;
      if (coverageInput) coverageInput.value = String(data.global.default_coverage ?? 80);

      // Cache defaults for the override modal "Inherit (currently: X)" labels
      // (issue #112). Must be updated here so reopening the override modal
      // after a Settings save reflects any in-session changes.
      setCachedGlobalDefaults({
        term: data.global.default_term || 3,
        payment: data.global.default_payment || 'no-upfront',
        coverage: data.global.default_coverage ?? 80,
      });

      const notifyDaysInput = document.getElementById('setting-notification-days') as HTMLInputElement | null;
      if (notifyDaysInput) notifyDaysInput.value = String(data.global.notification_days_before || 3);

      // Grace-period inputs (per provider). An absent key renders the
      // default (7) so the UI always shows a concrete value; an
      // explicit 0 is preserved so users can see which providers
      // have the feature disabled.
      const gpMap = data.global.grace_period_days || {};
      populateGraceInput('setting-grace-aws', gpMap['aws']);
      populateGraceInput('setting-grace-azure', gpMap['azure']);
      populateGraceInput('setting-grace-gcp', gpMap['gcp']);

      // EC2 RI offering class. Absent/null from the API means "convertible"
      // (backend default). Only "convertible" and "standard" are valid; any
      // other value from a future API version falls back to "convertible" so
      // the dropdown always has a valid selection.
      const offeringClassSelect = byId<HTMLSelectElement>('setting-ec2-offering-class');
      if (offeringClassSelect) {
        const oc = data.global.offering_class;
        offeringClassSelect.value = (oc === 'standard') ? 'standard' : 'convertible';
      }

      // Recommendations cycle params
      const staleHoursInput = byId<HTMLInputElement>('setting-recs-stale-hours');
      if (staleHoursInput) {
        staleHoursInput.value = String(data.global.recommendations_cache_stale_hours ?? 24);
      }
      const lookbackSelect = byId<HTMLSelectElement>('setting-recs-lookback-days');
      if (lookbackSelect) {
        lookbackSelect.value = String(data.global.recommendations_lookback_days ?? 7);
      }

      // 4-eyes approval mode (issue #1005)
      const requireDiffApproverEl = byId<HTMLInputElement>('setting-require-different-approver');
      if (requireDiffApproverEl) {
        requireDiffApproverEl.checked = data.global.require_different_approver === true;
      }

      // Update visibility based on loaded settings
      updateProviderSettingsVisibility();
      updateCollectionScheduleVisibility();

      // Render the Commitment Laddering section (flag-gated default-off,
      // issue #1333 phase 3). Runs asynchronously; failures are surfaced
      // inside the section without blocking the rest of Settings.
      void initLadderingSettings(data.global.laddering_enabled ?? false);
    }

    // Issue #613: rows saved under the old Azure/GCP ids (azure/vm, gcp/sql,
    // ...) are shown on their card and re-saved under the current id, which
    // is the only one the scheduler and purchase flow match. The card DOM ids
    // still use the old names, so look them up through SERVICE_FIELDS rather
    // than deriving them from the row's service id.
    const stored = data.services ?? [];
    const services = canonicalizeServiceConfigs(stored);
    loadedServiceConfigs = services;
    // Re-keyed rows have no backend row under the current id yet, so the next
    // Save writes them in full instead of as a partial change (#539).
    const storedKeys = new Set(stored.map(r => `${r.provider}/${r.service}`));
    legacyRekeyedServices = new Set(services.filter(r => !storedKeys.has(`${r.provider}/${r.service}`)).map(r => `${r.provider}/${r.service}`));
    for (const svc of services) {
      const field = SERVICE_FIELDS.find(f => f.provider === svc.provider && f.service === svc.service);
      if (!field) continue;
      const termEl = document.getElementById(field.termId) as HTMLSelectElement | null;
      if (termEl) termEl.value = String(svc.term);
      const paymentEl = field.paymentId ? document.getElementById(field.paymentId) as HTMLSelectElement | null : null;
      if (paymentEl) paymentEl.value = normalizePaymentValue(svc.payment, svc.provider);
    }

    // Issue #136: populate per-product SP coverage and enabled fields for every
    // SP card the DOM exposes, not just those with a service row. A card whose
    // service row is absent from the response must still fall back to
    // global.default_coverage (and enabled=true); leaving it at the HTML default
    // (80) would persist an incorrect value on the user's next save. Iterate
    // SERVICE_FIELDS (the source of truth for card IDs) and overlay any matching
    // service-config values keyed by `${provider}-${service}`.
    const svcByKey = new Map(services.map(s => [`${s.provider}-${s.service}`, s] as const));
    for (const field of SERVICE_FIELDS) {
      const svc = svcByKey.get(`${field.provider}-${field.service}`);
      if ('coverageId' in field && field.coverageId) {
        const el = byId<HTMLInputElement>(field.coverageId);
        if (el) el.value = String(svc?.coverage ?? data.global?.default_coverage ?? 80);
      }
      if ('enabledId' in field && field.enabledId) {
        const el = byId<HTMLInputElement>(field.enabledId);
        if (el) el.checked = svc?.enabled !== false;
      }
      // Inject (idempotent) and populate the per-service recommendation-filter
      // controls (include/exclude engines/types/regions + min-count). The
      // backend already persists and applies these; this is the GUI surface.
      injectServiceFilterControls(field);
      populateServiceFilterControls(field, svc);
    }

    // After loading persisted values, apply per-service commitment
    // restrictions (e.g., hide "No Upfront" on RDS 3yr). Runs even when
    // `data.services` is absent so fresh installs start with the invalid
    // options already suppressed. Any legacy-persisted invalid combo
    // (RDS 3yr + no-upfront, possible on databases loaded from before
    // this fix) gets clamped here to the first valid payment option,
    // which will then be re-persisted on the user's next save.
    syncAllServiceCommitmentConstraints();
    revalidateInlineRanges();

    if (loadingEl) loadingEl.classList.add('hidden');
    if (formEl) formEl.classList.remove('hidden');

    // Issue #365: settings is reachable for non-admins (General +
    // Purchasing policies sub-tabs aren't admin-gated). Render the
    // form read-only for those sessions: disable every input/select/
    // checkbox via a wrapping fieldset, hide the Save / Reset buttons
    // entirely, and replace the dirty-tracking baseline with the
    // post-load values so a non-admin who somehow flips a control
    // (shouldn't be possible with fieldset[disabled], but defense in
    // depth) still sees no Save prompt. Backend still 403s writes.
    applyReadOnlySettings(formEl as HTMLElement | null);

    // Establish the clean baseline for dirty tracking
    snapshotAllFields();
    updateDirtyMarkers();

    cachedSourceCloud = data.source_cloud ?? 'aws';
  } catch (error) {
    // Issue #979: when the backend returns 403 with a "permission denied"
    // message the non-admin user already has restricted visibility
    // (Exchange Automation etc. are hidden). Silently hide the section
    // so the page degrades gracefully instead of showing a red banner.
    // All other failures (network errors, 5xx, or unrelated 403s) still
    // surface the banner.
    if (isPermissionDeniedError(error)) {
      if (loadingEl) loadingEl.classList.add('hidden');
      return;
    }
    console.error('Failed to load settings:', error);
    if (loadingEl) loadingEl.classList.add('hidden');
    if (errorEl) {
      const err = error as Error;
      errorEl.textContent = `Failed to load settings: ${err.message}`;
      errorEl.classList.remove('hidden');
    }
  }
}

/**
 * Load the Accounts sub-tab: account lists, federation IaC panel, and
 * pending registrations. Self-contained — fetches sourceCloud from
 * cache or the config API if not yet available.
 */
export async function loadAccountsTab(): Promise<void> {
  let source = cachedSourceCloud;
  if (!source) {
    try {
      const cfg = await api.getConfig();
      source = cfg.source_cloud ?? 'aws';
      cachedSourceCloud = source;
    } catch {
      source = 'aws';
    }
  }
  void loadAccountsForProvider('aws');
  void loadAccountsForProvider('azure');
  void loadAccountsForProvider('gcp');
  void initFederationPanel(source);
  void import('./modules/registrations').then(m => m.initRegistrations());
  void import('./modules/override-transfer').then(m => m.initOverrideTransfer());
  void renderAccountsOverview();
}

/**
 * Render the Accounts overview chip row (known_issues #29 partial).
 * Pulls counts from /api/accounts and /api/registrations across all
 * providers, presents `[Pending N | Active N | Disabled N | Rejected N]`
 * chips. Each chip click scrolls to the relevant section so users get
 * a single at-a-glance view without rebuilding into a unified table.
 *
 * Failures are rendered as an empty state — the per-section tables are
 * the authoritative views and will surface their own errors.
 */
async function renderAccountsOverview(): Promise<void> {
  const container = document.getElementById('accounts-overview');
  if (!container) return;
  container.replaceChildren();

  type Bucket = { label: string; count: number; targetId: string };
  const buckets: Bucket[] = [
    { label: 'Pending', count: 0, targetId: 'accounts-registrations' },
    { label: 'Active', count: 0, targetId: 'accounts-aws-block' },
    { label: 'Disabled', count: 0, targetId: 'accounts-aws-block' },
    { label: 'Rejected', count: 0, targetId: 'accounts-registrations' },
  ];

  try {
    const [accounts, registrations] = await Promise.all([
      api.listAccounts(),
      // Registrations endpoint may 404 / 403 in some deployments; tolerate.
      api.listRegistrations().catch(() => [] as api.AccountRegistration[]),
    ]);
    accounts.forEach(a => {
      if (a.enabled) buckets[1]!.count += 1;
      else buckets[2]!.count += 1;
    });
    registrations.forEach(r => {
      if (r.status === 'pending') buckets[0]!.count += 1;
      else if (r.status === 'rejected') buckets[3]!.count += 1;
    });
  } catch {
    // Leave overview empty — per-section tables surface their own errors.
    return;
  }

  buckets.forEach(({ label, count, targetId }) => {
    const chip = document.createElement('button');
    chip.type = 'button';
    chip.className = 'accounts-overview-chip';
    chip.dataset['count'] = String(count);
    chip.setAttribute('aria-label', `${count} ${label.toLowerCase()} — jump to section`);
    const labelEl = document.createElement('span');
    labelEl.className = 'accounts-overview-chip-label';
    labelEl.textContent = label;
    const countEl = document.createElement('span');
    countEl.className = 'accounts-overview-chip-count';
    countEl.textContent = String(count);
    chip.appendChild(countEl);
    chip.appendChild(labelEl);
    chip.addEventListener('click', () => {
      const target = document.getElementById(targetId);
      if (target) target.scrollIntoView({ behavior: 'smooth', block: 'start' });
    });
    container.appendChild(chip);
  });
}


/**
 * Save global settings
 */
export async function saveGlobalSettings(e: Event): Promise<void> {
  e.preventDefault();

  // In-flight guard: silently drop concurrent submissions so rapid Save
  // clicks or an Enter-triggered resubmit don't produce duplicate PUTs
  // with non-deterministic last-write-wins ordering.
  if (saveInFlight) return;

  const saveBtn = byId<HTMLButtonElement>('save-settings-btn');
  saveInFlight = true;
  if (saveBtn) saveBtn.disabled = true;

  const enabledProviders: api.Provider[] = [];
  if (byId<HTMLInputElement>('provider-aws')?.checked) enabledProviders.push('aws');
  if (byId<HTMLInputElement>('provider-azure')?.checked) enabledProviders.push('azure');
  if (byId<HTMLInputElement>('provider-gcp')?.checked) enabledProviders.push('gcp');

  // Collect per-provider grace-period values before building the
  // settings object so we can reject out-of-range input early with a
  // targeted error instead of letting the API surface "invalid range"
  // without saying which input.
  //
  // Issue #478: aggregate every out-of-range provider into a single
  // toast instead of bailing on the first failure. Previously, a user
  // with bad values in AWS + Azure + GCP saw three separate Save
  // attempts before all errors surfaced — one per click. Now: one
  // toast naming every affected provider, in input order (AWS, Azure,
  // GCP). Both error kinds readGraceInput can emit ("enter a whole
  // number of days", "must be between 0 and 30 days") collapse to
  // the same actionable instruction, so the aggregated message reads
  // cleanly as a single rule even when providers fail for different
  // reasons.
  const gracePeriodDays: Record<string, number> = {};
  const graceErrors: string[] = [];
  for (const [provider, id] of GRACE_INPUTS) {
    const v = readGraceInput(id);
    if (v.err) {
      graceErrors.push(provider.toUpperCase());
      continue;
    }
    gracePeriodDays[provider] = v.value;
  }
  if (graceErrors.length > 0) {
    showToast({
      message: `Grace period must be a whole number between 0 and 30 days (${graceErrors.join(', ')}).`,
      kind: 'error',
    });
    if (saveBtn) saveBtn.disabled = false;
    saveInFlight = false;
    return;
  }

  // Validate the three numeric inputs before building the save payload.
  // Use Number() (not parseInt) so fractional input like "1.5" fails
  // Number.isInteger() instead of silently truncating to 1. Each range
  // is the same one declared on the <input>'s min/max attributes —
  // wireInlineRangeValidation surfaces these inline as the user types
  // (#465); this is the defensive guard for clients that bypass it.
  const rawNotifyDays = Number(byId<HTMLInputElement>('setting-notification-days')?.value ?? '3');
  if (!Number.isFinite(rawNotifyDays) || !Number.isInteger(rawNotifyDays) || rawNotifyDays < 1 || rawNotifyDays > 30) {
    showToast({ message: 'Notification days before purchase must be a whole number between 1 and 30', kind: 'error' });
    if (saveBtn) saveBtn.disabled = false;
    saveInFlight = false;
    return;
  }
  const coverageText = byId<HTMLInputElement>('setting-default-coverage')?.value ?? '80';
  const rawCoverage = Number(coverageText);
  // Number('') is 0, so an empty field must be rejected explicitly.
  if (coverageText.trim() === '' || !Number.isFinite(rawCoverage) || !Number.isInteger(rawCoverage) || rawCoverage < 0 || rawCoverage > 100) {
    showToast({ message: 'Dashboard coverage target must be a whole number between 0 and 100', kind: 'error' });
    if (saveBtn) saveBtn.disabled = false;
    saveInFlight = false;
    return;
  }
  const staleHoursText = byId<HTMLInputElement>('setting-recs-stale-hours')?.value ?? '24';
  const rawStaleHours = Number(staleHoursText);
  // 0 is a legal value (disables auto-refresh) but Number('') is also 0, so an
  // emptied field must be rejected explicitly rather than saved as "disable".
  if (staleHoursText.trim() === '' || !Number.isFinite(rawStaleHours) || !Number.isInteger(rawStaleHours) || rawStaleHours < 0 || rawStaleHours > 8760) {
    showToast({ message: 'Cache stale threshold must be a whole number between 0 and 8760 hours (0 = disable)', kind: 'error' });
    if (saveBtn) saveBtn.disabled = false;
    saveInFlight = false;
    return;
  }
  // The per-Savings-Plan coverage cards sit outside the settings <form>, so
  // native min/max validation never runs for them and the inline hint only
  // paints a message. Apply the global field's whole-number 0..100 rule here,
  // before any PUT, so a fractional value like 12.7 is not persisted.
  const badCoverageCards = invalidEditedCardCoverages();
  if (badCoverageCards.length > 0) {
    showToast({ message: `Dashboard coverage must be a whole number between 0 and 100 (${badCoverageCards.join(', ')})`, kind: 'error' });
    if (saveBtn) saveBtn.disabled = false;
    saveInFlight = false;
    return;
  }

  const rawOfferingClass = byId<HTMLSelectElement>('setting-ec2-offering-class')?.value ?? 'convertible';
  const offeringClass: 'convertible' | 'standard' = (rawOfferingClass === 'standard') ? 'standard' : 'convertible';

  const sentFields: Record<string, string> = {};
  TRACKED_FIELDS.forEach(id => { sentFields[id] = getFieldValue(id); });

  const settings: api.Config = {
    enabled_providers: enabledProviders,
    notification_email: byId<HTMLInputElement>('setting-notification-email')?.value || '',
    auto_collect: byId<HTMLInputElement>('setting-auto-collect')?.checked ?? true,
    collection_schedule: byId<HTMLSelectElement>('setting-collection-schedule')?.value || 'daily',
    default_term: parseInt(byId<HTMLSelectElement>('setting-default-term')?.value || '3', 10),
    default_payment: (byId<HTMLSelectElement>('setting-default-payment')?.value || 'no-upfront') as api.PaymentOption,
    default_coverage: rawCoverage,
    notification_days_before: rawNotifyDays,
    grace_period_days: gracePeriodDays,
    recommendations_cache_stale_hours: rawStaleHours,
    recommendations_lookback_days: parseInt(byId<HTMLSelectElement>('setting-recs-lookback-days')?.value || '7', 10),
    offering_class: offeringClass,
    require_different_approver: byId<HTMLInputElement>('setting-require-different-approver')?.checked ?? false,
  };

  try {
    // The backend copies a changed default onto every service row, so a changed
    // default must win over the stale per-service coverage (#522). Decide it from
    // the dirty snapshot before the save refreshes it.
    const defaultCoverageChanged = 'setting-default-coverage' in savedSnapshot
      && getFieldValue('setting-default-coverage') !== savedSnapshot['setting-default-coverage'];
    // Send only the global keys the user changed, so a tab opened before another
    // admin's save cannot write its stale defaults back (#539). The form and the
    // Save button are unreachable after a failed first load, so the snapshot is
    // always populated here. laddering_enabled is not part of Save: its toggle
    // saves itself (ladder.ts). Multi-field keys (grace map, enabled_providers)
    // are replaced whole by the backend, so rebaseMultiFieldKeys merges them first.
    const changedGlobal: Partial<api.Config> = { ...settings };
    const isFieldChanged = (id: string): boolean => sentFields[id] !== savedSnapshot[id];
    for (const [key, ids] of GLOBAL_CONFIG_FIELDS) {
      if (!ids.some(isFieldChanged)) delete changedGlobal[key];
    }
    const globalSent = Object.keys(changedGlobal).length > 0;
    if (globalSent) {
      if ('grace_period_days' in changedGlobal || 'enabled_providers' in changedGlobal) {
        await rebaseMultiFieldKeys(changedGlobal, isFieldChanged);
        // The backend disagrees with itself on an empty list (filters read it as
        // "all", the scheduler collects nothing), so never write one (#1531).
        if (changedGlobal.enabled_providers?.length === 0) {
          showToast({ message: 'Enable at least one provider before saving.', kind: 'error' });
          return;
        }
      }
      await api.updateConfig(changedGlobal);
      // Persisted: advance the snapshot now, so a later validation failure or
      // service-save error does not leave these fields looking unsaved.
      for (const [key, ids] of GLOBAL_CONFIG_FIELDS) {
        if (key in changedGlobal) ids.forEach(id => { savedSnapshot[id] = sentFields[id]!; });
      }
      if (changedGlobal.grace_period_days) showSavedGrace(changedGlobal.grace_period_days, sentFields);
    }
    if (defaultCoverageChanged) {
      // The default is persisted and propagated; mirror that locally so a retry
      // after a partial failure does not send the old coverage back.
      savedSnapshot['setting-default-coverage'] = sentFields['setting-default-coverage']!;
      loadedServiceConfigs = loadedServiceConfigs.map(s => ({ ...s, coverage: settings.default_coverage }));
      showPropagatedCoverage(settings.default_coverage, sentFields);
    }

    // Read + validate the per-service recommendation filters up front so a bad
    // min-count aborts the whole save with a targeted toast (rather than firing
    // some service saves and failing midway). Each entry holds the parsed
    // filter values for the matching card.
    const filterValuesByService = new Map<string, ReturnType<typeof readServiceFilterControls>>();
    for (const field of SERVICE_FIELDS) {
      const result = readServiceFilterControls(field);
      if (isServiceFilterError(result)) {
        showToast({ message: result.message, kind: 'error' });
        if (saveBtn) saveBtn.disabled = false;
        saveInFlight = false;
        return;
      }
      filterValuesByService.set(`${field.provider}-${field.service}`, result);
    }

    const serviceSaves = SERVICE_FIELDS.map((field) => {
      const { provider, service } = field;
      const base = loadedServiceConfigs.find(s => s.provider === provider && s.service === service);
      const filters = filterValuesByService.get(`${provider}-${service}`);
      // Only the rows (and the fields of a row) the user changed go out, so a
      // stale tab cannot revert a service another admin edited (#539).
      const changes = changedServiceFields(field, base, filters && !isServiceFilterError(filters) ? filters : undefined);
      const rekeyed = base !== undefined && legacyRekeyedServices.has(`${provider}/${service}`);
      if (!changes && !rekeyed) return null;
      const fresh = newServiceConfigDefaults(field, settings);
      let cfg: Partial<api.ServiceConfig>;
      if (!base) cfg = { ...fresh, ...changes };
      else if (rekeyed) cfg = { ...base, term: fresh.term, payment: fresh.payment, ...changes };
      else cfg = { provider, service, ...changes };
      return api.updateServiceConfig(provider, service, cfg).then(() => {
        legacyRekeyedServices.delete(`${provider}/${service}`);
        // The PUT does not return the row, so the stamp is the save time here;
        // the next load shows the server's value.
        const updatedAt = new Date().toISOString();
        loadedServiceConfigs = [
          ...loadedServiceConfigs.filter(s => !(s.provider === provider && s.service === service)),
          { ...(base ?? {}), ...cfg, updated_at: updatedAt } as api.ServiceConfig,
        ];
        renderUpdatedStamp(field, updatedAt);
      });
    });
    // Only rows with a PUT take part in the outcome; an unchanged row is neither
    // a success nor a failure.
    const savedFields = SERVICE_FIELDS.filter((_, i) => serviceSaves[i] !== null);
    const results = await Promise.allSettled(serviceSaves.filter((p): p is Promise<void> => p !== null));

    // The global PUT and every fulfilled service PUT are persisted, so the
    // snapshot moves forward for them. A rejected service keeps its previous
    // snapshot so its fields stay marked dirty and the user can retry.
    const previousSnapshot = { ...savedSnapshot };
    TRACKED_FIELDS.forEach(id => {
      if (getFieldValue(id) === sentFields[id]) savedSnapshot[id] = sentFields[id]!;
    });
    const failures: string[] = [];
    results.forEach((result, i) => {
      if (result.status === 'fulfilled') return;
      const field = savedFields[i]!;
      failures.push(`${field.provider}/${field.service}: ${(result.reason as Error).message}`);
      const ids = [field.termId, field.paymentId, 'coverageId' in field ? field.coverageId : null, 'enabledId' in field ? field.enabledId : null];
      for (const id of ids) {
        if (id && id in previousSnapshot) savedSnapshot[id] = previousSnapshot[id]!;
      }
    });
    updateDirtyMarkers();

    if (failures.length > 0) {
      console.error('Failed to save service configs:', failures);
      showToast({ message: serviceSaveFailureMessage(results, failures, globalSent), kind: 'error' });
    } else {
      showToast({ message: 'Settings saved successfully', kind: 'success', timeout: 5_000 });
    }
  } catch (error) {
    console.error('Failed to save settings:', error);
    const err = error as Error;
    showToast({ message: `Failed to save settings: ${err.message}`, kind: 'error' });
  } finally {
    saveInFlight = false;
    if (saveBtn) saveBtn.disabled = false;
  }
}

/**
 * Reset settings to defaults
 */
export async function resetSettings(): Promise<void> {
  const ok = await confirmDialog({
    title: 'Reset all settings?',
    body: 'This clears your provider selection, collection schedule, and notification email back to factory defaults. Service-level defaults stay as-is.',
    confirmLabel: 'Reset settings',
    destructive: true,
  });
  if (!ok) return;

  const awsCheck = document.getElementById('provider-aws') as HTMLInputElement | null;
  const azureCheck = document.getElementById('provider-azure') as HTMLInputElement | null;
  const gcpCheck = document.getElementById('provider-gcp') as HTMLInputElement | null;
  if (awsCheck) awsCheck.checked = true;
  if (azureCheck) azureCheck.checked = false;
  if (gcpCheck) gcpCheck.checked = false;

  const emailInput = document.getElementById('setting-notification-email') as HTMLInputElement | null;
  if (emailInput) emailInput.value = '';

  const autoCollect = document.getElementById('setting-auto-collect') as HTMLInputElement | null;
  if (autoCollect) autoCollect.checked = true;

  const termSelect = document.getElementById('setting-default-term') as HTMLSelectElement | null;
  if (termSelect) termSelect.value = '3';

  const paymentSelect = document.getElementById('setting-default-payment') as HTMLSelectElement | null;
  if (paymentSelect) paymentSelect.value = 'no-upfront';

  const coverageInput = document.getElementById('setting-default-coverage') as HTMLInputElement | null;
  if (coverageInput) coverageInput.value = '80';

  const notifyDaysInput = document.getElementById('setting-notification-days') as HTMLInputElement | null;
  if (notifyDaysInput) notifyDaysInput.value = '3';

  populateGraceInput('setting-grace-aws', 7);
  populateGraceInput('setting-grace-azure', 7);
  populateGraceInput('setting-grace-gcp', 7);

  const offeringClassSelect = byId<HTMLSelectElement>('setting-ec2-offering-class');
  if (offeringClassSelect) offeringClassSelect.value = 'convertible';

  const staleHoursInput = byId<HTMLInputElement>('setting-recs-stale-hours');
  if (staleHoursInput) staleHoursInput.value = '24';
  const lookbackSelect = byId<HTMLSelectElement>('setting-recs-lookback-days');
  if (lookbackSelect) lookbackSelect.value = '7';

  // Reset 4-eyes mode to off (default).
  const requireDiffApproverResetEl = byId<HTMLInputElement>('setting-require-different-approver');
  if (requireDiffApproverResetEl) requireDiffApproverResetEl.checked = false;

  // Issue #466: setting el.value / .checked via JS does not fire
  // 'change'/'input', so none of the change-event-driven visibility
  // togglers nor the dirty-tracking listeners run on their own here.
  // Drive all of them manually so the user sees (a) the provider
  // settings sections re-render to match the reset checkbox states,
  // (b) the Collection Schedule controls now that Auto-collect is
  // back ON, and (c) the same "Unsaved changes" affordance any
  // interactive field-edit would produce. Otherwise a user who
  // clicks Reset and navigates away loses their reset silently.
  updateProviderSettingsVisibility();
  updateCollectionScheduleVisibility();
  revalidateInlineRanges();
  updateDirtyMarkers();
}
