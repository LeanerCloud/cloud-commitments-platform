/**
 * Plans module tests: savePlan snapshot, guard and validation tests.
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
  savePlan,
  closePlanModal,
  openCreatePlanModal,
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

    test('closes modal after successful save', async () => {
      (api.createPlan as jest.Mock).mockResolvedValue({});
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await savePlan(event);

      const modal = document.getElementById('plan-modal');
      expect(modal?.classList.contains('hidden')).toBe(true);
    });

    test('shows error toast and keeps the modal open on save failure (#949)', async () => {
      // Issue #949 symptom: a failed create-plan left the modal open with NO
      // feedback. Guard both halves: the error toast surfaces AND the modal
      // stays open (so the user can retry) — never a silent no-op.
      (api.createPlan as jest.Mock).mockRejectedValue(new Error('Save failed'));
      console.error = jest.fn();

      const modal = document.getElementById('plan-modal');
      modal?.classList.remove('hidden');

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await savePlan(event);

      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
        kind: 'error',
        message: 'Failed to save plan: Save failed',
      }));
      // Modal must NOT be closed when the save errors.
      expect(modal?.classList.contains('hidden')).toBe(false);
    });

    test('uses weekly-25pct ramp schedule', async () => {
      (api.createPlan as jest.Mock).mockResolvedValue({});
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      const weeklyRadio = document.querySelector('input[value="weekly-25pct"]') as HTMLInputElement;
      weeklyRadio.checked = true;
      (document.querySelector('input[value="immediate"]') as HTMLInputElement).checked = false;

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await savePlan(event);

      expect(api.createPlan).toHaveBeenCalledWith(expect.objectContaining({
        ramp_schedule: 'weekly-25pct'
      }));
    });

    test('uses monthly-10pct ramp schedule', async () => {
      (api.createPlan as jest.Mock).mockResolvedValue({});
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      const monthlyRadio = document.querySelector('input[value="monthly-10pct"]') as HTMLInputElement;
      monthlyRadio.checked = true;
      (document.querySelector('input[value="immediate"]') as HTMLInputElement).checked = false;

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await savePlan(event);

      expect(api.createPlan).toHaveBeenCalledWith(expect.objectContaining({
        ramp_schedule: 'monthly-10pct'
      }));
    });

    test('does NOT open Archera offer modal on plan create (fix #499)', async () => {
      // After #499 the modal must be surfaced only after execution completes,
      // never at plan-save time. Regression guard: ensure the removed call
      // does not come back.
      (api.createPlan as jest.Mock).mockResolvedValue({});
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      (document.getElementById('plan-id') as HTMLInputElement).value = '';

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await savePlan(event);

      // Guard against false positives: confirm the save actually ran (not a
      // short-circuit) so the "modal not called" assertion is meaningful.
      expect(api.createPlan).toHaveBeenCalledTimes(1);
      expect(mockOpenArcheraOfferModal).not.toHaveBeenCalled();
    });

    test('does NOT open Archera offer modal on plan update (fix #499)', async () => {
      (api.updatePlan as jest.Mock).mockResolvedValue({});
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
      (document.getElementById('plan-id') as HTMLInputElement).value = 'plan-123';

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await savePlan(event);

      // Guard against false positives: confirm the update actually ran.
      expect(api.updatePlan).toHaveBeenCalledWith('plan-123', expect.any(Object));
      expect(mockOpenArcheraOfferModal).not.toHaveBeenCalled();
    });

    test('rejects submit and never calls createPlan when Target Accounts is empty (universal-plans fix)', async () => {
      // Universal plans (purchase_plans rows with no plan_accounts row) are
      // no longer allowed. The Save Plan button is also disabled in this
      // state via refreshPlanSaveButtonState; this assertion is the defence-
      // in-depth at the savePlan layer for scripted submissions or any
      // future regression that bypasses the disabled UI.
      (document.getElementById('plan-account-ids') as HTMLInputElement).value = '';
      (api.createPlan as jest.Mock).mockResolvedValue({ id: 'p1' });

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await savePlan(event);

      expect(api.createPlan).not.toHaveBeenCalled();
      expect(api.setPlanAccounts).not.toHaveBeenCalled();
      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
        kind: 'error',
        message: expect.stringContaining('Target accounts'),
      }));
    });

    test('forwards selected target_accounts on createPlan (universal-plans fix)', async () => {
      // Verifies the new wire contract: savePlan stamps the selected account
      // chip IDs onto the request body so the backend can validate and
      // persist plan_accounts in the same call. The 2-step PUT remains a
      // belt-and-suspenders write for update flows, but the create path
      // must include target_accounts inline.
      (document.getElementById('plan-account-ids') as HTMLInputElement).value
        = '11111111-1111-1111-1111-111111111111,22222222-2222-2222-2222-222222222222';
      (api.createPlan as jest.Mock).mockResolvedValue({ id: 'p1' });
      (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await savePlan(event);

      expect(api.createPlan).toHaveBeenCalledWith(expect.objectContaining({
        target_accounts: [
          '11111111-1111-1111-1111-111111111111',
          '22222222-2222-2222-2222-222222222222',
        ],
      }));
      // Regression guard for PR #861: the create path must NOT call
      // setPlanAccounts -- the backend already persists plan_accounts
      // atomically from target_accounts in the POST body (handler_plans.go
      // createPlan). A follow-up PUT would be a redundant double-write.
      expect(api.setPlanAccounts).not.toHaveBeenCalled();
    });

    // A plan create is sent once (issue #1734): Save is guarded while the
    // request is in flight, and a duplicate-name 409 offers the existing plan.
    describe('in-flight guard and duplicate name (#1734)', () => {
      const rec: api.Recommendation = {
        id: 'rec-1734', provider: 'aws', service: 'ec2', region: 'us-east-1',
        resource_type: 't3.medium', count: 1, term: 1, payment: 'partial-upfront',
        upfront_cost: 100, monthly_cost: 20, savings: 15, selected: true,
        purchased: false, cloud_account_id: 'acct-1734',
      };
      const account = { id: 'acct-1734', name: 'Prod AWS', external_id: '123456789012', provider: 'aws' };
      const settlers: Array<() => void> = [];
      const flush = async (): Promise<void> => { for (let i = 0; i < 6; i++) await Promise.resolve(); };
      const saveButton = (): HTMLButtonElement => document.querySelector('#plan-form button[type="submit"]') as HTMLButtonElement;
      const submit = (): Promise<void> => savePlan({ preventDefault: jest.fn() } as unknown as Event);
      // pendingCreate makes createPlan hang until release() or fail() runs;
      // afterEach settles whatever a test left pending so the module-level
      // in-flight flag never leaks into the next test.
      const pendingCreate = (): { release: () => void; fail: (err: Error) => void } => {
        let release!: () => void;
        let fail!: (err: Error) => void;
        (api.createPlan as jest.Mock).mockReturnValueOnce(new Promise((resolve, reject) => {
          release = () => resolve({});
          fail = reject;
        }));
        settlers.push(() => release());
        return { release, fail };
      };

      beforeEach(async () => {
        (api.listAccountsMinimal as jest.Mock).mockResolvedValueOnce([account]);
        (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
        (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
        openCreatePlanModal([rec]);
        await flush();
        (document.getElementById('plan-name') as HTMLInputElement).value = 'Weekly EC2';
        (document.getElementById('plan-coverage') as HTMLInputElement).value = '80';
        (document.getElementById('plan-notify-days') as HTMLInputElement).value = '3';
        expect(saveButton().disabled).toBe(false);
      });

      afterEach(async () => {
        settlers.splice(0).forEach((settle) => settle());
        await flush();
      });

      test('a double submit sends one create and disables Save until it settles', async () => {
        const { release } = pendingCreate();
        const first = submit();
        await submit();

        expect(api.createPlan).toHaveBeenCalledTimes(1);
        expect(saveButton().disabled).toBe(true);
        expect(saveButton().title).toBe('Saving…');

        release();
        await first;
        expect(saveButton().disabled).toBe(false);
      });

      test('Save is enabled again after a failed create', async () => {
        const { fail } = pendingCreate();
        const first = submit();
        expect(saveButton().disabled).toBe(true);

        fail(new Error('boom'));
        await first;
        expect(saveButton().disabled).toBe(false);
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'error' }));
      });

      test('adding an account while the create is pending leaves Save disabled', async () => {
        const { release } = pendingCreate();
        const first = submit();

        (api.listAccountsMinimal as jest.Mock).mockResolvedValueOnce([{ ...account, id: 'acct-other' }]);
        openCreatePlanModal([{ ...rec, cloud_account_id: 'acct-other' }]);
        await flush();
        expect((document.getElementById('plan-account-ids') as HTMLInputElement).value).toBe('acct-other');
        expect(saveButton().disabled).toBe(true);

        release();
        await first;
      });

      test('Save stays disabled after the create when no account is left', async () => {
        const { release } = pendingCreate();
        const first = submit();

        (document.querySelector('.account-chip-remove') as HTMLButtonElement).click();
        release();
        await first;
        expect(saveButton().disabled).toBe(true);
      });

      test('a plan_name_taken 409 offers the existing plan and opening it drops the selection snapshot', async () => {
        const { fail } = pendingCreate();
        const first = submit();
        fail(Object.assign(new Error('a plan named "weekly ec2" already exists; open it or choose another name'), {
          status: 409,
          details: { code: 'plan_name_taken', existing_plan_id: 'plan-existing' },
        }));
        await first;

        const toast = mockShowToast.mock.calls.map((c) => c[0] as { message: string; actions?: Array<{ label: string; onClick: () => void }> })
          .find((t) => t.actions !== undefined);
        expect(toast?.message).toContain('already exists');
        expect(toast?.actions?.map((a) => a.label)).toEqual(['Open existing plan']);
        expect((api.createPlan as jest.Mock).mock.calls[0][0]).toHaveProperty('groups');

        (api.getPlan as jest.Mock).mockRejectedValueOnce(new Error('gone'));
        toast?.actions?.[0]?.onClick();
        await flush();

        expect(api.getPlan).toHaveBeenCalledWith('plan-existing');
        expect(document.getElementById('plan-modal')?.classList.contains('hidden')).toBe(true);
        // The modal closed first, so the next create does not inherit the
        // Plan-from-selection snapshot.
        (api.createPlan as jest.Mock).mockResolvedValueOnce({});
        await submit();
        expect((api.createPlan as jest.Mock).mock.calls[1][0]).not.toHaveProperty('groups');
      });

      test('other errors get no open-existing action', async () => {
        const { fail } = pendingCreate();
        const first = submit();
        fail(Object.assign(new Error('conflict'), { status: 409, details: { code: 'editor_reconciliation_required' } }));
        await first;
        expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'error', actions: undefined }));
      });
    });
  });
});
