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
  savePlan,
  closePlanModal,
  openCreatePlanModal,
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

    test('disable action cancelled by user', async () => {
      // confirm() replaced by confirmDialog() (11-L2); control via mockConfirmDialog.
      mockConfirmDialog.mockResolvedValue(false);

      const disableBtn = document.querySelector('[data-action="disable"]') as HTMLButtonElement;
      disableBtn?.click();

      await flushPromises();

      expect(api.deletePlannedPurchase).not.toHaveBeenCalled();
    });

    test('declined cancel confirm refocuses the re-rendered row control (#1666)', async () => {
      mockConfirmDialog.mockResolvedValue(false);
      const oldBtn = document.querySelector('[data-action="disable"]') as HTMLButtonElement;
      oldBtn.click();
      await flushPromises();

      const newBtn = document.querySelector('[data-action="disable"][data-id="purchase-1"]') as HTMLButtonElement;
      expect(newBtn).not.toBe(oldBtn);
      expect(document.activeElement).toBe(newBtn);
    });

    test('declined cancel confirm falls back to the section heading when the row is gone (#1666)', async () => {
      mockConfirmDialog.mockResolvedValue(false);
      const header = document.createElement('div');
      header.id = 'planned-purchases-header';
      header.innerHTML = '<h2>Planned purchases</h2>';
      document.body.appendChild(header);
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      (document.querySelector('[data-action="disable"]') as HTMLButtonElement).click();
      await flushPromises();

      expect(document.activeElement).toBe(header.querySelector('h2'));
      header.remove();
    });

    test('accepted cancel confirm does not move focus to the row or heading (#1666)', async () => {
      mockConfirmDialog.mockResolvedValue(true);
      (api.deletePlannedPurchase as jest.Mock).mockResolvedValue({});
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      const header = document.createElement('div');
      header.id = 'planned-purchases-header';
      header.innerHTML = '<h2>Planned purchases</h2>';
      document.body.appendChild(header);

      (document.querySelector('[data-action="disable"]') as HTMLButtonElement).click();
      await flushPromises();

      expect(document.activeElement).not.toBe(header.querySelector('h2'));
      header.remove();
    });

    test('action shows error on failure', async () => {
      (api.pausePlannedPurchase as jest.Mock).mockRejectedValue(new Error('API Error'));
      console.error = jest.fn();

      const pauseBtn = document.querySelector('[data-action="pause"]') as HTMLButtonElement;
      pauseBtn?.click();

      await flushPromises();

      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'Failed to pause purchase: API Error' }));
    });

    test('edit form shows the stored plan coverage (#523)', async () => {
      (api.getPlan as jest.Mock).mockResolvedValue({
        id: 'plan-1',
        name: 'Test Plan',
        enabled: true,
        auto_purchase: false,
        notification_days_before: 3,
        services: {
          ec2: { provider: 'aws', service: 'ec2', enabled: true, term: 1, payment: 'all-upfront', coverage: 55 },
        },
        ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0 },
      });

      (document.querySelector('[data-action="edit"]') as HTMLButtonElement)?.click();
      await flushPromises();

      expect((document.getElementById('plan-coverage') as HTMLInputElement).value).toBe('55');
    });

    test('editing a bound plan sends plan-level fields only (#1746)', async () => {
      (api.getPlan as jest.Mock).mockResolvedValue({
        id: 'plan-bound', name: 'Bound', enabled: true, auto_purchase: false, notification_days_before: 3,
        services: { 'aws/ec2#g1': { provider: 'aws', service: 'ec2', enabled: true, term: 3, payment: 'all-upfront', coverage: 70, group_id: 'g1', bound_count: 2 } },
        ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0, current_step: 0, total_steps: 1, allocation_mode: 'bound_groups' },
      });
      (api.updatePlan as jest.Mock).mockResolvedValue({});
      (api.setPlanAccounts as jest.Mock).mockResolvedValue(undefined);
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      (document.querySelector('[data-action="edit"]') as HTMLButtonElement)?.click();
      await flushPromises();
      (document.getElementById('plan-account-ids') as HTMLInputElement).value = '11111111-1111-1111-1111-111111111111';
      (document.getElementById('plan-name') as HTMLInputElement).value = 'Renamed';

      expect((document.getElementById('plan-service') as HTMLSelectElement).disabled).toBe(true);
      await savePlan({ preventDefault: jest.fn() } as unknown as Event);

      const sent = (api.updatePlan as jest.Mock).mock.calls[0][1];
      expect(sent.name).toBe('Renamed');
      for (const field of ['provider', 'service', 'term', 'payment', 'target_coverage', 'groups']) {
        expect(sent).not.toHaveProperty(field);
      }
    });

    describe('plan field locks (#1746)', () => {
      const locked = () => ['plan-provider', 'plan-service', 'plan-term', 'plan-payment']
        .map((id) => (document.getElementById(id) as HTMLSelectElement).disabled);
      const boundPlan = {
        id: 'plan-bound', name: 'Bound', enabled: true, auto_purchase: false, notification_days_before: 3,
        services: {
          'aws/ec2#g1': { provider: 'aws', service: 'ec2', enabled: true, term: 3, payment: 'all-upfront', coverage: 70, bound_count: 5 },
          'aws/rds#g2': { provider: 'aws', service: 'rds', enabled: true, term: 3, payment: 'all-upfront', coverage: 70, bound_count: 1 },
        },
        ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0, current_step: 0, total_steps: 1, allocation_mode: 'bound_groups' },
      };
      const openBoundEdit = async () => {
        (api.getPlan as jest.Mock).mockResolvedValue(boundPlan);
        (document.querySelector('[data-action="edit"]') as HTMLButtonElement)?.click();
        await flushPromises();
      };
      const snapshot = [{ id: 'rec-1', provider: 'aws', service: 'ec2', term: 3, payment: 'all-upfront' }] as unknown as readonly api.Recommendation[];

      test('a multi-service bound plan saves with blank locked selects and no unsupported-service toast', async () => {
        (api.updatePlan as jest.Mock).mockResolvedValue({});
        (api.setPlanAccounts as jest.Mock).mockResolvedValue(undefined);
        (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
        (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
        await openBoundEdit();
        (document.getElementById('plan-account-ids') as HTMLInputElement).value = '11111111-1111-1111-1111-111111111111';
        (document.getElementById('plan-term') as HTMLSelectElement).value = '';
        (document.getElementById('plan-coverage') as HTMLInputElement).value = '';
        expect(mockShowToast).not.toHaveBeenCalledWith(expect.objectContaining({ kind: 'error' }));
        expect((document.getElementById('plan-coverage') as HTMLInputElement).readOnly).toBe(true);
        expect((document.getElementById('plan-service') as HTMLSelectElement).title).toBe('Set by the selected recommendations');
        await savePlan({ preventDefault: jest.fn() } as unknown as Event);
        expect(api.updatePlan).toHaveBeenCalledTimes(1);
      });

      test('a bound plan keeps its accounts of every provider (#1746)', async () => {
        (api.listPlanAccounts as jest.Mock).mockResolvedValueOnce([
          { id: 'acct-aws', name: 'AWS', external_id: '1', provider: 'aws' },
          { id: 'acct-azure', name: 'Azure', external_id: '2', provider: 'azure' },
        ]);
        await openBoundEdit();
        await flushPromises();
        expect((document.getElementById('plan-account-ids') as HTMLInputElement).value.split(',').sort()).toEqual(['acct-aws', 'acct-azure']);
      });

      test('editing a bound plan lists its groups in place of the service fields', async () => {
        await openBoundEdit();
        const items = Array.from(document.getElementById('plan-selection-groups')?.children ?? []).map((li) => li.textContent);
        expect(items).toHaveLength(2);
        expect(items.map((t) => t?.replace(/^.*: /, ''))).toEqual(['5 recommendations', '1 recommendation']);
        expect(items[0]).toContain('EC2');
        expect((document.getElementById('plan-service-fields') as HTMLElement).hidden).toBe(true);
        closePlanModal();
        expect((document.getElementById('plan-service-fields') as HTMLElement).hidden).toBe(false);
      });

      test('locks never leak into the next modal', async () => {
        await openBoundEdit();
        expect(locked()).toEqual([true, true, true, true]);
        closePlanModal();
        expect(locked()).toEqual([false, false, false, false]);
        expect((document.getElementById('plan-coverage') as HTMLInputElement).readOnly).toBe(false);
        expect((document.getElementById('plan-service') as HTMLSelectElement).title).toBe('');

        await openBoundEdit();
        openNewPlanModal();
        expect(locked()).toEqual([false, false, false, false]);
        expect((document.getElementById('plan-coverage') as HTMLInputElement).readOnly).toBe(false);

        await openBoundEdit();
        openCreatePlanModal(snapshot);
        expect(locked()).toEqual([true, true, true, true]);
        expect((document.getElementById('plan-coverage') as HTMLInputElement).readOnly).toBe(false);
        closePlanModal();
        expect(locked()).toEqual([false, false, false, false]);
      });

      test('a selection save does not depend on the locked selects', async () => {
        openCreatePlanModal(snapshot);
        (document.getElementById('plan-account-ids') as HTMLInputElement).value = '11111111-1111-1111-1111-111111111111';
        (document.getElementById('plan-service') as HTMLSelectElement).value = '';
        (document.getElementById('plan-term') as HTMLSelectElement).value = '';
        (api.createPlan as jest.Mock).mockResolvedValue({});
        (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
        (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
        await savePlan({ preventDefault: jest.fn() } as unknown as Event);
        expect((api.createPlan as jest.Mock).mock.calls[0][0].groups).toEqual([expect.objectContaining({ service: 'ec2', term: 3 })]);
      });

      test('editing an unbound plan drops a selection left open by the create flow', async () => {
        openCreatePlanModal(snapshot);
        (api.getPlan as jest.Mock).mockResolvedValue({
          ...boundPlan, services: { ec2: { provider: 'aws', service: 'ec2', enabled: true, term: 3, payment: 'all-upfront', coverage: 70 } },
          ramp_schedule: { ...boundPlan.ramp_schedule, allocation_mode: undefined },
        });
        (document.querySelector('[data-action="edit"]') as HTMLButtonElement)?.click();
        await flushPromises();
        (api.updatePlan as jest.Mock).mockResolvedValue({});
        (api.setPlanAccounts as jest.Mock).mockResolvedValue(undefined);
        (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
        (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
        (document.getElementById('plan-account-ids') as HTMLInputElement).value = '11111111-1111-1111-1111-111111111111';
        expect((document.getElementById('plan-selection-groups') as HTMLElement).hidden).toBe(true);
        await savePlan({ preventDefault: jest.fn() } as unknown as Event);
        const sent = (api.updatePlan as jest.Mock).mock.calls[0][1];
        expect(sent).not.toHaveProperty('groups');
        expect(sent).toHaveProperty('provider');
      });
    });
  });
});
