/**
 * Plans module tests: loadPlannedPurchases tests.
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
  setupPlanHandlers,
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

  describe('loadPlannedPurchases', () => {
    describe('step estimate (#631)', () => {
      const step = {
        id: 'purchase-est', plan_id: 'plan-1', plan_name: 'Azure compute 80% weekly', scheduled_date: '2026-10-07',
        provider: 'azure', service: 'compute', resource_type: 'Standard_D2s_v3', region: 'westeurope', count: 4,
        term: 1, payment: 'monthly', upfront_cost: 50, estimated_savings: 10, has_recommendations: false,
        status: 'pending', step_number: 1, total_steps: 4
      };
      const render = async (row: Record<string, unknown>) => {
        (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
        (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [{ ...step, ...row }] });
        await loadPlans();
        return document.getElementById('planned-purchases-list')?.innerHTML ?? '';
      };

      test('shows the figures with an Estimate label and keeps Run now disabled', async () => {
        const html = await render({ estimate: true });
        expect(html).toContain('Standard_D2s_v3');
        expect(html).toContain('estimate-label');
        expect(html).toContain('the real figures are fixed when the step runs');
        expect(html).not.toContain('No recommendations attached');
        expect(document.querySelector('tr.planned-purchase-row td.num')?.textContent?.trim()).toBe('4');
        expect(document.querySelector('[data-action="run"]')?.getAttribute('aria-disabled')).toBe('true');
      });

      test('shows the note instead of the placeholder text, with no figures', async () => {
        const html = await render({ estimate_note: 'sized when step 1 runs', count: 0, resource_type: 'Various', region: 'Multiple' });
        expect(html).toContain('sized when step 1 runs');
        expect(html).not.toContain('No recommendations attached');
        expect(html).not.toContain('estimate-label');
      });

      test('reloads the list once when the page becomes visible on the Plans tab', async () => {
        document.body.insertAdjacentHTML('beforeend', '<div id="plans-tab" class="tab-content active"></div>');
        Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' });
        (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
        (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
        setupPlanHandlers();
        setupPlanHandlers();
        document.dispatchEvent(new Event('visibilitychange'));
        await flushPromises();
        expect(api.getPlannedPurchases).toHaveBeenCalledTimes(1);

        document.getElementById('plans-tab')?.classList.remove('active');
        document.dispatchEvent(new Event('visibilitychange'));
        await flushPromises();
        expect(api.getPlannedPurchases).toHaveBeenCalledTimes(1);
      });
    });

    describe('Run now on a step with no recommendations (#1695)', () => {
      const bare = {
        id: 'purchase-bare',
        plan_id: 'plan-1',
        plan_name: 'Azure compute 80% weekly',
        scheduled_date: '2026-10-07',
        provider: 'azure',
        service: 'compute',
        resource_type: 'Various',
        region: 'Multiple',
        count: 0,
        term: 1,
        payment: 'monthly',
        upfront_cost: 0,
        estimated_savings: 0,
        has_recommendations: false,
        status: 'pending',
        step_number: 1,
        total_steps: 4
      };

      beforeEach(async () => {
        (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
        (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [bare] });
        await loadPlans();
      });

      test('renders the run button aria-disabled (still focusable) with an explanatory title', () => {
        const runBtn = document.querySelector('[data-action="run"]') as HTMLButtonElement;
        expect(runBtn.disabled).toBe(false);
        expect(runBtn.getAttribute('aria-disabled')).toBe('true');
        expect(runBtn.classList.contains('is-disabled')).toBe(true);
        expect(runBtn.title).toBe('Run now: this step has no recommendations, so there is nothing to run');
        expect(runBtn.getAttribute('aria-label')).toBe('Run now: this step has no recommendations, so there is nothing to run');
        expect(runBtn.getAttribute('data-id')).toBe('purchase-bare');
      });

      test('placeholder dashes share one muted style in the numeric columns (#1680)', () => {
        const cells = Array.from(document.querySelectorAll('tr.planned-purchase-row td.num'));
        expect(cells).toHaveLength(3);
        cells.forEach(td => {
          expect(td.innerHTML.trim()).toBe('<span class="cell-placeholder">\u2014</span>');
        });
      });

      test('the plan name is not clamped to the small truncate width (#1680)', () => {
        const name = document.querySelector('.plan-name') as HTMLElement;
        expect(name.classList.contains('cell-truncate--sm')).toBe(false);
        expect(name.classList.contains('plan-name-cell')).toBe(true);
        expect(name.title).toBe('Azure compute 80% weekly');
      });

      test('clicking the aria-disabled run button keeps focus and does not re-fetch', async () => {
        const runBtn = document.querySelector('[data-action="run"]') as HTMLButtonElement;
        runBtn.focus();
        (api.getPlannedPurchases as jest.Mock).mockClear();
        runBtn.click();
        await flushPromises();

        expect(api.getPlannedPurchases).not.toHaveBeenCalled();
        expect(document.activeElement).toBe(runBtn);
      });

      test('an all-upfront step with $0 upfront shows the placeholder dash', async () => {
        (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [
          { ...bare, has_recommendations: true, payment: 'all-upfront', upfront_cost: 0, count: 2, estimated_savings: 5 },
        ] });
        await loadPlans();
        const cells = document.querySelectorAll('tr.planned-purchase-row td.num');
        expect(cells[1]!.innerHTML.trim()).toBe('<span class="cell-placeholder">\u2014</span>');
      });

      test('the run action never calls the API or opens a confirm', async () => {
        const runBtn = document.querySelector('[data-action="run"]') as HTMLButtonElement;
        runBtn.click();
        await flushPromises();

        expect(api.runPlannedPurchase).not.toHaveBeenCalled();
        expect(mockConfirmDialog).not.toHaveBeenCalled();
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
          message: 'This step has no recommendations attached, so there is nothing to run.',
          kind: 'info',
        }));
      });
    });

    test('shows empty message when no planned purchases', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const container = document.getElementById('planned-purchases-list');
      expect(container?.innerHTML).toContain('No planned purchases');
    });

    test('shows error on planned purchases API failure', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockRejectedValue(new Error('API Error'));
      console.error = jest.fn();

      await loadPlans();

      const container = document.getElementById('planned-purchases-list');
      expect(container?.innerHTML).toContain('Failed to load planned purchases');
    });

    test('handles missing planned purchases container', async () => {
      document.getElementById('planned-purchases-list')?.remove();
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      // Should not throw
      await expect(loadPlans()).resolves.not.toThrow();
    });

    test('renders notified purchase with run, pause and disable buttons', async () => {
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
            status: 'notified',
            step_number: 1,
            total_steps: 4
          }
        ]
      });

      await loadPlans();

      const container = document.getElementById('planned-purchases-list');
      expect(container?.innerHTML).toContain('data-action="run"');
      expect(container?.innerHTML).toContain('data-action="pause"');
      expect(container?.innerHTML).toContain('data-action="disable"');
      expect(container?.innerHTML).not.toContain('data-action="resume"');
    });

    test('renders paused purchase with correct buttons', async () => {
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
            status: 'paused',
            step_number: 1,
            total_steps: 4
          }
        ]
      });

      await loadPlans();

      const container = document.getElementById('planned-purchases-list');
      expect(container?.innerHTML).toContain('data-action="resume"');
      expect(container?.innerHTML).toContain('data-action="run"');
      // Paused rows stay visible with a Paused badge and are NOT replaced by
      // the empty-state message.
      expect(container?.innerHTML).toContain('status-paused');
      expect(container?.querySelector('.badge.badge-muted')?.textContent).toBe('Paused');
      expect(container?.innerHTML).not.toContain('No planned purchases');
    });
  });
});
