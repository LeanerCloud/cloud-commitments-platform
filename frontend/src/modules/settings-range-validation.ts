// Setting `.value` from code fires neither 'input' nor 'blur', so the inline
// range validators below never see a value written by loadGlobalSettings or
// resetSettings. Without a re-run, an error from an earlier edit (e.g. an
// emptied coverage field) stays visible, with aria-invalid, next to a valid
// value (#642). A dedicated event re-runs only the validators, not the
// dirty-tracking or other 'input' listeners.
const RANGE_REVALIDATE_EVENT = 'cudly:range-revalidate';

/** Re-run every wired inline range validator against the current values. */
export function revalidateInlineRanges(): void {
  document.querySelectorAll<HTMLInputElement>('input[data-range-validated]').forEach((el) => {
    el.dispatchEvent(new Event(RANGE_REVALIDATE_EVENT));
  });
}

/**
 * Wire inline range validation on a numeric `<input>` driven by its
 * existing `min` / `max` HTML5 attributes. Surfaces a single message
 * under the field as the user types (and on blur) instead of waiting
 * for Save Settings to reject the value — addresses #465 by collapsing
 * the two-trip "must be ≥ X" / "must be ≤ Y" pattern into one inline
 * indication.
 *
 * When `requireInteger` is true the validator additionally rejects
 * fractional values like `1.5` (matching the save-time guards), and
 * the message reads "Must be a whole number between X and Y" to
 * explain why an in-range fraction is still rejected. Without it the
 * inline path accepts 1.5 while save-time rejects it — exactly the
 * two-trip behaviour this PR sets out to fix (CodeRabbit on #471).
 *
 * The element's min/max attributes are the source of truth so the
 * range stays in sync with the HTML. An empty value clears the error
 * (the input may have its own "required" enforcement; we don't fight
 * that here). Pass `required` for a field whose empty value would
 * save as 0 (the save path rejects it too), so the error shows inline.
 */
export function wireInlineRangeValidation(
  inputId: string,
  { signal, requireInteger = false, required = false }: { signal?: AbortSignal; requireInteger?: boolean; required?: boolean } = {},
): void {
  const input = document.getElementById(inputId) as HTMLInputElement | null;
  if (!input) return;
  const min = parseFloat(input.getAttribute('min') ?? '');
  const max = parseFloat(input.getAttribute('max') ?? '');
  if (!Number.isFinite(min) || !Number.isFinite(max)) return;

  const errorId = `${inputId}-range-error`;
  let errorEl = document.getElementById(errorId);
  if (!errorEl) {
    errorEl = document.createElement('small');
    errorEl.id = errorId;
    errorEl.className = 'field-error hidden';
    // role=status + aria-live=polite (not role=alert/aria-live=assertive)
    // so screen readers wait for a typing pause before announcing — an
    // assertive live region would interrupt the user on every keystroke
    // while the value is still in flux (WCAG SCR32). CodeRabbit on #471.
    errorEl.setAttribute('role', 'status');
    errorEl.setAttribute('aria-live', 'polite');
    // Insert the error element outside the nearest flex container so it
    // renders below the input row rather than as a flex item to its right.
    // Priority: after the .setting-input wrapper (settings form rows) ->
    // after the enclosing <label> (SP-coverage cards) -> directly after the
    // input as a final fallback (test DOM / modal forms without that wrapper).
    // Issue #1411.
    const insertionTarget =
      input.closest('.setting-input') ??
      (input.closest('label') as HTMLLabelElement | null) ??
      input;
    insertionTarget.insertAdjacentElement('afterend', errorEl);
    // Append (don't overwrite) any pre-existing aria-describedby so the
    // input's existing help text (e.g. unit hints) stays announced.
    const existingDescribedBy = input.getAttribute('aria-describedby');
    input.setAttribute(
      'aria-describedby',
      existingDescribedBy ? `${existingDescribedBy} ${errorId}` : errorId,
    );
  }
  // Capture as const so the closure doesn't need a non-null assertion.
  const error = errorEl;
  const message = requireInteger
    ? `Must be a whole number between ${min} and ${max}`
    : `Must be between ${min} and ${max}`;

  const check = (): void => {
    const raw = input.value.trim();
    if (raw === '' && !required) {
      input.removeAttribute('aria-invalid');
      error.classList.add('hidden');
      return;
    }
    // Number('') is 0, so a required empty field is invalid explicitly.
    const n = Number(raw);
    const invalid =
      raw === '' ||
      !Number.isFinite(n) ||
      (requireInteger && !Number.isInteger(n)) ||
      n < min ||
      n > max;
    if (invalid) {
      input.setAttribute('aria-invalid', 'true');
      error.textContent = message;
      error.classList.remove('hidden');
    } else {
      input.removeAttribute('aria-invalid');
      error.classList.add('hidden');
    }
  };
  input.addEventListener('input', check, { signal });
  input.addEventListener('blur', check, { signal });
  input.addEventListener(RANGE_REVALIDATE_EVENT, check, { signal });
  input.dataset['rangeValidated'] = '';
}
