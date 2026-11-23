/**
 * Tests for commitmentOptions: the Azure server overlay and the payment adjustment notice (#1503).
 */
import {
  fetchAndPopulateCommitmentOptions,
  getCommitmentConfig,
  isValidCombination,
  formatPaymentAdjustmentNotice,
} from '../commitmentOptions';
import type { PaymentAdjustment } from '../api/types';

describe('commitmentOptions', () => {
  describe('fetchAndPopulateCommitmentOptions azure overlay', () => {
    type Combos = Array<{ term: number; payment: string }>;
    const respond = (body: unknown) =>
      jest.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve(body) } as Response);
    const full: Combos = [1, 3].flatMap(term => ['upfront', 'monthly'].map(payment => ({ term, payment })));
    const spInvalid = () => getCommitmentConfig('azure', 'savingsplans').invalidCombinations;

    afterEach(() => fetchAndPopulateCommitmentOptions(respond({ status: 'ok', azure: { savingsplans: full } })));

    it('narrows savingsplans from an azure-only response', async () => {
      await fetchAndPopulateCommitmentOptions(
        respond({ status: 'ok', azure: { savingsplans: [{ term: 1, payment: 'upfront' }, { term: 3, payment: 'upfront' }, { term: 3, payment: 'monthly' }] } }),
      );
      expect(isValidCombination('azure', 'savingsplans', 1, 'monthly')).toBe(false);
      expect(isValidCombination('azure', 'savingsplans', 3, 'monthly')).toBe(true);
      expect(isValidCombination('azure', 'savingsplans', 1, 'upfront')).toBe(true);
      // VM reservations (the _default) are untouched.
      expect(getCommitmentConfig('azure', 'vm').invalidCombinations).toBeUndefined();
      expect(getCommitmentConfig('azure').invalidCombinations).toBeUndefined();
    });

    it('applies aws and azure sections together', async () => {
      await fetchAndPopulateCommitmentOptions(
        respond({
          status: 'ok',
          aws: { rds: [{ term: 1, payment: 'all-upfront' }] },
          azure: { savingsplans: [{ term: 1, payment: 'upfront' }] },
        }),
      );
      expect(isValidCombination('aws', 'rds', 1, 'partial-upfront')).toBe(false);
      expect(isValidCombination('azure', 'savingsplans', 3, 'upfront')).toBe(false);
      expect(isValidCombination('azure', 'savingsplans', 1, 'upfront')).toBe(true);
      await fetchAndPopulateCommitmentOptions(
        respond({ status: 'ok', aws: { rds: [{ term: 1, payment: 'all-upfront' }, { term: 1, payment: 'partial-upfront' }, { term: 1, payment: 'no-upfront' }, { term: 3, payment: 'all-upfront' }, { term: 3, payment: 'partial-upfront' }] } }),
      );
    });

    it('leaves azure unchanged when there is no azure key', async () => {
      const before = JSON.parse(JSON.stringify(getCommitmentConfig('azure', 'savingsplans')));
      await fetchAndPopulateCommitmentOptions(respond({ status: 'ok', aws: {} }));
      expect(spInvalid()).toBeUndefined();
      expect(JSON.parse(JSON.stringify(getCommitmentConfig('azure', 'savingsplans')))).toEqual(before);
    });

    it('narrows by the well-formed entries when malformed ones are mixed in', async () => {
      await expect(
        fetchAndPopulateCommitmentOptions(
          respond({ status: 'ok', azure: { savingsplans: [null, 5, { term: 1, payment: 'upfront' }] } }),
        ),
      ).resolves.toBeUndefined();
      expect(isValidCombination('azure', 'savingsplans', 1, 'upfront')).toBe(true);
      expect(isValidCombination('azure', 'savingsplans', 3, 'upfront')).toBe(false);
    });

    it('applies nothing when status is unavailable even with an azure body', async () => {
      await fetchAndPopulateCommitmentOptions(
        respond({ status: 'unavailable', azure: { savingsplans: [{ term: 1, payment: 'upfront' }] } }),
      );
      expect(spInvalid()).toBeUndefined();
    });

    it('ignores an empty azure map and an empty savingsplans list', async () => {
      await fetchAndPopulateCommitmentOptions(respond({ status: 'ok', azure: {} }));
      expect(spInvalid()).toBeUndefined();
      await fetchAndPopulateCommitmentOptions(respond({ status: 'ok', azure: { savingsplans: [] } }));
      expect(spInvalid()).toBeUndefined();
    });

    it('ignores unknown services, unsafe keys and garbage values', async () => {
      const unsafe = JSON.parse('{"status":"ok","azure":{"__proto__":[{"term":1,"payment":"upfront"}],"constructor":[{"term":1,"payment":"upfront"}],"vm":[{"term":1,"payment":"upfront"}]}}');
      await fetchAndPopulateCommitmentOptions(respond(unsafe));
      expect(spInvalid()).toBeUndefined();
      expect(getCommitmentConfig('azure', 'vm').invalidCombinations).toBeUndefined();
      expect(getCommitmentConfig('azure', 'constructor').invalidCombinations).toBeUndefined();
      for (const azure of [null, 'x', 7, ['a'], { savingsplans: 'x' }, { savingsplans: [null, 5, { term: '1', payment: 'upfront' }] }]) {
        await fetchAndPopulateCommitmentOptions(respond({ status: 'ok', azure }));
        expect(spInvalid()).toBeUndefined();
      }
    });

    it('never widens: unknown terms and payments add no option and monthly stays offered', async () => {
      await fetchAndPopulateCommitmentOptions(
        respond({ status: 'ok', azure: { savingsplans: [{ term: 5, payment: 'upfront' }, { term: 1, payment: 'partial-upfront' }, { term: 1, payment: 'upfront' }] } }),
      );
      const cfg = getCommitmentConfig('azure', 'savingsplans');
      expect(cfg.terms.map(t => t.value)).toEqual([1, 3]);
      expect(cfg.payments.map(p => p.value)).toEqual(['upfront', 'monthly']);
      expect(isValidCombination('azure', 'savingsplans', 5, 'upfront')).toBe(true); // term 5 is not offered, so nothing to narrow
      expect(isValidCombination('azure', 'savingsplans', 1, 'partial-upfront')).toBe(true);
      // Full probed support leaves monthly exactly as offered today.
      await fetchAndPopulateCommitmentOptions(respond({ status: 'ok', azure: { savingsplans: full } }));
      const cfg2 = getCommitmentConfig('azure', 'savingsplans');
      expect(cfg2.payments.map(p => p.value)).toEqual(['upfront', 'monthly']);
      expect(cfg2.invalidCombinations).toBeUndefined();
    });

    it('re-widens when a later response reports broader support', async () => {
      await fetchAndPopulateCommitmentOptions(respond({ status: 'ok', azure: { savingsplans: [{ term: 1, payment: 'upfront' }] } }));
      expect(isValidCombination('azure', 'savingsplans', 3, 'monthly')).toBe(false);
      await fetchAndPopulateCommitmentOptions(respond({ status: 'ok', azure: { savingsplans: full } }));
      expect(isValidCombination('azure', 'savingsplans', 3, 'monthly')).toBe(true);
    });
  });

  // #1503: the backend coerces a payment option the target provider cannot
  // express (Azure has only Upfront/Monthly) instead of rejecting it. That is
  // only acceptable if the change is disclosed, so this formatter is the copy
  // every purchase-submit path renders. A null return means "nothing to say".
  describe('formatPaymentAdjustmentNotice', () => {
    const adj = (over: Partial<PaymentAdjustment> = {}): PaymentAdjustment => ({
      rec_index: 0,
      provider: 'azure',
      service: 'vm',
      requested_payment_option: 'partial-upfront',
      applied_payment_option: 'monthly',
      reason: 'no azure equivalent',
      ...over,
    });

    it('returns null when there is nothing to disclose', () => {
      expect(formatPaymentAdjustmentNotice(undefined)).toBeNull();
      expect(formatPaymentAdjustmentNotice([])).toBeNull();
    });

    it('names both the requested and the applied schedule for a single rec', () => {
      const msg = formatPaymentAdjustmentNotice([adj()]);
      // Human labels, not raw API tokens -- the user picked "Partial Upfront"
      // in the UI, so that is the wording they will recognise.
      expect(msg).toContain('Partial Upfront');
      expect(msg).toContain('Pay Monthly');
      expect(msg).toContain('AZURE');
      expect(msg).toContain('vm');
      // The raw tokens must not leak into user-facing copy.
      expect(msg).not.toContain('partial-upfront');
    });

    it('shows the service display name, not the raw slug', () => {
      const msg = formatPaymentAdjustmentNotice([adj({ provider: 'aws', service: 'rds' })]);
      expect(msg).toContain('AWS RDS does not offer');
      expect(msg).not.toContain('rds');
    });

    it('collapses a batch to the distinct requested -> applied pairs', () => {
      const msg = formatPaymentAdjustmentNotice([
        adj({ rec_index: 0 }),
        adj({ rec_index: 1 }),
        adj({ rec_index: 2 }),
      ]);
      // Count reflects every affected rec...
      expect(msg).toContain('3 recommendations');
      // ...but the identical mapping is stated once, not three times.
      expect(msg?.match(/Partial Upfront/g)).toHaveLength(1);
    });

    it('lists every distinct mapping when a batch was coerced differently', () => {
      const msg = formatPaymentAdjustmentNotice([
        adj({ requested_payment_option: 'partial-upfront', applied_payment_option: 'monthly' }),
        adj({ requested_payment_option: 'all-upfront', applied_payment_option: 'upfront' }),
      ]);
      expect(msg).toContain('2 recommendations');
      expect(msg).toContain('Partial Upfront');
      expect(msg).toContain('All Upfront');
      expect(msg).toContain('Pay Monthly');
      expect(msg).toContain('Pay Upfront');
    });
  });
});
