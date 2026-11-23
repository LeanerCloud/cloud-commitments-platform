/**
 * Plans module tests: openCreatePlanModal prefill tests and the add purchases modal.
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
import { populateTermSelect, populatePaymentSelect } from '../commitmentOptions';
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

  describe('openCreatePlanModal', () => {
    // #770: Purchase Configuration section should be prefilled from the
    // selected commitment when exactly one commitment is passed in the snapshot.
    describe('prefill from single selected commitment (#770)', () => {
      const fixture: api.Recommendation = {
        id: 'rec-770',
        provider: 'aws',
        service: 'ec2',
        region: 'us-east-1',
        resource_type: 't3.medium',
        count: 1,
        term: 1,
        payment: 'partial-upfront',
        upfront_cost: 100,
        monthly_cost: 20,
        savings: 15,
        selected: true,
        purchased: false,
        cloud_account_id: 'acct-uuid-123',
      };

      test('prefills provider and service selects', () => {
        openCreatePlanModal([fixture]);

        expect((document.getElementById('plan-provider') as HTMLSelectElement).value).toBe('aws');
        expect((document.getElementById('plan-service') as HTMLSelectElement).value).toBe('ec2');
      });

      test('prefills term and payment selects', () => {
        openCreatePlanModal([fixture]);

        expect((document.getElementById('plan-term') as HTMLSelectElement).value).toBe('1');
        expect((document.getElementById('plan-payment') as HTMLSelectElement).value).toBe('partial-upfront');
      });

      test('calls populateTermSelect and populatePaymentSelect with provider+service', () => {
        openCreatePlanModal([fixture]);

        expect(populateTermSelect).toHaveBeenCalledWith(
          expect.any(HTMLSelectElement), 'aws', 'ec2'
        );
        expect(populatePaymentSelect).toHaveBeenCalledWith(
          expect.any(HTMLSelectElement), 'aws', 'ec2'
        );
      });

      test('fetches account via minimal list by cloud_account_id to prefill chip (#949/#951)', async () => {
        // The prefill now resolves the target from the minimal-disclosure list
        // (view:recommendations) so it works for Standard / Read-Only users —
        // the old per-id GET /api/accounts/:id (view:accounts) 403'd for them,
        // leaving the target empty and making Save Plan silently no-op.
        (api.listAccountsMinimal as jest.Mock).mockResolvedValueOnce([
          { id: 'other-acct', name: 'Other', external_id: '999', provider: 'aws' },
          { id: 'acct-uuid-123', name: 'Prod AWS', external_id: '123456789012', provider: 'aws' },
        ]);

        openCreatePlanModal([fixture]);

        // Wait for the async prefillAccountChipFromId to settle
        await Promise.resolve();
        await Promise.resolve();

        expect(api.listAccountsMinimal).toHaveBeenCalled();
        const chips = document.getElementById('plan-accounts-selected');
        expect(chips?.textContent).toContain('Prod AWS');
        const hiddenIds = (document.getElementById('plan-account-ids') as HTMLInputElement).value;
        expect(hiddenIds).toContain('acct-uuid-123');
      });

      test('does not throw and leaves account section empty when minimal list fails', async () => {
        (api.listAccountsMinimal as jest.Mock).mockRejectedValueOnce(new Error('network error'));

        openCreatePlanModal([fixture]);

        await Promise.resolve();
        await Promise.resolve();

        // Account section should be empty -- no chip added
        const hiddenIds = (document.getElementById('plan-account-ids') as HTMLInputElement).value;
        expect(hiddenIds).toBe('');
      });

      test('leaves account section empty when cloud_account_id is not in the minimal list', async () => {
        // Defensive: the target account isn't visible to this user (scoped out
        // by allowed_accounts). No chip should be added; the user can search.
        (api.listAccountsMinimal as jest.Mock).mockResolvedValueOnce([
          { id: 'some-other-acct', name: 'Other', external_id: '999', provider: 'aws' },
        ]);

        openCreatePlanModal([fixture]);
        await Promise.resolve();
        await Promise.resolve();

        const hiddenIds = (document.getElementById('plan-account-ids') as HTMLInputElement).value;
        expect(hiddenIds).toBe('');
      });

      test('skips prefill when no cloud_account_id present', async () => {
        const noAccount: api.Recommendation = { ...fixture, cloud_account_id: undefined };
        openCreatePlanModal([noAccount]);

        await Promise.resolve();

        expect(api.listAccountsMinimal).not.toHaveBeenCalled();
      });

      test('discards stale minimal-list result when modal is reopened before promise resolves', async () => {
        // Simulate a slow first list call that resolves after the modal
        // is closed and a new modal session starts (the race condition fixed
        // by the planModalSession guard — #770 CR Major).
        let resolveFirstCall!: (value: Array<{ id: string; name: string; external_id: string; provider: string }>) => void;
        const firstCallPromise = new Promise<Array<{ id: string; name: string; external_id: string; provider: string }>>(resolve => {
          resolveFirstCall = resolve;
        });

        (api.listAccountsMinimal as jest.Mock)
          .mockReturnValueOnce(firstCallPromise) // first open: hangs
          .mockResolvedValueOnce([]);            // second open: no accounts

        // First modal open with cloud_account_id
        openCreatePlanModal([fixture]);

        // Close and reopen — this increments planModalSession
        closePlanModal();
        const noAccount: api.Recommendation = { ...fixture, cloud_account_id: undefined };
        openCreatePlanModal([noAccount]);

        // Now resolve the stale first promise — should be discarded
        resolveFirstCall([{ id: 'acct-uuid-123', name: 'Prod AWS', external_id: '123456789012', provider: 'aws' }]);
        await Promise.resolve();
        await Promise.resolve();

        // The stale chip must NOT have been added to the new modal session
        const hiddenIds = (document.getElementById('plan-account-ids') as HTMLInputElement).value;
        expect(hiddenIds).toBe('');
        const chips = document.getElementById('plan-accounts-selected');
        expect(chips?.textContent).not.toContain('Prod AWS');
      });

      // #898: extend prefill to a homogeneous multi-commitment selection. The
      // "Plan from N selected" button only enables when provider/service/term/
      // payment match across the selection, so the first commitment is a valid
      // representative for the Purchase Configuration fields.
      describe('prefill from multiple homogeneous commitments (#898)', () => {
        test('prefills provider/service/term/payment from the shared values', () => {
          const second: api.Recommendation = { ...fixture, id: 'rec-771', region: 'us-west-2' };
          openCreatePlanModal([fixture, second]);

          expect((document.getElementById('plan-provider') as HTMLSelectElement).value).toBe('aws');
          expect((document.getElementById('plan-service') as HTMLSelectElement).value).toBe('ec2');
          expect((document.getElementById('plan-term') as HTMLSelectElement).value).toBe('1');
          expect((document.getElementById('plan-payment') as HTMLSelectElement).value).toBe('partial-upfront');
        });

        test('prefills the account chip when all commitments share one account', async () => {
          (api.listAccountsMinimal as jest.Mock).mockResolvedValueOnce([
            { id: 'acct-uuid-123', name: 'Prod AWS', external_id: '123456789012', provider: 'aws' },
          ]);
          const second: api.Recommendation = { ...fixture, id: 'rec-771', region: 'us-west-2' };
          openCreatePlanModal([fixture, second]);

          await Promise.resolve();
          await Promise.resolve();

          expect(api.listAccountsMinimal).toHaveBeenCalled();
          const hiddenIds = (document.getElementById('plan-account-ids') as HTMLInputElement).value;
          expect(hiddenIds).toContain('acct-uuid-123');
        });

        test('pre-selects every selected account with one lookup (#1746)', async () => {
          (api.listAccountsMinimal as jest.Mock).mockResolvedValueOnce([
            { id: 'acct-uuid-123', name: 'Prod AWS', external_id: '123456789012', provider: 'aws' },
            { id: 'acct-uuid-999', name: 'Dev AWS', external_id: '210987654321', provider: 'aws' },
            { id: 'acct-unselected', name: 'Other', external_id: '1', provider: 'aws' },
          ]);
          const second: api.Recommendation = { ...fixture, id: 'rec-771', cloud_account_id: 'acct-uuid-999' };
          openCreatePlanModal([fixture, second]);
          await flushPromises();

          expect(api.listAccountsMinimal).toHaveBeenCalledTimes(1);
          const hiddenIds = (document.getElementById('plan-account-ids') as HTMLInputElement).value.split(',').sort();
          expect(hiddenIds).toEqual(['acct-uuid-123', 'acct-uuid-999']);
        });
      });

      describe('mixed selection (#1746)', () => {
        const azure: api.Recommendation = { ...fixture, id: 'rec-az', provider: 'azure', service: 'compute', term: 3, payment: 'monthly' };

        test('lists the groups instead of the service fields and saves every group', async () => {
          (api.listAccountsMinimal as jest.Mock).mockResolvedValueOnce([]);
          openCreatePlanModal([fixture, azure]);
          const list = document.getElementById('plan-selection-groups') as HTMLElement;
          expect(list.hidden).toBe(false);
          expect(Array.from(list.children).map((li) => li.textContent)).toEqual([
            expect.stringContaining('AWS'), expect.stringContaining('AZURE'),
          ]);
          expect((document.getElementById('plan-service-fields') as HTMLElement).hidden).toBe(true);

          (document.getElementById('plan-account-ids') as HTMLInputElement).value = '11111111-1111-1111-1111-111111111111';
          (api.createPlan as jest.Mock).mockResolvedValue({});
          (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
          (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
          await savePlan({ preventDefault: jest.fn() } as unknown as Event);
          const sent = (api.createPlan as jest.Mock).mock.calls[0][0];
          expect(sent.groups.map((g: { provider: string; recommendation_ids: string[] }) => [g.provider, g.recommendation_ids]))
            .toEqual([['aws', [fixture.id]], ['azure', ['rec-az']]]);
          expect(sent).not.toHaveProperty('provider');
          expect((document.getElementById('plan-selection-groups') as HTMLElement).hidden).toBe(true);
          expect((document.getElementById('plan-service-fields') as HTMLElement).hidden).toBe(false);
        });

        test('group labels are text, not markup', () => {
          openCreatePlanModal([{ ...fixture, service: '<img src=x onerror=alert(1)>' }, azure]);
          const list = document.getElementById('plan-selection-groups') as HTMLElement;
          expect(list.querySelector('img')).toBeNull();
          expect(list.textContent).toContain('<img src=x onerror=alert(1)>');
          expect((document.getElementById('plan-selection-groups-intro') as HTMLElement).hidden).toBe(false);
        });

        test('does not warn when every selected account is listed', async () => {
          (api.listAccountsMinimal as jest.Mock).mockResolvedValueOnce([{ id: fixture.cloud_account_id, name: 'A', external_id: '1', provider: 'aws' }]);
          openCreatePlanModal([fixture, azure]);
          await flushPromises();
          expect(mockShowToast).not.toHaveBeenCalledWith(expect.objectContaining({ kind: 'warning' }));
        });

        test('warns when a selected account cannot be listed', async () => {
          (api.listAccountsMinimal as jest.Mock).mockResolvedValueOnce([{ id: fixture.cloud_account_id, name: 'A', external_id: '1', provider: 'aws' }]);
          openCreatePlanModal([fixture, { ...azure, cloud_account_id: 'acct-unlisted' }]);
          await flushPromises();
          expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'warning', message: expect.stringContaining('Target accounts') }));
        });

        test('a late account lookup does not fill the next modal', async () => {
          let resolveLookup: (v: unknown[]) => void = () => undefined;
          (api.listAccountsMinimal as jest.Mock).mockReturnValueOnce(new Promise((r) => { resolveLookup = r; }));
          openCreatePlanModal([fixture, azure]);
          closePlanModal();
          openNewPlanModal();
          resolveLookup([{ id: fixture.cloud_account_id, name: 'A', external_id: '1', provider: 'aws' }]);
          await flushPromises();
          expect((document.getElementById('plan-account-ids') as HTMLInputElement).value).toBe('');
        });

        test('a single-group selection keeps the service fields', () => {
          openCreatePlanModal([fixture]);
          expect((document.getElementById('plan-selection-groups-intro') as HTMLElement).hidden).toBe(true);
          expect((document.getElementById('plan-selection-groups') as HTMLElement).hidden).toBe(true);
          expect((document.getElementById('plan-service-fields') as HTMLElement).hidden).toBe(false);
        });
      });
    });
  });

  describe('add purchases modal', () => {
    beforeEach(async () => {
      (api.getPlans as jest.Mock).mockResolvedValue({
        plans: [
          {
            id: 'plan-1',
            name: 'Test Plan',
            enabled: true,
            auto_purchase: false,
            notification_days_before: 3,
            services: {}
          }
        ]
      });
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      await loadPlans();

      const addBtn = document.querySelector('[data-action="add-purchases"]') as HTMLButtonElement;
      addBtn?.click();

      await flushPromises();
    });

    test('default date leaves the displayed plan notification window intact', () => {
      const now = new Date();
      const expected = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() + 5));
      const input = document.getElementById('add-purchases-start-date') as HTMLInputElement;
      expect(input.value).toBe(expected.toISOString().slice(0, 10));
    });

    test('cancel button closes modal', async () => {
      const cancelBtn = document.getElementById('add-purchases-cancel') as HTMLButtonElement;
      cancelBtn?.click();

      await flushPromises();

      const modal = document.getElementById('add-purchases-modal');
      expect(modal).toBeNull();
    });

    test('submit form creates planned purchases', async () => {
      (api.createPlannedPurchases as jest.Mock).mockResolvedValue({});
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      const countInput = document.getElementById('add-purchases-count') as HTMLInputElement;
      countInput.value = '3';

      const form = document.getElementById('add-purchases-form') as HTMLFormElement;
      form.dispatchEvent(new Event('submit'));

      await flushPromises();

      expect(api.createPlannedPurchases).toHaveBeenCalledWith('plan-1', 3, expect.any(String));
      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'Successfully scheduled 3 purchases' }));
    });

    test('submit form with single purchase shows singular message', async () => {
      (api.createPlannedPurchases as jest.Mock).mockResolvedValue({});
      (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      const countInput = document.getElementById('add-purchases-count') as HTMLInputElement;
      countInput.value = '1';

      const form = document.getElementById('add-purchases-form') as HTMLFormElement;
      form.dispatchEvent(new Event('submit'));

      await flushPromises();

      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'Successfully scheduled 1 purchase' }));
    });

    test('submit form shows error on failure', async () => {
      (api.createPlannedPurchases as jest.Mock).mockRejectedValue(new Error('API Error'));

      const form = document.getElementById('add-purchases-form') as HTMLFormElement;
      form.dispatchEvent(new Event('submit'));

      await flushPromises();

      const errorDiv = document.getElementById('add-purchases-error');
      expect(errorDiv?.textContent).toBe('API Error');
      expect(errorDiv?.classList.contains('hidden')).toBe(false);
    });

    test('submit form preserves a specific scheduling refusal', async () => {
      const refusal = 'purchase plan has no remaining steps';
      (api.createPlannedPurchases as jest.Mock).mockRejectedValue(new Error(refusal));

      const form = document.getElementById('add-purchases-form') as HTMLFormElement;
      form.dispatchEvent(new Event('submit'));

      await flushPromises();

      const errorDiv = document.getElementById('add-purchases-error');
      expect(errorDiv?.textContent).toBe(refusal);
      expect(errorDiv?.classList.contains('hidden')).toBe(false);
      expect(mockShowToast).not.toHaveBeenCalledWith(expect.objectContaining({ message: expect.stringContaining('Successfully') }));
    });
  });
});
