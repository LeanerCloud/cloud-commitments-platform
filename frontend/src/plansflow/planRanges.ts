import * as api from '../api';
import { canAccess } from '../permissions';
import { serviceLabel } from '../lib/service-labels';
import { plansState } from './plansState';

/**
 * Generate a plan name based on the selected ramp schedule
 */
function generatePlanName(rampSchedule: string, customStepPercent?: number, customIntervalDays?: number): string {
  const service = (document.getElementById('plan-service') as HTMLSelectElement)?.value || 'EC2';
  const serviceName = serviceLabel(service);

  switch (rampSchedule) {
    case 'immediate':
      return `${serviceName} Full Coverage Purchase`;
    case 'weekly-25pct':
      return `${serviceName} Weekly 25% Ramp-up (4 weeks)`;
    case 'monthly-10pct':
      return `${serviceName} Monthly 10% Ramp-up (10 months)`;
    case 'custom':
      if (customStepPercent && customIntervalDays) {
        // Same count as the backend (buildCustomRampSchedule) and the schedule
        // preview: whole steps only, at least one. 30% is 3 steps, not 4 (#1185).
        const totalSteps = Math.max(1, Math.floor(100 / customStepPercent));
        const intervalLabel = customIntervalDays === 7 ? 'weekly' :
                              customIntervalDays === 30 ? 'monthly' :
                              `every ${customIntervalDays} days`;
        return `${serviceName} Custom ${customStepPercent}% ${intervalLabel} (${totalSteps} step${totalSteps === 1 ? '' : 's'})`;
      }
      return `${serviceName} Custom Ramp-up Plan`;
    default:
      return `${serviceName} Purchase Plan`;
  }
}

/**
 * Update plan name field based on current ramp schedule selection
 */
export function updatePlanNameFromSchedule(): void {
  const planNameInput = document.getElementById('plan-name') as HTMLInputElement;
  const planIdInput = document.getElementById('plan-id') as HTMLInputElement;

  // Only auto-generate name for new plans (not editing existing ones)
  if (planIdInput?.value) return;

  const rampScheduleRadio = document.querySelector<HTMLInputElement>('input[name="ramp-schedule"]:checked');
  const rampSchedule = rampScheduleRadio?.value || 'immediate';

  let customStepPercent: number | undefined;
  let customIntervalDays: number | undefined;

  if (rampSchedule === 'custom') {
    customStepPercent = parseInt((document.getElementById('ramp-step-percent') as HTMLInputElement)?.value || '20', 10);
    customIntervalDays = parseInt((document.getElementById('ramp-interval-days') as HTMLInputElement)?.value || '7', 10);
  }

  if (planNameInput) {
    planNameInput.value = generatePlanName(rampSchedule, customStepPercent, customIntervalDays);
  }
}

/**
 * Wire live range + integer-only validation on a plan numeric input.
 *
 * Registers an `input` handler that:
 *   - reject non-integer values via the regex `^\d+$` (blocks scientific
 *     notation such as `1e+30` and decimal fractions)
 *   - show a sibling `.field-error` span when the value is out of [min, max]
 *     or non-integer, and hide it when the value is valid or the field is empty
 *   - set / clear `aria-invalid` for screen-reader accessibility
 *
 * It never rewrites the value: Save blurs the field first, so a clamp would
 * save a number the user did not type (#1534). The Save handlers refuse instead.
 *
 * Idempotent: the `data-range-wired` attribute guards against re-registering
 * duplicate listeners when the modal is closed and reopened. The error span
 * is created once on the first call and reused thereafter. Explicit `min` /
 * `max` parameters are used instead of reading HTML attributes so callers are
 * the source of truth and the helper stays self-contained.
 */
export function wireRangeInput(inputId: string, min: number, max: number): void {
  const input = document.getElementById(inputId) as HTMLInputElement | null;
  if (!input) return;

  // Idempotency guard: skip registration if already wired during a prior
  // modal open. The error span, aria-describedby, and listeners persist
  // across opens because the modal node stays in the DOM.
  if (input.dataset['rangeWired']) {
    // Re-trigger validation so stale error UI is reconciled on modal reopen.
    input.dispatchEvent(new Event('input'));
    return;
  }
  input.dataset['rangeWired'] = '1';

  const errorId = `${inputId}-range-error`;
  let errorEl = document.getElementById(errorId);
  if (!errorEl) {
    errorEl = document.createElement('small');
    errorEl.id = errorId;
    errorEl.className = 'field-error hidden';
    errorEl.setAttribute('role', 'status');
    errorEl.setAttribute('aria-live', 'polite');
    input.insertAdjacentElement('afterend', errorEl);
    const existing = input.getAttribute('aria-describedby');
    input.setAttribute(
      'aria-describedby',
      existing ? `${existing} ${errorId}` : errorId,
    );
  }
  const error = errorEl;
  const message = `Must be a whole number between ${min} and ${max}`;
  const integerPattern = /^\d+$/;

  const check = (): void => {
    const raw = input.value.trim();
    if (raw === '') {
      input.removeAttribute('aria-invalid');
      error.classList.add('hidden');
      return;
    }
    if (!integerPattern.test(raw)) {
      input.setAttribute('aria-invalid', 'true');
      error.textContent = message;
      error.classList.remove('hidden');
      return;
    }
    const parsed = parseInt(raw, 10);
    if (parsed < min || parsed > max) {
      input.setAttribute('aria-invalid', 'true');
      error.textContent = message;
      error.classList.remove('hidden');
    } else {
      input.removeAttribute('aria-invalid');
      error.classList.add('hidden');
    }
  };

  input.addEventListener('input', check);
}

/**
 * Wire live range validation on all five plan-creation number inputs.
 * Called every time the plan modal opens so validation is active for both
 * the create and edit flows.
 */
export function wirePlanRangeInputs(): void {
  wireRangeInput('plan-coverage', 0, 100);
  wireRangeInput('ramp-step-percent', 1, 100);
  wireRangeInput('ramp-interval-days', 1, 365);
  wireRangeInput('plan-notify-days', 1, 30);
  const cap = document.getElementById('plan-max-step-commitment') as HTMLInputElement | null;
  const auto = document.getElementById('plan-auto-purchase') as HTMLInputElement | null;
  if (cap && auto) {
    cap.required = auto.checked;
    if (!auto.dataset['capWired']) {
      auto.dataset['capWired'] = '1';
      auto.addEventListener('change', () => { cap.required = auto.checked; });
    }
  }
  void refreshPlanAutomationState();
}

export function parsePlanStepCap(raw: string, automatic: boolean): number | null {
  if (raw === '') {
    if (automatic) throw new Error('Maximum commitment per step is required for Auto-Purchase');
    return null;
  }
  const value = Number(raw);
  const [whole = '', fraction = ''] = raw.split('.');
  const normalized = `${whole.replace(/^0+(?=\d)/, '')}.${fraction.padEnd(2, '0')}`;
  const [wireWhole = '', wireFraction = ''] = String(value).split('.');
  const wireAmount = `${wireWhole}.${wireFraction.padEnd(2, '0')}`;
  // Match the backend's decimal-text validation without multiplying binary floats by 100.
  if (!/^\d+(?:\.\d{1,2})?$/.test(raw) || !Number.isFinite(value) || value < 0.01 || value > 1e15 || wireAmount !== normalized) {
    throw new Error('Maximum commitment per step must be an exact amount from 0.01 to 1000000000000000 with at most two decimal places');
  }
  return value;
}

async function refreshPlanAutomationState(): Promise<void> {
  const request = ++plansState.planAutomationRead;
  const banner = document.getElementById('plan-auto-purchase-state');
  if (!banner) return;
  banner.textContent = 'Global Auto-Purchase state is unavailable.';
  if (!canAccess('view', 'config')) return;
  try {
    const config = await api.getConfig();
    if (request !== plansState.planAutomationRead) return;
    const enabled = config.global?.auto_purchase_enabled;
    if (enabled === true) banner.textContent = 'Global Auto-Purchase is enabled. Automatic plans still require a per-step cap and purchase permission.';
    if (enabled === false) banner.textContent = 'Global Auto-Purchase is off. Automatic plans will not execute until it is enabled.';
  } catch {
    if (request === plansState.planAutomationRead) banner.textContent = 'Global Auto-Purchase state could not be loaded.';
  }
}
