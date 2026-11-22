/**
 * Settings module for CUDly
 */

import * as api from './api';
import { confirmDialog } from './confirmDialog';
import { showToast } from './toast';
import { renderUpdatedStamp, readServiceFilterControls, isServiceFilterError } from './serviceFilters';
import { updateDirtyMarkers } from './modules/settings-dirty';
import { byId, getFieldValue, populateGraceInput, readGraceInput } from './modules/settings-fields';
import { updateCollectionScheduleVisibility, updateProviderSettingsVisibility } from './modules/settings-handlers';
import { revalidateInlineRanges } from './modules/settings-range-validation';
import { GRACE_INPUTS, changedServiceFields, invalidEditedCardCoverages, newServiceConfigDefaults, rebaseMultiFieldKeys, serviceSaveFailureMessage, showPropagatedCoverage, showSavedGrace } from './modules/settings-save-helpers';
import { GLOBAL_CONFIG_FIELDS, SERVICE_FIELDS, TRACKED_FIELDS, savedSnapshot } from './modules/settings-state';
import { settingsState } from './settingsflow/settingsState';
export { loadAccountsForProvider, openAccountModal } from './modules/settings-accounts';
export type { AccountModalOptions } from './modules/settings-accounts';
export { resetConfigCache } from './modules/settings-aws-trust';
export { isUnsavedChanges } from './modules/settings-dirty';
export { isPermissionDeniedError, applyReadOnlySettings, copyToClipboard } from './modules/settings-fields';
export { setupSettingsHandlers } from './modules/settings-handlers';
export { setGlobalDefaultsForTest, loadOverridesPanel, openOverrideModal } from './modules/settings-overrides';
export { loadGlobalSettings, loadAccountsTab } from './settingsflow/load';

// saveInFlight guards saveGlobalSettings against concurrent invocations
// (rapid Save clicks, Enter-in-form, etc.). Toggled in a try/finally and
// mirrored on the Save button's disabled attribute so the UI reflects the
// in-progress state.
let saveInFlight = false;

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
      settingsState.loadedServiceConfigs = settingsState.loadedServiceConfigs.map(s => ({ ...s, coverage: settings.default_coverage }));
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
      const base = settingsState.loadedServiceConfigs.find(s => s.provider === provider && s.service === service);
      const filters = filterValuesByService.get(`${provider}-${service}`);
      // Only the rows (and the fields of a row) the user changed go out, so a
      // stale tab cannot revert a service another admin edited (#539).
      const changes = changedServiceFields(field, base, filters && !isServiceFilterError(filters) ? filters : undefined);
      const rekeyed = base !== undefined && settingsState.legacyRekeyedServices.has(`${provider}/${service}`);
      if (!changes && !rekeyed) return null;
      const fresh = newServiceConfigDefaults(field, settings);
      let cfg: Partial<api.ServiceConfig>;
      if (!base) cfg = { ...fresh, ...changes };
      else if (rekeyed) cfg = { ...base, term: fresh.term, payment: fresh.payment, ...changes };
      else cfg = { provider, service, ...changes };
      return api.updateServiceConfig(provider, service, cfg).then(() => {
        settingsState.legacyRekeyedServices.delete(`${provider}/${service}`);
        // The PUT does not return the row, so the stamp is the save time here;
        // the next load shows the server's value.
        const updatedAt = new Date().toISOString();
        settingsState.loadedServiceConfigs = [
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
