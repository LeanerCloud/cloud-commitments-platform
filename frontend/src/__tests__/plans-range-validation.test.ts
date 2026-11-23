/**
 * Live range + integer-only validation on the five plan-creation numeric
 * inputs (issue #702).
 *
 * Covers wireRangeInput (via wirePlanRangeInputs) for:
 *   #plan-coverage       min=0  max=100
 *   #ramp-step-percent   min=1  max=100
 *   #ramp-interval-days  min=1  max=365  (max attr was previously missing)
 *   #plan-notify-days    min=1  max=30
 *
 * Each case fires a synthetic `input` or `blur` event and asserts on
 * aria-invalid + the sibling .field-error span. Scientific notation
 * (1e+30) must also be rejected because parseInt('1e+30') === 1
 * but the digits-only regex ^\d+$ rejects the exponent form.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockShowToast, buildPlanModalDOM, fire, errorEl } from './plans-range-validation-harness';
import { openNewPlanModal, closePlanModal, openAddPurchasesModal } from '../plans';
import { flushPromises } from './flush-promises';

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('plan numeric inputs: live range validation (#702)', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    buildPlanModalDOM();
    // openNewPlanModal calls wirePlanRangeInputs which wires all five inputs.
    openNewPlanModal();
  });

  // --- out-of-range values show inline error --------------------------------

  it.each([
    ['plan-coverage', '101', 'Must be a whole number between 0 and 100'],
    ['plan-coverage', '-1', 'Must be a whole number between 0 and 100'],
    ['ramp-step-percent', '0', 'Must be a whole number between 1 and 100'],
    ['ramp-step-percent', '101', 'Must be a whole number between 1 and 100'],
    ['ramp-interval-days', '0', 'Must be a whole number between 1 and 365'],
    ['ramp-interval-days', '366', 'Must be a whole number between 1 and 365'],
    ['plan-notify-days', '0', 'Must be a whole number between 1 and 30'],
    ['plan-notify-days', '31', 'Must be a whole number between 1 and 30'],
  ])('typing %s=%s shows inline error "%s"', (inputId, value, expectedMsg) => {
    const input = document.getElementById(inputId) as HTMLInputElement;
    input.value = value;
    fire(input, 'input');

    expect(input.getAttribute('aria-invalid')).toBe('true');
    const err = errorEl(inputId);
    expect(err).not.toBeNull();
    expect(err!.classList.contains('hidden')).toBe(false);
    expect(err!.textContent).toBe(expectedMsg);
  });

  // --- scientific notation rejected ----------------------------------------

  it('rejects scientific notation (1e+30) in ramp-interval-days', () => {
    const input = document.getElementById('ramp-interval-days') as HTMLInputElement;
    input.value = '1e+30';
    fire(input, 'input');

    expect(input.getAttribute('aria-invalid')).toBe('true');
    const err = errorEl('ramp-interval-days');
    expect(err).not.toBeNull();
    expect(err!.classList.contains('hidden')).toBe(false);
  });

  it('rejects scientific notation (1e+30) in plan-coverage', () => {
    const input = document.getElementById('plan-coverage') as HTMLInputElement;
    input.value = '1e+30';
    fire(input, 'input');

    expect(input.getAttribute('aria-invalid')).toBe('true');
  });

  it('rejects scientific notation (2e2) in ramp-step-percent', () => {
    const input = document.getElementById('ramp-step-percent') as HTMLInputElement;
    input.value = '2e2';
    fire(input, 'input');

    expect(input.getAttribute('aria-invalid')).toBe('true');
  });

  // --- valid values clear the error ----------------------------------------

  it.each([
    ['plan-coverage', '80'],
    ['plan-coverage', '0'],
    ['plan-coverage', '1'],
    ['plan-coverage', '100'],
    ['ramp-step-percent', '25'],
    ['ramp-interval-days', '7'],
    ['ramp-interval-days', '365'],
    ['ramp-interval-days', '1'],
    ['plan-notify-days', '3'],
    ['plan-notify-days', '1'],
    ['plan-notify-days', '30'],
  ])('valid value %s=%s clears aria-invalid and hides the error span', (inputId, value) => {
    const input = document.getElementById(inputId) as HTMLInputElement;
    // First trigger an error state.
    input.value = '9999';
    fire(input, 'input');
    expect(input.getAttribute('aria-invalid')).toBe('true');

    // Then type a valid value.
    input.value = value;
    fire(input, 'input');
    expect(input.getAttribute('aria-invalid')).toBeNull();
    const err = errorEl(inputId);
    expect(err).not.toBeNull();
    expect(err!.classList.contains('hidden')).toBe(true);
  });

  // --- empty field clears the error ----------------------------------------

  it('clearing the field removes aria-invalid and hides the error', () => {
    const input = document.getElementById('plan-notify-days') as HTMLInputElement;
    input.value = '999';
    fire(input, 'input');
    expect(input.getAttribute('aria-invalid')).toBe('true');

    input.value = '';
    fire(input, 'input');
    expect(input.getAttribute('aria-invalid')).toBeNull();
    expect(errorEl('plan-notify-days')!.classList.contains('hidden')).toBe(true);
  });

  // --- blur leaves the typed value alone ------------------------------------

  // Clicking Save blurs the field first, so a clamp would save a value the
  // user never typed (#523, #1534).
  it.each([
    ['plan-coverage', '-1'],
    ['plan-coverage', '150'],
    ['ramp-step-percent', '150'],
    ['ramp-step-percent', '0'],
    ['ramp-interval-days', '366'],
    ['plan-notify-days', '0'],
    ['plan-notify-days', '31'],
  ])('%s=%s is not rewritten on blur and keeps its error', (inputId, value) => {
    const input = document.getElementById(inputId) as HTMLInputElement;
    input.value = value;
    fire(input, 'input');
    fire(input, 'blur');

    expect(input.value).toBe(value);
    expect(input.getAttribute('aria-invalid')).toBe('true');
    expect(errorEl(inputId)!.classList.contains('hidden')).toBe(false);
  });

  it('non-integer value is NOT clamped on blur (error stays visible)', () => {
    const input = document.getElementById('plan-coverage') as HTMLInputElement;
    input.value = '1e+30';
    fire(input, 'input');
    expect(input.getAttribute('aria-invalid')).toBe('true');

    fire(input, 'blur');
    // Non-integer cannot be clamped; error persists so the user sees feedback.
    expect(input.value).toBe('1e+30');
    expect(input.getAttribute('aria-invalid')).toBe('true');
  });

  // --- idempotency: stale error UI is reconciled on reopen -----------------

  it('re-opening the modal reconciles stale error UI for valid default values', () => {
    // Trigger an error.
    const coverage = document.getElementById('plan-coverage') as HTMLInputElement;
    coverage.value = '999';
    fire(coverage, 'input');
    expect(coverage.getAttribute('aria-invalid')).toBe('true');

    // Reset the field to a valid value (simulating form reset) then reopen.
    coverage.value = '80';
    closePlanModal();
    openNewPlanModal();

    // The reopen re-dispatches 'input', which clears the stale error.
    expect(coverage.getAttribute('aria-invalid')).toBeNull();
    expect(errorEl('plan-coverage')!.classList.contains('hidden')).toBe(true);
  });

  // --- idempotency: re-opening does not stack duplicate error spans ---------

  it('re-opening the modal does not create duplicate error spans', () => {
    // Trigger validation to create the error spans.
    const coverage = document.getElementById('plan-coverage') as HTMLInputElement;
    coverage.value = '999';
    fire(coverage, 'input');

    // Close and re-open.
    closePlanModal();
    openNewPlanModal();

    // Exactly one error span per input — no duplicates.
    expect(document.querySelectorAll('#plan-coverage-range-error')).toHaveLength(1);
    expect(document.querySelectorAll('#ramp-interval-days-range-error')).toHaveLength(1);
    expect(document.querySelectorAll('#plan-notify-days-range-error')).toHaveLength(1);
    expect(document.querySelectorAll('#ramp-step-percent-range-error')).toHaveLength(1);
  });

  // --- aria attributes are set correctly ------------------------------------

  it('error span has role=status and aria-live=polite', () => {
    const input = document.getElementById('plan-coverage') as HTMLInputElement;
    input.value = '999';
    fire(input, 'input');

    const err = errorEl('plan-coverage');
    expect(err).not.toBeNull();
    expect(err!.getAttribute('role')).toBe('status');
    expect(err!.getAttribute('aria-live')).toBe('polite');
  });

  it('input aria-describedby references the error span id', () => {
    const input = document.getElementById('plan-coverage') as HTMLInputElement;
    input.value = '999';
    fire(input, 'input');

    const describedBy = input.getAttribute('aria-describedby') ?? '';
    expect(describedBy).toContain('plan-coverage-range-error');
  });
});

// ---------------------------------------------------------------------------
// Hidden custom-ramp inputs must not block Save (#1536)
// ---------------------------------------------------------------------------

describe('hidden custom ramp inputs stay out of validation (#1536)', () => {
  let savePlan: typeof import('../plans').savePlan;

  beforeEach(() => {
    jest.clearAllMocks();
    // The ramp handlers install once per module, so a rebuilt DOM needs a fresh module.
    jest.resetModules();
    // eslint-disable-next-line @typescript-eslint/no-require-imports
    const fresh = require('../plans') as typeof import('../plans');
    savePlan = fresh.savePlan;
    buildPlanModalDOM();
    fresh.openNewPlanModal();
  });

  const stepInput = (): HTMLInputElement => document.getElementById('ramp-step-percent') as HTMLInputElement;
  const intervalInput = (): HTMLInputElement => document.getElementById('ramp-interval-days') as HTMLInputElement;
  const form = (): HTMLFormElement => document.getElementById('plan-form') as HTMLFormElement;

  function pickRamp(value: string): void {
    const radio = document.querySelector<HTMLInputElement>(`input[name="ramp-schedule"][value="${value}"]`)!;
    radio.checked = true;
    radio.dispatchEvent(new Event('change', { bubbles: true }));
  }

  it('opens with the hidden custom inputs disabled', () => {
    expect(stepInput().disabled).toBe(true);
    expect(intervalInput().disabled).toBe(true);
  });

  it.each([
    [['immediate']],
    [['weekly-25pct', 'immediate']],
    [['custom', 'immediate']],
  ])('after %j the hidden inputs are disabled and the form passes checkValidity', (steps) => {
    for (const step of steps) pickRamp(step);
    expect(document.getElementById('custom-ramp-config')!.classList.contains('hidden')).toBe(true);
    expect(stepInput().disabled).toBe(true);
    expect(intervalInput().disabled).toBe(true);
    expect(form().checkValidity()).toBe(true);
  });

  it('selecting Custom re-enables the inputs and an out-of-range value is still refused', async () => {
    pickRamp('immediate');
    pickRamp('custom');
    expect(stepInput().disabled).toBe(false);
    expect(intervalInput().disabled).toBe(false);

    (document.getElementById('plan-coverage') as HTMLInputElement).value = '80';
    (document.getElementById('plan-notify-days') as HTMLInputElement).value = '3';
    intervalInput().value = '0';
    expect(form().checkValidity()).toBe(false);
    await savePlan(new Event('submit'));
    expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
      message: 'Ramp Interval Days must be a whole number between 1 and 365',
    }));
  });
});

// ---------------------------------------------------------------------------
// Add Purchases modal: inline range validation on Number of Purchases (#771)
// ---------------------------------------------------------------------------

/**
 * Wire live range validation on the "Number of Purchases" field
 * in the Add Purchases modal (issue #771).
 *
 * The field has min=1 max=52 (weekly cadence cap). Mirrors the same
 * wireRangeInput pattern as the plan-creation modal (#702/#714):
 *   - aria-invalid + sibling .field-error on out-of-range / non-integer
 *     input / blur
 *   - Submit button is disabled while the field is invalid
 *   - Save-time guard uses Number() + Number.isInteger() (not parseInt)
 *     so fractional values like 2.5 are rejected before reaching the API
 */
describe('Add Purchases modal: inline range validation (#771)', () => {
  beforeEach(async () => {
    jest.clearAllMocks();
    // Provide a minimal body with the plans-list container that
    // openAddPurchasesModal's DOM-removal path expects.
    const plansList = document.createElement('div');
    plansList.id = 'plans-list';
    const ppList = document.createElement('div');
    ppList.id = 'planned-purchases-list';
    document.body.replaceChildren(plansList, ppList);

    await openAddPurchasesModal('plan-xyz', 'My Test Plan', 3);
  });

  function countInput(): HTMLInputElement {
    return document.getElementById('add-purchases-count') as HTMLInputElement;
  }

  function submitBtn(): HTMLButtonElement {
    return document.querySelector<HTMLButtonElement>('#add-purchases-modal button[type="submit"]') as HTMLButtonElement;
  }

  function errorEl(): HTMLElement | null {
    return document.getElementById('add-purchases-count-range-error');
  }

  // --- out-of-range values show inline error --------------------------------

  it('typing 0 (below min) sets aria-invalid and shows the error span', () => {
    const input = countInput();
    input.value = '0';
    input.dispatchEvent(new Event('input'));

    expect(input.getAttribute('aria-invalid')).toBe('true');
    const err = errorEl();
    expect(err).not.toBeNull();
    expect(err!.classList.contains('hidden')).toBe(false);
    expect(err!.textContent).toBe('Must be a whole number between 1 and 52');
  });

  it('typing 53 (above max) sets aria-invalid and shows the error span', () => {
    const input = countInput();
    input.value = '53';
    input.dispatchEvent(new Event('input'));

    expect(input.getAttribute('aria-invalid')).toBe('true');
    const err = errorEl();
    expect(err).not.toBeNull();
    expect(err!.classList.contains('hidden')).toBe(false);
    expect(err!.textContent).toBe('Must be a whole number between 1 and 52');
  });

  // --- submit button disabled while field is invalid -----------------------

  it('submit button is disabled when value is 0', () => {
    const input = countInput();
    input.value = '0';
    input.dispatchEvent(new Event('input'));

    expect(submitBtn().disabled).toBe(true);
  });

  it('submit button is disabled when value is 53', () => {
    const input = countInput();
    input.value = '53';
    input.dispatchEvent(new Event('input'));

    expect(submitBtn().disabled).toBe(true);
  });

  it('submit button is re-enabled when value comes back in range', () => {
    const input = countInput();
    input.value = '0';
    input.dispatchEvent(new Event('input'));
    expect(submitBtn().disabled).toBe(true);

    input.value = '4';
    input.dispatchEvent(new Event('input'));
    expect(submitBtn().disabled).toBe(false);
  });

  // --- valid boundary values clear the error --------------------------------

  it.each([['1'], ['26'], ['52']])(
    'valid value %s clears aria-invalid and hides the error span',
    (value) => {
      const input = countInput();
      // First trigger an error.
      input.value = '0';
      input.dispatchEvent(new Event('input'));
      expect(input.getAttribute('aria-invalid')).toBe('true');

      input.value = value;
      input.dispatchEvent(new Event('input'));
      expect(input.getAttribute('aria-invalid')).toBeNull();
      const err = errorEl();
      expect(err).not.toBeNull();
      expect(err!.classList.contains('hidden')).toBe(true);
    },
  );

  // --- fractional input rejected (feedback_strict_int_parse) ---------------

  it('fractional value 2.5 is rejected as non-integer', () => {
    const input = countInput();
    input.value = '2.5';
    input.dispatchEvent(new Event('input'));

    expect(input.getAttribute('aria-invalid')).toBe('true');
    expect(errorEl()!.classList.contains('hidden')).toBe(false);
  });

  // --- error span a11y attributes ------------------------------------------

  it('error span has role=status and aria-live=polite', () => {
    const input = countInput();
    input.value = '0';
    input.dispatchEvent(new Event('input'));

    const err = errorEl();
    expect(err).not.toBeNull();
    expect(err!.getAttribute('role')).toBe('status');
    expect(err!.getAttribute('aria-live')).toBe('polite');
  });

  it('input aria-describedby references the error span id', () => {
    const input = countInput();
    input.value = '0';
    input.dispatchEvent(new Event('input'));

    expect(input.getAttribute('aria-describedby')).toContain('add-purchases-count-range-error');
  });

  it.each(['0', '53'])('count=%s is not rewritten on blur and keeps the submit button disabled', value => {
    const input = countInput();
    input.value = value;
    input.dispatchEvent(new Event('input'));
    input.dispatchEvent(new Event('blur'));

    expect(input.value).toBe(value);
    expect(input.getAttribute('aria-invalid')).toBe('true');
    expect(errorEl()!.classList.contains('hidden')).toBe(false);
    expect(submitBtn().disabled).toBe(true);
  });

  // --- save-time guard prevents API call on out-of-range value -------------

  it('submitting count=0 is blocked by save-time guard without calling the API', async () => {
    (require('../api').createPlannedPurchases as jest.Mock).mockResolvedValue({});

    const input = countInput();
    input.value = '0';
    input.dispatchEvent(new Event('input'));

    const form = document.getElementById('add-purchases-form') as HTMLFormElement;
    form.dispatchEvent(new Event('submit'));

    await flushPromises();

    expect(require('../api').createPlannedPurchases).not.toHaveBeenCalled();
    const errDiv = document.getElementById('add-purchases-error');
    expect(errDiv?.classList.contains('hidden')).toBe(false);
    expect(errDiv?.textContent).toMatch(/whole number between 1 and 52/i);
  });

  it('submitting count=53 is blocked by save-time guard without calling the API', async () => {
    (require('../api').createPlannedPurchases as jest.Mock).mockResolvedValue({});

    const input = countInput();
    input.value = '53';
    input.dispatchEvent(new Event('input'));

    const form = document.getElementById('add-purchases-form') as HTMLFormElement;
    form.dispatchEvent(new Event('submit'));

    await flushPromises();

    expect(require('../api').createPlannedPurchases).not.toHaveBeenCalled();
  });
});
