/**
 * Plans module tests: planned purchase edit and resume tests and the first savePlan tests.
 * Split from plans.test.ts to stay under the 500-line ceiling; the module
 * mocks live in plans-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import {
  mockConfirmDialog,
  mockOpenArcheraOfferModal,
} from './plans-harness';
import {
  loadPlans,
  closePlanModal,
  openNewPlanModal,
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

    describe('read-only plan filters (#1737)', () => {
      const plan = (services: Record<string, unknown>) => ({
        id: 'plan-1', name: 'Test Plan', enabled: true, auto_purchase: false, notification_days_before: 3,
        services, ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0 },
      });
      const openEdit = async () => {
        (document.querySelector('[data-action="edit"]') as HTMLButtonElement)?.click();
        await flushPromises();
        return document.getElementById('plan-filters-notice') as HTMLElement;
      };

      test('edit modal lists the filters with the reason', async () => {
        (api.getPlan as jest.Mock).mockResolvedValue(plan({
          'aws/ec2': { provider: 'aws', service: 'ec2', enabled: true, term: 1, payment: 'all-upfront', coverage: 55, include_regions: ['us-east-1'], min_count: 2 },
        }));
        const notice = await openEdit();
        expect(notice.classList.contains('hidden')).toBe(false);
        expect(notice.textContent).toContain('plan steps cannot honor');
        expect(notice.textContent).toContain('Cancel the scheduled root and recreate the plan');
        expect(notice.querySelector('li')?.textContent).toBe('aws/ec2: include regions: us-east-1; minimum count: 2');
      });

      test('a later new-plan modal does not show the previous plan notice', async () => {
        (api.getPlan as jest.Mock).mockResolvedValue(plan({
          'aws/ec2': { provider: 'aws', service: 'ec2', enabled: true, term: 1, payment: 'all-upfront', coverage: 55, min_count: 2 },
        }));
        const notice = await openEdit();
        expect(notice.classList.contains('hidden')).toBe(false);
        closePlanModal();
        openNewPlanModal();
        expect(notice.classList.contains('hidden')).toBe(true);
        expect(notice.textContent).toBe('');
      });

      test('notice stays hidden when the plan has no filters', async () => {
        (api.getPlan as jest.Mock).mockResolvedValue(plan({
          'aws/ec2': { provider: 'aws', service: 'ec2', enabled: true, term: 1, payment: 'all-upfront', coverage: 55 },
        }));
        const notice = await openEdit();
        expect(notice.classList.contains('hidden')).toBe(true);
        expect(notice.textContent).toBe('');
      });

      test('server strings render as text, not markup', async () => {
        (api.getPlan as jest.Mock).mockResolvedValue(plan({
          'aws/ec2': { provider: 'aws', service: 'ec2', enabled: true, term: 1, payment: 'x', coverage: 1, include_engines: ['<img src=x onerror=alert(1)>'] },
        }));
        const notice = await openEdit();
        expect(notice.querySelector('img')).toBeNull();
        expect(notice.textContent).toContain('<img src=x onerror=alert(1)>');
      });
    });
  });
});
