/**
 * Plans module tests: plan modal open and close tests.
 * Split from plans.test.ts to stay under the 500-line ceiling; the module
 * mocks live in plans-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import {
  mockConfirmDialog,
  mockOpenArcheraOfferModal,
  mockShowToast,
} from './plans-harness';
import {
  loadPlans,
  savePlan,
  closePlanModal,
  openCreatePlanModal,
  openNewPlanModal,
  closePurchaseModal,
  setupPlanHandlers,
  _resetRampHandlersForTest,
} from '../plans';

import * as api from '../api';
import * as state from '../state';
import { flushPromises } from './flush-promises';

describe('Plans Module', () => {
  beforeEach(() => {
    mockOpenArcheraOfferModal.mockClear();
    // Reset DOM with full form structure
    document.body.innerHTML = `
      <div id="plans-list"></div>
      <div id="planned-purchases-list"></div>
      <div id="plan-modal" class="hidden">
        <h3 id="plan-modal-title">Create Plan</h3>
        <form id="plan-form">
          <input type="hidden" id="plan-id">
          <input type="text" id="plan-name">
          <textarea id="plan-description"></textarea>
          <p id="plan-selection-groups-intro" hidden></p>
          <ul id="plan-selection-groups" hidden></ul>
          <div id="plan-service-fields">
          <select id="plan-provider">
            <option value="aws">AWS</option>
            <option value="azure">Azure</option>
            <option value="gcp">GCP</option>
          </select>
          <select id="plan-service">
            <optgroup label="AWS Services" data-provider="aws">
              <option value="ec2">EC2</option>
              <option value="rds">RDS</option>
            </optgroup>
            <!-- Issue #241: mirrors the real markup's AWS Savings Plans
                 optgroup, whose human-readable label ("Savings Plans") does
                 not contain "aws" -- only the data-provider attribute ties
                 it to the AWS provider. -->
            <optgroup label="Savings Plans" data-provider="aws">
              <option value="savings-plans-compute">Compute SP</option>
            </optgroup>
            <optgroup label="Azure Services" data-provider="azure">
              <option value="compute">Compute</option>
            </optgroup>
            <optgroup label="GCP Services" data-provider="gcp">
              <option value="compute">Compute</option>
            </optgroup>
          </select>
          <select id="plan-term">
            <option value="1">1 Year</option>
            <option value="3">3 Years</option>
          </select>
          <select id="plan-payment">
            <option value="no-upfront">No Upfront</option>
            <option value="partial-upfront">Partial Upfront</option>
            <option value="all-upfront">All Upfront</option>
          </select>
          </div>
          <input type="number" id="plan-coverage" value="80">
          <input type="checkbox" id="plan-auto-purchase">
          <input type="text" id="plan-max-step-commitment">
          <p id="plan-auto-purchase-state"></p>
          <input type="number" id="plan-notify-days" value="3">
          <input type="checkbox" id="plan-enabled" checked>
          <input type="radio" name="ramp-schedule" value="immediate" checked>
          <input type="radio" name="ramp-schedule" value="weekly-25pct">
          <input type="radio" name="ramp-schedule" value="monthly-10pct">
          <input type="radio" name="ramp-schedule" value="custom">
          <div id="custom-ramp-config" class="hidden">
            <input type="number" id="ramp-step-percent" value="20">
            <input type="number" id="ramp-interval-days" value="7">
          </div>
          <!-- Target Accounts section (universal-plans fix). The hidden
               plan-account-ids field is the contract between renderPlan
               AccountChips and savePlan; the submit button's disabled
               state is recomputed every time the chip list changes. -->
          <div id="plan-accounts-selected" class="selected-accounts"></div>
          <input type="hidden" id="plan-account-ids" value="">
          <div id="plan-filters-notice" class="hidden"></div>
          <button type="submit">Save plan</button>
        </form>
      </div>
      <div id="purchase-modal" class="hidden"></div>
    `;

    jest.clearAllMocks();
    window.alert = jest.fn();
    window.confirm = jest.fn().mockReturnValue(true);
    // Default confirmDialog to confirmed so tests that don't override it
    // behave like the old window.confirm() returning true (finding 11-L2).
    mockConfirmDialog.mockResolvedValue(true);
    // Reset ramp-handlers install-once guard: DOM is rebuilt each beforeEach,
    // so static modal elements are fresh and need listeners re-attached (H3 fix).
    _resetRampHandlersForTest();
  });

  describe('savePlan', () => {
    beforeEach(() => {
      closePlanModal(); // drop a selection or bound-edit state a previous test left open
      // Set up form values
      (document.getElementById('plan-name') as HTMLInputElement).value = 'New Plan';
      (document.getElementById('plan-description') as HTMLTextAreaElement).value = 'Test description';
      (document.getElementById('plan-provider') as HTMLSelectElement).value = 'aws';
      (document.getElementById('plan-service') as HTMLSelectElement).value = 'ec2';
      (document.getElementById('plan-term') as HTMLSelectElement).value = '1';
      (document.getElementById('plan-payment') as HTMLSelectElement).value = 'all-upfront';
      (document.getElementById('plan-coverage') as HTMLInputElement).value = '80';
      (document.getElementById('plan-auto-purchase') as HTMLInputElement).checked = true;
      (document.getElementById('plan-max-step-commitment') as HTMLInputElement).value = '100.10';
      (document.getElementById('plan-notify-days') as HTMLInputElement).value = '3';
      (document.getElementById('plan-enabled') as HTMLInputElement).checked = true;
      // Universal-plans fix: savePlan rejects an empty Target Accounts list,
      // so default the hidden field to a single account UUID for every test
      // in this block. Tests that exercise the empty-accounts rejection set
      // it back to '' explicitly inside the test.
      (document.getElementById('plan-account-ids') as HTMLInputElement).value = '11111111-1111-1111-1111-111111111111';
    });

    // -------------------------------------------------------------------------
    // Regression tests for finding 11-M1: plan numeric fields must be validated
    // with Number.isInteger(Number(raw)) before being sent to the API.
    // -------------------------------------------------------------------------
    describe('11-M1: strict integer validation for plan numeric fields', () => {
      test('rejects fractional target_coverage (2.5 should not truncate to 2)', async () => {
        (document.getElementById('plan-coverage') as HTMLInputElement).value = '2.5';
        const event = { preventDefault: jest.fn() } as unknown as Event;
        await savePlan(event);
        expect(api.createPlan).not.toHaveBeenCalled();
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'error' }));
      });

      test('rejects an empty target_coverage instead of sending 0 (#523)', async () => {
        (document.getElementById('plan-coverage') as HTMLInputElement).value = '';
        const event = { preventDefault: jest.fn() } as unknown as Event;
        await savePlan(event);
        expect(api.createPlan).not.toHaveBeenCalled();
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
          message: 'Target Coverage must be a whole number between 0 and 100',
          kind: 'error',
        }));
      });

      test.each(['-1', '101', '2.5'])('rejects target_coverage %s (#523)', async (value) => {
        (document.getElementById('plan-coverage') as HTMLInputElement).value = value;
        const event = { preventDefault: jest.fn() } as unknown as Event;
        await savePlan(event);
        expect(api.createPlan).not.toHaveBeenCalled();
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
          message: 'Target Coverage must be a whole number between 0 and 100',
        }));
      });

      test('sends target_coverage 0 as 0, not 80 (#523)', async () => {
        (api.createPlan as jest.Mock).mockResolvedValue({});
        (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
        (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
        (document.getElementById('plan-coverage') as HTMLInputElement).value = '0';
        const event = { preventDefault: jest.fn() } as unknown as Event;
        await savePlan(event);
        expect(api.createPlan).toHaveBeenCalledWith(expect.objectContaining({ target_coverage: 0 }));
      });

      test('sends target_coverage 1 as 1 (#523)', async () => {
        (api.createPlan as jest.Mock).mockResolvedValue({});
        (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
        (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
        (document.getElementById('plan-coverage') as HTMLInputElement).value = '1';
        const event = { preventDefault: jest.fn() } as unknown as Event;
        await savePlan(event);
        expect(api.createPlan).toHaveBeenCalledWith(expect.objectContaining({ target_coverage: 1 }));
      });

      test('rejects NaN notification_days_before (e.g. empty string after clear)', async () => {
        (document.getElementById('plan-notify-days') as HTMLInputElement).value = 'abc';
        const event = { preventDefault: jest.fn() } as unknown as Event;
        await savePlan(event);
        expect(api.createPlan).not.toHaveBeenCalled();
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'error' }));
      });

      test('rejects out-of-range notification_days_before (0 is below min 1)', async () => {
        (document.getElementById('plan-notify-days') as HTMLInputElement).value = '0';
        const event = { preventDefault: jest.fn() } as unknown as Event;
        await savePlan(event);
        expect(api.createPlan).not.toHaveBeenCalled();
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'error' }));
      });

      test('accepts valid integer values and proceeds to API call', async () => {
        (api.createPlan as jest.Mock).mockResolvedValue({});
        (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
        (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
        (document.getElementById('plan-coverage') as HTMLInputElement).value = '80';
        (document.getElementById('plan-notify-days') as HTMLInputElement).value = '3';
        const event = { preventDefault: jest.fn() } as unknown as Event;
        await savePlan(event);
        expect(api.createPlan).toHaveBeenCalledWith(expect.objectContaining({
          target_coverage: 80,
          notification_days_before: 3,
        }));
      });

      test.each([
        ['ramp-step-percent', '150', 'Ramp Step Percent must be a whole number between 1 and 100'],
        ['ramp-step-percent', '0', 'Ramp Step Percent must be a whole number between 1 and 100'],
        ['ramp-interval-days', '366', 'Ramp Interval Days must be a whole number between 1 and 365'],
        ['ramp-interval-days', '0', 'Ramp Interval Days must be a whole number between 1 and 365'],
        ['plan-notify-days', '31', 'Notification Days must be a whole number between 1 and 30'],
        ['plan-notify-days', '0', 'Notification Days must be a whole number between 1 and 30'],
      ])('refuses %s=%s with a toast and sends nothing (#1534)', async (inputId, value, message) => {
        (document.querySelector('input[value="custom"]') as HTMLInputElement).checked = true;
        (document.getElementById('ramp-step-percent') as HTMLInputElement).value = '20';
        (document.getElementById('ramp-interval-days') as HTMLInputElement).value = '7';
        const field = document.getElementById(inputId) as HTMLInputElement;
        field.value = value;
        field.dispatchEvent(new Event('blur'));
        await savePlan({ preventDefault: jest.fn() } as unknown as Event);
        expect(field.value).toBe(value);
        expect(api.createPlan).not.toHaveBeenCalled();
        expect(api.updatePlan).not.toHaveBeenCalled();
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ message, kind: 'error' }));
      });

      test('rejects fractional custom_step_percent when ramp is custom', async () => {
        const customRadio = document.querySelector('input[value="custom"]') as HTMLInputElement;
        customRadio.checked = true;
        (document.getElementById('ramp-step-percent') as HTMLInputElement).value = '10.5';
        (document.getElementById('ramp-interval-days') as HTMLInputElement).value = '7';
        const event = { preventDefault: jest.fn() } as unknown as Event;
        await savePlan(event);
        expect(api.createPlan).not.toHaveBeenCalled();
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'error' }));
      });
    });
  });

  describe('closePlanModal', () => {
    test('adds hidden class to plan modal', () => {
      const modal = document.getElementById('plan-modal');
      modal?.classList.remove('hidden');

      closePlanModal();

      expect(modal?.classList.contains('hidden')).toBe(true);
    });

    test('handles missing modal element', () => {
      document.body.innerHTML = '';

      expect(() => closePlanModal()).not.toThrow();
    });
  });

  test.each([openNewPlanModal, openCreatePlanModal])('collapses Advanced per open and removes its native-invalid listener on close (%#)', open => {
    const form = document.getElementById('plan-form')!, advanced = document.createElement('details');
    advanced.id = 'plan-advanced-settings'; form.appendChild(advanced);
    const notify = document.getElementById('plan-notify-days')!; advanced.appendChild(notify);
    advanced.open = true; open(); expect(advanced.open).toBe(false);
    document.getElementById('plan-name')!.dispatchEvent(new Event('invalid')); expect(advanced.open).toBe(false);
    notify.dispatchEvent(new Event('invalid')); expect(advanced.open).toBe(true);
    closePlanModal(); advanced.open = false;
    notify.dispatchEvent(new Event('invalid')); expect(advanced.open).toBe(false);
    advanced.open = true; open(); expect(advanced.open).toBe(false); open(); closePlanModal();
    advanced.open = false; notify.dispatchEvent(new Event('invalid')); expect(advanced.open).toBe(false);
  });

  describe('openNewPlanModal', () => {
    test('opens modal without requiring selected recommendations', () => {
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());

      openNewPlanModal();

      const modal = document.getElementById('plan-modal');
      expect(modal?.classList.contains('hidden')).toBe(false);
    });

    test('sets correct title for new plan', () => {
      openNewPlanModal();

      const title = document.getElementById('plan-modal-title');
      expect(title?.textContent).toBe('New purchase plan');
    });

    test('resets form', () => {
      (document.getElementById('plan-name') as HTMLInputElement).value = 'Old Plan';

      openNewPlanModal();

      expect((document.getElementById('plan-id') as HTMLInputElement).value).toBe('');
    });

    test('sets up ramp schedule handlers', () => {
      openNewPlanModal();

      // Verify handlers are attached by triggering change
      const rampRadio = document.querySelector('input[value="custom"]') as HTMLInputElement;
      rampRadio.dispatchEvent(new Event('change'));

      const customConfig = document.getElementById('custom-ramp-config');
      expect(customConfig?.classList.contains('hidden')).toBe(false);
    });
  });

  describe('closePurchaseModal', () => {
    test('adds hidden class to purchase modal', () => {
      const modal = document.getElementById('purchase-modal');
      modal?.classList.remove('hidden');

      closePurchaseModal();

      expect(modal?.classList.contains('hidden')).toBe(true);
    });
  });

  describe('setupPlanHandlers', () => {
    test('sets up provider change handler', () => {
      setupPlanHandlers();

      const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement;
      providerSelect.value = 'azure';
      providerSelect.dispatchEvent(new Event('change'));

      // Should hide + disable the AWS services optgroup. Previously this
      // test asserted on inline style.display — that was indicator-only
      // and missed the real bug (issue #11: the hidden class stayed
      // baked in, so switching away from AWS never re-showed the target
      // provider's services). The class + disabled state is the new
      // source of truth.
      const serviceSelect = document.getElementById('plan-service') as HTMLSelectElement;
      const awsOptgroup = serviceSelect.querySelector('optgroup[label="AWS Services"]') as HTMLOptGroupElement;
      expect(awsOptgroup.classList.contains('hidden')).toBe(true);
      expect(awsOptgroup.disabled).toBe(true);
    });

    test('handles missing elements gracefully', () => {
      document.body.innerHTML = '';

      expect(() => setupPlanHandlers()).not.toThrow();
    });
  });

  // H-5 regression: payment option must not be auto-selected when absent.
  // Pre-fix: `firstService?.payment || 'no-upfront'` pre-selected 'no-upfront'
  // when payment was missing, silently changing the plan on re-save.
  // These tests must FAIL on pre-fix code.
  describe('H-5: absent payment leaves select blank (no no-upfront default)', () => {
    const planBase = {
      id: 'plan-1',
      name: 'Test Plan',
      enabled: true,
      auto_purchase: false,
      notification_days_before: 3,
      ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0 }
    };

    test('absent payment field leaves payment select value empty in edit modal', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{
          id: 'plan-1', name: 'Test Plan', enabled: true, auto_purchase: false,
          services: { ec2: { provider: 'aws', service: 'ec2', term: 1, coverage: 80 } },
          ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0, current_step: 0, total_steps: 1 },
        }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      (api.getPlan as jest.Mock).mockResolvedValue({
        ...planBase,
        services: {
          ec2: {
            provider: 'aws',
            service: 'ec2',
            term: 1,
            coverage: 80,
            // payment deliberately absent
          }
        }
      });

      await loadPlans();
      const editBtn = document.querySelector('[data-action="edit-plan"]') as HTMLButtonElement;
      editBtn?.click();
      await flushPromises();

      const paymentSelect = document.getElementById('plan-payment') as HTMLSelectElement;
      // Pre-fix: paymentSelect.value === 'no-upfront' (hardcoded default)
      // Post-fix: paymentSelect.value === '' (unset, user must choose)
      expect(paymentSelect.value).not.toBe('no-upfront');
      expect(paymentSelect.value).toBe('');
    });

    test('present payment field is correctly pre-selected in edit modal', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{
          id: 'plan-1', name: 'Test Plan', enabled: true, auto_purchase: false,
          services: { ec2: { provider: 'aws', service: 'ec2', term: 1, payment: 'all-upfront', coverage: 80 } },
          ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0, current_step: 0, total_steps: 1 },
        }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      (api.getPlan as jest.Mock).mockResolvedValue({
        ...planBase,
        services: {
          ec2: { provider: 'aws', service: 'ec2', term: 1, payment: 'all-upfront', coverage: 80 }
        }
      });

      await loadPlans();
      const editBtn = document.querySelector('[data-action="edit-plan"]') as HTMLButtonElement;
      editBtn?.click();
      await flushPromises();

      const paymentSelect = document.getElementById('plan-payment') as HTMLSelectElement;
      // When payment IS present, it should be correctly pre-selected
      expect(paymentSelect.value).toBe('all-upfront');
    });
  });

  describe('openCreatePlanModal', () => {
    test('opens modal with "New Purchase Plan" title when selection is empty (issue #17)', () => {
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());

      openCreatePlanModal();

      const modal = document.getElementById('plan-modal');
      expect(modal?.classList.contains('hidden')).toBe(false);
      const title = document.getElementById('plan-modal-title');
      expect(title?.textContent).toBe('New purchase plan');
      // Previously we early-returned behind a toast that users missed
      // (e.g. after a provider-filter switch). Falling through to the
      // new-plan flow never silently noop-s.
      expect(mockShowToast).not.toHaveBeenCalled();
    });

    test('opens modal when recommendations are selected', () => {
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set([0]));

      openCreatePlanModal();

      const modal = document.getElementById('plan-modal');
      expect(modal?.classList.contains('hidden')).toBe(false);
    });

    test('sets "Create Purchase Plan" title when a snapshot is passed', () => {
      // #273 CR follow-up: title now keys off the snapshot the caller
      // passes (the resolved-target captured at button-click time)
      // rather than the live selection state, which would otherwise be
      // racy between modal-open and modal-render.
      const snapshot = [
        { id: 'rec-x', service: 'ec2' },
      ] as unknown as readonly api.Recommendation[];
      openCreatePlanModal(snapshot);

      const title = document.getElementById('plan-modal-title');
      expect(title?.textContent).toBe('Create purchase plan');
    });

    test('clears plan ID for new plan', () => {
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set([0]));
      (document.getElementById('plan-id') as HTMLInputElement).value = 'existing-plan';

      openCreatePlanModal();

      expect((document.getElementById('plan-id') as HTMLInputElement).value).toBe('');
    });
  });
});
