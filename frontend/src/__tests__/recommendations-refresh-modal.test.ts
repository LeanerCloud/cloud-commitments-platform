/**
 * Recommendations module tests: auto-refresh on page open and the first openPurchaseModal tests.
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
  loadRecommendations,
  openPurchaseModal,
  getPurchaseModalRecommendations,
  resetAutoRefreshInFlight,
} from '../recommendations';
import * as api from '../api';
import * as state from '../state';
import * as recsApi from '../api/recommendations';

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

  describe('auto-refresh on page open (#284)', () => {
    // Helper: set up the minimal DOM + api mock that loadRecommendations needs.
    function mockGetRecs() {
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [],
        regions: [],
      });
    }

    beforeEach(() => {
      // Reset the dedup guard so each test starts with no refresh in flight.
      resetAutoRefreshInFlight();
    });

    test('202 accepted — success toast only after the started collect finishes', async () => {
      mockGetRecs();
      (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValueOnce({
        last_collected_at: null,
        last_collection_error: null,
      });
      (recsApi.refreshRecommendations as jest.Mock).mockResolvedValue({
        started_at: new Date().toISOString(),
        last_collected_at: null,
      });
      let collecting = true;
      (recsApi.getRecommendationsFreshness as jest.Mock).mockImplementation(async () => ({
        last_collected_at: collecting ? null : new Date().toISOString(),
        last_collection_error: null,
        last_collection_started_at: collecting ? new Date().toISOString() : null,
      }));

      await loadRecommendations();
      await Promise.resolve();
      await Promise.resolve();
      await jest.advanceTimersByTimeAsync(10_000);

      // Marker still held: nothing announced and no reload yet.
      expect(mockShowToast).not.toHaveBeenCalledWith(
        expect.objectContaining({ message: 'Recommendations refreshed' }),
      );
      expect(api.getRecommendations).toHaveBeenCalledTimes(1);

      collecting = false;
      await jest.advanceTimersByTimeAsync(5_000);

      expect(mockShowToast).toHaveBeenCalledWith(
        expect.objectContaining({ message: 'Recommendations refreshed', kind: 'success' }),
      );
      expect(mockShowToast).not.toHaveBeenCalledWith(
        expect.objectContaining({ kind: 'error' }),
      );
      expect(api.getRecommendations).toHaveBeenCalledTimes(2);
    });

    test('202 accepted — explicit failure when the started collect outlasts the poll budget', async () => {
      mockGetRecs();
      (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValueOnce({
        last_collected_at: null,
        last_collection_error: null,
      });
      (recsApi.refreshRecommendations as jest.Mock).mockResolvedValue({
        started_at: new Date().toISOString(),
        last_collected_at: null,
      });
      (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
        last_collected_at: null,
        last_collection_error: null,
        last_collection_started_at: new Date().toISOString(),
      });

      await loadRecommendations();
      await Promise.resolve();
      await Promise.resolve();
      await jest.advanceTimersByTimeAsync(60 * 5_000);

      expect(mockShowToast).toHaveBeenCalledWith(
        expect.objectContaining({
          message: 'Recommendations refresh is taking longer than expected; try again shortly',
          kind: 'error',
        }),
      );
      expect(mockShowToast).not.toHaveBeenCalledWith(
        expect.objectContaining({ message: 'Recommendations refreshed' }),
      );
      expect(api.getRecommendations).toHaveBeenCalledTimes(1);
    });

    test('dedup — concurrent stale loads fire refreshRecommendations only once', async () => {
      mockGetRecs();
      (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
        last_collected_at: null,
        last_collection_error: null,
      });

      // A pending promise that won't resolve until we call resolveRefresh().
      let resolveRefresh!: () => void;
      const pendingRefresh = new Promise<void>((r) => { resolveRefresh = r; });
      (recsApi.refreshRecommendations as jest.Mock).mockReturnValue(pendingRefresh);

      // Fire two stale loads without awaiting the first refresh to settle.
      const first = loadRecommendations();
      const second = loadRecommendations();

      await first;
      await second;
      // Flush the microtasks that kick off triggerAutoRefreshIfStale.
      await Promise.resolve();
      await Promise.resolve();

      // Only one API call despite two stale-load triggers.
      expect(recsApi.refreshRecommendations).toHaveBeenCalledTimes(1);

      // Clean up: resolve the pending promise so no timers hang after the test.
      resolveRefresh();
      await pendingRefresh;
    });

    test('warns how many GCP recommendations the last collection skipped (#1472)', async () => {
      mockGetRecs();
      (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
        last_collected_at: new Date().toISOString(),
        last_collection_error: null,
        last_collection_partial: {
          providers: [{
            provider: 'gcp', incomplete_accounts: 0, attempted_scopes: 0, failed_scopes: 0,
            failed_details: 0, skipped_recommendations: 3, reasons: {},
          }],
        },
      });

      await loadRecommendations();
      await Promise.resolve();
      await Promise.resolve();

      expect(mockShowToast).toHaveBeenCalledWith(
        expect.objectContaining({ kind: 'warning', message: expect.stringContaining('3 GCP recommendation(s) skipped') }),
      );
    });

    describe('expired Azure pricing holds (#1900)', () => {
      const sweep = (ids?: string[], extra: Record<string, unknown> = {}) => ({
        provider: 'azure', incomplete_accounts: 0, attempted_scopes: 0, failed_scopes: 0,
        failed_details: 0, reasons: {}, ...(ids === undefined ? {} : { expired_hold_account_ids: ids }), ...extra,
      });
      const run = async (providers?: unknown[]) => {
        mockGetRecs();
        (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
          last_collected_at: new Date().toISOString(),
          last_collection_error: null,
          ...(providers ? { last_collection_partial: { providers } } : {}),
        });
        await loadRecommendations();
        await Promise.resolve();
        await Promise.resolve();
      };
      const holdCalls = () =>
        mockShowToast.mock.calls.filter(([o]) => String((o as { message: string }).message).startsWith('Azure prices unavailable'));

      test('no toast when the field is absent', async () => {
        await run([sweep()]);
        expect(holdCalls()).toHaveLength(0);
      });
      test('no toast when the list is empty', async () => {
        await run([sweep([])]);
        expect(holdCalls()).toHaveLength(0);
      });
      test('singular for 1 account, without ids', async () => {
        await run([sweep(['acct-secret'])]);
        expect(holdCalls()).toHaveLength(1);
        const o = holdCalls()[0]![0] as { kind: string; message: string };
        expect(o.kind).toBe('warning');
        expect(o.message).toBe('Azure prices unavailable for over a day; stale recommendations removed for 1 account');
        expect(o.message).not.toContain('acct-secret');
      });
      test('renders a persistent banner above the summary, and clears it when the holds are gone', async () => {
        document.body.innerHTML = '<section id="recommendations-summary"></section>';
        await run([sweep(['a', 'b'])]);
        const banner = document.getElementById('recommendations-hold-banner');
        expect(banner?.textContent).toBe('Azure prices unavailable for over a day; stale recommendations removed for 2 accounts');
        expect(banner?.nextElementSibling?.id).toBe('recommendations-summary');
      });
      test('plural for 3 accounts', async () => {
        await run([sweep(['a', 'b', 'c'])]);
        expect((holdCalls()[0]![0] as { message: string }).message).toContain('removed for 3 accounts');
      });
      test('counts an account once when the same id is in two sweeps', async () => {
        await run([sweep(['a']), sweep(['a'])]);
        expect((holdCalls()[0]![0] as { message: string }).message).toContain('removed for 1 account');
      });
      test('counts distinct accounts across overlapping sweeps', async () => {
        await run([sweep(['a', 'b']), sweep(['b', 'c'])]);
        expect((holdCalls()[0]![0] as { message: string }).message).toContain('removed for 3 accounts');
      });
      test('shown together with the skipped-recommendations notice', async () => {
        await run([sweep(['a', 'b'], { skipped_recommendations: 4 })]);
        expect(holdCalls()).toHaveLength(1);
        expect(mockShowToast).toHaveBeenCalledWith(
          expect.objectContaining({ message: expect.stringContaining('4 AZURE recommendation(s) skipped') }),
        );
      });
    });

    test('persistent in-flight toast — shown with timeout: null so it stays until settled', async () => {
      mockGetRecs();
      (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
        last_collected_at: null,
        last_collection_error: null,
      });

      await loadRecommendations();
      await Promise.resolve();

      // Find the in-flight "Refreshing…" toast call and confirm it has no
      // auto-dismiss timeout so it outlives long-running refreshes.
      const inFlightCall = mockShowToast.mock.calls.find(
        ([opts]) => (opts as { message: string }).message === 'Refreshing recommendations…',
      );
      expect(inFlightCall).toBeDefined();
      const inFlightOpts = inFlightCall![0] as { timeout?: number | null };
      expect(inFlightOpts.timeout).toBeNull();
    });

    // Runaway-loop regression. Reproduces the production cost incident: the
    // backend collect SUCCEEDS (POST /recommendations/refresh resolves) but the
    // collector never advances last_collected_at (it fails at a DB error), so
    // freshness stays stale forever. The success path calls onReload()
    // (loadRecommendations by default), which re-enters triggerAutoRefreshIfStale.
    // Pre-fix this recurses and re-fires the paid collect endpoint on every
    // reload — observed at ~4-10 calls/sec in production. Post-fix the
    // once-per-session latch caps it at exactly one collect across repeated
    // loads / the post-refresh reload.
    test('runaway loop — stale-that-never-clears fires the collect exactly once across reloads (#cost-risk)', async () => {
      mockGetRecs();
      // Freshness is ALWAYS stale (collector never advances it).
      (recsApi.getRecommendationsFreshness as jest.Mock).mockResolvedValue({
        last_collected_at: null,
        last_collection_error: null,
      });
      // The refresh POST SUCCEEDS (real scenario: backend returns 202), which
      // is what drives the success-path onReload() re-entry.
      (recsApi.refreshRecommendations as jest.Mock).mockResolvedValue({});

      // First page open: should auto-refresh once. The default onReload is
      // loadRecommendations, so the success path re-enters this whole chain.
      await loadRecommendations();
      // Drain the .then(onReload) -> loadRecommendations -> triggerAutoRefreshIfStale
      // microtask/promise chain several times; pre-fix each drain re-fires the
      // collect, so a multi-iteration drain makes the runaway unmistakable.
      for (let i = 0; i < 10; i++) {
        await Promise.resolve();
      }

      // Even simulated subsequent reloads (filter change, tab re-entry) must
      // NOT re-arm the auto-collect within the same session.
      await loadRecommendations();
      await loadRecommendations();
      for (let i = 0; i < 10; i++) {
        await Promise.resolve();
      }

      // Exactly one paid collect, regardless of how many reloads occurred.
      expect(recsApi.refreshRecommendations).toHaveBeenCalledTimes(1);
    });
  });

  describe('openPurchaseModal', () => {
    // Issue #111 (iii): openPurchaseModal is now async (it pre-fetches
    // per-account service overrides to seed each row's Payment default).
    // Tests must `await` it so the DOM is populated before assertions.
    test('displays purchase modal', async () => {
      const recommendations = [
        { id: 'rec-1', provider: 'aws' as const,
          service: 'ec2',
          resource_type: 't3.medium',
          region: 'us-east-1',
          count: 5,
          term: 1,
          savings: 100,
          upfront_cost: 500
        }
      ];

      await openPurchaseModal(recommendations);

      const modal = document.getElementById('purchase-modal');
      expect(modal?.classList.contains('hidden')).toBe(false);
    });

    test('shows purchase summary', async () => {
      // Use a non-admin session so the approval-required note renders instead
      // of the execute-mode toggle (issue #923 carve-out: execute-any/own is
      // gated on isAdmin(), which requires the Administrators group; a regular
      // user without group membership sees the approval-required note).
      // Restore the default admin mock after this test so sibling tests are
      // unaffected; the outer beforeEach only calls clearAllMocks() which does
      // not reset return values.
      (state.getCurrentUser as jest.Mock).mockReturnValue({ id: 'u-reg', email: 'user@example.com', groups: [] });
      const recommendations = [
        { id: 'rec-2', provider: 'aws' as const, service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 5, term: 1, savings: 100, upfront_cost: 500 },
        { id: 'rec-3', provider: 'aws' as const, service: 'rds', resource_type: 'db.r5.large', region: 'us-east-1', count: 2, term: 1, savings: 200, upfront_cost: 1000 }
      ];

      await openPurchaseModal(recommendations);
      (state.getCurrentUser as jest.Mock).mockReturnValue({ id: 'u-admin', email: 'admin@example.com', groups: ['00000000-0000-5000-8000-000000000001'] });

      const details = document.getElementById('purchase-details');
      // Issue #320: the modal now renders a full breakdown table with column
      // headers and a totals row instead of a "Purchase Summary" heading.
      // Verify that the table header and approval note are present.
      expect(details?.textContent).toContain('Include'); // select-all column header label
      expect(details?.textContent).toContain('Totals');  // totals row label
      expect(details?.textContent).toContain('approval'); // approval-required note
    });

    test('lists individual recommendations', async () => {
      const recommendations = [
        { id: 'rec-4', provider: 'aws' as const, service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 5, term: 1, savings: 100, upfront_cost: 500 }
      ];

      await openPurchaseModal(recommendations);

      const details = document.getElementById('purchase-details');
      expect(details?.textContent).toContain('EC2');
      expect(details?.textContent).toContain('t3.medium');
      expect(details?.textContent).toContain('us-east-1');
    });

    test('handles missing modal element', async () => {
      document.body.replaceChildren();

      await expect(openPurchaseModal([])).resolves.not.toThrow();
    });

    // Issue #288: the primary action does NOT execute the purchase — it
    // sends an approval-request email. Pin the post-fix label + body
    // wording so a regression that reverts to the misleading "Execute
    // Purchase" framing fails this suite.
    describe('approval-required messaging (issue #288)', () => {
      beforeEach(() => {
        // These tests exercise the approval-required path that renders the
        // explanatory note. After issue #923 / PR #924, the execute-mode
        // toggle renders only for sessions with execute-any:purchases or
        // execute-own:purchases (both gated on isAdmin()). Using a regular
        // user here ensures the approval-required note is rendered and the
        // execute-mode toggle is absent, which is what the assertions below
        // expect. The admin execute-mode toggle path is covered in
        // execute-mode-toggle.test.ts.
        (state.getCurrentUser as jest.Mock).mockReturnValue({ id: 'u-reg', email: 'user@example.com', groups: [] });
      });

      afterEach(() => {
        // Restore the default admin mock so tests in sibling describe blocks
        // are not affected by this scope's non-admin override. The outer
        // beforeEach only calls clearAllMocks() which does not reset return
        // values, so we must restore explicitly.
        (state.getCurrentUser as jest.Mock).mockReturnValue({ id: 'u-admin', email: 'admin@example.com', groups: ['00000000-0000-5000-8000-000000000001'] });
      });

      const baseRec = {
        id: 'rec-288',
        provider: 'aws' as const,
        service: 'ec2',
        resource_type: 't3.medium',
        region: 'us-east-1',
        count: 5,
        term: 1,
        savings: 100,
        upfront_cost: 500,
      };

      test('primary button reads "Send for approval", not "Execute Purchase"', async () => {
        await openPurchaseModal([baseRec]);

        const btn = document.getElementById('execute-purchase-btn');
        expect(btn?.textContent).toBe('Send for approval');
        // Belt-and-braces: ensure no element on the rendered modal still
        // carries the pre-#288 text — guards against a future template
        // re-introducing the misleading wording somewhere new.
        const modal = document.getElementById('purchase-modal');
        expect(modal?.textContent).not.toContain('Execute Purchase');
      });

      test('modal body carries the approval-required explanation', async () => {
        await openPurchaseModal([baseRec]);

        const details = document.getElementById('purchase-details');
        expect(details?.textContent).toContain('will email an approval request');
      });

      test('approval-required note renders with its dedicated class', async () => {
        await openPurchaseModal([baseRec]);

        const note = document.querySelector('#purchase-details .approval-required-note');
        expect(note).not.toBeNull();
        expect(note?.textContent).toMatch(/approval request/i);
      });
    });

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

      test('all rows are checked by default on modal open', async () => {
        await openPurchaseModal([makeRec('a'), makeRec('b'), makeRec('c')]);

        const checkboxes = document.querySelectorAll<HTMLInputElement>('.purchase-modal-row-include');
        expect(checkboxes.length).toBe(3);
        checkboxes.forEach((cb) => {
          expect(cb.checked).toBe(true);
        });
      });

      test('getPurchaseModalRecommendations returns all recs when all checked', async () => {
        const recs = [makeRec('r1'), makeRec('r2')];
        await openPurchaseModal(recs);

        const result = getPurchaseModalRecommendations();
        expect(result.map((r) => r.id)).toEqual(['r1', 'r2']);
      });
    });
  });
});
