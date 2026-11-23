/**
 * Recommendations module tests: openPurchaseModal tail, refresh and handler tests, and parseNumericFilter.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults, setupRecommendationsTest } from './recommendations-harness';
import { openPurchaseModal, getPurchaseModalRecommendations } from '../recommendations';

beforeEach(() => {
  for (const [fn, factoryDefault] of mockFactoryDefaults) {
    fn.mockReset();
    if (factoryDefault) fn.mockImplementation(factoryDefault);
  }
});

describe('Recommendations Module', () => {
  beforeEach(setupRecommendationsTest);

  afterEach(() => {
    jest.useRealTimers();
  });

  describe('openPurchaseModal', () => {
    // Issue #320: per-row checkboxes, select-all, live totals, Execute button state.
    describe('per-row skip checkboxes (issue #320)', () => {
      const makeRec = (id: string, opts: Partial<{
        savings: number; upfront_cost: number; monthly_cost: number | null;
        on_demand_cost: number | null; count: number; term: number;
      }> = {}) => ({
        id,
        provider: 'aws' as const,
        service: 'ec2',
        resource_type: 't3.medium',
        region: 'us-east-1',
        count: opts.count ?? 1,
        term: opts.term ?? 1,
        payment: 'partial-upfront',
        savings: opts.savings ?? 100,
        upfront_cost: opts.upfront_cost ?? 600,
        monthly_cost: opts.monthly_cost !== undefined ? opts.monthly_cost : 400,
        on_demand_cost: opts.on_demand_cost !== undefined ? opts.on_demand_cost : null,
      });

      test('unchecking a row excludes it from getPurchaseModalRecommendations', async () => {
        const recs = [makeRec('r1'), makeRec('r2'), makeRec('r3')];
        await openPurchaseModal(recs);

        // Uncheck the second row (index 1).
        const checkboxes = document.querySelectorAll<HTMLInputElement>('.purchase-modal-row-include');
        checkboxes[1]!.checked = false;
        checkboxes[1]!.dispatchEvent(new Event('change'));

        const result = getPurchaseModalRecommendations();
        expect(result.map((r) => r.id)).toEqual(['r1', 'r3']);
      });

      test('unchecking all rows disables the Execute Purchase button', async () => {
        await openPurchaseModal([makeRec('r1'), makeRec('r2')]);

        const checkboxes = document.querySelectorAll<HTMLInputElement>('.purchase-modal-row-include');
        checkboxes.forEach((cb) => {
          cb.checked = false;
          cb.dispatchEvent(new Event('change'));
        });

        const btn = document.getElementById('execute-purchase-btn') as HTMLButtonElement | null;
        expect(btn?.disabled).toBe(true);
      });

      test('re-checking at least one row re-enables the Execute Purchase button', async () => {
        await openPurchaseModal([makeRec('r1'), makeRec('r2')]);

        const checkboxes = document.querySelectorAll<HTMLInputElement>('.purchase-modal-row-include');
        // Uncheck all first.
        checkboxes.forEach((cb) => {
          cb.checked = false;
          cb.dispatchEvent(new Event('change'));
        });
        // Re-check first row.
        checkboxes[0]!.checked = true;
        checkboxes[0]!.dispatchEvent(new Event('change'));

        const btn = document.getElementById('execute-purchase-btn') as HTMLButtonElement | null;
        expect(btn?.disabled).toBe(false);
      });

      test('Execute button starts enabled when all rows are checked', async () => {
        await openPurchaseModal([makeRec('r1')]);

        const btn = document.getElementById('execute-purchase-btn') as HTMLButtonElement | null;
        expect(btn?.disabled).toBe(false);
      });
    });
  });
});
