/**
 * Unit tests for API module (dashboard and recommendations)
 */
import { fetchMock } from './setup';
import { getDashboardSummary, getUpcomingPurchases, getRecommendations, refreshRecommendations } from '../api';
import type { UpcomingPurchasesResponse } from '../api';

describe('Dashboard API', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({})
    });
  });

  describe('getDashboardSummary', () => {
    test('fetches summary with provider filter', async () => {
      await getDashboardSummary('aws');
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/dashboard/summary?provider=aws',
        expect.anything()
      );
    });

    test('uses all providers by default (empty provider filter)', async () => {
      await getDashboardSummary();
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/dashboard/summary?provider=',
        expect.anything()
      );
    });
  });

  describe('getUpcomingPurchases', () => {
    test('fetches upcoming purchases', async () => {
      await getUpcomingPurchases();
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/dashboard/upcoming',
        expect.anything()
      );
    });

    test('sends provider and account chips as query params (#713)', async () => {
      await getUpcomingPurchases('aws', ['a1', 'a2']);
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/dashboard/upcoming?provider=aws&account_ids=a1%2Ca2',
        expect.anything()
      );
    });

    // #1189: the endpoint returns { purchases: [...] }, not a bare array. The
    // annotation makes tsc (npm run typecheck) reject a regression to the
    // old UpcomingPurchase[] return type.
    test('resolves the { purchases } envelope the handler returns', async () => {
      const row = {
        execution_id: 'exec-1', plan_id: 'plan-1', plan_name: 'Plan', scheduled_date: '2026-11-01',
        provider: 'aws', service: 'ec2', step_number: 1, total_steps: 2, estimated_savings: 10,
        has_recommendations: true,
      };
      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, headers: new Headers(), json: () => Promise.resolve({ purchases: [row] }) });
      const res: UpcomingPurchasesResponse = await getUpcomingPurchases();
      expect(Array.isArray(res)).toBe(false);
      expect(res.purchases).toEqual([row]);
    });
  });
});

describe('Recommendations API', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({})
    });
  });

  describe('getRecommendations', () => {
    test('fetches with no filters', async () => {
      await getRecommendations();
      expect(fetchMock).toHaveBeenCalledWith('/api/recommendations?limit=1000', expect.anything());
    });

    // Issue #723: the list is read in bounded pages so no single response
    // can pass the 6 MB Lambda cap.
    describe('cursor paging (issue #723)', () => {
      const page = (body: unknown) => ({ ok: true, status: 200, headers: { get: () => null }, json: () => Promise.resolve(body) });
      const urls = (): string[] => fetchMock.mock.calls.map((c) => c[0] as string);

      test('follows next_cursor, sends the filters on every page and merges the rows', async () => {
        fetchMock
          .mockResolvedValueOnce(page({ recommendations: [{ id: 'a' }, { id: 'b' }], summary: { total_count: 3 }, regions: ['us-east-1'], next_cursor: 'c1' }))
          .mockResolvedValueOnce(page({ recommendations: [{ id: 'c' }], summary: { total_count: 3 }, regions: ['us-east-1'] }));
        const result = await getRecommendations({ provider: 'aws', account_ids: ['acct-1'] }) as unknown as Record<string, unknown>;
        expect(urls()).toEqual([
          '/api/recommendations?provider=aws&account_ids=acct-1&limit=1000',
          '/api/recommendations?provider=aws&account_ids=acct-1&limit=1000&cursor=c1',
        ]);
        expect(result).toEqual({ recommendations: [{ id: 'a' }, { id: 'b' }, { id: 'c' }], summary: { total_count: 3 }, regions: ['us-east-1'] });
      });

      test('a single page (or a server that ignores limit) is one request', async () => {
        fetchMock.mockResolvedValueOnce(page({ recommendations: [{ id: 'a' }], summary: {}, regions: [] }));
        const result = await getRecommendations() as unknown as Record<string, unknown>;
        expect(fetchMock).toHaveBeenCalledTimes(1);
        expect(result).toEqual({ recommendations: [{ id: 'a' }], summary: {}, regions: [] });
      });

      test('a repeated cursor fails instead of looping', async () => {
        fetchMock
          .mockResolvedValueOnce(page({ recommendations: [{ id: 'a' }], next_cursor: 'same' }))
          .mockResolvedValueOnce(page({ recommendations: [{ id: 'b' }], next_cursor: 'same' }));
        await expect(getRecommendations()).rejects.toThrow(/cursor/i);
        expect(fetchMock).toHaveBeenCalledTimes(2);
      });

      test('stops with an error after the page cap', async () => {
        let n = 0;
        fetchMock.mockImplementation(() => Promise.resolve(page({ recommendations: [{ id: `r${n}` }], next_cursor: `c${++n}` })));
        await expect(getRecommendations()).rejects.toThrow(/pages/i);
        expect(fetchMock.mock.calls.length).toBeLessThanOrEqual(200);
      });

      test('an empty (204) page after a cursor fails instead of returning a partial list', async () => {
        fetchMock
          .mockResolvedValueOnce(page({ recommendations: [{ id: 'a' }], next_cursor: 'c1' }))
          .mockResolvedValueOnce({ ok: true, status: 204, headers: { get: () => null }, json: () => Promise.reject(new Error('no body')) });
        await expect(getRecommendations()).rejects.toThrow(/empty page/i);
      });

      test('an empty (204) first page still resolves null', async () => {
        fetchMock.mockResolvedValueOnce({ ok: true, status: 204, headers: { get: () => null }, json: () => Promise.reject(new Error('no body')) });
        await expect(getRecommendations()).resolves.toBeNull();
      });
    });

    // Regression test for issue #1089: minSavingsUsd (dollar) and minSavingsPct
    // (percentage) must map to distinct query params -- min_savings_usd and
    // min_savings_pct -- and must NEVER both map to the same legacy `min_savings`
    // key. Conflating them would silently treat a percentage as a dollar amount
    // or vice versa.
    test('minSavingsUsd maps to min_savings_usd (dollar) query param', async () => {
      await getRecommendations({
        provider: 'aws',
        service: 'ec2',
        region: 'us-east-1',
        minSavingsUsd: 100
      });

      const url = fetchMock.mock.calls[0][0] as string;
      expect(url).toContain('provider=aws');
      expect(url).toContain('service=ec2');
      expect(url).toContain('region=us-east-1');
      // Dollar filter uses the distinct usd-suffixed key.
      expect(url).toContain('min_savings_usd=100');
      // Must NOT appear as the bare (ambiguous) key or as the pct key.
      expect(url).not.toContain('min_savings=');
      expect(url).not.toContain('min_savings_pct=');
    });

    test('minSavingsPct maps to min_savings_pct (percentage) query param', async () => {
      await getRecommendations({
        minSavingsPct: 30
      });

      const url = fetchMock.mock.calls[0][0] as string;
      // Percentage filter uses the distinct pct-suffixed key.
      expect(url).toContain('min_savings_pct=30');
      // Must NOT appear as the bare (ambiguous) key or as the usd key.
      expect(url).not.toContain('min_savings=');
      expect(url).not.toContain('min_savings_usd=');
    });

    test('usd and pct filters can be combined and remain distinct', async () => {
      await getRecommendations({
        minSavingsUsd: 50,
        minSavingsPct: 20
      });

      const url = fetchMock.mock.calls[0][0] as string;
      expect(url).toContain('min_savings_usd=50');
      expect(url).toContain('min_savings_pct=20');
      // The same value "50" for usd must not appear on the pct key and vice versa.
      expect(url).not.toContain('min_savings_pct=50');
      expect(url).not.toContain('min_savings_usd=20');
      expect(url).not.toContain('min_savings=');
    });

    // Issue #65: non-finite or non-positive savings floors must never reach
    // the query string. The backend 400s on NaN/Infinity/negatives, which
    // would turn a bad widget value into an opaque load error.
    test.each([
      ['NaN', NaN],
      ['Infinity', Infinity],
      ['-Infinity', -Infinity],
      ['a negative value', -5],
      ['zero', 0],
    ])('drops min_savings_usd and min_savings_pct when the floor is %s', async (_label, value) => {
      await getRecommendations({ provider: 'aws', minSavingsUsd: value, minSavingsPct: value });

      const url = fetchMock.mock.calls[0][0] as string;
      expect(url).toBe('/api/recommendations?provider=aws&limit=1000');
      expect(url).not.toContain('min_savings_usd');
      expect(url).not.toContain('min_savings_pct');
    });

    test('a bad usd floor does not suppress a valid pct floor', async () => {
      await getRecommendations({ minSavingsUsd: Infinity, minSavingsPct: 12.5 });

      const url = fetchMock.mock.calls[0][0] as string;
      expect(url).toBe('/api/recommendations?min_savings_pct=12.5&limit=1000');
    });

    test('applies other filters to query string', async () => {
      await getRecommendations({
        provider: 'aws',
        service: 'ec2',
        region: 'us-east-1',
      });

      const url = fetchMock.mock.calls[0][0] as string;
      expect(url).toContain('provider=aws');
      expect(url).toContain('service=ec2');
      expect(url).toContain('region=us-east-1');
    });
  });

  describe('refreshRecommendations', () => {
    test('sends POST request', async () => {
      await refreshRecommendations();
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/recommendations/refresh',
        expect.objectContaining({ method: 'POST' })
      );
    });
  });
});
