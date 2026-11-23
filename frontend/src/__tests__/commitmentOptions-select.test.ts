/**
 * Tests for commitmentOptions: select population, payment labels and normalization, and the fetch overlay.
 */
import {
  fetchAndPopulateCommitmentOptions,
  getCommitmentConfig,
  isValidCombination,
  populateTermSelect,
  populatePaymentSelect,
  getPaymentLabel,
  normalizePaymentValue,
  CommitmentConfig,
  PaymentOption,
  TermOption,
} from '../commitmentOptions';

describe('commitmentOptions', () => {
  describe('populateTermSelect', () => {
    let selectElement: HTMLSelectElement;

    beforeEach(() => {
      selectElement = document.createElement('select');
      document.body.appendChild(selectElement);
    });

    it('should populate select with all term options when no payment specified', () => {
      populateTermSelect(selectElement, 'aws', 'ec2');

      expect(selectElement.options).toHaveLength(2);
      expect(selectElement.options[0]!.value).toBe('1');
      expect(selectElement.options[0]!.text).toBe('1 Year');
      expect(selectElement.options[1]!.value).toBe('3');
      expect(selectElement.options[1]!.text).toBe('3 Years');
    });

    it('should populate select with filtered terms based on payment', () => {
      populateTermSelect(selectElement, 'aws', 'rds', 'no-upfront');

      expect(selectElement.options).toHaveLength(1);
      expect(selectElement.options[0]!.value).toBe('1');
      expect(selectElement.options[0]!.text).toBe('1 Year');
    });

    it('should preserve current selection if still valid', () => {
      // First populate with all options
      populateTermSelect(selectElement, 'aws', 'ec2');
      selectElement.value = '3';

      // Repopulate - should preserve selection
      populateTermSelect(selectElement, 'aws', 'ec2', 'all-upfront');

      expect(selectElement.value).toBe('3');
    });

    it('should not preserve selection if no longer valid', () => {
      // First populate with all options and select 3 years
      populateTermSelect(selectElement, 'aws', 'rds');
      selectElement.value = '3';

      // Repopulate with restricted options
      populateTermSelect(selectElement, 'aws', 'rds', 'no-upfront');

      // Value should be first available option
      expect(selectElement.value).toBe('1');
    });

    it('should handle Azure provider', () => {
      populateTermSelect(selectElement, 'azure', 'vm');

      expect(selectElement.options).toHaveLength(2);
    });

    it('should handle GCP provider', () => {
      populateTermSelect(selectElement, 'gcp', 'compute');

      expect(selectElement.options).toHaveLength(2);
    });

    it('should clear existing options before populating', () => {
      // Add some initial options
      selectElement.innerHTML = '<option value="old">Old Option</option>';

      populateTermSelect(selectElement, 'aws', 'ec2');

      expect(selectElement.options).toHaveLength(2);
      expect(selectElement.querySelector('option[value="old"]')).toBeNull();
    });

    it('should handle undefined service', () => {
      populateTermSelect(selectElement, 'aws');

      expect(selectElement.options).toHaveLength(2);
    });
  });

  describe('populatePaymentSelect', () => {
    let selectElement: HTMLSelectElement;

    beforeEach(() => {
      selectElement = document.createElement('select');
      document.body.appendChild(selectElement);
    });

    it('should populate select with all payment options when no term specified', () => {
      populatePaymentSelect(selectElement, 'aws', 'ec2');

      expect(selectElement.options).toHaveLength(3);
      expect(selectElement.options[0]!.value).toBe('no-upfront');
      expect(selectElement.options[0]!.text).toBe('No Upfront');
      expect(selectElement.options[1]!.value).toBe('partial-upfront');
      expect(selectElement.options[1]!.text).toBe('Partial Upfront');
      expect(selectElement.options[2]!.value).toBe('all-upfront');
      expect(selectElement.options[2]!.text).toBe('All Upfront');
    });

    it('should populate select with filtered payments based on term', () => {
      populatePaymentSelect(selectElement, 'aws', 'rds', 3);

      expect(selectElement.options).toHaveLength(2);
      expect(selectElement.options[0]!.value).toBe('partial-upfront');
      expect(selectElement.options[1]!.value).toBe('all-upfront');
    });

    it('should preserve current selection if still valid', () => {
      // First populate with all options
      populatePaymentSelect(selectElement, 'aws', 'ec2');
      selectElement.value = 'all-upfront';

      // Repopulate - should preserve selection
      populatePaymentSelect(selectElement, 'aws', 'ec2', 1);

      expect(selectElement.value).toBe('all-upfront');
    });

    it('should not preserve selection if no longer valid', () => {
      // First populate with all options and select no-upfront
      populatePaymentSelect(selectElement, 'aws', 'rds');
      selectElement.value = 'no-upfront';

      // Repopulate with 3-year restriction
      populatePaymentSelect(selectElement, 'aws', 'rds', 3);

      // Value should be first available option
      expect(selectElement.value).toBe('partial-upfront');
    });

    it('should handle Azure provider', () => {
      populatePaymentSelect(selectElement, 'azure', 'vm');

      expect(selectElement.options).toHaveLength(2);
      expect(selectElement.options[0]!.value).toBe('upfront');
      expect(selectElement.options[1]!.value).toBe('monthly');
    });

    it('should handle GCP provider', () => {
      populatePaymentSelect(selectElement, 'gcp', 'compute');

      expect(selectElement.options).toHaveLength(1);
      expect(selectElement.options[0]!.value).toBe('monthly');
    });

    it('should clear existing options before populating', () => {
      // Add some initial options
      selectElement.innerHTML = '<option value="old">Old Option</option>';

      populatePaymentSelect(selectElement, 'aws', 'ec2');

      expect(selectElement.options).toHaveLength(3);
      expect(selectElement.querySelector('option[value="old"]')).toBeNull();
    });

    it('should handle undefined service', () => {
      populatePaymentSelect(selectElement, 'aws');

      expect(selectElement.options).toHaveLength(3);
    });

    it('should handle 1-year term with all options available', () => {
      populatePaymentSelect(selectElement, 'aws', 'rds', 1);

      // 1-year has no restrictions
      expect(selectElement.options).toHaveLength(3);
    });
  });

  describe('getPaymentLabel', () => {
    describe('AWS payment values', () => {
      it('should return correct label for no-upfront', () => {
        expect(getPaymentLabel('no-upfront')).toBe('No Upfront');
      });

      it('should return correct label for partial-upfront', () => {
        expect(getPaymentLabel('partial-upfront')).toBe('Partial Upfront');
      });

      it('should return correct label for all-upfront', () => {
        expect(getPaymentLabel('all-upfront')).toBe('All Upfront');
      });
    });

    describe('Azure payment values', () => {
      it('should return correct label for upfront', () => {
        expect(getPaymentLabel('upfront')).toBe('Pay Upfront');
      });

      it('should return correct label for monthly', () => {
        // Azure 'monthly' label is 'Pay Monthly' (comes before GCP in the array)
        expect(getPaymentLabel('monthly')).toBe('Pay Monthly');
      });
    });

    describe('GCP payment values', () => {
      it('should note that monthly matches Azure first due to array order', () => {
        // Note: GCP monthly has same value as Azure but Azure comes first in the array
        // So 'monthly' returns Azure's 'Pay Monthly' label, not GCP's 'Monthly'
        // This is a quirk of the implementation - testing actual behavior
        expect(getPaymentLabel('monthly')).toBe('Pay Monthly');
      });
    });

    describe('Unknown values', () => {
      it('should return the value itself for unknown payment type', () => {
        expect(getPaymentLabel('unknown-payment')).toBe('unknown-payment');
      });

      it('should return empty string for empty input', () => {
        expect(getPaymentLabel('')).toBe('');
      });
    });
  });

  describe('normalizePaymentValue', () => {
    describe('Azure normalization', () => {
      it('should convert all-upfront to upfront for Azure', () => {
        expect(normalizePaymentValue('all-upfront', 'azure')).toBe('upfront');
      });

      // Regression test for #1503: partial-upfront has no Azure equivalent
      // and must land on 'monthly', never 'upfront'. Azure's billing plan is
      // immutable after purchase and both plans cost the same total, so
      // pre-selecting 'upfront' would hand the user an irreversible full
      // upfront charge they never chose. Mirrors the Go-side assertion in
      // internal/config/validation_test.go:TestNormalizePaymentOption.
      it('should convert partial-upfront to monthly (not upfront) for Azure', () => {
        expect(normalizePaymentValue('partial-upfront', 'azure')).toBe('monthly');
      });

      it('should convert no-upfront to monthly for Azure', () => {
        expect(normalizePaymentValue('no-upfront', 'azure')).toBe('monthly');
      });

      it('should preserve native Azure values', () => {
        expect(normalizePaymentValue('upfront', 'azure')).toBe('upfront');
        expect(normalizePaymentValue('monthly', 'azure')).toBe('monthly');
      });
    });

    describe('GCP normalization', () => {
      it('should convert all values to monthly for GCP', () => {
        expect(normalizePaymentValue('no-upfront', 'gcp')).toBe('monthly');
        expect(normalizePaymentValue('partial-upfront', 'gcp')).toBe('monthly');
        expect(normalizePaymentValue('all-upfront', 'gcp')).toBe('monthly');
        expect(normalizePaymentValue('monthly', 'gcp')).toBe('monthly');
        expect(normalizePaymentValue('any-value', 'gcp')).toBe('monthly');
      });
    });

    describe('AWS normalization', () => {
      it('should preserve AWS values unchanged', () => {
        expect(normalizePaymentValue('no-upfront', 'aws')).toBe('no-upfront');
        expect(normalizePaymentValue('partial-upfront', 'aws')).toBe('partial-upfront');
        expect(normalizePaymentValue('all-upfront', 'aws')).toBe('all-upfront');
      });
    });

    describe('Unknown provider', () => {
      it('should preserve value for unknown provider', () => {
        expect(normalizePaymentValue('some-value', 'unknown')).toBe('some-value');
      });
    });
  });

  describe('Type exports', () => {
    it('should export CommitmentConfig interface', () => {
      const config: CommitmentConfig = {
        terms: [{ value: 1, label: '1 Year' }],
        payments: [{ value: 'monthly', label: 'Monthly' }]
      };
      expect(config.terms).toHaveLength(1);
      expect(config.payments).toHaveLength(1);
    });

    it('should export PaymentOption interface', () => {
      const option: PaymentOption = { value: 'test', label: 'Test' };
      expect(option.value).toBe('test');
      expect(option.label).toBe('Test');
    });

    it('should export TermOption interface', () => {
      const option: TermOption = { value: 1, label: '1 Year' };
      expect(option.value).toBe(1);
      expect(option.label).toBe('1 Year');
    });

    it('should support optional invalidCombinations in CommitmentConfig', () => {
      const configWithoutInvalid: CommitmentConfig = {
        terms: [{ value: 1, label: '1 Year' }],
        payments: [{ value: 'monthly', label: 'Monthly' }]
      };

      const configWithInvalid: CommitmentConfig = {
        terms: [{ value: 1, label: '1 Year' }],
        payments: [{ value: 'monthly', label: 'Monthly' }],
        invalidCombinations: [{ term: 3, payment: 'no-upfront' }]
      };

      expect(configWithoutInvalid.invalidCombinations).toBeUndefined();
      expect(configWithInvalid.invalidCombinations).toHaveLength(1);
    });
  });

  describe('fetchAndPopulateCommitmentOptions', () => {
    // These tests mutate the shared commitmentConfigs module state via the
    // function under test. Restore a known-good overlay at the end of each
    // test by re-populating with the hardcoded fallback shape (no supported
    // combos → full invalidCombinations → close enough to the starting
    // state for subsequent tests to see consistent results).
    const mockOk = (aws: Record<string, Array<{ term: number; payment: string }>>) =>
      jest.fn().mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ status: 'ok', aws })
      } as Response);

    afterEach(() => {
      // Reset RDS back to hardcoded — other tests in this file assert RDS
      // retains `{term:3, payment:'no-upfront'}` as invalid.
      return fetchAndPopulateCommitmentOptions(
        mockOk({ rds: [
          { term: 1, payment: 'all-upfront' },
          { term: 1, payment: 'partial-upfront' },
          { term: 1, payment: 'no-upfront' },
          { term: 3, payment: 'all-upfront' },
          { term: 3, payment: 'partial-upfront' },
        ]})
      );
    });

    it('overlays server-supplied combos onto AWS services', async () => {
      // Server says RDS supports ONLY 1yr all-upfront — everything else is
      // invalid. Overlay should reflect that exactly.
      const fetchMock = mockOk({
        rds: [{ term: 1, payment: 'all-upfront' }],
      });

      await fetchAndPopulateCommitmentOptions(fetchMock);

      expect(fetchMock).toHaveBeenCalledWith('/api/commitment-options');
      expect(isValidCombination('aws', 'rds', 1, 'all-upfront')).toBe(true);
      expect(isValidCombination('aws', 'rds', 1, 'partial-upfront')).toBe(false);
      expect(isValidCombination('aws', 'rds', 3, 'no-upfront')).toBe(false);
    });

    it('leaves hardcoded rules intact on status:unavailable', async () => {
      const fetchMock = jest.fn().mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ status: 'unavailable' })
      } as Response);

      await fetchAndPopulateCommitmentOptions(fetchMock);

      // RDS hardcoded rule: 3yr no-upfront invalid, others valid.
      expect(isValidCombination('aws', 'rds', 3, 'no-upfront')).toBe(false);
      expect(isValidCombination('aws', 'rds', 1, 'all-upfront')).toBe(true);
    });

    it('leaves hardcoded rules intact on HTTP error', async () => {
      const fetchMock = jest.fn().mockResolvedValue({ ok: false } as Response);

      await fetchAndPopulateCommitmentOptions(fetchMock);

      expect(isValidCombination('aws', 'rds', 3, 'no-upfront')).toBe(false);
      expect(isValidCombination('aws', 'rds', 1, 'all-upfront')).toBe(true);
    });

    it('leaves hardcoded rules intact on network error', async () => {
      const fetchMock = jest.fn().mockRejectedValue(new Error('network down'));

      await fetchAndPopulateCommitmentOptions(fetchMock);

      expect(isValidCombination('aws', 'rds', 3, 'no-upfront')).toBe(false);
    });

    // #320: a "__proto__" service key in the response must not replace the
    // prototype of the AWS config table; the other keys still apply.
    it('skips a __proto__ service key from the server', async () => {
      const body = JSON.parse('{"status":"ok","aws":{"__proto__":[{"term":1,"payment":"all-upfront"}],"rds":[{"term":1,"payment":"all-upfront"}]}}');
      const fetchMock = jest.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve(body) } as Response);

      await fetchAndPopulateCommitmentOptions(fetchMock);

      expect(isValidCombination('aws', 'rds', 1, 'partial-upfront')).toBe(false);
      const config = getCommitmentConfig('aws', 'terms');
      expect(config.terms).toHaveLength(2);
      expect(config.invalidCombinations).toBeUndefined();
    });

    it('clears invalidCombinations when server reports full support', async () => {
      const allCombos = [1, 3].flatMap(term =>
        ['all-upfront', 'partial-upfront', 'no-upfront'].map(payment => ({ term, payment }))
      );
      const fetchMock = mockOk({ rds: allCombos });

      await fetchAndPopulateCommitmentOptions(fetchMock);

      // Every combo now valid — previously 3yr no-upfront was blocked.
      expect(isValidCombination('aws', 'rds', 3, 'no-upfront')).toBe(true);
      expect(isValidCombination('aws', 'rds', 1, 'partial-upfront')).toBe(true);
    });

    it('re-widens after a subsequent overlay reports broader support', async () => {
      // Settings tab is re-entered after the server probe completes with
      // a fuller combo set. The overlay must diff against the canonical
      // STANDARD_TERMS × AWS_PAYMENTS product, not against the narrow
      // result of the first call, or the UI stays stuck on the
      // intersection and never widens.
      const narrow = mockOk({ rds: [{ term: 1, payment: 'all-upfront' }] });
      await fetchAndPopulateCommitmentOptions(narrow);
      expect(isValidCombination('aws', 'rds', 3, 'no-upfront')).toBe(false);

      const allCombos = [1, 3].flatMap(term =>
        ['all-upfront', 'partial-upfront', 'no-upfront'].map(payment => ({ term, payment }))
      );
      const wide = mockOk({ rds: allCombos });
      await fetchAndPopulateCommitmentOptions(wide);

      expect(isValidCombination('aws', 'rds', 3, 'no-upfront')).toBe(true);
      expect(isValidCombination('aws', 'rds', 1, 'partial-upfront')).toBe(true);
    });
  });
});
