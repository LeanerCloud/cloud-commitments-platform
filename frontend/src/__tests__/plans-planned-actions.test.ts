/**
 * Plans module tests: planned purchase action tests.
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
  _resetRampHandlersForTest,
} from '../plans';

import * as api from '../api';
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

  describe('planned purchase actions', () => {
    beforeEach(async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({
        purchases: [
          {
            id: 'purchase-1',
            plan_id: 'plan-1',
            plan_name: 'Test Plan',
            scheduled_date: '2024-02-20',
            provider: 'aws',
            service: 'ec2',
            resource_type: 't3.medium',
            region: 'us-east-1',
            count: 5,
            term: 1,
            payment: 'all-upfront',
            upfront_cost: 1000,
            estimated_savings: 200,
            status: 'pending',
            step_number: 1,
            total_steps: 4
          }
        ]
      });
      await loadPlans();
    });

    test('run action executes purchase with confirmation', async () => {
      (api.runPlannedPurchase as jest.Mock).mockResolvedValue({});
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      // confirm() replaced by confirmDialog() (11-L2); control via mockConfirmDialog.
      mockConfirmDialog.mockResolvedValue(true);

      const runBtn = document.querySelector('[data-action="run"]') as HTMLButtonElement;
      runBtn?.click();

      await flushPromises();

      expect(mockConfirmDialog).toHaveBeenCalled();
      expect(api.runPlannedPurchase).toHaveBeenCalledWith('purchase-1');
      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'Purchase executed successfully' }));
    });

    test('run action cancelled by user', async () => {
      // confirm() replaced by confirmDialog() (11-L2); control via mockConfirmDialog.
      mockConfirmDialog.mockResolvedValue(false);

      const runBtn = document.querySelector('[data-action="run"]') as HTMLButtonElement;
      runBtn?.click();

      await flushPromises();

      expect(api.runPlannedPurchase).not.toHaveBeenCalled();
    });

    test('run confirm names the step and shows the amount charged today', async () => {
      mockConfirmDialog.mockClear();
      mockConfirmDialog.mockResolvedValue(false);

      (document.querySelector('[data-action="run"]') as HTMLButtonElement).click();
      await flushPromises();

      const opts = mockConfirmDialog.mock.calls[0]![0] as { title: string; body: HTMLElement };
      expect(opts.title).toBe('Run step 1/4 of "Test Plan" now?');
      expect(opts.body.textContent).toMatch(/Charged today\$1,?000/);
      expect(opts.body.textContent).toContain('5 \u00d7 EC2 t3.medium');
    });

    test('a second click while the run confirm is open does not stack a second dialog', async () => {
      mockConfirmDialog.mockClear();
      let resolveDialog: (v: boolean) => void = () => undefined;
      mockConfirmDialog.mockReturnValue(new Promise<boolean>(r => { resolveDialog = r; }));

      const runBtn = document.querySelector('[data-action="run"]') as HTMLButtonElement;
      runBtn.click();
      runBtn.click();
      await flushPromises();
      expect(mockConfirmDialog).toHaveBeenCalledTimes(1);

      resolveDialog(false);
      await flushPromises();
    });

    test('pause action pauses purchase', async () => {
      (api.pausePlannedPurchase as jest.Mock).mockResolvedValue({});
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      const pauseBtn = document.querySelector('[data-action="pause"]') as HTMLButtonElement;
      pauseBtn?.click();

      await flushPromises();

      expect(api.pausePlannedPurchase).toHaveBeenCalledWith('purchase-1');
      // Pause must give visible feedback via a success toast.
      expect(mockShowToast).toHaveBeenCalledWith(
        expect.objectContaining({ message: 'Purchase paused', kind: 'success' }));
    });

    test('disable action deletes planned purchase with confirmation', async () => {
      (api.deletePlannedPurchase as jest.Mock).mockResolvedValue({});
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      // confirm() replaced by confirmDialog() (11-L2); control via mockConfirmDialog.
      mockConfirmDialog.mockResolvedValue(true);

      // Clear prior getPlans calls from setup so the assertion below only
      // counts the reload triggered by the disable action itself.
      (api.getPlans as jest.Mock).mockClear();

      const disableBtn = document.querySelector('[data-action="disable"]') as HTMLButtonElement;
      disableBtn?.click();

      await flushPromises();

      // The action cancels this scheduled purchase AND disables its plan
      // (backend deletePlannedPurchase), so the dialog must say both.
      expect(mockConfirmDialog).toHaveBeenCalledWith(expect.objectContaining({
        title: 'Cancel this scheduled purchase?',
        body: expect.stringContaining('plan will be disabled'),
      }));
      expect(api.deletePlannedPurchase).toHaveBeenCalledWith('purchase-1');
      // Issue #774: after disable the Plans page must refresh so the toggle
      // reflects the backend's new enabled=false. getPlans is the API call
      // that loadPlans() fires to repopulate the Plans list.
      expect(api.getPlans).toHaveBeenCalledTimes(1);
    });
  });
});
