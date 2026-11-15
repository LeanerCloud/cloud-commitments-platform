/**
 * Settings module for CUDly
 */

import * as api from './api';
import { fetchAndPopulateCommitmentOptions } from './commitmentOptions';
import { initFederationPanel } from './federation';
import { confirmDialog } from './confirmDialog';
import { showToast } from './toast';
import {
  normalizePaymentValue,
} from './commitmentOptions';
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
import {
  closeAccountModal,
  handleAccountFormSubmit,
  handleDiscoverOrgAccounts,
  loadAccountsForProvider,
  openAccountModal,
  updateAwsAuthModeFields,
  updateAzureAuthModeFields,
  updateGCPAuthModeFields,
} from './modules/settings-accounts';
import { renderAwsBastionTrustPolicy } from './modules/settings-aws-trust';
import { setupDirtyTracking, snapshotAllFields, updateDirtyMarkers } from './modules/settings-dirty';
import {
  applyReadOnlySettings,
  byId,
  copyToClipboard,
  getFieldValue,
  isPermissionDeniedError,
  populateGraceInput,
  readGraceInput,
} from './modules/settings-fields';
import {
  closeAccountOverridesModal,
  closeOverrideModal,
  setCachedGlobalDefaults,
  submitOverrideForm,
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
export { loadAccountsForProvider, openAccountModal } from './modules/settings-accounts';
export type { AccountModalOptions } from './modules/settings-accounts';
export { resetConfigCache } from './modules/settings-aws-trust';
export { isUnsavedChanges } from './modules/settings-dirty';
export { isPermissionDeniedError, applyReadOnlySettings, copyToClipboard } from './modules/settings-fields';
export { setGlobalDefaultsForTest, loadOverridesPanel, openOverrideModal } from './modules/settings-overrides';

let cachedSourceCloud: string | undefined;

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

      // Experimental GCP plan-term collection (issue #858); off unless the API says true.
      const gcpPlanTermEl = byId<HTMLInputElement>('setting-experimental-gcp-plan-term');
      if (gcpPlanTermEl) {
        gcpPlanTermEl.checked = data.global.experimental_gcp_plan_term === true;
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
    experimental_gcp_plan_term: byId<HTMLInputElement>('setting-experimental-gcp-plan-term')?.checked ?? false,
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

  // Reset the experimental GCP plan-term switch to off (default).
  const gcpPlanTermResetEl = byId<HTMLInputElement>('setting-experimental-gcp-plan-term');
  if (gcpPlanTermResetEl) gcpPlanTermResetEl.checked = false;

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
