import { populateTermSelect, populatePaymentSelect, isValidCombination } from '../commitmentOptions';
import { plansState } from './plansState';
import { renderPlanAccountChips, updatePlanAccountIdsField } from './planAccounts';
import { updatePlanNameFromSchedule } from './planRanges';

// Install-once guard for setupRampScheduleHandlers. The elements it binds are
// static modal singletons that are never replaced, so adding listeners on
// every modal open stacks duplicate handlers. The flag ensures we bind once;
// reset to false only in tests via resetRampHandlersForTest().
let rampHandlersInstalled = false;

/**
 * Set up event handlers for ramp schedule changes.
 * Guarded by rampHandlersInstalled so re-opening the plan modal does not
 * stack duplicate listeners on the static modal elements (H3, feedback_event_listener_dedup).
 */
export function setupRampScheduleHandlers(): void {
  if (rampHandlersInstalled) return;
  rampHandlersInstalled = true;

  // Listen to ramp schedule radio changes
  document.querySelectorAll<HTMLInputElement>('input[name="ramp-schedule"]').forEach(radio => {
    radio.addEventListener('change', () => {
      // Update custom config fields based on selected preset
      updateCustomConfigFromPreset(radio.value);

      updatePlanNameFromSchedule();

      // Show/hide custom config
      setCustomRampConfigVisible(radio.value === 'custom');
    });
  });

  // Listen to custom schedule field changes
  const stepPercentInput = document.getElementById('ramp-step-percent');
  const intervalDaysInput = document.getElementById('ramp-interval-days');

  stepPercentInput?.addEventListener('input', updatePlanNameFromSchedule);
  intervalDaysInput?.addEventListener('input', updatePlanNameFromSchedule);

  // Listen to provider/service changes to update payment/term options
  const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement | null;
  const serviceSelect = document.getElementById('plan-service') as HTMLSelectElement | null;
  const termSelect = document.getElementById('plan-term') as HTMLSelectElement | null;
  const paymentSelect = document.getElementById('plan-payment') as HTMLSelectElement | null;

  providerSelect?.addEventListener('change', () => {
    updateCommitmentOptions();
    updatePlanNameFromSchedule();
    // Clear all selected accounts when the provider changes. Selected account
    // entries only carry id/name/external_id (no provider field), so we cannot
    // filter by provider directly. Clearing on change is the safe default:
    // an account valid for the old provider is almost never valid for the new
    // one, and it prevents a cross-provider assignment from silently reaching
    // the backend validator.
    plansState.planSelectedAccounts = [];
    renderPlanAccountChips();
    updatePlanAccountIdsField();
    // Clear and hide any open account suggestion dropdown so stale suggestions
    // from the previous provider cannot be clicked and add a mismatched account.
    const suggestions = document.getElementById('plan-account-suggestions') as HTMLElement | null;
    if (suggestions) {
      suggestions.textContent = '';
      suggestions.classList.add('hidden');
    }
    // Re-enable/disable the account search input to match the new provider state.
    const accountSearchInput = document.getElementById('plan-account-search') as HTMLInputElement | null;
    if (accountSearchInput) {
      accountSearchInput.value = '';
      accountSearchInput.disabled = !providerSelect.value;
    }
  });

  serviceSelect?.addEventListener('change', () => {
    updateCommitmentOptions();
    updatePlanNameFromSchedule();
  });

  termSelect?.addEventListener('change', () => {
    updatePaymentOptionsForTerm();
  });

  paymentSelect?.addEventListener('change', () => {
    updateTermOptionsForPayment();
  });

  // Initialize options based on default provider/service
  updateCommitmentOptions();
}

/**
 * Update term and payment options based on current provider/service selection
 */
function updateCommitmentOptions(): void {
  const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement | null;
  const serviceSelect = document.getElementById('plan-service') as HTMLSelectElement | null;
  const termSelect = document.getElementById('plan-term') as HTMLSelectElement | null;
  const paymentSelect = document.getElementById('plan-payment') as HTMLSelectElement | null;

  if (!providerSelect || !serviceSelect || !termSelect || !paymentSelect) return;

  const provider = providerSelect.value;
  const service = serviceSelect.value;

  // Populate both selects with provider/service specific options
  populateTermSelect(termSelect, provider, service);
  populatePaymentSelect(paymentSelect, provider, service);

  // Validate current selection
  validateAndFixCombination();
}

/**
 * Update payment options based on selected term
 */
function updatePaymentOptionsForTerm(): void {
  const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement | null;
  const serviceSelect = document.getElementById('plan-service') as HTMLSelectElement | null;
  const termSelect = document.getElementById('plan-term') as HTMLSelectElement | null;
  const paymentSelect = document.getElementById('plan-payment') as HTMLSelectElement | null;

  if (!providerSelect || !termSelect || !paymentSelect) return;

  const provider = providerSelect.value;
  const service = serviceSelect?.value;
  const term = parseInt(termSelect.value, 10);

  populatePaymentSelect(paymentSelect, provider, service, term);
}

/**
 * Update term options based on selected payment
 */
function updateTermOptionsForPayment(): void {
  const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement | null;
  const serviceSelect = document.getElementById('plan-service') as HTMLSelectElement | null;
  const termSelect = document.getElementById('plan-term') as HTMLSelectElement | null;
  const paymentSelect = document.getElementById('plan-payment') as HTMLSelectElement | null;

  if (!providerSelect || !termSelect || !paymentSelect) return;

  const provider = providerSelect.value;
  const service = serviceSelect?.value;
  const payment = paymentSelect.value;

  populateTermSelect(termSelect, provider, service, payment);
}

/**
 * Validate and fix invalid term/payment combinations
 */
function validateAndFixCombination(): void {
  const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement | null;
  const serviceSelect = document.getElementById('plan-service') as HTMLSelectElement | null;
  const termSelect = document.getElementById('plan-term') as HTMLSelectElement | null;
  const paymentSelect = document.getElementById('plan-payment') as HTMLSelectElement | null;

  if (!providerSelect || !termSelect || !paymentSelect) return;

  const provider = providerSelect.value;
  const service = serviceSelect?.value;
  const term = parseInt(termSelect.value, 10);
  const payment = paymentSelect.value;

  // Check if current combination is valid
  if (!isValidCombination(provider, service, term, payment)) {
    // Invalid combination - update payment to first valid option
    updatePaymentOptionsForTerm();
  }
}

export function syncCustomRampConfigToSelection(): void {
  const checked = document.querySelector<HTMLInputElement>('input[name="ramp-schedule"]:checked');
  setCustomRampConfigVisible(checked?.value === 'custom');
}

/**
 * Show or hide the custom ramp config. A hidden input is disabled so its value,
 * which a preset fills with 0 days, is skipped by native validation instead of
 * blocking Save on a control the user cannot reach (#1536).
 */
export function setCustomRampConfigVisible(visible: boolean): void {
  document.getElementById('custom-ramp-config')?.classList.toggle('hidden', !visible);
  for (const id of ['ramp-step-percent', 'ramp-interval-days']) {
    const input = document.getElementById(id) as HTMLInputElement | null;
    if (input) input.disabled = !visible;
  }
}

/**
 * Update custom config fields based on the selected ramp schedule preset
 */
function updateCustomConfigFromPreset(rampSchedule: string): void {
  const stepPercentInput = document.getElementById('ramp-step-percent') as HTMLInputElement;
  const intervalDaysInput = document.getElementById('ramp-interval-days') as HTMLInputElement;

  if (!stepPercentInput || !intervalDaysInput) return;

  switch (rampSchedule) {
    case 'immediate':
      stepPercentInput.value = '100';
      intervalDaysInput.value = '0';
      break;
    case 'weekly-25pct':
      stepPercentInput.value = '25';
      intervalDaysInput.value = '7';
      break;
    case 'monthly-10pct':
      stepPercentInput.value = '10';
      intervalDaysInput.value = '30';
      break;
    case 'custom':
      // Don't change values when switching to custom - let user modify
      break;
  }
}

/**
 * Reset the ramp-handlers install-once guard.
 * Exported for unit tests only: each test rebuilds the DOM so the static
 * modal elements are fresh; without this reset the guard prevents listener
 * re-attachment and tests that open the modal multiple times break.
 * Must NOT be called in production code.
 */
export function _resetRampHandlersForTest(): void {
  rampHandlersInstalled = false;
}
