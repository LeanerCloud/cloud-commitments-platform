import * as api from '../../api';
import { friendlyError } from '../../api/errors';
import { showToast } from '../../toast';
import { isValidCombination } from '../../commitmentOptions';
import { byId } from '../settings-fields';
import type { AccountProvider } from '../settings-state';
import { overridesState } from './state';
import type { OverrideModalContext } from './state';
import { refreshRecommendationsAfterOverrideChange } from './refresh';
import { closeOverrideModal } from './modal';
import { loadOverridesPanel } from './panel';

/**
 * Submit the create-override form. Sends a sparse PUT — fields left blank
 * (term/payment/coverage = "Inherit") are omitted so the backend's
 * overlayOverrideScalars treats them as "unset → inherit global default".
 */
export async function submitOverrideForm(e: Event): Promise<void> {
  e.preventDefault();
  const ctx = overridesState.overrideModalContext;
  if (!ctx) return;

  const termRaw = (byId<HTMLSelectElement>('override-term')?.value ?? '').trim();
  const paymentRaw = (byId<HTMLSelectElement>('override-payment')?.value ?? '').trim();
  const coverageRaw = (byId<HTMLInputElement>('override-coverage')?.value ?? '').trim();

  const req: api.AccountServiceOverrideRequest = {};
  if (termRaw !== '') req.term = parseInt(termRaw, 10);
  if (paymentRaw !== '') req.payment = paymentRaw;
  if (coverageRaw !== '') {
    const n = Number(coverageRaw);
    if (!Number.isFinite(n) || !Number.isInteger(n) || n < 0 || n > 100) {
      const errEl = document.getElementById('override-form-error');
      if (errEl) errEl.textContent = 'Dashboard coverage must be a whole number between 0 and 100.';
      return;
    }
    req.coverage = n;
  }

  if (Object.keys(req).length === 0) {
    // The backend would happily store an all-null override row, but it'd
    // be a no-op semantically — no field overrides anything. Block at the
    // UI to avoid silent confusion.
    const errEl = document.getElementById('override-form-error');
    if (errEl) errEl.textContent = 'Set at least one of Term, Payment, or Dashboard coverage.';
    return;
  }

  // Determine which services to save to.
  const bulkToggle = document.getElementById('override-bulk-toggle') as HTMLInputElement | null;
  const isBulk = bulkToggle?.checked ?? false;

  if (isBulk) {
    await submitBulkOverrideForm(ctx, req);
    return;
  }

  // Single-service path (original behaviour).
  const service = (byId<HTMLSelectElement>('override-service')?.value ?? '').trim();
  if (!service) return;

  // Submit-side validation per #107: when both term and payment are
  // explicitly set, refuse to save invalid (service, term, payment)
  // combinations even if a stale select option somehow leaked through
  // (e.g., browser back-button restoring a stale dropdown state).
  // Mirror of the global Settings → Purchasing card's checkCommitmentOptionCombo.
  if (req.term != null && req.payment) {
    if (!isValidCombination(ctx.provider, service, req.term, req.payment)) {
      const errEl = document.getElementById('override-form-error');
      if (errEl) {
        errEl.textContent = `${ctx.provider}/${service} does not support ${req.term}-year ${req.payment}. Pick a different term or payment.`;
      }
      return;
    }
  }

  const submitBtn = document.getElementById('override-submit-btn') as HTMLButtonElement | null;
  if (submitBtn) submitBtn.disabled = true;
  try {
    await api.saveAccountServiceOverride(ctx.accountId, ctx.provider, service, req);
    showToast({
      message: `Override created for ${ctx.provider}/${service}.`,
      kind: 'success',
    });
    closeOverrideModal();
    await loadOverridesPanel(ctx.accountId, ctx.panel, ctx.provider as AccountProvider);
    await refreshRecommendationsAfterOverrideChange();
  } catch (err) {
    const errEl = document.getElementById('override-form-error');
    if (errEl) errEl.textContent = `Failed to create override: ${(err as Error).message}`;
  } finally {
    if (submitBtn) submitBtn.disabled = false;
  }
}

/**
 * Fan out save calls for all checked services (issue #119).
 *
 * Uses Promise.allSettled so a failure on one service does not abort the
 * others. Shows one aggregated toast on completion summarising how many
 * succeeded and which failed.
 */
async function submitBulkOverrideForm(
  ctx: OverrideModalContext,
  req: api.AccountServiceOverrideRequest,
): Promise<void> {
  const listDiv = document.getElementById('override-bulk-services-list');
  const checked = Array.from(
    listDiv?.querySelectorAll<HTMLInputElement>('input[type="checkbox"]:checked') ?? [],
  ).map(cb => cb.value);

  if (checked.length === 0) {
    const errEl = document.getElementById('override-form-error');
    if (errEl) errEl.textContent = 'Select at least one service.';
    return;
  }

  // Validate (provider, service, term, payment) combinations before fanning out.
  if (req.term != null && req.payment) {
    const invalid = checked.filter(
      service => !isValidCombination(ctx.provider, service, req.term!, req.payment!),
    );
    if (invalid.length > 0) {
      const errEl = document.getElementById('override-form-error');
      if (errEl) {
        errEl.textContent = `${invalid.join(', ')}: does not support ${req.term}-year ${req.payment}. Pick a different term or payment.`;
      }
      return;
    }
  }

  const submitBtn = document.getElementById('override-submit-btn') as HTMLButtonElement | null;
  if (submitBtn) submitBtn.disabled = true;
  const errEl = document.getElementById('override-form-error');
  if (errEl) errEl.textContent = '';

  const results = await Promise.allSettled(
    checked.map(service => api.saveAccountServiceOverride(ctx.accountId, ctx.provider, service, req)),
  );

  const failed = checked.filter((_, i) => results[i]?.status === 'rejected');
  const succeeded = checked.length - failed.length;

  if (failed.length === 0) {
    showToast({
      message: `Created ${succeeded} override${succeeded !== 1 ? 's' : ''}.`,
      kind: 'success',
    });
    closeOverrideModal();
    await loadOverridesPanel(ctx.accountId, ctx.panel, ctx.provider as AccountProvider);
    await refreshRecommendationsAfterOverrideChange();
  } else if (succeeded === 0) {
    if (submitBtn) submitBtn.disabled = false;
    if (errEl) {
      // Show the server's reason (e.g. a validation error) instead of
      // always blaming the connection (issue #748).
      const firstFailure = results.find((r): r is PromiseRejectedResult => r.status === 'rejected');
      errEl.textContent = `All ${failed.length} save${failed.length !== 1 ? 's' : ''} failed: ${friendlyError(firstFailure?.reason)}`;
    }
  } else {
    // Partial success: close and reload, surface the failures in a toast.
    showToast({
      message: `Created ${succeeded} override${succeeded !== 1 ? 's' : ''}; failed for: ${failed.join(', ')}.`,
      kind: 'warning',
    });
    closeOverrideModal();
    await loadOverridesPanel(ctx.accountId, ctx.panel, ctx.provider as AccountProvider);
    await refreshRecommendationsAfterOverrideChange();
  }
}
