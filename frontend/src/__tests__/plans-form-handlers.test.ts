/**
 * Plans module tests: plan form handler tests.
 * Split from plans.test.ts to stay under the 500-line ceiling; the module
 * mocks live in plans-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import {
  mockConfirmDialog,
  mockOpenArcheraOfferModal,
} from './plans-harness';
import {
  setupPlanHandlers,
  _resetRampHandlersForTest,
} from '../plans';


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

  describe('provider scopes service dropdown (issue #11)', () => {
    // Match optgroups by label-prefix so both the real HTML labels
    // ("AWS"/"Azure"/"GCP") and the test fixture's labels
    // ("AWS Services"/"Azure Services"/"GCP Services") work.
    const byProvider = (prefix: string) =>
      document.querySelector(
        `#plan-service > optgroup[label^="${prefix}"]`,
      ) as HTMLOptGroupElement | null;

    beforeEach(() => {
      setupPlanHandlers();
    });

    test('initial state (provider=aws): only AWS optgroup enabled', () => {
      const aws = byProvider('AWS')!;
      const azure = byProvider('Azure')!;
      const gcp = byProvider('GCP')!;

      expect(aws.classList.contains('hidden')).toBe(false);
      expect(aws.disabled).toBe(false);
      expect(azure.classList.contains('hidden')).toBe(true);
      expect(azure.disabled).toBe(true);
      expect(gcp.classList.contains('hidden')).toBe(true);
      expect(gcp.disabled).toBe(true);
    });

    // Issue #241: the AWS Savings Plans optgroup is labeled "Savings Plans",
    // which does not contain the substring "aws". The provider select has
    // no empty option, so this runs at app init with the default provider
    // ('aws'), exactly reproducing the reported all-session lockout.
    test('initial state (provider=aws): the Savings Plans optgroup is also enabled, not hidden/disabled', () => {
      const savingsPlans = document.querySelector(
        '#plan-service > optgroup[label="Savings Plans"]',
      ) as HTMLOptGroupElement;

      expect(savingsPlans).not.toBeNull();
      expect(savingsPlans.classList.contains('hidden')).toBe(false);
      expect(savingsPlans.disabled).toBe(false);
    });

    test('switching away from aws hides the Savings Plans optgroup too', () => {
      const provider = document.getElementById('plan-provider') as HTMLSelectElement;
      provider.value = 'azure';
      provider.dispatchEvent(new Event('change'));

      const savingsPlans = document.querySelector(
        '#plan-service > optgroup[label="Savings Plans"]',
      ) as HTMLOptGroupElement;
      expect(savingsPlans.classList.contains('hidden')).toBe(true);
      expect(savingsPlans.disabled).toBe(true);
    });

    test('switching to azure enables only the Azure optgroup', () => {
      const provider = document.getElementById('plan-provider') as HTMLSelectElement;
      provider.value = 'azure';
      provider.dispatchEvent(new Event('change'));

      expect(byProvider('AWS')!.classList.contains('hidden')).toBe(true);
      expect(byProvider('AWS')!.disabled).toBe(true);
      expect(byProvider('Azure')!.classList.contains('hidden')).toBe(false);
      expect(byProvider('Azure')!.disabled).toBe(false);
      expect(byProvider('GCP')!.classList.contains('hidden')).toBe(true);
      expect(byProvider('GCP')!.disabled).toBe(true);
    });

    test('switching to gcp enables only the GCP optgroup', () => {
      const provider = document.getElementById('plan-provider') as HTMLSelectElement;
      provider.value = 'gcp';
      provider.dispatchEvent(new Event('change'));

      expect(byProvider('AWS')!.disabled).toBe(true);
      expect(byProvider('Azure')!.disabled).toBe(true);
      expect(byProvider('GCP')!.disabled).toBe(false);
      expect(byProvider('GCP')!.classList.contains('hidden')).toBe(false);
    });

    test('resets service to first visible option when AWS-only selection becomes hidden', () => {
      const provider = document.getElementById('plan-provider') as HTMLSelectElement;
      const service = document.getElementById('plan-service') as HTMLSelectElement;

      // User picks an AWS-only service, then switches provider to azure.
      service.value = 'ec2';
      provider.value = 'azure';
      provider.dispatchEvent(new Event('change'));

      // Implementation picks the first option in the first visible
      // optgroup. Assert the chosen value lives inside an optgroup
      // labelled with the selected provider — decoupling the test
      // from the specific service name in the fixture.
      const selected = service.options[service.selectedIndex];
      expect(selected).toBeDefined();
      const parent = selected?.parentElement;
      expect(parent).toBeInstanceOf(HTMLOptGroupElement);
      expect((parent as HTMLOptGroupElement).label.toLowerCase()).toMatch(/^azure/);
      expect(service.value).not.toBe('ec2');
    });
  });
});
