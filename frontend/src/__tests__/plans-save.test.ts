/**
 * Plans module tests: savePlan tests.
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
  savePlan,
  closePlanModal,
  _resetRampHandlersForTest,
} from '../plans';

import * as api from '../api';

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

    describe('save ordering and outcomes (#1057)', () => {
      const PARTIAL = 'The plan settings were saved, but the account change failed: accounts boom. Review the target accounts and save again.';
      const evt = () => ({ preventDefault: jest.fn() } as unknown as Event);
      const openModal = () => {
        const modal = document.getElementById('plan-modal');
        modal?.classList.remove('hidden');
        return modal;
      };
      const useUpdate = () => {
        (document.getElementById('plan-id') as HTMLInputElement).value = 'plan-123';
        (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
        (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
        console.error = jest.fn();
      };

      test('updatePlan failure keeps the modal open and never writes accounts', async () => {
        useUpdate();
        (api.updatePlan as jest.Mock).mockRejectedValue(new Error('update boom'));
        const modal = openModal();
        await savePlan(evt());
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
          kind: 'error', message: 'Failed to save plan: update boom',
        }));
        expect(api.setPlanAccounts).not.toHaveBeenCalled();
        expect(modal?.classList.contains('hidden')).toBe(false);
      });

      test('create path closes the modal and does not call setPlanAccounts', async () => {
        (api.createPlan as jest.Mock).mockResolvedValue({});
        (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
        (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
        const modal = openModal();
        await savePlan(evt());
        expect(api.createPlan).toHaveBeenCalledTimes(1);
        expect(api.setPlanAccounts).not.toHaveBeenCalled();
        expect(modal?.classList.contains('hidden')).toBe(true);
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'Plan created successfully', kind: 'success' }));
      });

      test('createPlan failure keeps the modal open with the failure toast', async () => {
        (api.createPlan as jest.Mock).mockRejectedValue(new Error('create boom'));
        console.error = jest.fn();
        const modal = openModal();
        await savePlan(evt());
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
          kind: 'error', message: 'Failed to save plan: create boom',
        }));
        expect(api.setPlanAccounts).not.toHaveBeenCalled();
        expect(modal?.classList.contains('hidden')).toBe(false);
      });

      test('saving again after a partial outcome updates the same plan, never creates', async () => {
        useUpdate();
        (api.updatePlan as jest.Mock).mockResolvedValue({});
        (api.setPlanAccounts as jest.Mock).mockRejectedValueOnce(new Error('accounts boom')).mockResolvedValue(undefined);
        openModal();
        await savePlan(evt());
        await savePlan(evt());
        expect(api.updatePlan).toHaveBeenCalledTimes(2);
        expect((api.updatePlan as jest.Mock).mock.calls[1][0]).toBe('plan-123');
        expect(api.setPlanAccounts).toHaveBeenCalledTimes(2);
        expect(api.createPlan).not.toHaveBeenCalled();
      });

      test('a failing plans reload keeps the partial toast and adds no failure toast', async () => {
        useUpdate();
        (api.updatePlan as jest.Mock).mockResolvedValue({});
        (api.setPlanAccounts as jest.Mock).mockRejectedValue(new Error('accounts boom'));
        (api.getPlans as jest.Mock).mockRejectedValue(new Error('reload boom'));
        openModal();
        await savePlan(evt());
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'error', message: PARTIAL }));
        const messages = mockShowToast.mock.calls.map(c => (c[0] as { message: string }).message);
        expect(messages.some(m => m.startsWith('Failed to save plan'))).toBe(false);
      });
    });

    test('includes custom ramp settings when custom schedule selected', async () => {
      (api.createPlan as jest.Mock).mockResolvedValue({});
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      const customRadio = document.querySelector('input[value="custom"]') as HTMLInputElement;
      customRadio.checked = true;
      (document.getElementById('ramp-step-percent') as HTMLInputElement).value = '25';
      (document.getElementById('ramp-interval-days') as HTMLInputElement).value = '14';

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await savePlan(event);

      expect(api.createPlan).toHaveBeenCalledWith(expect.objectContaining({
        custom_step_percent: 25,
        custom_interval_days: 14
      }));
    });
  });
});
