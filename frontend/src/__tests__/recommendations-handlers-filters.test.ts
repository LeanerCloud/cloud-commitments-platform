/**
 * Recommendations module tests: openPurchaseModal tail, refresh and handler tests, and parseNumericFilter.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import {
  mockFactoryDefaults,
  mockShowToast,
  setupRecommendationsTest,
} from './recommendations-harness';
import {
  openPurchaseModal,
  getPurchaseModalRecommendations,
  clearPurchaseModalRecommendations,
  refreshRecommendations,
  setupRecommendationsHandlers,
  parseNumericFilter,
} from '../recommendations';
import * as api from '../api';
import * as state from '../state';

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

      test('select-all checkbox deselects all rows when unchecked', async () => {
        const recs = [makeRec('s1'), makeRec('s2'), makeRec('s3')];
        await openPurchaseModal(recs);

        const selectAll = document.getElementById('purchase-modal-select-all') as HTMLInputElement | null;
        expect(selectAll).not.toBeNull();

        selectAll!.checked = false;
        selectAll!.dispatchEvent(new Event('change'));

        const checkboxes = document.querySelectorAll<HTMLInputElement>('.purchase-modal-row-include');
        checkboxes.forEach((cb) => {
          expect(cb.checked).toBe(false);
        });
        const result = getPurchaseModalRecommendations();
        expect(result.length).toBe(0);
      });

      test('select-all checkbox re-selects all rows when checked', async () => {
        const recs = [makeRec('s1'), makeRec('s2')];
        await openPurchaseModal(recs);

        const selectAll = document.getElementById('purchase-modal-select-all') as HTMLInputElement | null;

        // Uncheck one row so select-all is in indeterminate state.
        const checkboxes = document.querySelectorAll<HTMLInputElement>('.purchase-modal-row-include');
        checkboxes[0]!.checked = false;
        checkboxes[0]!.dispatchEvent(new Event('change'));

        // Now re-check via select-all.
        selectAll!.checked = true;
        selectAll!.dispatchEvent(new Event('change'));

        checkboxes.forEach((cb) => {
          expect(cb.checked).toBe(true);
        });
        const result = getPurchaseModalRecommendations();
        expect(result.map((r) => r.id)).toEqual(['s1', 's2']);
      });

      test('totals row reflects only checked rows', async () => {
        // rec 'ta': count 2, upfront 400, savings 100
        // rec 'tb': count 3, upfront 600, savings 200
        const recs = [
          makeRec('ta', { count: 2, upfront_cost: 400, savings: 100, monthly_cost: null }),
          makeRec('tb', { count: 3, upfront_cost: 600, savings: 200, monthly_cost: null }),
        ];
        await openPurchaseModal(recs);

        // Uncheck second row.
        const checkboxes = document.querySelectorAll<HTMLInputElement>('.purchase-modal-row-include');
        checkboxes[1]!.checked = false;
        checkboxes[1]!.dispatchEvent(new Event('change'));

        // Only rec 'ta' should be in totals: count=2, upfront=400.
        const countCell = document.getElementById('purchase-modal-total-count');
        expect(countCell?.textContent).toContain('2');

        const upfrontCell = document.getElementById('purchase-modal-total-upfront');
        // formatCurrency is mocked as `$${val}` so total upfront = $400.
        expect(upfrontCell?.textContent).toContain('400');
      });

      test('totals row weighted effective % uses on_demand_cost when monthly_cost is null', async () => {
        const azureVm = { provider: 'azure' as const, service: 'compute', payment: 'monthly' };
        const recs = [
          { ...makeRec('ta', { savings: 100, upfront_cost: 0, monthly_cost: null, on_demand_cost: 200 }), ...azureVm },
          { ...makeRec('tb', { savings: 100, upfront_cost: 0, monthly_cost: 50 }), ...azureVm },
        ];
        await openPurchaseModal(recs);

        // ta: 100/200; tb (Azure, reconstructed): 100/(50+100+0) -> 200 + 150 baseline.
        const pctCell = document.getElementById('purchase-modal-total-eff-pct');
        expect(pctCell?.textContent).toContain('57.1%');
      });

      test('totals Eff. % skips AWS rows whose own Eff. % is an em-dash (#136)', async () => {
        // tb is AWS with no on_demand_cost: its row shows an em-dash, so the
        // discredited reconstruction must not leak into the total.
        const recs = [
          makeRec('ta', { savings: 100, upfront_cost: 0, monthly_cost: null, on_demand_cost: 200 }),
          makeRec('tb', { savings: 100, upfront_cost: 0, monthly_cost: 50 }),
        ];
        await openPurchaseModal(recs);

        const rowPcts = Array.from(document.querySelectorAll('#purchase-details tbody tr'))
          .map((tr) => tr.querySelector('td:nth-last-child(3)')?.textContent);
        expect(rowPcts).toEqual(['50.0%', '—']);
        expect(document.getElementById('purchase-modal-total-eff-pct')?.textContent).toBe('50.0%');
      });

      test('totals Eff. % is an em-dash when no selected row has a known Eff. %', async () => {
        await openPurchaseModal([makeRec('tb', { savings: 100, upfront_cost: 0, monthly_cost: 50 })]);

        expect(document.getElementById('purchase-modal-total-eff-pct')?.textContent).toBe('—');
      });

      test('totals Eff. % uses the provider savings_percentage the rows display', async () => {
        const recs = [
          { ...makeRec('ta', { savings: 30, upfront_cost: 0, monthly_cost: 70 }), savings_percentage: 30 },
          { ...makeRec('tb', { savings: 50, upfront_cost: 0, monthly_cost: null, on_demand_cost: 100 }), savings_percentage: 50 },
        ];
        await openPurchaseModal(recs);

        // ta implied baseline 100 at 30%, tb baseline 100 at 50% -> 40%.
        expect(document.getElementById('purchase-modal-total-eff-pct')?.textContent).toBe('40.0%');
      });

      test('clearPurchaseModalRecommendations also clears checked state', async () => {
        await openPurchaseModal([makeRec('c1'), makeRec('c2')]);

        clearPurchaseModalRecommendations();

        // After clear, getPurchaseModalRecommendations returns empty.
        const result = getPurchaseModalRecommendations();
        expect(result.length).toBe(0);
      });

      test('modal renders Account column header', async () => {
        await openPurchaseModal([makeRec('r1')]);

        const details = document.getElementById('purchase-details');
        expect(details?.textContent).toContain('Account');
      });

      test('modal renders Upfront, Monthly Cost, Eff. Savings, and Eff. % column headers', async () => {
        await openPurchaseModal([makeRec('r1')]);

        const details = document.getElementById('purchase-details');
        expect(details?.textContent).toContain('Upfront');
        expect(details?.textContent).toContain('Monthly Cost');
        expect(details?.textContent).toContain('Eff. Savings');
        expect(details?.textContent).toContain('Eff. %');
      });
    });
  });

  describe('refreshRecommendations', () => {
    test('calls API to refresh recommendations', async () => {
      (api.refreshRecommendations as jest.Mock).mockResolvedValue({});
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [],
        regions: []
      });

      await refreshRecommendations();

      expect(api.refreshRecommendations).toHaveBeenCalled();
    });

    test('shows success toast (alert replaced by showToast, finding 11-L3)', async () => {
      (api.refreshRecommendations as jest.Mock).mockResolvedValue({});

      await refreshRecommendations();

      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
        message: 'Recommendation refresh started. This may take a few minutes.',
        kind: 'success',
      }));
    });

    test('schedules reload after 5 seconds', async () => {
      (api.refreshRecommendations as jest.Mock).mockResolvedValue({});
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [],
        regions: []
      });

      await refreshRecommendations();

      jest.advanceTimersByTime(5000);

      expect(api.getRecommendations).toHaveBeenCalled();
    });

    test('shows error toast on failure (alert replaced by showToast, finding 11-L3)', async () => {
      (api.refreshRecommendations as jest.Mock).mockRejectedValue(new Error('API Error'));
      console.error = jest.fn();

      await refreshRecommendations();

      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
        message: 'Failed to start recommendation refresh',
        kind: 'error',
      }));
    });
  });

  describe('setupRecommendationsHandlers', () => {
    function makeDiv(id: string): HTMLDivElement {
      const el = document.createElement('div');
      el.id = id;
      return el;
    }

    beforeEach(() => {
      // Issue #477: Opportunities now subscribes to the global provider/account
      // filter and reloads on change. The DOM only needs the opportunities-tab
      // wrapper (active-class toggled per test) plus the elements the loader
      // touches (recommendations-list, recommendations-summary).
      while (document.body.firstChild) document.body.removeChild(document.body.firstChild);
      document.body.appendChild(makeDiv('opportunities-tab'));
      document.body.appendChild(makeDiv('recommendations-list'));
      document.body.appendChild(makeDiv('recommendations-summary'));
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [],
        regions: []
      });
      // Outer describe enables fake timers; we want real timers here so the
      // subscriber callback's promise chain inside loadRecommendations can
      // settle. afterEach() at the outer level resets via useRealTimers.
      jest.useRealTimers();
    });

    test('subscribes to provider changes and reloads when opportunities-tab is active', async () => {
      const tab = document.getElementById('opportunities-tab')!;
      tab.classList.add('active');

      setupRecommendationsHandlers();

      // Capture the callback registered with subscribeProvider and invoke it
      // directly (state is jest.mock()'d for this file, so the real listener
      // set never fires; the test verifies the wiring shape).
      const providerCb = (state.subscribeProvider as jest.Mock).mock.calls[0]?.[0];
      expect(typeof providerCb).toBe('function');
      (api.getRecommendations as jest.Mock).mockClear();
      providerCb();
      // loadRecommendations awaits Promise.all internally; flush microtasks.
      await new Promise((r) => setTimeout(r, 0));

      expect(api.getRecommendations).toHaveBeenCalled();
    });

    test('subscribes to account changes and reloads when opportunities-tab is active', async () => {
      const tab = document.getElementById('opportunities-tab')!;
      tab.classList.add('active');

      setupRecommendationsHandlers();

      const accountCb = (state.subscribeAccount as jest.Mock).mock.calls[0]?.[0];
      expect(typeof accountCb).toBe('function');
      (api.getRecommendations as jest.Mock).mockClear();
      accountCb();
      await new Promise((r) => setTimeout(r, 0));

      expect(api.getRecommendations).toHaveBeenCalled();
    });

    test('does NOT reload when opportunities-tab is inactive', async () => {
      // No .active class — user is on Home / Plans / Purchases.
      setupRecommendationsHandlers();

      const providerCb = (state.subscribeProvider as jest.Mock).mock.calls[0]?.[0];
      const accountCb = (state.subscribeAccount as jest.Mock).mock.calls[0]?.[0];
      (api.getRecommendations as jest.Mock).mockClear();
      providerCb();
      accountCb();
      await new Promise((r) => setTimeout(r, 0));

      expect(api.getRecommendations).not.toHaveBeenCalled();
    });

    // CR pass 1 (PR #488): the topbar provider-change handler updates BOTH
    // account and provider state slots in sequence, firing both subscribers
    // from a single user action. Coalesce via queueMicrotask so the user
    // sees exactly one reload, not two — and avoid the stale-overwrite race
    // where the first response lands after the second.
    test('coalesces back-to-back account+provider changes into a single reload', async () => {
      const tab = document.getElementById('opportunities-tab')!;
      tab.classList.add('active');

      setupRecommendationsHandlers();

      const providerCb = (state.subscribeProvider as jest.Mock).mock.calls[0]?.[0];
      const accountCb = (state.subscribeAccount as jest.Mock).mock.calls[0]?.[0];
      (api.getRecommendations as jest.Mock).mockClear();

      // Simulate the topbar's #185 ordering: clear account first, then set
      // provider. Both callbacks fire synchronously; coalescing must collapse
      // them into one microtask-deferred reload.
      accountCb();
      providerCb();
      await new Promise((r) => setTimeout(r, 0));

      expect(api.getRecommendations).toHaveBeenCalledTimes(1);
    });
  });
});

//
// Bundle A: numeric expression parser + applyColumnFilters
//

describe('parseNumericFilter', () => {
  const accept = (expr: string, n: number): boolean => {
    const r = parseNumericFilter(expr);
    if (!r.ok) throw new Error(`unexpected parse failure for "${expr}": ${r.error}`);
    return r.predicate(n);
  };

  test('empty / whitespace expression matches everything', () => {
    expect(accept('', 0)).toBe(true);
    expect(accept('   ', 42)).toBe(true);
    expect(accept('\t\n', -7)).toBe(true);
  });

  test('plain integer matches by equality', () => {
    expect(accept('42', 42)).toBe(true);
    expect(accept('42', 41)).toBe(false);
    expect(accept('-3', -3)).toBe(true);
  });

  test('plain decimal matches by equality', () => {
    expect(accept('3.14', 3.14)).toBe(true);
    expect(accept('3.14', 3.15)).toBe(false);
  });

  test('comparator > / >= / < / <=', () => {
    expect(accept('>10', 11)).toBe(true);
    expect(accept('>10', 10)).toBe(false);
    expect(accept('>=10', 10)).toBe(true);
    expect(accept('<5', 4)).toBe(true);
    expect(accept('<5', 5)).toBe(false);
    expect(accept('<=5', 5)).toBe(true);
  });

  test('inclusive range X..Y', () => {
    expect(accept('10..20', 10)).toBe(true);
    expect(accept('10..20', 20)).toBe(true);
    expect(accept('10..20', 15)).toBe(true);
    expect(accept('10..20', 9)).toBe(false);
    expect(accept('10..20', 21)).toBe(false);
  });

  test('reversed range still works (max..min)', () => {
    expect(accept('20..10', 15)).toBe(true);
  });

  test('comma-separated terms OR together', () => {
    // `5, >100, 200..400`
    expect(accept('5, >100, 200..400', 5)).toBe(true);
    expect(accept('5, >100, 200..400', 150)).toBe(true);
    expect(accept('5, >100, 200..400', 250)).toBe(true);
    expect(accept('5, >100, 200..400', 50)).toBe(false);
    expect(accept('5, >100, 200..400', 500)).toBe(true); // matches >100
  });

  test('whitespace inside terms is tolerated', () => {
    expect(accept('  >  10  ', 11)).toBe(true);
    expect(accept('10 .. 20', 15)).toBe(true);
  });
});
