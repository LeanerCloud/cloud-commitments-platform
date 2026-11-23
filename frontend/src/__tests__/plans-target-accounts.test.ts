/**
 * Plans module tests: Target Accounts provider filter tests.
 * Split from plans.test.ts to stay under the 500-line ceiling; the module
 * mocks live in plans-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import {
  mockConfirmDialog,
  mockOpenArcheraOfferModal,
} from './plans-harness';
import {
  openCreatePlanModal,
  openNewPlanModal,
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
  });
});
