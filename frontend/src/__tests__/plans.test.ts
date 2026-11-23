/**
 * Plans module tests
 */
// The harness registers the module mocks, so it must be imported first.
import {
  mockConfirmDialog,
  mockOpenArcheraOfferModal,
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
  // Issue #340 follow-up (PR #376): per-plan health-score badge.
  describe('plan health-score badge', () => {
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
