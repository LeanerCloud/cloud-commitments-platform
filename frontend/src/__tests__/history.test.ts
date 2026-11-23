/**
 * History module tests
 */
// The harness registers the module mocks, so it must be imported first.
import { mockGetCurrentProvider, mockGetCurrentAccountIDs } from './history-harness';
import { initHistoryDateRange, loadHistory } from '../history';
import * as api from '../api';
import { getAmortizeUpfront } from '../state';

describe('History Module', () => {
  beforeEach(() => {
    // Reset DOM. Issue #344 T2: provider/account filters now live in
    // the global topbar (state-driven); only the per-section controls
    // remain on the history page.
    document.body.innerHTML = `
      <input type="date" id="history-start">
      <input type="date" id="history-end">
      <div id="history-summary"></div>
      <div id="history-list"></div>
      <div id="purchases-approval-queue"></div>
    `;

    jest.clearAllMocks();
    mockGetCurrentProvider.mockReturnValue('all');
    mockGetCurrentAccountIDs.mockReturnValue([]);
    (getAmortizeUpfront as jest.Mock).mockReturnValue(false);
  });

  test.each([false, true])('unknown upfront with amortization %s preserves recurring versus effective cost', async (amortize) => {
    (getAmortizeUpfront as jest.Mock).mockReturnValue(amortize);
    const purchase = {
      timestamp: '2026-09-29T00:00:00Z', provider: 'aws', service: 'ec2', resource_type: 'm5.large',
      region: 'us-east-1', count: 1, term: 1, upfront_cost: null, monthly_cost: 50, estimated_savings: 10,
    };
    (api.getHistory as jest.Mock).mockResolvedValue({ summary: { total_upfront: null }, purchases: [
      { ...purchase, purchase_id: 'complete', status: 'completed' },
      { ...purchase, purchase_id: 'pending', status: 'pending' },
    ] });
    await loadHistory();
    const history = document.querySelector('#history-list tr[data-execution-id="complete"]');
    const queue = document.querySelector('#purchases-approval-queue tr[data-execution-id="pending"]');
    expect(history?.querySelectorAll('td')[8]?.textContent).toBe('—');
    expect(history?.querySelectorAll('td')[9]?.textContent).toBe(amortize ? '—' : '$50');
    expect(queue?.querySelectorAll('td')[7]?.textContent).toBe(amortize ? '—' : '$50');
    expect(queue?.querySelectorAll('td')[8]?.textContent).toBe('—');
    // Count and money columns right-align (issue #739).
    const numCols = (row: Element | null): number[] => Array.from(row?.querySelectorAll('td') ?? [])
      .flatMap((td, i) => (td.classList.contains('num') ? [i] : []));
    expect(numCols(history)).toEqual([6, 8, 9, 10]);
    expect(numCols(queue)).toEqual([4, 7, 8, 9]);
  });

  // Issue #318: with amortize on, the upfront is already in Monthly Cost, so
  // the Upfront cell is muted and labeled to keep it from reading as additive.
  test.each([false, true])('upfront folded into monthly with amortization %s is labeled only when on', async (amortize) => {
    (getAmortizeUpfront as jest.Mock).mockReturnValue(amortize);
    const purchase = {
      timestamp: '2026-09-29T00:00:00Z', provider: 'aws', service: 'ec2', resource_type: 'm5.large',
      region: 'us-east-1', count: 1, term: 1, upfront_cost: 1200, monthly_cost: 100, estimated_savings: 300,
    };
    (api.getHistory as jest.Mock).mockResolvedValue({ summary: {}, purchases: [
      { ...purchase, purchase_id: 'complete', status: 'completed' },
      { ...purchase, purchase_id: 'pending', status: 'pending' },
    ] });
    await loadHistory();
    const historyCells = document.querySelectorAll('#history-list tr[data-execution-id="complete"] td');
    const queueCells = document.querySelectorAll('#purchases-approval-queue tr[data-execution-id="pending"] td');
    expect(historyCells[9]?.textContent).toBe(amortize ? '$200' : '$100');
    expect(queueCells[7]?.textContent).toBe(amortize ? '$200' : '$100');
    for (const upfront of [historyCells[8], queueCells[8]]) {
      expect(upfront?.classList.contains('upfront-in-monthly')).toBe(amortize);
      expect(upfront?.textContent).toBe(amortize ? '$1200(in monthly)' : '$1200');
      expect(upfront?.getAttribute('title')).toBe(amortize ? 'Included in the amortized monthly cost' : null);
    }
  });

  describe('initHistoryDateRange', () => {
    // Issue #51: the inputs are seeded from the LOCAL calendar day. Jest
    // runs in America/New_York (globalSetup), so pin "now" to an evening
    // instant where the local day trails the UTC day: the old toISOString()
    // seeding showed tomorrow in To and dropped a day from From.
    test('sets default 90-day range from the local calendar day', () => {
      jest.useFakeTimers().setSystemTime(new Date('2026-07-28T03:30:00Z')); // Jul 27, 23:30 EDT
      try {
        initHistoryDateRange();
      } finally {
        jest.useRealTimers();
      }

      const startInput = document.getElementById('history-start') as HTMLInputElement;
      const endInput = document.getElementById('history-end') as HTMLInputElement;
      // Same 90-day default as Savings History (#savings-period).
      expect(endInput.value).toBe('2026-07-27');
      expect(startInput.value).toBe('2026-04-28');
    });

    test('the seeded range requests a purchase made earlier the same local day', async () => {
      // A purchase at 21:00 EDT on Jul 27 is 01:00 UTC on Jul 28. The
      // server filters on whole UTC days, so the request must reach Jul 28
      // even though the To input (correctly) shows Jul 27.
      const purchaseAt = '2026-07-28T01:00:00Z';
      (api.getHistory as jest.Mock).mockResolvedValue({ summary: {}, purchases: [] });
      jest.useFakeTimers().setSystemTime(new Date('2026-07-28T03:30:00Z'));
      try {
        initHistoryDateRange();
      } finally {
        jest.useRealTimers();
      }
      await loadHistory();

      const filters = (api.getHistory as jest.Mock).mock.calls[0][0] as { start: string; end: string };
      const purchaseUtcDay = purchaseAt.slice(0, 10);
      expect(filters.start <= purchaseUtcDay && purchaseUtcDay <= filters.end).toBe(true);
      expect(filters).toEqual(expect.objectContaining({ start: '2026-04-28', end: '2026-07-28' }));
    });

    test('does not overwrite existing values', () => {
      const startInput = document.getElementById('history-start') as HTMLInputElement;
      const endInput = document.getElementById('history-end') as HTMLInputElement;

      startInput.value = '2024-01-01';
      endInput.value = '2024-02-01';

      initHistoryDateRange();

      expect(startInput.value).toBe('2024-01-01');
      expect(endInput.value).toBe('2024-02-01');
    });

    test('handles missing elements gracefully', () => {
      document.body.innerHTML = '';

      expect(() => initHistoryDateRange()).not.toThrow();
    });
  });

  describe('loadHistory', () => {
    test('loads history with filters from form', async () => {
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: {},
        purchases: []
      });

      const startInput = document.getElementById('history-start') as HTMLInputElement;
      const endInput = document.getElementById('history-end') as HTMLInputElement;

      startInput.value = '2024-01-01';
      endInput.value = '2024-03-01';
      // Provider scope now comes from the global topbar (state.ts).
      mockGetCurrentProvider.mockReturnValue('aws');

      await loadHistory();

      // The inputs are local (America/New_York) days; the request carries
      // the UTC days covering them, so the To day's evening (already Mar 2
      // in UTC) is included (issue #51).
      expect(api.getHistory).toHaveBeenCalledWith({
        start: '2024-01-01',
        end: '2024-03-02',
        provider: 'aws'
      });
    });

    test('renders purchase list', async () => {
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: {},
        purchases: [
          {
            purchase_date: '2024-01-15',
            provider: 'aws',
            service: 'ec2',
            resource_type: 't3.medium',
            region: 'us-east-1',
            count: 5,
            term: 1,
            upfront_cost: 500,
            monthly_savings: 100,
            plan_name: 'Test Plan'
          }
        ]
      });

      await loadHistory();

      const list = document.getElementById('history-list');
      expect(list?.innerHTML).toContain('table');
      expect(list?.innerHTML).toContain('EC2');
      expect(list?.innerHTML).toContain('us-east-1');
      // Both Monthly Cost and Monthly Savings columns must be present (issue #788).
      expect(list?.innerHTML).toContain('Monthly Cost');
      expect(list?.innerHTML).toContain('Monthly Savings');
    });

    test('renders monthly_cost cell with value when present, dash when absent (issue #788)', async () => {
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: {},
        purchases: [
          {
            timestamp: '2024-01-15T00:00:00Z',
            provider: 'aws',
            service: 'ec2',
            resource_type: 't3.medium',
            region: 'us-east-1',
            count: 1,
            term: 1,
            upfront_cost: 500,
            monthly_cost: 42.5,
            estimated_savings: 20,
          },
          {
            timestamp: '2024-01-16T00:00:00Z',
            provider: 'aws',
            service: 'rds',
            resource_type: 'db.m5.large',
            region: 'us-east-1',
            count: 1,
            term: 1,
            upfront_cost: 0,
            // monthly_cost intentionally absent
            estimated_savings: 10,
          },
        ]
      });

      await loadHistory();

      const list = document.getElementById('history-list');
      const html = list?.innerHTML || '';
      // Row with monthly_cost=42.5: formatCurrency mock returns "$42.5".
      expect(html).toContain('$42.5');
      // Row without monthly_cost: must render the muted dash, not "$0".
      expect(html).toContain('class="muted"');
    });

    test('Issue #1416: explicit null monthly_cost renders as dash, not "$0"', async () => {
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: {},
        purchases: [
          {
            timestamp: '2024-02-01T00:00:00Z',
            provider: 'aws',
            service: 'ec2',
            resource_type: 't3.medium',
            region: 'us-east-1',
            count: 1,
            term: 1,
            upfront_cost: 500,
            monthly_cost: null,
            estimated_savings: 20,
          },
        ]
      });

      await loadHistory();

      const list = document.getElementById('history-list');
      const html = list?.innerHTML || '';
      // Explicit null must render the muted dash, never "$0".
      expect(html).toContain('class="muted"');
      expect(html).not.toContain('$0');
    });

    test('shows empty message when no purchases', async () => {
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: {},
        purchases: []
      });

      await loadHistory();

      const list = document.getElementById('history-list');
      expect(list?.innerHTML).toContain('No purchase history found');
    });

    test('shows error on API failure', async () => {
      (api.getHistory as jest.Mock).mockRejectedValue(new Error('API Error'));
      console.error = jest.fn();

      await loadHistory();

      const list = document.getElementById('history-list');
      expect(list?.innerHTML).toContain('Failed to load history');
    });

    // Issue #621: an approved/running/paused execution is in-flight — its
    // synchronous AWS purchase may have been interrupted (Lambda timeout /
    // crash). It MUST render as "In Progress", never the green "Completed"
    // badge, or the user could think the purchase finished and re-approve it
    // (double-spend). Pre-fix these statuses fell through to the Completed
    // default in statusBadgeHTML.
    test('renders approved/running/paused as In Progress, not Completed', async () => {
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: {},
        purchases: [
          { purchase_id: 'appr-1', status: 'approved', provider: 'aws', region: 'us-east-1' },
          { purchase_id: 'run-1', status: 'running', provider: 'aws', region: 'us-east-1' },
          { purchase_id: 'paus-1', status: 'paused', provider: 'aws', region: 'us-east-1' },
        ],
      });

      await loadHistory();

      const list = document.getElementById('history-list');
      const html = list?.innerHTML || '';
      const inProgressBadges = (html.match(/In Progress/g) || []).length;
      expect(inProgressBadges).toBe(3);
      expect(html).not.toContain('>Completed<');
    });

    // Issue #239: a scheduled execution (Gmail-style pre-fire delay, issue
    // #291 wave-2) has not had its provider call fire yet. It MUST render
    // as "Scheduled", never the green "Completed" badge, or the user could
    // think a delayed purchase already fired -- while a Revoke button sits
    // right beside it. Pre-fix "scheduled" had no case in statusBadgeHTML
    // and fell through to the Completed default.
    test('renders scheduled as Scheduled, not Completed, and counts it under Pending', async () => {
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: {},
        purchases: [
          { purchase_id: 'sched-1', status: 'scheduled', provider: 'aws', region: 'us-east-1', upfront_cost: 500 },
          { purchase_id: 'comp-1', status: 'completed', provider: 'aws', region: 'us-east-1', upfront_cost: 100 },
        ],
      });

      await loadHistory();

      const list = document.getElementById('history-list');
      const scheduledRow = list?.querySelector('tr[data-execution-id="sched-1"]');
      expect(scheduledRow?.innerHTML).toContain('Scheduled');
      expect(scheduledRow?.innerHTML).not.toContain('>Completed<');
      // The genuinely completed row is the negative control: it must still
      // render as Completed, proving this isn't just a blanket badge change.
      const completedRow = list?.querySelector('tr[data-execution-id="comp-1"]');
      expect(completedRow?.innerHTML).toContain('>Completed<');

      // The Pending chip count must include the scheduled row (it buckets
      // via isInFlightStatus alongside approved/running/paused), not the
      // Completed chip.
      const pendingChip = document.querySelector('[data-history-status="pending"]');
      expect(pendingChip?.textContent).toContain('Pending (1)');
      const completedChip = document.querySelector('[data-history-status="completed"]');
      expect(completedChip?.textContent).toContain('Completed (1)');
    });

    // Issue #706: partially_completed rows counted in the Completed chip bucket
    // but excluded from the chip filter. Clicking "Completed" must show ALL rows
    // that the chip counted -- including partially_completed ones.
    test('Completed chip shows partially_completed rows (issue #706)', async () => {
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: {},
        purchases: [
          { purchase_id: 'comp-1', status: 'completed', provider: 'aws', region: 'us-east-1' },
          { purchase_id: 'part-1', status: 'partially_completed', provider: 'aws', region: 'us-east-1' },
          { purchase_id: 'fail-1', status: 'failed', provider: 'aws', region: 'us-east-1' },
        ],
      });

      await loadHistory();

      const list = document.getElementById('history-list');

      // Before clicking: verify the Completed chip counts 2 (comp-1 + part-1).
      const completedChip = list?.querySelector<HTMLButtonElement>('[data-history-status="completed"]');
      expect(completedChip?.textContent).toContain('2');

      // Click the Completed chip -- triggers re-render via renderHistoryList.
      completedChip?.click();

      const html = list?.innerHTML || '';
      // Both the clean success and the partial success must be visible.
      expect(html).toContain('comp-1');
      expect(html).toContain('part-1');
      // The failed row must be hidden.
      expect(html).not.toContain('fail-1');
    });

    test('handles empty provider filter', async () => {
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: {},
        purchases: []
      });

      // 'all' is the topbar-chip "All providers" value, which loadHistory
      // translates to `provider: undefined` (no provider scope on the API
      // call). Same semantic as the old empty-string select value.
      mockGetCurrentProvider.mockReturnValue('all');

      await loadHistory();

      // Empty strings are passed when inputs have no value
      expect(api.getHistory).toHaveBeenCalledWith({
        start: '',
        end: '',
        provider: undefined
      });
    });

    // Migration 000089 (expand-contract rename cancelled -> canceled): during
    // the rolling deploy window the backend returns BOTH spellings on
    // /api/history. Each must render as the muted "Cancelled" badge, be
    // counted under the Cancelled chip, and be visible when that chip is
    // clicked. Pre-fix the FE only matched 'cancelled', so 'canceled' rows
    // fell through to the green Completed badge and were silently bucketed
    // into the Completed total -- a user-facing regression that defeats the
    // purpose of the rename.
    test('Cancelled badge + chip surface BOTH spellings during deploy window (migration 000089)', async () => {
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: {},
        purchases: [
          // Row written by new code post-deploy: US spelling.
          { purchase_id: 'cx-new', status: 'canceled', provider: 'aws', region: 'us-east-1' },
          // Row written by old code mid-deploy: legacy British spelling.
          { purchase_id: 'cx-legacy', status: 'cancelled', provider: 'aws', region: 'us-east-1' },
          // Sanity baseline: a real completed row -- must NOT bucket into Cancelled.
          { purchase_id: 'comp-1', status: 'completed', provider: 'aws', region: 'us-east-1' },
        ],
      });

      await loadHistory();

      const list = document.getElementById('history-list');
      const html = list?.innerHTML || '';

      // Both rows must render the muted Cancelled badge, NOT the green
      // Completed default. Two of the three rows are canceled, so we expect
      // exactly two Cancelled badges.
      const cancelledBadges = (html.match(/>Cancelled</g) || []).length;
      expect(cancelledBadges).toBe(2);

      // The Cancelled chip must count 2 (both spellings), and it must render
      // at all (it only renders when its count > 0).
      const cancelledChip = list?.querySelector<HTMLButtonElement>('[data-history-status="cancelled"]');
      expect(cancelledChip).not.toBeNull();
      expect(cancelledChip?.textContent).toContain('2');

      // The Completed chip must count 1 (only the real completed row); the
      // pre-fix bug bucketed canceled rows into completed, yielding 3.
      const completedChip = list?.querySelector<HTMLButtonElement>('[data-history-status="completed"]');
      expect(completedChip?.textContent).toContain('1');

      // Clicking the Cancelled chip must reveal BOTH spellings, not just the
      // British one (s === activeStatusFilter would only match 'cancelled').
      cancelledChip?.click();
      const filteredHtml = list?.innerHTML || '';
      expect(filteredHtml).toContain('cx-new');
      expect(filteredHtml).toContain('cx-legacy');
      expect(filteredHtml).not.toContain('comp-1');
    });
  });
});
