/**
 * Plans module tests: loadPlans list rendering tests.
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

  describe('loadPlans', () => {
    test('renders plan cards with status badges', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [
          {
            id: 'plan-1',
            name: 'Test Plan',
            enabled: true,
            auto_purchase: true,
            services: {
              'ec2': {
                provider: 'aws',
                service: 'ec2',
                enabled: true,
                term: 1,
                payment: 'all-upfront',
                coverage: 80
              }
            },
            ramp_schedule: {
              type: 'immediate',
              percent_per_step: 100,
              step_interval_days: 0
            }
          }
        ]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      // getStatusBadge is mocked to the success tone; the card renders it as
      // the shared .badge pill (issue #737).
      expect(list?.querySelector('.plan-status .badge.badge-success')?.textContent).toBe('Active');
    });

    test('renders toggle switches for plans', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [
          {
            id: 'plan-1',
            name: 'Test Plan',
            enabled: true,
            auto_purchase: false,
            services: {
              'ec2': {
                provider: 'aws',
                service: 'ec2',
                enabled: true,
                term: 1,
                payment: 'all-upfront',
                coverage: 80
              }
            }
          }
        ]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(list?.innerHTML).toContain('data-action="toggle-plan"');
    });

    test('renders action buttons', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [
          {
            id: 'plan-1',
            name: 'Test Plan',
            enabled: true,
            auto_purchase: false,
            services: {
              'ec2': {
                provider: 'aws',
                service: 'ec2',
                enabled: true,
                term: 1,
                payment: 'all-upfront',
                coverage: 80
              }
            }
          }
        ]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(list?.innerHTML).toContain('data-action="edit-plan"');
      expect(list?.innerHTML).toContain('data-action="view-history"');
      expect(list?.innerHTML).toContain('data-action="delete-plan"');
      expect(list?.innerHTML).toContain('data-action="add-purchases"');
    });

    test('shows next execution date when available', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [
          {
            id: 'plan-1',
            name: 'Test Plan',
            enabled: true,
            auto_purchase: false,
            services: {
              'ec2': {
                provider: 'aws',
                service: 'ec2',
                enabled: true,
                term: 1,
                payment: 'all-upfront',
                coverage: 80
              }
            },
            next_execution_date: '2024-02-15'
          }
        ]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(list?.innerHTML).toContain('Next Purchase');
    });

    test('handles plan without services gracefully', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [
          {
            id: 'plan-1',
            name: 'Empty Plan',
            enabled: true,
            auto_purchase: false,
            services: {}
          }
        ]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(list?.innerHTML).toContain('Empty Plan');
    });

    test('handles plan with null services', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [
          {
            id: 'plan-1',
            name: 'Null Services Plan',
            enabled: true,
            auto_purchase: false,
            services: null
          }
        ]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(list?.innerHTML).toContain('Null Services Plan');
    });

    // Regression guard for issue #973: a plan with unassigned=true must
    // appear in the "Unassigned" section, and an assigned plan must NOT
    // appear there. Before the fix the backend silently dropped zero-account
    // plans from account-filtered responses, so no "Unassigned" section was
    // ever rendered.
    test('unassigned plan renders under Unassigned section, assigned plan does not (issue #973)', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [
          {
            id: 'assigned-plan',
            name: 'Assigned Plan',
            enabled: true,
            auto_purchase: false,
            unassigned: false,
            services: {
              'ec2': { provider: 'aws', service: 'ec2', enabled: true, term: 1, payment: 'no-upfront', coverage: 80 }
            },
            ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0, current_step: 0, total_steps: 1 }
          },
          {
            id: 'legacy-plan',
            name: 'Legacy Unscoped Plan',
            enabled: true,
            auto_purchase: false,
            unassigned: true,
            services: {
              'rds': { provider: 'aws', service: 'rds', enabled: true, term: 3, payment: 'no-upfront', coverage: 70 }
            },
            ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0, current_step: 0, total_steps: 1 }
          }
        ]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      const html = list?.innerHTML ?? '';

      // Both plans are rendered.
      expect(html).toContain('Assigned Plan');
      expect(html).toContain('Legacy Unscoped Plan');

      // The "Unassigned" section header is present.
      expect(html).toContain('unassigned-plans-header');
      expect(html).toContain('Unassigned');

      // The unassigned section header must appear AFTER the assigned plan card
      // (assigned plans come first, unassigned section is appended after).
      const assignedPos = html.indexOf('Assigned Plan');
      const unassignedHeaderPos = html.indexOf('unassigned-plans-header');
      const legacyPos = html.indexOf('Legacy Unscoped Plan');
      expect(assignedPos).toBeLessThan(unassignedHeaderPos);
      expect(unassignedHeaderPos).toBeLessThan(legacyPos);
    });

    test('no Unassigned section when all plans are assigned', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [
          {
            id: 'plan-a',
            name: 'Normal Plan',
            enabled: true,
            auto_purchase: false,
            unassigned: false,
            services: {
              'ec2': { provider: 'aws', service: 'ec2', enabled: true, term: 1, payment: 'no-upfront', coverage: 80 }
            },
            ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0, current_step: 0, total_steps: 1 }
          }
        ]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(list?.innerHTML).not.toContain('unassigned-plans-header');
    });
  });

  describe('loadPlannedPurchases', () => {
    test('renders planned purchases table', async () => {
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

      const container = document.getElementById('planned-purchases-list');
      expect(container?.innerHTML).toContain('Test Plan');
      expect(container?.innerHTML).toContain('t3.medium');
      expect(container?.innerHTML).toContain('us-east-1');
    });

    // #1683: the Service cell shows the display name, not the slug.
    test('shows the service display name in the planned purchases table', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({
        purchases: [{
          id: 'purchase-rds', plan_id: 'plan-1', plan_name: 'RDS plan', scheduled_date: '2024-02-20',
          provider: 'aws', service: 'rds', resource_type: 'db.t3.micro', region: 'us-east-1', count: 1,
          term: 1, payment: 'no-upfront', upfront_cost: 0, estimated_savings: 5, status: 'pending',
          step_number: 1, total_steps: 1,
        }],
      });

      await loadPlans();

      const cells = Array.from(document.querySelectorAll('#planned-purchases-list tbody td'))
        .map((td) => td.textContent?.trim());
      expect(cells).toContain('RDS');
      expect(cells).not.toContain('rds');
    });

    test('labels a step with no recommendations instead of rendering zeros (#609)', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({
        purchases: [
          {
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
          }
        ]
      });

      await loadPlans();

      const html = document.getElementById('planned-purchases-list')?.innerHTML ?? '';
      expect(html).toContain('No recommendations attached');
      expect(html).not.toContain('Various');
      expect(html).not.toContain('Multiple');
      expect(html).not.toContain('$0');
    });
  });
});
