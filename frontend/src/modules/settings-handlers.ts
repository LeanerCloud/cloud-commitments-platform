import { closeAccountModal, handleAccountFormSubmit, handleDiscoverOrgAccounts, loadAccountsForProvider, openAccountModal, updateAwsAuthModeFields, updateAzureAuthModeFields, updateGCPAuthModeFields } from './settings-accounts';
import { renderAwsBastionTrustPolicy } from './settings-aws-trust';
import { setupDirtyTracking } from './settings-dirty';
import { copyToClipboard } from './settings-fields';
import { closeAccountOverridesModal, closeOverrideModal, submitOverrideForm } from './settings-overrides';
import { confirmAndPropagatePayment, confirmAndPropagateTerm, syncPaymentConstraintsForService } from './settings-propagation';
import { wireInlineRangeValidation } from './settings-range-validation';
import { SERVICE_FIELDS } from './settings-state';
import type { AccountProvider } from './settings-state';

/**
 * Update visibility of provider settings sections based on checkboxes
 */
export function updateProviderSettingsVisibility(): void {
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
export function updateCollectionScheduleVisibility(): void {
  const autoCollect = document.getElementById('setting-auto-collect') as HTMLInputElement | null;
  const scheduleRow = document.getElementById('collection-schedule-row');

  if (scheduleRow) {
    scheduleRow.classList.toggle('hidden', !autoCollect?.checked);
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
    if (!field.paymentId) return;
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
