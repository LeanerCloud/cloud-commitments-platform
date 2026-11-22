import * as api from '../api';
import { fetchAndPopulateCommitmentOptions, normalizePaymentValue } from '../commitmentOptions';
import { initFederationPanel } from '../federation';
import { injectServiceFilterControls, populateServiceFilterControls } from '../serviceFilters';
import { initLadderingSettings } from '../ladder';
import { showSkeletonText } from '../lib/skeleton';
import { canonicalizeServiceConfigs } from '../lib/settings-service-ids';
import { loadAccountsForProvider } from '../modules/settings-accounts';
import { snapshotAllFields, updateDirtyMarkers } from '../modules/settings-dirty';
import { applyReadOnlySettings, byId, isPermissionDeniedError, populateGraceInput } from '../modules/settings-fields';
import { updateCollectionScheduleVisibility, updateProviderSettingsVisibility } from '../modules/settings-handlers';
import { setCachedGlobalDefaults } from '../modules/settings-overrides';
import { syncAllServiceCommitmentConstraints } from '../modules/settings-propagation';
import { revalidateInlineRanges } from '../modules/settings-range-validation';
import { SERVICE_FIELDS } from '../modules/settings-state';
import { settingsState } from './settingsState';

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
    settingsState.loadedServiceConfigs = services;
    // Re-keyed rows have no backend row under the current id yet, so the next
    // Save writes them in full instead of as a partial change (#539).
    const storedKeys = new Set(stored.map(r => `${r.provider}/${r.service}`));
    settingsState.legacyRekeyedServices = new Set(services.filter(r => !storedKeys.has(`${r.provider}/${r.service}`)).map(r => `${r.provider}/${r.service}`));
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

    settingsState.cachedSourceCloud = data.source_cloud ?? 'aws';
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
  let source = settingsState.cachedSourceCloud;
  if (!source) {
    try {
      const cfg = await api.getConfig();
      source = cfg.source_cloud ?? 'aws';
      settingsState.cachedSourceCloud = source;
    } catch {
      source = 'aws';
    }
  }
  void loadAccountsForProvider('aws');
  void loadAccountsForProvider('azure');
  void loadAccountsForProvider('gcp');
  void initFederationPanel(source);
  void import('../modules/registrations').then(m => m.initRegistrations());
  void import('../modules/override-transfer').then(m => m.initOverrideTransfer());
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
