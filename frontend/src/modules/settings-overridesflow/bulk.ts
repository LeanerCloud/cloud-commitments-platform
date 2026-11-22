import { isValidCombination } from '../../commitmentOptions';
import { overridesState } from './state';

/**
 * Switch the override modal between single-service and bulk-service mode.
 *
 * In single mode the normal <select id="override-service"> is shown.
 * In bulk mode it is hidden and a checkbox group is shown instead (issue #119).
 * Uses DOM APIs throughout — no innerHTML — so no XSS risk from service values.
 */
export function applyOverrideBulkMode(
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
  syncBulkServiceConflicts(overridesState.overrideModalContext?.provider ?? '');
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
export function syncBulkServiceConflicts(provider: string): void {
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
