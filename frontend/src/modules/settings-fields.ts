import { createIcon } from '../icons';
import { canAccess } from '../permissions';

/**
 * Returns true when an API error signals a 403 permission-denied response.
 * Exported so sibling modules (riexchange.ts) can apply the same graceful
 * degradation without duplicating the predicate.
 */
export function isPermissionDeniedError(error: unknown): boolean {
  return error instanceof Error
    && (error as { status?: number }).status === 403
    && error.message.startsWith('permission denied');
}

/**
 * byId is the null-safe replacement for `document.getElementById(...) as T`.
 * Returning `T | null` forces callers to guard with optional chaining (`?.value`,
 * `?.checked`) rather than dereferencing blindly, so a missing element in the
 * DOM produces a silent no-op instead of a TypeError.
 *
 * Pattern to migrate from:
 *   (document.getElementById('foo') as HTMLInputElement).value = v;
 * to:
 *   const el = byId<HTMLInputElement>('foo'); if (el) el.value = v;
 * or:
 *   byId<HTMLInputElement>('foo')?.value ?? '';  // read site
 */
// Grace-period input: 0 is a valid user choice (disables the feature
// for that provider), so an explicit 0 must round-trip as 0, not get
// swallowed. A missing server value renders as the default (7).
export function populateGraceInput(id: string, value: number | undefined): void {
  const el = document.getElementById(id) as HTMLInputElement | null;
  if (!el) return;
  el.value = String(value ?? 7);
}

// readGraceInput parses the grace-period input, returning the numeric
// value or an error message (empty, out of range, not a number). Empty is
// refused, not defaulted: a cleared field most likely means 0.
//
// The error wording mirrors what wireInlineRangeValidation renders
// under the field on input/blur, so the inline indicator and the
// save-time toast carry the same text — a user who ignores the inline
// red mark and clicks Save shouldn't see a differently-phrased message
// describing the same failure (CodeRabbit follow-up to #471).
export function readGraceInput(id: string): { value: number; err?: undefined } | { value: 0; err: string } {
  const el = document.getElementById(id) as HTMLInputElement | null;
  if (!el) return { value: 7 };
  const raw = el.value.trim();
  const n = Number(raw);
  if (raw === '' || !Number.isFinite(n) || !Number.isInteger(n) || n < 0 || n > 30) {
    return { value: 0, err: 'Must be a whole number between 0 and 30' };
  }
  return { value: n };
}

export function byId<T extends HTMLElement>(id: string): T | null {
  return document.getElementById(id) as T | null;
}

/**
 * setInputValue writes a value to an <input>/<textarea> looked up by id, no-op
 * if the element is missing. Consolidates the repetitive
 * `byId<HTMLInputElement>('x'); if (el) el.value = v;` pattern used across
 * modal-populate helpers.
 */
export function setInputValue(id: string, value: string): void {
  const el = byId<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>(id);
  if (el) el.value = value;
}

/**
 * setInputChecked writes `checked` to a checkbox input, no-op if the element
 * is missing. Same motivation as setInputValue.
 */
export function setInputChecked(id: string, checked: boolean): void {
  const el = byId<HTMLInputElement>(id);
  if (el) el.checked = checked;
}

export function getFieldValue(id: string): string {
  const el = document.getElementById(id);
  if (!el) return '';
  if (el instanceof HTMLInputElement) {
    return el.type === 'checkbox' ? String(el.checked) : el.value;
  }
  if (el instanceof HTMLSelectElement || el instanceof HTMLTextAreaElement) return el.value;
  return '';
}

export function termLabel(value: string): string {
  return value === '1' ? '1 Year' : value === '3' ? '3 Years' : `${value} Years`;
}

/**
 * Build the "Inherit (currently: X)" label for override-modal "Inherit"
 * options (issue #112). `currentValue` must already be human-readable (e.g.,
 * "3 Years", "All Upfront", "80%") -- callers are responsible for formatting.
 * The value is injected via textContent, not innerHTML, so no escaping needed.
 */
export function inheritCurrentlyLabel(currentValue: string): string {
  return `Inherit (currently: ${currentValue})`;
}

export function paymentLabel(value: string): string {
  switch (value) {
    case 'no-upfront': return 'No Upfront';
    case 'partial-upfront': return 'Partial Upfront';
    case 'all-upfront': return 'All Upfront';
    default: return value;
  }
}

/**
 * Issue #365: render the global-settings form read-only for sessions
 * that lack `admin:*`. Disable every form control so the user can't
 * even start editing, and hide Save / Reset / Delete-override CTAs.
 * Backend remains authoritative; this is a UX gate.
 *
 * Issue #870: also covers the Purchasing Policies panel (#purchasing-panel),
 * which is a sibling <section> outside #global-settings-form. Called from
 * riexchange.ts after the RI Exchange Automation form is rendered so
 * dynamically-injected inputs are covered too.
 *
 * The form stays VISIBLE so non-admin sessions can still inspect the
 * configured providers, default term/payment, and grace windows.
 */
export function applyReadOnlySettings(formEl: HTMLElement | null): void {
  const purchasingPanel = document.getElementById('purchasing-panel');
  const controlSelector = 'input, select, textarea, button';

  if (canAccess('admin', '*')) {
    // Admin: ensure controls are enabled in case a prior session
    // (e.g. role downgraded mid-session) left them disabled.
    if (formEl) {
      formEl.querySelectorAll<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement | HTMLButtonElement>(
        controlSelector,
      ).forEach((el) => { el.disabled = false; });
    }
    if (purchasingPanel) {
      purchasingPanel.querySelectorAll<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement | HTMLButtonElement>(
        controlSelector,
      ).forEach((el) => { el.disabled = false; });
    }
    document.querySelectorAll<HTMLButtonElement>(
      '#save-settings-btn, #reset-settings-btn, #save-purchasing-btn, #reset-purchasing-btn',
    ).forEach((el) => { el.hidden = false; });
    return;
  }

  // Non-admin: disable every form input + select so accidental edits
  // are blocked, and hide the destructive / write-back CTAs. The
  // dirty-tracking baseline already runs on load so no Save prompt
  // fires even if a control sneaks past the disable.
  if (formEl) {
    formEl.querySelectorAll<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement | HTMLButtonElement>(
      controlSelector,
    ).forEach((el) => { el.disabled = true; });
  }
  if (purchasingPanel) {
    purchasingPanel.querySelectorAll<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement | HTMLButtonElement>(
      controlSelector,
    ).forEach((el) => { el.disabled = true; });
  }
  document.querySelectorAll<HTMLButtonElement>(
    '#save-settings-btn, #reset-settings-btn, #save-purchasing-btn, #reset-purchasing-btn',
  ).forEach((el) => { el.hidden = true; });
}

/**
 * Copy text from an element to clipboard
 */
export function copyToClipboard(elementId: string): void {
  const element = document.getElementById(elementId);
  if (!element) return;

  // Prefer .value for form controls (the AWS External ID uses an
  // <input readonly>) and fall back to textContent for code/span
  // elements.
  const text = element instanceof HTMLInputElement || element instanceof HTMLTextAreaElement
    ? element.value
    : element.textContent ?? '';
  navigator.clipboard.writeText(text).then(() => {
    // Show feedback on the first .copy-btn following the source in DOM
    // order — handles both the adjacent-sibling layout (code + btn) and
    // the .input-with-copy layout (input + btn nested under a wrapper).
    const btn = element.nextElementSibling instanceof HTMLButtonElement
      ? element.nextElementSibling
      : element.parentElement?.querySelector<HTMLButtonElement>('.copy-btn') ?? null;
    if (!btn) return;
    const previousChildren = Array.from(btn.childNodes);
    // Swap for a single "copied" checkmark without touching innerHTML —
    // the content is static but XSS-scanner-friendly code avoids the
    // pattern entirely so we don't need to re-prove safety at each edit.
    btn.replaceChildren();
    const tick = document.createElement('span');
    tick.className = 'copy-icon';
    tick.appendChild(createIcon('check'));
    btn.appendChild(tick);
    btn.classList.add('copied');
    setTimeout(() => {
      btn.replaceChildren(...previousChildren);
      btn.classList.remove('copied');
    }, 2000);
  }).catch(err => {
    console.error('Failed to copy:', err);
  });
}
