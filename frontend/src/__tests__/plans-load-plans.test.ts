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
  });
});
