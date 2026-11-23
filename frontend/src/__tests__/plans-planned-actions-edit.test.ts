/**
 * Plans module tests: planned purchase edit and resume tests and the first savePlan tests.
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

    test('edit action calls getPlan with plan_id, not the purchase id (#773)', async () => {
      // The purchase row has id="purchase-1" and plan_id="plan-1".
      // Before the fix, editPlan received "purchase-1", causing GET /plans/purchase-1
      // to return 404 and surfacing "Failed to load plan details".
      (api.getPlan as jest.Mock).mockResolvedValue({
        id: 'plan-1',
        name: 'Test Plan',
        enabled: true,
        auto_purchase: false,
        notification_days_before: 3,
        services: {
          ec2: { provider: 'aws', service: 'ec2', enabled: true, term: 1, payment: 'all-upfront', coverage: 80 },
        },
        ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0 },
      });

      const editBtn = document.querySelector('[data-action="edit"]') as HTMLButtonElement;
      editBtn?.click();

      await flushPromises();

      // Must use the plan FK, not the purchase PK.
      expect(api.getPlan).toHaveBeenCalledWith('plan-1');
      expect(api.getPlan).not.toHaveBeenCalledWith('purchase-1');
      // No error toast should fire.
      expect(mockShowToast).not.toHaveBeenCalledWith(
        expect.objectContaining({ message: 'Failed to load plan details' }),
      );
    });

    test('edit action with empty plan id is a no-op (defensive guard)', async () => {
      // Simulate a button whose data-plan-id attribute is missing/empty by
      // directly injecting a button without the attribute and clicking it.
      const container = document.getElementById('planned-purchases-list');
      const btn = document.createElement('button');
      btn.dataset.action = 'edit';
      btn.dataset.id = 'purchase-1';
      // intentionally omit data-plan-id so planId defaults to ''
      container?.appendChild(btn);
      btn.click();

      await flushPromises();

      // getPlan must NOT be called when planId is empty.
      expect(api.getPlan).not.toHaveBeenCalled();
    });

    test('edit action reconciles the list when the plan is gone (#1403)', async () => {
      // Regression for #1403: a scheduled-purchase row can outlive its plan
      // (deleted, or the caller's account scope changed). Clicking Edit passes
      // the correct plan_id (the #773/#780 fix is intact), but GET /plans/{id}
      // now returns 404. The UI must surface an actionable message AND refresh
      // the planned-purchases list so the orphaned row is dropped, instead of
      // dead-ending on the generic "Failed to load plan details" toast.
      const notFound = Object.assign(new Error('HTTP 404'), { status: 404 });
      (api.getPlan as jest.Mock).mockRejectedValue(notFound);
      // Clear the getPlannedPurchases call made during beforeEach's loadPlans
      // so the assertion below counts only the reconcile triggered by the edit.
      (api.getPlannedPurchases as jest.Mock).mockClear();

      const editBtn = document.querySelector('[data-action="edit"]') as HTMLButtonElement;
      editBtn?.click();

      await flushPromises();

      // The right plan FK was used (guards against a #773-class regression).
      expect(api.getPlan).toHaveBeenCalledWith('plan-1');
      // Actionable, plan-gone message — NOT the generic dead-end.
      expect(mockShowToast).toHaveBeenCalledWith(
        expect.objectContaining({ message: 'This plan is no longer available. It may have been deleted.', kind: 'error' }),
      );
      // The stale row is reconciled by refetching the planned purchases.
      expect(api.getPlannedPurchases).toHaveBeenCalledTimes(1);
    });
  });

  describe('resume action for paused purchase', () => {
    test('resume action resumes paused purchase', async () => {
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

      (api.resumePlannedPurchase as jest.Mock).mockResolvedValue({});
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      const resumeBtn = document.querySelector('[data-action="resume"]') as HTMLButtonElement;
      resumeBtn?.click();

      await flushPromises();

      expect(api.resumePlannedPurchase).toHaveBeenCalledWith('purchase-1');
      // Resume must give visible feedback via a success toast.
      expect(mockShowToast).toHaveBeenCalledWith(
        expect.objectContaining({ message: 'Purchase resumed', kind: 'success' }));
    });
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

    test('prevents default form submission', async () => {
      (api.createPlan as jest.Mock).mockResolvedValue({});
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await savePlan(event);

      expect(event.preventDefault).toHaveBeenCalled();
    });

    test('creates new plan when no plan ID', async () => {
      (api.createPlan as jest.Mock).mockResolvedValue({});
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      (document.getElementById('plan-id') as HTMLInputElement).value = '';

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await savePlan(event);

      expect(api.createPlan).toHaveBeenCalled();
      // The plan description is not stored (the field is disabled in the form), so it is not sent.
      expect((api.createPlan as jest.Mock).mock.calls[0][0]).not.toHaveProperty('description');
      expect(api.createPlan).toHaveBeenCalledWith(expect.objectContaining({ max_step_commitment: 100.10 }));
      // PR #861: no follow-up setPlanAccounts on the create path.
      expect(api.setPlanAccounts).not.toHaveBeenCalled();
      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'Plan created successfully' }));
    });

    test.each(['1.005', '0.015', '1e3', 'NaN', 'Infinity', '0', '-1', '1000000000000001', '999999999999999.99'])('rejects an invalid or inexact cap %s', async (cap) => {
      (document.getElementById('plan-max-step-commitment') as HTMLInputElement).value = cap;
      await savePlan({ preventDefault: jest.fn() } as unknown as Event);
      expect(api.createPlan).not.toHaveBeenCalled();
      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'error', message: expect.stringContaining('commitment') }));
    });

    test.each(['', '1.005'])('opens Advanced settings and focuses the cap on invalid cap %j', async (value) => {
      const form = document.getElementById('plan-form')!;
      const advanced = document.createElement('details');
      advanced.id = 'plan-advanced-settings';
      advanced.appendChild(document.getElementById('plan-max-step-commitment')!);
      form.appendChild(advanced);
      const cap = document.getElementById('plan-max-step-commitment') as HTMLInputElement;
      cap.value = value;
      advanced.open = false;
      await savePlan({ preventDefault: jest.fn() } as unknown as Event);
      expect(api.createPlan).not.toHaveBeenCalled();
      expect(advanced.open).toBe(true);
      expect(document.activeElement).toBe(cap);
    });

    test('requires a cap for automatic purchases', async () => {
      (document.getElementById('plan-max-step-commitment') as HTMLInputElement).value = '';
      await savePlan({ preventDefault: jest.fn() } as unknown as Event);
      expect(api.createPlan).not.toHaveBeenCalled();
      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ message: expect.stringContaining('required') }));
    });

    test.each(['0.01', '100.10', '100000000000000.10', '1000000000000000'])('submits exact cap %s unchanged', async (cap) => {
      (document.getElementById('plan-max-step-commitment') as HTMLInputElement).value = cap;
      (api.createPlan as jest.Mock).mockResolvedValue({});
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      await savePlan({ preventDefault: jest.fn() } as unknown as Event);
      expect(api.createPlan).toHaveBeenCalledWith(expect.objectContaining({ max_step_commitment: Number(cap) }));
    });

    test('clears an optional manual cap explicitly on edit', async () => {
      (document.getElementById('plan-id') as HTMLInputElement).value = 'plan-123';
      (document.getElementById('plan-auto-purchase') as HTMLInputElement).checked = false;
      (document.getElementById('plan-max-step-commitment') as HTMLInputElement).value = '';
      (api.updatePlan as jest.Mock).mockResolvedValue({});
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      await savePlan({ preventDefault: jest.fn() } as unknown as Event);
      expect(api.updatePlan).toHaveBeenCalledWith('plan-123', expect.objectContaining({ max_step_commitment: null, auto_purchase: false }));
    });

    test('allows a manual plan without a cap', async () => {
      (document.getElementById('plan-auto-purchase') as HTMLInputElement).checked = false;
      (document.getElementById('plan-max-step-commitment') as HTMLInputElement).value = '';
      (api.createPlan as jest.Mock).mockResolvedValue({});
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      await savePlan({ preventDefault: jest.fn() } as unknown as Event);
      expect(api.createPlan).toHaveBeenCalledWith(expect.objectContaining({ max_step_commitment: null }));
    });

    test('updates existing plan when plan ID present', async () => {
      (api.updatePlan as jest.Mock).mockResolvedValue({});
      (api.setPlanAccounts as jest.Mock).mockResolvedValue(undefined);
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      (document.getElementById('plan-id') as HTMLInputElement).value = 'plan-123';

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await savePlan(event);

      expect(api.updatePlan).toHaveBeenCalledWith('plan-123', expect.any(Object));
      // PR #861: update path still calls setPlanAccounts (no atomic backend write on PUT).
      expect(api.setPlanAccounts).toHaveBeenCalledWith('plan-123', expect.any(Array));
      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'Plan updated successfully' }));
    });

    test('reports a partial save when the plan row saved but the account write failed (#1057)', async () => {
      (api.updatePlan as jest.Mock).mockResolvedValue({});
      (api.setPlanAccounts as jest.Mock).mockRejectedValue(new Error('accounts boom'));
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      (document.getElementById('plan-id') as HTMLInputElement).value = 'plan-123';
      console.error = jest.fn();
      const modal = document.getElementById('plan-modal');
      modal?.classList.remove('hidden');

      await savePlan({ preventDefault: jest.fn() } as unknown as Event);

      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
        kind: 'error',
        message: 'The plan settings were saved, but the account change failed: accounts boom. Review the target accounts and save again.',
      }));
      expect(api.getPlans).toHaveBeenCalled();
      expect(modal?.classList.contains('hidden')).toBe(false);
    });
  });
});
