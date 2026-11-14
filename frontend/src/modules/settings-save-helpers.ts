import * as api from '../api';
import { normalizePaymentValue } from '../commitmentOptions';
import { readServiceFilterControls } from '../serviceFilters';
import { byId, getFieldValue, isPermissionDeniedError, populateGraceInput } from './settings-fields';
import { revalidateInlineRanges } from './settings-range-validation';
import { SERVICE_FIELDS, savedSnapshot } from './settings-state';

/**
 * Names of the per-Savings-Plan coverage cards whose edited value is not a
 * whole number between 0 and 100. Untouched cards are not checked; an edited
 * card cleared to empty is invalid, not silently dropped.
 */
export function invalidEditedCardCoverages(): string[] {
  const bad: string[] = [];
  for (const field of SERVICE_FIELDS) {
    if (!('coverageId' in field) || !field.coverageId) continue;
    const input = byId<HTMLInputElement>(field.coverageId);
    if (!input) continue;
    const raw = input.value;
    if (getFieldValue(field.coverageId) === savedSnapshot[field.coverageId]) continue;
    // Number('') is 0, so an edited-to-empty input must be rejected explicitly.
    const parsed = Number(raw);
    if (raw.trim() !== '' && Number.isInteger(parsed) && parsed >= 0 && parsed <= 100) continue;
    const title = input?.closest('.service-default-card')?.querySelector('h5')?.textContent?.trim();
    bad.push(title || field.service);
  }
  return bad;
}

const SERVICE_FILTER_KEYS = [
  'include_engines', 'exclude_engines', 'include_types', 'exclude_types',
  'include_regions', 'exclude_regions',
] as const;

function isDirtyField(id: string): boolean {
  return id in savedSnapshot && getFieldValue(id) !== savedSnapshot[id];
}

/**
 * The fields of one service row the user changed since load/save, or null when
 * nothing changed (no PUT). A card's term, payment, coverage and enabled
 * controls count as changed when they differ from the saved snapshot; the
 * recommendation filters are compared with the loaded row.
 */
export function changedServiceFields(
  field: (typeof SERVICE_FIELDS)[number],
  base: api.ServiceConfig | undefined,
  filters: Exclude<ReturnType<typeof readServiceFilterControls>, { message: string }> | undefined,
): Partial<api.ServiceConfig> | null {
  const changes: Partial<api.ServiceConfig> = {};
  if (isDirtyField(field.termId)) {
    changes.term = parseInt(byId<HTMLSelectElement>(field.termId)?.value || '3', 10);
  }
  if (field.paymentId && isDirtyField(field.paymentId)) {
    changes.payment = byId<HTMLSelectElement>(field.paymentId)?.value || normalizePaymentValue('no-upfront', field.provider);
  }
  if ('coverageId' in field && field.coverageId && isDirtyField(field.coverageId)) {
    const rawCov = byId<HTMLInputElement>(field.coverageId)?.value ?? '';
    if (rawCov !== '' && Number.isFinite(Number(rawCov))) changes.coverage = Number(rawCov);
  }
  if ('enabledId' in field && field.enabledId && isDirtyField(field.enabledId)) {
    changes.enabled = byId<HTMLInputElement>(field.enabledId)?.checked ?? true;
  }
  if (filters) {
    for (const key of SERVICE_FILTER_KEYS) {
      if (filters[key].join(',') !== (base?.[key] ?? []).join(',')) changes[key] = filters[key];
    }
    if (filters.min_count !== (base?.min_count ?? 0)) changes.min_count = filters.min_count;
  }
  if (Object.keys(changes).length === 0) return null;
  if (!changes.payment && base) {
    // A stored payment the card normalises on load (#545) goes out with any
    // PUT of the row, or the backend validates the legacy token and rejects it.
    // GCP cards have no payment control and GCP only accepts monthly, so a row
    // holding another token (an AWS default copied onto it) is repaired too.
    const shown = field.paymentId ? byId<HTMLSelectElement>(field.paymentId)?.value : field.provider === 'gcp' ? 'monthly' : undefined;
    if (shown && shown !== base.payment) changes.payment = shown;
  }
  return changes;
}

/**
 * Required fields for a service row the backend has no record of yet, where a
 * partial body would fail validation. Mirrors what a first Save always sent.
 */
export function newServiceConfigDefaults(
  field: (typeof SERVICE_FIELDS)[number],
  settings: api.Config,
): api.ServiceConfig {
  const paymentEl = field.paymentId ? byId<HTMLSelectElement>(field.paymentId) : null;
  return {
    provider: field.provider,
    service: field.service,
    enabled: true,
    term: parseInt(byId<HTMLSelectElement>(field.termId)?.value || '3', 10),
    payment: paymentEl?.value || normalizePaymentValue(field.paymentId ? 'no-upfront' : settings.default_payment, field.provider),
    coverage: settings.default_coverage,
  };
}

/**
 * The backend copied a changed default coverage onto every service row, so show
 * it on the Savings Plan cards the user did not edit in this Save (an edited
 * card keeps its value and is sent as its own change).
 */
export function showPropagatedCoverage(coverage: number, sentFields: Record<string, string>): void {
  for (const field of SERVICE_FIELDS) {
    if (!('coverageId' in field) || !field.coverageId) continue;
    const el = byId<HTMLInputElement>(field.coverageId);
    if (!el || getFieldValue(field.coverageId) !== savedSnapshot[field.coverageId]) continue;
    el.value = String(coverage);
    savedSnapshot[field.coverageId] = String(coverage);
    sentFields[field.coverageId] = String(coverage);
  }
}

export const GRACE_INPUTS = [['aws', 'setting-grace-aws'], ['azure', 'setting-grace-azure'], ['gcp', 'setting-grace-gcp']] as const;

/**
 * grace_period_days and enabled_providers are replaced whole by the backend, so
 * a tab opened before another admin's save would revert that admin's providers
 * (#539). Re-read the server's current value and overlay only the fields this
 * user changed, judged by the values captured at Save (the form may change while
 * the read is pending). Rejects, leaving nothing sent, if the server value is unavailable.
 */
export async function rebaseMultiFieldKeys(
  changedGlobal: Partial<api.Config>,
  isFieldChanged: (id: string) => boolean,
): Promise<void> {
  const fresh = await api.getConfig();
  if (!fresh?.global) throw new Error('could not load the current settings to merge into');
  if ('grace_period_days' in changedGlobal) {
    const merged: Record<string, number> = { ...(fresh.global.grace_period_days ?? {}) };
    for (const [provider, id] of GRACE_INPUTS) {
      if (isFieldChanged(id)) merged[provider] = changedGlobal.grace_period_days![provider]!;
    }
    changedGlobal.grace_period_days = merged;
  }
  if ('enabled_providers' in changedGlobal) {
    let merged: api.Provider[] = [...((fresh.global.enabled_providers ?? []) as api.Provider[])];
    for (const provider of ['aws', 'azure', 'gcp'] as const) {
      if (!isFieldChanged(`provider-${provider}`)) continue;
      const at = merged.indexOf(provider);
      const checked = changedGlobal.enabled_providers!.includes(provider);
      if (checked && at < 0) merged.push(provider);
      if (!checked) merged = merged.filter(p => p !== provider);
    }
    changedGlobal.enabled_providers = merged;
  }
}

/** Show the persisted grace map, keeping any input the user edited while the save ran. */
export function showSavedGrace(saved: Record<string, number>, sentFields: Record<string, string>): void {
  for (const [provider, id] of GRACE_INPUTS) {
    if (getFieldValue(id) !== sentFields[id]) continue;
    populateGraceInput(id, saved[provider]);
    savedSnapshot[id] = sentFields[id] = getFieldValue(id);
  }
  revalidateInlineRanges();
}

/**
 * Toast text for a Save where at least one service PUT failed. Only claim
 * "Settings saved" when something was: the global PUT went out or a service
 * PUT succeeded. When nothing saved and every service PUT was denied (a
 * scoped admin after #614 gated service config on unrestricted account
 * scope), show the single backend reason, matching the global PUT's
 * "Failed to save settings: permission denied: ..." toast instead of
 * repeating it once per service (#617).
 */
export function serviceSaveFailureMessage(
  results: PromiseSettledResult<void>[],
  failures: string[],
  globalSent: boolean,
): string {
  const savedSomething = globalSent || results.some(r => r.status === 'fulfilled');
  if (savedSomething) {
    return `Settings saved, but ${failures.length} service${failures.length === 1 ? '' : 's'} failed: ${failures.join('; ')}`;
  }
  const reasons = results.map(r => (r.status === 'rejected' ? r.reason : null));
  if (reasons.every(isPermissionDeniedError)) {
    return `Failed to save settings: ${(reasons[0] as Error).message}`;
  }
  return `Failed to save settings: ${failures.length} service${failures.length === 1 ? '' : 's'} failed: ${failures.join('; ')}`;
}
