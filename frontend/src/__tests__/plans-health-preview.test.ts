/**
 * Plans module tests: plan health-score badge and inline schedule preview tests.
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

  // Issue #340 follow-up (PR #376): per-plan health-score badge.
  describe('plan health-score badge', () => {
    // Mirror of the band thresholds in plans.ts. Declared here rather than
    // imported because plans.ts does not export them; if they diverge the
    // boundary cases below fail, which is the point.
    const HEALTH_SCORE_GOOD_BOUNDARY = 80;
    const HEALTH_SCORE_WARN_BOUNDARY = 50;

    const basePlan = {
      id: 'plan-1',
      name: 'Test Plan',
      enabled: true,
      auto_purchase: true,
      services: {
        ec2: { provider: 'aws', service: 'ec2', enabled: true, term: 1, payment: 'all-upfront', coverage: 80 }
      },
      ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0 }
    };

    // The plan's own status badge (Active, success tone) shares the card, so
    // assert on the health badge element rather than on the card's markup.
    const healthBadge = (): Element | undefined => Array.from(
      document.querySelectorAll('#plans-list .badge'),
    ).find((b) => (b.textContent ?? '').startsWith('Health:'));

    test('renders a green badge for a healthy score (>= 80)', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{ ...basePlan, health_score: 90, health_factors: [] }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(healthBadge()?.className).toBe('badge badge-success');
      expect(list?.innerHTML).toContain('Health: 90');
    });

    test('renders an amber badge for a medium score (50-79)', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{
          ...basePlan,
          health_score: 65,
          health_factors: [{ code: 'behind_schedule', penalty: 20, note: 'on step 1, expected step 3 by now' }]
        }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(healthBadge()?.className).toBe('badge badge-warning');
      expect(list?.innerHTML).toContain('Health: 65');
    });

    test('renders a red badge for a poor score (< 50)', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{
          ...basePlan,
          health_score: 30,
          health_factors: [
            { code: 'overdue', penalty: 30, note: 'next purchase date has passed' },
            { code: 'failed_executions', penalty: 40, note: '4 failed execution(s)' }
          ]
        }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(healthBadge()?.className).toBe('badge badge-danger');
      expect(list?.innerHTML).toContain('Health: 30');
    });

    test('omits the badge entirely when health_score is absent (pre-feature API response)', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [{ ...basePlan }] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(list?.innerHTML).not.toContain('Health:');
    });

    // Regression guard for the fabricated-score bug: when the backend can't
    // compute a score it sends null, and the UI must say so. Pre-fix the
    // backend substituted 100 on a store failure, painting every plan with a
    // confident green "healthy" badge built from data it never read.
    test('renders an explicit unknown badge when health_score is null', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{ ...basePlan, health_score: null }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(list?.innerHTML).toContain('Health: unknown');
      // Critically, it must NOT be dressed up as a healthy score.
      expect(healthBadge()?.className).toBe('badge badge-muted');
      expect(list?.innerHTML).not.toContain('Health: 100');
    });

    // Band boundaries, not just mid-band samples: with only 90/65/30 the
    // suite cannot tell `>=` from `>`, so flipping either comparison would
    // silently reclassify every plan sitting exactly on a threshold.
    test.each([
      [HEALTH_SCORE_GOOD_BOUNDARY, 'badge-success'],
      [HEALTH_SCORE_GOOD_BOUNDARY - 1, 'badge-warning'],
      [HEALTH_SCORE_WARN_BOUNDARY, 'badge-warning'],
      [HEALTH_SCORE_WARN_BOUNDARY - 1, 'badge-danger'],
    ])('score %i sits in the %s band', async (score, expectedClass) => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{ ...basePlan, health_score: score, health_factors: [] }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(healthBadge()?.className).toBe(`badge ${expectedClass}`);
      expect(list?.innerHTML).toContain(`Health: ${score}`);
    });
  });
});
