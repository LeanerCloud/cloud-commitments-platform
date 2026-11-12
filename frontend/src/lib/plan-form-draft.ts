// Session-scoped copy of the plan form, so a sign-in after the session
// expires mid-edit (which reloads the page) does not discard the user's
// typing (#1735). Stores form field values and chosen account ids/names only:
// no tokens or credentials. Every storage access is guarded because storage
// can be blocked or full.

const KEY = 'cudly:plan-form-draft';
const RAMP_RADIO_NAME = 'ramp-schedule';
// Derived or transient controls that are not part of the draft.
const SKIPPED_IDS = new Set(['plan-id', 'plan-account-search', 'plan-account-ids']);
// Applied first, in this order, because each change event rebuilds the
// options (and clears the accounts) of the controls that follow.
const MAX_AGE_MS = 24 * 60 * 60 * 1000;
const FIELD_ID = /^[A-Za-z0-9_-]+$/;
const LEADING_IDS = ['plan-provider', 'plan-service'];

export interface PlanFormAccount { id: string; name: string; external_id: string }

export interface PlanFormDraft {
  savedAt: number;
  userId: string;
  planId: string;
  ramp: string;
  fields: Record<string, string | boolean>;
  accounts: PlanFormAccount[];
}

export function readPlanForm(form: HTMLFormElement): Pick<PlanFormDraft, 'ramp' | 'fields'> {
  const fields: Record<string, string | boolean> = {};
  let ramp = '';
  for (const el of Array.from(form.elements)) {
    if (!(el instanceof HTMLInputElement || el instanceof HTMLSelectElement || el instanceof HTMLTextAreaElement)) continue;
    if (el instanceof HTMLInputElement && el.type === 'radio') {
      if (el.name === RAMP_RADIO_NAME && el.checked) ramp = el.value;
      continue;
    }
    if (!el.id || SKIPPED_IDS.has(el.id)) continue;
    fields[el.id] = el instanceof HTMLInputElement && el.type === 'checkbox' ? el.checked : el.value;
  }
  return { ramp, fields };
}

/** Write a saved draft back into the (already opened) form. */
export function applyPlanForm(form: HTMLFormElement, draft: Pick<PlanFormDraft, 'ramp' | 'fields'>): void {
  const set = (id: string, value: string | boolean, fire: boolean): void => {
    const el = Array.from(form.elements).find(e => e.id === id) as HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement | undefined;
    if (!el) return;
    if (el instanceof HTMLInputElement && el.type === 'checkbox') el.checked = value === true;
    else el.value = String(value);
    if (fire) el.dispatchEvent(new Event('change', { bubbles: true }));
  };
  for (const id of LEADING_IDS) {
    if (id in draft.fields) set(id, draft.fields[id] as string, true);
  }
  const radio = form.querySelector<HTMLInputElement>(`input[name="${RAMP_RADIO_NAME}"][value="${draft.ramp}"]`);
  if (radio) {
    radio.checked = true;
    radio.dispatchEvent(new Event('change', { bubbles: true }));
  }
  // The plan name goes last: the provider, service and ramp changes above
  // regenerate it.
  const rest = Object.keys(draft.fields).filter(id => !LEADING_IDS.includes(id) && id !== 'plan-name');
  for (const id of rest) set(id, draft.fields[id] as string | boolean, false);
  if ('plan-name' in draft.fields) set('plan-name', draft.fields['plan-name'] as string, false);
}

export function savePlanDraft(draft: Omit<PlanFormDraft, 'savedAt'>): void {
  try {
    sessionStorage.setItem(KEY, JSON.stringify({ ...draft, savedAt: Date.now() }));
  } catch {
    // Storage blocked or full: the draft just is not kept.
  }
}

export function clearPlanDraft(): void {
  try {
    sessionStorage.removeItem(KEY);
  } catch {
    // Nothing to clear when storage is unavailable.
  }
}

/** The stored draft, or null when absent, unreadable or malformed. */
export function loadPlanDraft(): PlanFormDraft | null {
  try {
    const raw = sessionStorage.getItem(KEY);
    if (!raw) return null;
    const d = JSON.parse(raw) as Partial<PlanFormDraft> | null;
    if (!d || typeof d.userId !== 'string' || typeof d.planId !== 'string' || typeof d.savedAt !== 'number' || typeof d.ramp !== 'string'
      || !d.fields || typeof d.fields !== 'object' || !Array.isArray(d.accounts)) return null;
    if (Date.now() - d.savedAt > MAX_AGE_MS) {
      clearPlanDraft();
      return null;
    }
    // Storage can be edited by anything on the origin: keep only well-formed entries.
    const fields = Object.fromEntries(Object.entries(d.fields).filter(([id, v]) =>
      FIELD_ID.test(id) && (typeof v === 'string' || typeof v === 'boolean')));
    const accounts = d.accounts.filter((a): a is PlanFormAccount => !!a && typeof a === 'object'
      && typeof a.id === 'string' && typeof a.name === 'string' && typeof a.external_id === 'string');
    return { ...(d as PlanFormDraft), fields, accounts };
  } catch {
    return null;
  }
}
