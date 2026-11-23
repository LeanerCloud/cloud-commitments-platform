/**
 * Plans module tests
 */
// The harness registers the module mocks, so it must be imported first.
import {
  mockConfirmDialog,
  mockOpenArcheraOfferModal,
  mockShowToast,
} from './plans-harness';
import {
  loadPlans,
  closePlanModal,
  openCreatePlanModal,
  openNewPlanModal,
  _resetRampHandlersForTest,
} from '../plans';

import * as api from '../api';
import * as state from '../state';
import { viewPlanHistory } from '../history';
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

  describe('plan cap and global state on modal open', () => {
    test.each([true, false, undefined])('shows the read-only global state %s', async (enabled) => {
      (api.getConfig as jest.Mock).mockResolvedValue({ global: { auto_purchase_enabled: enabled } });
      openNewPlanModal();
      await Promise.resolve();
      expect(document.getElementById('plan-auto-purchase-state')?.textContent).toContain(enabled === true ? 'is enabled' : enabled === false ? 'is off' : 'unavailable');
    });

    test('does not fetch configuration without permission', () => {
      (state.getCurrentUser as jest.Mock).mockReturnValueOnce({ id: 'reader', groups: [], effectivePermissions: [] });
      openNewPlanModal();
      expect(api.getConfig).not.toHaveBeenCalled();
      expect(document.getElementById('plan-auto-purchase-state')?.textContent).toContain('unavailable');
    });

    test('shows configuration read failure explicitly', async () => {
      (api.getConfig as jest.Mock).mockRejectedValueOnce(new Error('unavailable'));
      openNewPlanModal();
      await Promise.resolve();
      expect(document.getElementById('plan-auto-purchase-state')?.textContent).toContain('could not be loaded');
    });

    test('does not let a stale configuration response overwrite a new modal session', async () => {
      let finish!: (config: unknown) => void;
      (api.getConfig as jest.Mock).mockReturnValueOnce(new Promise(resolve => { finish = resolve; }));
      openNewPlanModal();
      closePlanModal();
      (api.getConfig as jest.Mock).mockResolvedValueOnce({ global: { auto_purchase_enabled: false } });
      openNewPlanModal();
      await Promise.resolve();
      finish({ global: { auto_purchase_enabled: true } });
      await Promise.resolve();
      expect(document.getElementById('plan-auto-purchase-state')?.textContent).toContain('is off');
    });

    test('new and selected-recommendation modals clear an existing cap', () => {
      const cap = document.getElementById('plan-max-step-commitment') as HTMLInputElement;
      cap.value = '100.10';
      openNewPlanModal();
      expect(cap.value).toBe('');
      cap.value = '100.10';
      openCreatePlanModal();
      expect(cap.value).toBe('');
    });
  });

  describe('loadPlans', () => {
    test('fetches and renders plans', async () => {
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
              step_interval_days: 0,
              current_step: 2,
              total_steps: 4
            }
          }
        ]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(list?.innerHTML).toContain('Test Plan');
      // #1683: the Service field shows the display name.
      expect(list?.innerHTML).toContain('EC2');
    });

    test.each([
      [{ current_step: 2, total_steps: 4 }, '2/4 steps'],
      [{ current_step: 0, total_steps: 0 }, '\u2014'],
      [{ current_step: 0 }, '\u2014'],
    ])('ramp progress %j renders %s instead of a fabricated 0/1 (#136)', async (steps, expected) => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{
          id: 'plan-1', name: 'Ramp Plan', enabled: true, auto_purchase: true,
          services: { ec2: { provider: 'aws', service: 'ec2', enabled: true, term: 1, payment: 'all-upfront', coverage: 80 } },
          ramp_schedule: { type: 'weekly', percent_per_step: 25, step_interval_days: 7, ...steps },
        }],
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const progress = Array.from(document.querySelectorAll('#plans-list .plan-detail'))
        .find((el) => el.querySelector('.plan-detail-label')?.textContent === 'Progress');
      expect(progress?.querySelector('.plan-detail-value')?.textContent).toBe(expected);
    });

    test('a plan with no ramp_schedule renders progress as unknown, not 0/1 (#151)', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{
          id: 'plan-1', name: 'No Ramp Plan', enabled: true, auto_purchase: true,
          services: { ec2: { provider: 'aws', service: 'ec2', enabled: true, term: 1, payment: 'all-upfront', coverage: 80 } },
        }],
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const progress = Array.from(document.querySelectorAll('#plans-list .plan-detail'))
        .find((el) => el.querySelector('.plan-detail-label')?.textContent === 'Progress');
      expect(progress?.querySelector('.plan-detail-value')?.textContent).toBe('\u2014');
      expect(document.getElementById('plans-list')?.textContent).toContain('Immediate');
    });

    test('multi-SP plan summary lists every plan-type covered (issue #131)', async () => {
      // Pre-fix: extractPlanInfo took serviceValues[0], so a plan
      // covering both Compute SP and SageMaker SP rendered only
      // "Compute Savings Plans" — the SageMaker side was hidden.
      // Post-fix: every entry is rendered, comma-joined, with the
      // SP slugs abbreviated so 4 plan-types still fit in the card.
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [
          {
            id: 'plan-multi-sp',
            name: 'Multi SP plan',
            enabled: true,
            auto_purchase: true,
            services: {
              'aws:savings-plans-compute': {
                provider: 'aws',
                service: 'savings-plans-compute',
                enabled: true,
                term: 3,
                payment: 'no-upfront',
                coverage: 80,
              },
              'aws:savings-plans-sagemaker': {
                provider: 'aws',
                service: 'savings-plans-sagemaker',
                enabled: true,
                term: 3,
                payment: 'no-upfront',
                coverage: 80,
              },
            },
            ramp_schedule: {
              type: 'immediate',
              percent_per_step: 100,
              step_interval_days: 0,
              current_step: 0,
              total_steps: 1,
            },
          },
        ],
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(list?.innerHTML).toContain('Compute SP');
      expect(list?.innerHTML).toContain('SageMaker SP');
      // The pre-fix "Multiple" placeholder must be gone.
      expect(list?.innerHTML).not.toContain('Multiple');
    });

    test('single-service plan still renders one label (no regression)', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [
          {
            id: 'plan-ec2',
            name: 'EC2 only',
            enabled: true,
            services: { ec2: { provider: 'aws', service: 'ec2', term: 3, coverage: 80 } },
            ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0, current_step: 0, total_steps: 1 },
          },
        ],
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      // #1683: the Service field shows the display name, not the slug.
      const values = Array.from(list?.querySelectorAll('.plan-detail') ?? [])
        .filter((d) => d.querySelector('.plan-detail-label')?.textContent === 'Service')
        .map((d) => d.querySelector('.plan-detail-value')?.textContent);
      expect(values).toEqual(['EC2']);
    });

    test('shows empty message when no plans', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: []
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(list?.innerHTML).toContain('No purchase plans configured');
    });

    test('provider filter derives provider from first service entry', async () => {
      // Regression: backend config.PurchasePlan has no top-level
      // `provider` field — it lives on each service inside the `services`
      // map. Filtering via p.provider directly returned 0 rows every
      // time and was the cause of "switching Provider: AWS empties the
      // list" seen in the 2026-04-22 screenshots.
      //
      // Issue #344 T2: provider filter source moved from a per-section
      // <select> to the global topbar (state.ts). Simulate "user picked
      // AWS in the topbar" by pointing the mock at 'aws'.
      const state = await import('../state');
      (state.getCurrentProvider as jest.Mock).mockReturnValue('aws');

      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [
          {
            id: 'plan-aws',
            name: 'AWS fanout',
            enabled: true,
            services: { ec2: { provider: 'aws', service: 'ec2', term: 3, coverage: 80 } },
            ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0, current_step: 0, total_steps: 1 },
          },
          {
            id: 'plan-gcp',
            name: 'GCP CUDs',
            enabled: true,
            services: { compute: { provider: 'gcp', service: 'compute', term: 3, coverage: 80 } },
            ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0, current_step: 0, total_steps: 1 },
          },
        ],
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      try {
        await loadPlans();

        const list = document.getElementById('plans-list');
        expect(list?.innerHTML).toContain('AWS fanout');
        expect(list?.innerHTML).not.toContain('GCP CUDs');
      } finally {
        // Restore the mock so the next test's "no provider scope"
        // default holds (it relies on getCurrentProvider returning '').
        (state.getCurrentProvider as jest.Mock).mockReturnValue('');
      }
    });

    describe('plans with several service groups (#1746)', () => {
      const ramp = { type: 'immediate', percent_per_step: 100, step_interval_days: 0, current_step: 0, total_steps: 1 };
      const group = (provider: string, service: string, term: number, coverage: number, extra: object = {}) =>
        ({ provider, service, enabled: true, term, payment: 'all-upfront', coverage, ...extra });
      const bound = {
        id: 'plan-bound', name: 'Bound mix', enabled: true,
        services: {
          'aws/ec2#g1': group('aws', 'ec2', 3, 70, { bound_count: 5 }),
          'aws/ec2#g2': group('aws', 'ec2', 1, 80, { bound_count: 2 }),
          'azure/compute#g3': group('azure', 'compute', 1, 80, { bound_count: 4 }),
          'gcp/compute#g4': group('gcp', 'compute', 1, 0, { bound_count: 0 }),
        },
        ramp_schedule: { ...ramp, allocation_mode: 'bound_groups' },
      };
      const unmarked = {
        id: 'plan-legacy', name: 'Legacy two providers', enabled: true,
        services: { ec2: group('aws', 'ec2', 3, 80, { bound_count: 9 }), compute: group('azure', 'compute', 1, 60) },
        ramp_schedule: ramp,
      };
      const detail = (label: string, planName: string) => {
        const card = Array.from(document.querySelectorAll('.plan-card')).find((c) => c.textContent?.includes(planName));
        return Array.from(card?.querySelectorAll('.plan-detail') ?? [])
          .find((d) => d.querySelector('.plan-detail-label')?.textContent === label)?.querySelector('.plan-detail-value')?.textContent;
      };
      const load = async (provider: string) => {
        const state = await import('../state');
        (state.getCurrentProvider as jest.Mock).mockReturnValue(provider);
        (api.getPlans as jest.Mock).mockResolvedValue({ plans: [bound, unmarked] });
        (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
        try { await loadPlans(); } finally { (state.getCurrentProvider as jest.Mock).mockReturnValue(''); }
      };

      test('a bound plan card shows every provider, service, term, coverage and the recommendation count', async () => {
        await load('');
        expect(detail('Service', 'Bound mix')).toBe('EC2, Compute');
        expect(detail('Term', 'Bound mix')).toContain(',');
        expect(detail('Coverage', 'Bound mix')).toBe('70%, 80%, 0%');
        expect(detail('Recommendations', 'Bound mix')).toBe('11');
        expect(detail('Provider', 'Bound mix')).toMatch(/AWS.*Azure/i);
        expect(detail('Provider', 'Legacy two providers')).not.toMatch(/Azure/i);
        expect(detail('Recommendations', 'Legacy two providers')).toBeUndefined();
        expect(detail('Coverage', 'Legacy two providers')).toBe('80%');
      });

      test('the provider filter keeps a bound plan that has a group of that provider', async () => {
        await load('azure');
        expect(document.getElementById('plans-list')?.innerHTML).toContain('Bound mix');
        expect(document.getElementById('plans-list')?.innerHTML).not.toContain('Legacy two providers');
      });

      test('a plan without the marker still filters on its first entry only', async () => {
        await load('aws');
        expect(document.getElementById('plans-list')?.innerHTML).toContain('Legacy two providers');
        await load('gcp');
        expect(document.getElementById('plans-list')?.innerHTML).not.toContain('Legacy two providers');
        expect(document.getElementById('plans-list')?.innerHTML).toContain('Bound mix');
      });
    });

    test('passes account_ids to api.getPlans when account filter is active (issue #705)', async () => {
      // Regression test for the Account global filter being non-functional
      // on the Plans page. loadPlans must forward the account selection to
      // api.getPlans so the backend can JOIN plan_accounts and prune the list.
      const state = await import('../state');
      const accountID = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa';
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([accountID]);

      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      try {
        await loadPlans();

        expect(api.getPlans).toHaveBeenCalledWith({ account_ids: [accountID] });
      } finally {
        (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([]);
      }
    });

    test('calls api.getPlans with empty object when no account is selected', async () => {
      // When no account chip is active, getPlans receives {} so the backend
      // returns all plans (no account_ids filter applied).
      const state = await import('../state');
      (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([]);

      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      expect(api.getPlans).toHaveBeenCalledWith({});
    });

    test('shows error on API failure', async () => {
      (api.getPlans as jest.Mock).mockRejectedValue(new Error('API Error'));
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      console.error = jest.fn();

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(list?.innerHTML).toContain('Failed to load plans');
    });

  });
  describe('plan action buttons', () => {
    test('toggle plan button calls patchPlan and reloads', async () => {
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
      (api.patchPlan as jest.Mock).mockResolvedValue({});

      await loadPlans();

      // Reset mock to track reload
      (api.getPlans as jest.Mock).mockClear();
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });

      const toggleInput = document.querySelector('[data-action="toggle-plan"]') as HTMLInputElement;
      toggleInput?.dispatchEvent(new Event('change'));

      await flushPromises();

      expect(api.patchPlan).toHaveBeenCalledWith('plan-1', expect.objectContaining({ enabled: true }));
      expect(api.getPlans).toHaveBeenCalled();
    });

    test('toggle plan shows error on failure', async () => {
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
      (api.patchPlan as jest.Mock).mockRejectedValue(new Error('API Error'));
      console.error = jest.fn();

      await loadPlans();

      const toggleInput = document.querySelector('[data-action="toggle-plan"]') as HTMLInputElement;
      toggleInput?.dispatchEvent(new Event('change'));

      await flushPromises();

      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'Failed to update plan' }));
    });

    test('edit plan button loads plan details and opens modal', async () => {
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
      (api.getPlan as jest.Mock).mockResolvedValue({
        id: 'plan-1',
        name: 'Test Plan',
        enabled: true,
        auto_purchase: true,
        max_step_commitment: 100.10,
        notification_days_before: 3,
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
      });

      await loadPlans();

      const editBtn = document.querySelector('[data-action="edit-plan"]') as HTMLButtonElement;
      editBtn?.click();

      await flushPromises();

      expect(api.getPlan).toHaveBeenCalledWith('plan-1');
      const modal = document.getElementById('plan-modal');
      expect(modal?.classList.contains('hidden')).toBe(false);
      expect((document.getElementById('plan-name') as HTMLInputElement).value).toBe('Test Plan');
      expect((document.getElementById('plan-max-step-commitment') as HTMLInputElement).value).toBe('100.1');
    });

    test('editing a plan with no stored cap clears the cap left by the previously edited plan', async () => {
      const plan = (id: string, cap: number | null) => ({
        id, name: id, enabled: true, auto_purchase: false, max_step_commitment: cap, notification_days_before: 3,
        services: { ec2: { provider: 'aws', service: 'ec2', enabled: true, term: 1, payment: 'all-upfront', coverage: 80 } },
        ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0 }
      });
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [plan('plan-a', 100), plan('plan-b', null)] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      (api.getPlan as jest.Mock).mockImplementation(async (id: string) => plan(id, id === 'plan-a' ? 100 : null));
      await loadPlans();
      const cap = document.getElementById('plan-max-step-commitment') as HTMLInputElement;
      const edit = (id: string) => (document.querySelector(`[data-action="edit-plan"][data-id="${id}"]`) as HTMLButtonElement).click();
      edit('plan-a');
      await flushPromises();
      expect(cap.value).toBe('100');
      edit('plan-b');
      await flushPromises();
      expect(api.getPlan).toHaveBeenLastCalledWith('plan-b');
      expect(cap.value).toBe('');
    });

    test('edit plan with weekly-25pct ramp schedule', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [
          {
            id: 'plan-1',
            name: 'Test Plan',
            enabled: true,
            auto_purchase: false,
            services: {}
          }
        ]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      (api.getPlan as jest.Mock).mockResolvedValue({
        id: 'plan-1',
        name: 'Weekly Plan',
        enabled: true,
        auto_purchase: true,
        notification_days_before: 3,
        services: {
          'ec2': {
            provider: 'aws',
            service: 'ec2',
            term: 1,
            payment: 'all-upfront',
            coverage: 80
          }
        },
        ramp_schedule: {
          type: 'weekly',
          percent_per_step: 25,
          step_interval_days: 7
        }
      });

      await loadPlans();

      const editBtn = document.querySelector('[data-action="edit-plan"]') as HTMLButtonElement;
      editBtn?.click();

      await flushPromises();

      const weeklyRadio = document.querySelector('input[value="weekly-25pct"]') as HTMLInputElement;
      expect(weeklyRadio.checked).toBe(true);
    });

    test('edit plan with monthly-10pct ramp schedule', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [
          {
            id: 'plan-1',
            name: 'Test Plan',
            enabled: true,
            auto_purchase: false,
            services: {}
          }
        ]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      (api.getPlan as jest.Mock).mockResolvedValue({
        id: 'plan-1',
        name: 'Monthly Plan',
        enabled: true,
        auto_purchase: true,
        notification_days_before: 3,
        services: {
          'ec2': {
            provider: 'aws',
            service: 'ec2',
            term: 1,
            payment: 'all-upfront',
            coverage: 80
          }
        },
        ramp_schedule: {
          type: 'monthly',
          percent_per_step: 10,
          step_interval_days: 30
        }
      });

      await loadPlans();

      const editBtn = document.querySelector('[data-action="edit-plan"]') as HTMLButtonElement;
      editBtn?.click();

      await flushPromises();

      const monthlyRadio = document.querySelector('input[value="monthly-10pct"]') as HTMLInputElement;
      expect(monthlyRadio.checked).toBe(true);
    });

    test('edit plan with custom ramp schedule populates custom fields', async () => {
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
      (api.getPlan as jest.Mock).mockResolvedValue({
        id: 'plan-1',
        name: 'Test Plan',
        enabled: true,
        auto_purchase: true,
        notification_days_before: 3,
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
          type: 'custom',
          percent_per_step: 25,
          step_interval_days: 14
        }
      });

      await loadPlans();

      const editBtn = document.querySelector('[data-action="edit-plan"]') as HTMLButtonElement;
      editBtn?.click();

      await flushPromises();

      expect((document.getElementById('ramp-step-percent') as HTMLInputElement).value).toBe('25');
      expect((document.getElementById('ramp-interval-days') as HTMLInputElement).value).toBe('14');
    });

    test('edit plan shows error on failure', async () => {
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
      (api.getPlan as jest.Mock).mockRejectedValue(new Error('API Error'));
      console.error = jest.fn();

      await loadPlans();

      const editBtn = document.querySelector('[data-action="edit-plan"]') as HTMLButtonElement;
      editBtn?.click();

      await flushPromises();

      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'Failed to load plan details' }));
    });

    test('view history button calls viewPlanHistory', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [
          {
            id: 'plan-1',
            name: 'Test Plan',
            enabled: true,
            auto_purchase: false,
            services: {}
          }
        ]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const historyBtn = document.querySelector('[data-action="view-history"]') as HTMLButtonElement;
      historyBtn?.click();

      await flushPromises();

      expect(viewPlanHistory).toHaveBeenCalledWith('plan-1');
    });

    test('delete plan button deletes plan and reloads', async () => {
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
      (api.deletePlan as jest.Mock).mockResolvedValue({});
      // confirmDialog() replaces window.confirm() (11-L2); set via mockConfirmDialog.
      mockConfirmDialog.mockResolvedValue(true);

      await loadPlans();

      // Reset mock to track reload
      (api.getPlans as jest.Mock).mockClear();
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });

      const deleteBtn = document.querySelector('[data-action="delete-plan"]') as HTMLButtonElement;
      deleteBtn?.click();

      await flushPromises();

      expect(api.deletePlan).toHaveBeenCalledWith('plan-1');
      expect(api.getPlans).toHaveBeenCalled();
    });

    test('delete plan does nothing if user cancels', async () => {
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
      mockConfirmDialog.mockResolvedValueOnce(false);

      await loadPlans();

      const deleteBtn = document.querySelector('[data-action="delete-plan"]') as HTMLButtonElement;
      deleteBtn?.click();

      await flushPromises();

      expect(api.deletePlan).not.toHaveBeenCalled();
    });

    test('delete plan shows error on failure', async () => {
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
      (api.deletePlan as jest.Mock).mockRejectedValue(new Error('API Error'));
      // confirmDialog() replaces window.confirm() (11-L2); set via mockConfirmDialog.
      mockConfirmDialog.mockResolvedValue(true);
      console.error = jest.fn();

      await loadPlans();

      const deleteBtn = document.querySelector('[data-action="delete-plan"]') as HTMLButtonElement;
      deleteBtn?.click();

      await flushPromises();

      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'Failed to delete plan' }));
    });

    test('add purchases button opens modal', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [
          {
            id: 'plan-1',
            name: 'Test Plan',
            enabled: true,
            auto_purchase: false,
            services: {}
          }
        ]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const addBtn = document.querySelector('[data-action="add-purchases"]') as HTMLButtonElement;
      addBtn?.click();

      await flushPromises();

      const modal = document.getElementById('add-purchases-modal');
      expect(modal).toBeTruthy();
      expect(modal?.innerHTML).toContain('Add Planned Purchases');
    });
  });

  describe('Target Accounts provider filter (issue #703)', () => {
    // These tests need the account-search DOM elements in addition to the
    // standard plan-modal markup provided by the outer beforeEach.
    beforeEach(() => {
      // Inject account-search elements into the existing form using safe DOM API calls.
      const form = document.getElementById('plan-form');
      if (form) {
        const accountSection = document.createElement('div');
        accountSection.className = 'plan-accounts-search';

        const searchInput = document.createElement('input');
        searchInput.type = 'text';
        searchInput.id = 'plan-account-search';
        searchInput.placeholder = 'Search accounts';
        accountSection.appendChild(searchInput);

        const suggestions = document.createElement('div');
        suggestions.id = 'plan-account-suggestions';
        suggestions.className = 'account-suggestions hidden';
        accountSection.appendChild(suggestions);

        const selectedContainer = document.createElement('div');
        selectedContainer.id = 'plan-accounts-selected';
        selectedContainer.className = 'selected-accounts';
        accountSection.appendChild(selectedContainer);

        const hiddenIds = document.createElement('input');
        hiddenIds.type = 'hidden';
        hiddenIds.id = 'plan-account-ids';
        hiddenIds.value = '';
        accountSection.appendChild(hiddenIds);

        form.appendChild(accountSection);
      }

      // Use fake timers so setTimeout in handlePlanAccountSearch can be
      // controlled synchronously.
      jest.useFakeTimers();
    });

    afterEach(() => {
      jest.useRealTimers();
    });

    test('account search passes the current plan provider to listAccountsMinimal (#949/#951)', async () => {
      (api.listAccountsMinimal as jest.Mock).mockResolvedValue([]);

      // openNewPlanModal calls form.reset() synchronously, resetting the provider
      // select back to its first option ('aws'). Wire up the modal first, then
      // change provider, then trigger the search so the handler reads the new value.
      openNewPlanModal();
      // Let setupPlanAccountsSection's async portion settle (it clones the input node).
      await Promise.resolve();

      // Now change provider AFTER the form reset so setupPlanAccountsSection already ran.
      const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement;
      providerSelect.value = 'azure';

      // The search input node was replaced (cloneNode) inside setupPlanAccountsSection;
      // re-query by id to get the live node with the registered listener.
      const searchInput = document.getElementById('plan-account-search') as HTMLInputElement;
      searchInput.value = 'my-azure';
      searchInput.dispatchEvent(new Event('input'));

      // Advance past the 300 ms debounce timer and flush async api call.
      jest.runAllTimers();
      await Promise.resolve();
      await Promise.resolve();

      // Search uses the minimal-disclosure endpoint so it works for Standard /
      // Read-Only users (view:recommendations), not the view:accounts list.
      expect(api.listAccountsMinimal).toHaveBeenCalledWith(
        expect.objectContaining({ search: 'my-azure', provider: 'azure' })
      );
    });

    describe('account search for plans built from a selection (#1746)', () => {
      const rec = (id: string, term: number): api.Recommendation => ({
        id, provider: 'azure', service: 'compute', term, payment: 'monthly', cloud_account_id: 'acct-az',
      } as unknown as api.Recommendation);
      const searchCall = async (selection: api.Recommendation[]) => {
        (api.listAccountsMinimal as jest.Mock).mockResolvedValue([]);
        openCreatePlanModal(selection);
        await Promise.resolve();
        (api.listAccountsMinimal as jest.Mock).mockClear();
        const searchInput = document.getElementById('plan-account-search') as HTMLInputElement;
        searchInput.value = 'prod';
        searchInput.dispatchEvent(new Event('input'));
        jest.runAllTimers();
        await Promise.resolve();
        await Promise.resolve();
        return (api.listAccountsMinimal as jest.Mock).mock.calls[0][0] as { provider?: string };
      };

      test('a selection of several groups is not restricted to the provider select', async () => {
        expect(await searchCall([rec('a', 3), rec('b', 1)])).not.toHaveProperty('provider');
      });

      test('a one-group selection is restricted to its provider', async () => {
        expect(await searchCall([rec('a', 3)])).toEqual(expect.objectContaining({ provider: 'azure' }));
      });
    });

    test('account search passes aws provider when plan provider is aws', async () => {
      (api.listAccountsMinimal as jest.Mock).mockResolvedValue([]);

      openNewPlanModal();
      await Promise.resolve();

      // form.reset() resets to first option 'aws', so provider is already 'aws'.
      const searchInput = document.getElementById('plan-account-search') as HTMLInputElement;
      searchInput.value = 'prod';
      searchInput.dispatchEvent(new Event('input'));

      jest.runAllTimers();
      await Promise.resolve();
      await Promise.resolve();

      expect(api.listAccountsMinimal).toHaveBeenCalledWith(
        expect.objectContaining({ provider: 'aws' })
      );
    });

    test('switching provider clears existing account chips and hidden field', async () => {
      // openNewPlanModal wires up setupRampScheduleHandlers, which attaches the
      // provider-change listener that clears accounts. Must call it before the test.
      openNewPlanModal();
      await Promise.resolve();

      // Pre-populate the hidden field to simulate an earlier account selection.
      // renderPlanAccountChips() will clear the DOM container from planSelectedAccounts,
      // so populating it directly in the DOM is sufficient to assert the clear.
      const selectedContainer = document.getElementById('plan-accounts-selected') as HTMLElement;
      const chip = document.createElement('span');
      chip.className = 'account-chip';
      chip.textContent = 'old-acct';
      selectedContainer.appendChild(chip);
      (document.getElementById('plan-account-ids') as HTMLInputElement).value = 'acct-old-id';

      // Switch provider — should clear chips and hidden field via the handler added in
      // setupRampScheduleHandlers (called by openNewPlanModal).
      const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement;
      providerSelect.value = 'gcp';
      providerSelect.dispatchEvent(new Event('change'));

      expect(selectedContainer.textContent).toBe('');
      expect((document.getElementById('plan-account-ids') as HTMLInputElement).value).toBe('');
    });

    test('switching provider clears and hides open account suggestion dropdown', async () => {
      openNewPlanModal();
      await Promise.resolve();

      // Simulate an open suggestion dropdown with stale results from a previous search.
      const suggestions = document.getElementById('plan-account-suggestions') as HTMLElement;
      suggestions.textContent = 'stale-item';
      suggestions.classList.remove('hidden');

      // Also put text in the search input to verify it is cleared.
      const searchInput = document.getElementById('plan-account-search') as HTMLInputElement;
      searchInput.value = 'old query';

      // Switch provider — the handler must clear + hide the dropdown and clear the input.
      const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement;
      providerSelect.value = 'gcp';
      providerSelect.dispatchEvent(new Event('change'));

      expect(suggestions.textContent).toBe('');
      expect(suggestions.classList.contains('hidden')).toBe(true);
      expect(searchInput.value).toBe('');
    });

    test('account search input is disabled when provider is cleared after modal open', async () => {
      // Open the modal with default provider (aws from form reset).
      openNewPlanModal();
      await Promise.resolve();

      // Simulate user clearing the provider via the change listener, which should disable search.
      const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement;
      providerSelect.value = '';
      providerSelect.dispatchEvent(new Event('change'));

      const searchInput = document.getElementById('plan-account-search') as HTMLInputElement;
      expect(searchInput.disabled).toBe(true);
    });

    test('account search input is enabled when plan-provider has a value', async () => {
      // Default after form.reset() is 'aws' (first option), so disabled should be false.
      openNewPlanModal();
      await Promise.resolve();

      // The cloned input replaces the original in setupPlanAccountsSection;
      // query by id to get the current node.
      const searchInput = document.getElementById('plan-account-search') as HTMLInputElement;
      expect(searchInput.disabled).toBe(false);
    });
  });

  // Regression tests for H3: setupRampScheduleHandlers must not stack duplicate
  // listeners on static modal elements across multiple modal opens.
  // Pre-fix: opening the plan modal N times would attach N handlers to each
  // element, so a single radio change would fire updateCommitmentOptions N times.
  describe('H3 regression: setupRampScheduleHandlers install-once guard', () => {
    test('provider-change handler fires exactly once after opening modal 3 times', () => {
      const { populateTermSelect: mockPopulate } = jest.requireMock('../commitmentOptions') as {
        populateTermSelect: jest.Mock;
      };

      // beforeEach already called _resetRampHandlersForTest() so the flag is clear.
      // Open the modal 3 times without resetting between them.
      openNewPlanModal(); // first open: installs handlers (rampHandlersInstalled = true)
      openNewPlanModal(); // second open: guard returns early -- no re-attach
      openNewPlanModal(); // third open: guard returns early -- no re-attach

      mockPopulate.mockClear(); // clear any calls from setup above

      // Dispatch one change event on the provider select.
      const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement;
      providerSelect.value = 'azure';
      providerSelect.dispatchEvent(new Event('change'));

      // populateTermSelect is called exactly once from updateCommitmentOptions.
      // Pre-fix (no guard) it would be called 3 times (once per open).
      expect(mockPopulate).toHaveBeenCalledTimes(1);
    });

    test('ramp schedule radio handler fires exactly once after 2 modal opens', () => {
      // The radio-change handler calls updatePlanNameFromSchedule which reads
      // the plan-name input. Count invocations via a side-effect on populateTermSelect.
      const { populateTermSelect: mockPopulate } = jest.requireMock('../commitmentOptions') as {
        populateTermSelect: jest.Mock;
      };

      // beforeEach called _resetRampHandlersForTest(); open twice without reset between.
      openNewPlanModal(); // installs
      openNewPlanModal(); // guard: no re-install

      mockPopulate.mockClear();

      // The provider-select change handler calls updateCommitmentOptions -> populateTermSelect.
      const providerSelect = document.getElementById('plan-provider') as HTMLSelectElement;
      providerSelect.value = 'gcp';
      providerSelect.dispatchEvent(new Event('change'));

      // With stacked handlers it would fire twice (once per open).
      // With the guard it fires exactly once.
      expect(mockPopulate).toHaveBeenCalledTimes(1);
    });
  });

  // Regression for H1: providerBadgeHtml must be used for planned-purchase rows
  // and plan card detail (prevents XSS via class attribute injection).
  describe('H1 regression: provider badge XSS neutralisation in plans.ts', () => {
    test('providerBadgeHtml is called with the purchase provider for planned-purchase rows', async () => {
      const mockBadge = (jest.requireMock('../utils') as { providerBadgeHtml: jest.Mock }).providerBadgeHtml;

      const maliciousProvider = 'aws"><img src=x onerror=alert(1)>';
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({
        purchases: [
          {
            id: 'pp-1',
            plan_id: 'plan-1',
            plan_name: 'Test Plan',
            provider: maliciousProvider,
            service: 'ec2',
            resource_type: 'Standard',
            region: 'us-east-1',
            count: 1,
            term: 1,
            payment: 'no-upfront',
            upfront_cost: 0,
            estimated_savings: 50,
            scheduled_date: '2024-06-01',
            status: 'pending',
            step_number: 1,
            total_steps: 4,
          }
        ]
      });
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });

      await loadPlans();

      // providerBadgeHtml must have been called with the malicious string.
      // Pre-fix the string was raw-interpolated into a class attribute via innerHTML.
      // Post-fix it goes through the helper which whitelists and escapes it.
      expect(mockBadge).toHaveBeenCalledWith(maliciousProvider);
    });
  });

  // H-4 regression: extractPlanInfo must not fabricate provider/term/coverage.
  // Pre-fix: absent fields defaulted to 'aws'/3/80 so the card and edit modal
  // showed hardcoded values. These tests must FAIL on pre-fix code.
  describe('H-4: extractPlanInfo does not fabricate provider, term, or coverage', () => {
    const planBase = {
      id: 'plan-1',
      name: 'Test Plan',
      enabled: true,
      auto_purchase: false,
      ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0, current_step: 0, total_steps: 1 },
    };

    test('absent provider in services renders an em dash, not aws in plan card', async () => {
      const mockBadge = (jest.requireMock('../utils') as { providerBadgeHtml: jest.Mock }).providerBadgeHtml;
      mockBadge.mockClear();

      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{ ...planBase, services: { ec2: { service: 'ec2', term: 1, coverage: 80 } } }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      // providerBadgeHtml is called with null when provider is absent.
      // Pre-fix it was called with 'aws' (the hardcoded default).
      expect(mockBadge).not.toHaveBeenCalledWith('aws');
      expect(mockBadge).toHaveBeenCalledWith(null);
    });

    test('absent term in services renders an em dash, not 3yr in plan card', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{ ...planBase, services: { ec2: { provider: 'aws', service: 'ec2', coverage: 80 } } }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      // Pre-fix rendered '3 Years' for a missing term; post-fix renders '—'
      expect(list?.textContent).not.toContain('3 Years');
      expect(list?.textContent).toContain('—');
    });

    test('absent coverage in services renders an em dash, not 80% in plan card', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{ ...planBase, services: { ec2: { provider: 'aws', service: 'ec2', term: 1 } } }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      // Pre-fix rendered '80%' for a missing coverage; post-fix renders '—'
      expect(list?.textContent).not.toContain('80%');
      expect(list?.textContent).toContain('—');
    });

    test('empty services object renders all em-dash fields in plan card', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{ ...planBase, services: {} }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      // Pre-fix: all-zero fallback { provider:'aws', term:3, coverage:80 }
      // Post-fix: all '—' for missing data
      expect(list?.textContent).not.toContain('80%');
      expect(list?.textContent).not.toContain('3 Years');
    });

    test('edit modal: absent term leaves select unset (not 3yr default)', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{ ...planBase, id: 'plan-1', services: { ec2: { provider: 'aws', service: 'ec2', coverage: 80 } } }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      (api.getPlan as jest.Mock).mockResolvedValue({
        ...planBase,
        id: 'plan-1',
        notification_days_before: 3,
        services: { ec2: { provider: 'aws', service: 'ec2', coverage: 80 } },
        ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0 }
      });

      await loadPlans();
      const editBtn = document.querySelector('[data-action="edit-plan"]') as HTMLButtonElement;
      editBtn?.click();
      await flushPromises();

      const termSelect = document.getElementById('plan-term') as HTMLSelectElement;
      // Pre-fix: termSelect.value === '3' (hardcoded fallback)
      // Post-fix: termSelect.value === '' (unset, user must choose)
      expect(termSelect.value).not.toBe('3');
      expect(termSelect.value).toBe('');
    });
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

    // A score outside the 0-100 the API contracts to send is no more
    // trustworthy than a NaN. Banding it would paint -1 red and 101 green as
    // though both had been measured.
    test.each([-1, 101, 1000])('renders unknown for out-of-range score %i', async (score) => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{ ...basePlan, health_score: score }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(list?.innerHTML).toContain('Health: unknown');
      expect(list?.innerHTML).toContain('badge-muted');
      expect(list?.innerHTML).not.toContain(`Health: ${score}`);
    });

    // A garbled score off the wire is an uncomputable score, so it must
    // reach the same "unknown" badge rather than silently vanishing (which
    // reads as "this deploy predates the feature").
    test('renders unknown rather than dropping the badge for a non-finite score', async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{ ...basePlan, health_score: NaN }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      expect(list?.innerHTML).toContain('Health: unknown');
      expect(list?.innerHTML).not.toContain('Health: NaN');
    });

    test('escapes health factor notes in the tooltip (XSS regression)', async () => {
      const maliciousNote = '"><img src=x onerror=alert(1)>';
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [{
          ...basePlan,
          health_score: 40,
          health_factors: [{ code: 'stalled', penalty: 15, note: maliciousNote }]
        }]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const list = document.getElementById('plans-list');
      // Pre-fix, the unescaped `"` would close the title attribute early and
      // the rest would parse as a live <img onerror> element. Post-fix the
      // quote is entity-encoded so the whole malicious string stays inert
      // text inside the title attribute -- assert on the parsed DOM (not the
      // raw HTML string, since HTML serializers are free to leave `<`/`>`
      // un-re-escaped inside an already-safe attribute value).
      expect(list?.querySelector('img')).toBeNull();
      const badge = list?.querySelector('.badge-danger[title]');
      expect(badge?.getAttribute('title')).toContain(maliciousNote);
    });
  });
  describe('inline schedule preview', () => {
    const restartNotice = 'restarts from today for a plan already in progress; executed purchases are not shown';
    const host = (): HTMLElement => document.getElementById('plan-editor-panel')!;
    const points = (): number => host().querySelectorAll('[data-preview-step]').length;
    const backendPlan = (ramp: Record<string, unknown>, coverage: number | null = 80) => ({
      id: 'plan-1', name: 'Plan', enabled: true, auto_purchase: false, notification_days_before: 3,
      services: { ec2: { provider: 'aws', service: 'ec2', term: 1, payment: 'all-upfront', ...(coverage === null ? {} : { coverage }) } },
      ramp_schedule: ramp,
    });
    const openEdit = async (plan: unknown): Promise<void> => {
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [{ id: 'plan-1', name: 'Plan', enabled: true, auto_purchase: false, services: {} }] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      (api.getPlan as jest.Mock).mockResolvedValue(plan);
      await loadPlans();
      expect(points()).toBe(0);
      (document.querySelector('[data-action="edit-plan"]') as HTMLButtonElement).click();
      await jest.advanceTimersByTimeAsync(50);
    };
    beforeEach(() => {
      jest.useFakeTimers({ now: new Date('2026-10-15T12:00:00Z') });
      const form = document.getElementById('plan-form')!.innerHTML;
      document.body.innerHTML = `<div id="plans-list"></div><div id="planned-purchases-list"></div>
        <div id="plan-modal" class="modal hidden"><div class="modal-content"><h2 id="plan-modal-title">Create purchase plan</h2>
        <div class="plan-editor-layout"><section id="plan-editor-panel" hidden></section><form id="plan-form">${form}</form></div>
        <div class="modal-buttons"><button type="button" id="close-plan-modal-btn">Cancel</button><button type="submit" form="plan-form">Save</button></div>
        </div></div><div id="purchase-modal" class="hidden"></div>`;
      _resetRampHandlersForTest();
    });
    afterEach(() => { closePlanModal(); jest.useRealTimers(); });

    test.each([
      ['weekly 25%', { type: 'weekly', percent_per_step: 25, step_interval_days: 7 }, 4],
      ['custom 33% / 14 days', { type: 'custom', percent_per_step: 33, step_interval_days: 14 }, 3],
      ['monthly 10%', { type: 'monthly', percent_per_step: 10, step_interval_days: 30 }, 10],
    ])('Edit shows the %s schedule preview and the restart notice', async (_name, ramp, count) => {
      await openEdit(backendPlan(ramp));
      expect(points()).toBe(count);
      expect(host().hidden).toBe(false);
      expect(host().querySelector('.editor-draft-notice')!.textContent).toContain(restartNotice);
      expect(api.createPlan).not.toHaveBeenCalled(); expect(api.updatePlan).not.toHaveBeenCalled();
      expect(api.createPlannedPurchases).not.toHaveBeenCalled();
    });

    const checkedRamp = (): string => document.querySelector<HTMLInputElement>('input[name="ramp-schedule"]:checked')!.value;
    const customFields = (): string[] => ['ramp-step-percent', 'ramp-interval-days'].map(id => (document.getElementById(id) as HTMLInputElement).value);

    test.each([
      ['weekly 20% / 7d', { type: 'weekly', percent_per_step: 20, step_interval_days: 7 }, 'custom', ['20', '7'], 5],
      ['monthly 5% / 30d', { type: 'monthly', percent_per_step: 5, step_interval_days: 30 }, 'custom', ['5', '30'], 20],
      ['weekly 25% on a 14 day interval', { type: 'weekly', percent_per_step: 25, step_interval_days: 14 }, 'custom', ['25', '14'], 4],
      ['monthly 10% on a 15 day interval', { type: 'monthly', percent_per_step: 10, step_interval_days: 15 }, 'custom', ['10', '15'], 10],
      ['weekly 25% / 7d', { type: 'weekly', percent_per_step: 25, step_interval_days: 7 }, 'weekly-25pct', null, 4],
      ['monthly 10% / 30d', { type: 'monthly', percent_per_step: 10, step_interval_days: 30 }, 'monthly-10pct', null, 10],
      ['immediate', { type: 'immediate', percent_per_step: 100, step_interval_days: 0 }, 'immediate', null, 1],
    ])('Edit prefills a stored %s ramp as %s (#1156)', async (_name, ramp, radio, fields, count) => {
      await openEdit(backendPlan(ramp));
      expect(checkedRamp()).toBe(radio);
      if (fields) expect(customFields()).toEqual(fields);
      expect(points()).toBe(count);
    });

    test('Edit with absent coverage shows the validation message and no points', async () => {
      await openEdit(backendPlan({ type: 'weekly', percent_per_step: 25, step_interval_days: 7 }, null));
      expect(points()).toBe(0);
      expect(host().querySelector('[data-editor="summary"]')!.textContent).not.toBe('');
    });

    // Panel and modal listeners on the form (input/change bubble, invalid/submit capture), keyed by type:capture.
    const trackFormListeners = () => {
      const form = document.getElementById('plan-form')!;
      const owned = new Set(['input:false', 'change:false', 'invalid:true', 'submit:true']);
      const key = (type: string, o?: boolean | AddEventListenerOptions): string => `${type}:${o === true || (typeof o === 'object' && o.capture === true)}`;
      const entries: { key: string; fn: unknown }[] = [];
      const add = form.addEventListener.bind(form), remove = form.removeEventListener.bind(form);
      jest.spyOn(form, 'addEventListener').mockImplementation((type: string, fn: EventListenerOrEventListenerObject, o?: boolean | AddEventListenerOptions) => {
        if (owned.has(key(type, o))) entries.push({ key: key(type, o), fn });
        add(type, fn, o);
      });
      jest.spyOn(form, 'removeEventListener').mockImplementation((type: string, fn: EventListenerOrEventListenerObject, o?: boolean | AddEventListenerOptions) => {
        const index = entries.findIndex(entry => entry.key === key(type, o) && entry.fn === fn);
        if (index >= 0) entries.splice(index, 1);
        remove(type, fn, o);
      });
      return entries;
    };

    test('closing the Edit modal removes every listener the panel and the modal added', async () => {
      const entries = trackFormListeners();
      await openEdit(backendPlan({ type: 'weekly', percent_per_step: 25, step_interval_days: 7 }));
      expect(entries.map(entry => entry.key).sort()).toEqual(['change:false', 'input:false', 'invalid:true', 'submit:true']);
      closePlanModal();
      expect(entries).toEqual([]);
      expect(host().hidden).toBe(true); expect(host().childElementCount).toBe(0);
      expect(host().closest('.plan-editor-layout')!.classList.contains('has-editor-draft')).toBe(false);
    });

    test('Edit then Create without closing leaves exactly one panel listening and no Edit notice', async () => {
      const entries = trackFormListeners();
      await openEdit(backendPlan({ type: 'weekly', percent_per_step: 25, step_interval_days: 7 }));
      openCreatePlanModal();
      expect(entries.map(entry => entry.key).sort()).toEqual(['change:false', 'input:false', 'invalid:true', 'submit:true']);
      expect(host().querySelector('.editor-draft-notice')!.textContent).not.toContain('restarts from today');
      const coverage = document.getElementById('plan-coverage') as HTMLInputElement;
      coverage.value = '60'; coverage.dispatchEvent(new Event('input', { bubbles: true }));
      expect(host().querySelector('[data-editor="summary"]')!.textContent).toContain('Target 60%');
    });

    test('Create previews without the restart notice', () => {
      openCreatePlanModal();
      expect(host().hidden).toBe(false);
      expect(host().querySelector('.editor-draft-notice')!.textContent).not.toContain('restarts from today');
    });
  });
});
