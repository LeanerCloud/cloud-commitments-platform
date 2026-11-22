import * as api from '../../api';
import { getValidPaymentOptions, getCommitmentConfig } from '../../commitmentOptions';
import { openModal, closeModal } from '../../modal';
import { inheritCurrentlyLabel, paymentLabel, setInputValue, termLabel } from '../settings-fields';
import { overridesState } from './state';
import { getOverrideServicesForProvider } from './services';
import { applyOverrideBulkMode, syncBulkServiceConflicts } from './bulk';

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
