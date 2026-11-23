/**
 * History module: setupHistoryHandlers wiring (issue #701), stale-load and
 * date-format guards (issue #1681), the absent-summary KPI cards (H-2) and the
 * amortize toggle (issue #1957). Split from history.test.ts to stay under the
 * 500-line ceiling.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockGetCurrentProvider, mockGetCurrentAccountIDs } from './history-harness';
import { loadHistory, viewPlanHistory, setupHistoryHandlers } from '../history';
import * as api from '../api';
import { getAmortizeUpfront, setAmortizeUpfront, subscribeAmortizeUpfront } from '../state';

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

  // Issue #701: setupHistoryHandlers must subscribe to the global topbar
  // provider/account filter chips so the Purchase History table and
  // Approval Queue reload when a chip changes. PR #716 fixed the backend
  // filter params but the frontend subscription was never registered in
  // app.ts -- adding this suite guards against a regression.
  describe('setupHistoryHandlers (issue #701)', () => {
    beforeEach(() => {
      (api.getHistory as jest.Mock).mockResolvedValue({ summary: {}, purchases: [] });
    });

    test('registers a callback with state.subscribeProvider', () => {
      setupHistoryHandlers();
      expect((require('../state').subscribeProvider as jest.Mock)).toHaveBeenCalledTimes(1);
    });

    test('registers a callback with state.subscribeAccount', () => {
      setupHistoryHandlers();
      expect((require('../state').subscribeAccount as jest.Mock)).toHaveBeenCalledTimes(1);
    });

    test('provider change triggers loadHistory', async () => {
      setupHistoryHandlers();
      const stateModule = require('../state');
      const providerCb = (stateModule.subscribeProvider as jest.Mock).mock.calls[0]?.[0] as () => void;
      expect(typeof providerCb).toBe('function');

      (api.getHistory as jest.Mock).mockClear();
      providerCb();
      await new Promise((r) => setTimeout(r, 0));

      expect(api.getHistory).toHaveBeenCalledTimes(1);
    });

    test('account change triggers loadHistory', async () => {
      setupHistoryHandlers();
      const stateModule = require('../state');
      const accountCb = (stateModule.subscribeAccount as jest.Mock).mock.calls[0]?.[0] as () => void;
      expect(typeof accountCb).toBe('function');

      (api.getHistory as jest.Mock).mockClear();
      accountCb();
      await new Promise((r) => setTimeout(r, 0));

      expect(api.getHistory).toHaveBeenCalledTimes(1);
    });

    test.each(['history-start', 'history-end'])('changing #%s reloads history (issue #1681)', async (id) => {
      setupHistoryHandlers();
      (api.getHistory as jest.Mock).mockClear();
      document.getElementById(id)!.dispatchEvent(new Event('change'));
      await new Promise((r) => setTimeout(r, 0));
      expect(api.getHistory).toHaveBeenCalledTimes(1);
    });
  });

  test('a slower earlier load does not overwrite a later one (issue #1681 date-change reloads)', async () => {
    const row = (id: string) => ({ timestamp: '2026-09-29T12:00:00Z', purchase_id: id, status: 'completed', provider: 'aws', service: 'ec2' });
    let resolveOld!: (v: unknown) => void;
    (api.getHistory as jest.Mock)
      .mockImplementationOnce(() => new Promise((r) => { resolveOld = r; }))
      .mockResolvedValueOnce({ summary: {}, purchases: [row('new')] });
    const first = loadHistory();
    await loadHistory();
    resolveOld({ summary: {}, purchases: [row('old')] });
    await first;
    expect(document.querySelector('#history-list tr[data-execution-id="new"]')).not.toBeNull();
    expect(document.querySelector('#history-list tr[data-execution-id="old"]')).toBeNull();
  });

  test('Date cells use the numeric locale form of the date inputs, not "Sep 29, 2026" (issue #1681)', async () => {
    (api.getHistory as jest.Mock).mockResolvedValue({ summary: {}, purchases: [
      { timestamp: '2026-09-29T12:00:00Z', purchase_id: 'a', status: 'completed', provider: 'aws', service: 'ec2' },
      { timestamp: '2026-09-29T12:00:00Z', purchase_id: 'b', status: 'pending', provider: 'aws', service: 'ec2' },
    ] });
    await loadHistory();
    const hist = document.querySelector('#history-list tr[data-execution-id="a"] td[data-label="Date"]');
    const queue = document.querySelector('#purchases-approval-queue tr[data-execution-id="b"] td[data-label="Date"]');
    expect(hist?.textContent).toMatch(/^\d{2}[./-]\d{2}[./-]\d{4}$/);
    expect(queue?.textContent).toMatch(/^\d{2}[./-]\d{2}[./-]\d{4}$/);
  });

  // H-2 regression: absent API summary must render '—' on KPI cards, not '$0'.
  // Pre-fix: `data.summary || {}` passed an empty HistorySummary to
  // renderHistorySummary, and `?? 0` coercions made money fields render as
  // '$0' (or the mock's '$0'). These tests must FAIL on the pre-fix code.
  describe('H-2: absent summary renders the em-dash sentinel on KPI cards', () => {
    test('renderHistorySummary: absent summary (undefined) shows an em dash on all money cards (loadHistory)', async () => {
      (api.getHistory as jest.Mock).mockResolvedValue({
        // summary field deliberately absent
        purchases: []
      });

      await loadHistory();

      const summary = document.getElementById('history-summary');
      expect(summary?.innerHTML).toContain('Total Upfront Spent');
      expect(summary?.innerHTML).toContain('Monthly Savings');
      expect(summary?.innerHTML).toContain('Annual Savings');
      // Must show the absent sentinel, not a fabricated '$0'
      expect(summary?.innerHTML).toContain('—');
      // The mock formatCurrency returns '$0' for 0 and an em dash for null;
      // verify no '$0' slips through for the money cards.
      expect(summary?.textContent).not.toMatch(/Total Upfront Spent.*\$0/s);
      expect(summary?.textContent).not.toMatch(/Monthly Savings.*\$0/s);
    });

    test('renderHistorySummary: absent total_upfront renders an em dash, not $0 (viewPlanHistory)', async () => {
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: {
          total_purchases: 3,
          total_completed: 3,
          // total_upfront intentionally absent
          total_monthly_savings: 100,
          total_annual_savings: 1200,
        },
        purchases: []
      });

      await viewPlanHistory('plan-abc');

      const summary = document.getElementById('history-summary');
      // total_purchases is present; money fields that are absent must be an em dash
      expect(summary?.textContent).toContain('3');
      // formatCurrency must be called with null for the absent total_upfront
      // field and must produce an em dash, not '$0'. This assertion fails if the
      // production code regresses to passing 0 or a fabricated value.
      const { formatCurrency } = jest.requireMock('../utils') as { formatCurrency: jest.Mock };
      expect(formatCurrency).toHaveBeenCalledWith(null);
      // The rendered card must show an em dash, not a dollar amount.
      expect(summary?.innerHTML).toContain('Total Upfront Spent');
      expect(summary?.innerHTML).toContain('<p class="value">—</p>');
      expect(summary?.innerHTML).not.toContain('<p class="value">$0</p>');
    });

    // Regression: attribute-boundary XSS via purchase_id containing a double-quote.
    // Pre-fix, data-approve-id / data-cancel-id / data-retry-id / data-execution-id
    // used escapeHtml which did not encode ", allowing a crafted ID to break out of
    // the attribute and inject markup. Post-fix, escapeHtmlAttr is used and " becomes
    // &quot; so the attribute stays well-formed.
    test('encodes double-quotes in purchase_id for data-* attributes (XSS regression)', async () => {
      const maliciousId = 'a" onmouseover="alert(1)';
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: {},
        purchases: [
          {
            purchase_id: maliciousId,
            status: 'pending',
            provider: 'aws',
            service: 'ec2',
            resource_type: 't3.medium',
            region: 'us-east-1',
            count: 1,
            term: 1,
            upfront_cost: 100,
            estimated_savings: 10,
            can_approve: true,
            can_cancel: true,
          }
        ]
      });

      await loadHistory();

      const html = document.getElementById('history-list')?.innerHTML || '';
      // The encoded form must appear in the attribute value.
      expect(html).toContain('&quot;');
      // The raw injection payload must not appear as unescaped markup.
      expect(html).not.toContain('onmouseover="alert(1)"');
    });

    test('renderHistorySummary: null summary shows unknown-state banner with all four cards', async () => {
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: null as unknown as undefined,
        purchases: []
      });

      await loadHistory();

      const summary = document.getElementById('history-summary');
      // All four cards must still render (just with em-dash values).
      expect(summary?.innerHTML).toContain('Total Purchases');
      expect(summary?.innerHTML).toContain('Total Upfront Spent');
      expect(summary?.innerHTML).toContain('Monthly Savings');
      expect(summary?.innerHTML).toContain('Annual Savings');
      // The unknown-state banner shows the em dash for every value, and an
      // absent figure is not styled as a saving (issue #739).
      const allDashes = (summary?.innerHTML.match(/—/g) || []).length;
      expect(allDashes).toBeGreaterThanOrEqual(4);
      expect(summary?.querySelector('.value.savings')).toBeNull();
    });
  });

  // Issue #1957 (A12-037): the amortize toggle wiring itself, not only the
  // per-row values it produces.
  describe('amortize toggle', () => {
    const purchase = {
      timestamp: '2026-09-29T00:00:00Z', provider: 'aws', service: 'ec2', resource_type: 'm5.large',
      region: 'us-east-1', count: 1, term: 1, upfront_cost: 1200, monthly_cost: 100, estimated_savings: 300,
    };
    const mountDOM = (): void => {
      document.body.innerHTML = `
        <div id="history-controls"></div>
        <div id="history-summary"></div>
        <div id="history-list"></div>
        <div id="purchases-approval-queue-section"><div id="purchases-approval-queue"></div></div>
      `;
    };
    const load = async (): Promise<void> => {
      (api.getHistory as jest.Mock).mockResolvedValue({ summary: {}, purchases: [
        { ...purchase, purchase_id: 'complete', status: 'completed' },
        { ...purchase, purchase_id: 'pending', status: 'pending' },
      ] });
      await loadHistory();
    };
    const headers = (id: string): string[] =>
      Array.from(document.querySelectorAll(`#${id} th`)).map(th => th.textContent ?? '');
    const checkbox = (id: string): HTMLInputElement => document.getElementById(id) as HTMLInputElement;

    test('mounts one checkbox per table, once, reflecting the stored toggle', async () => {
      mountDOM();
      (getAmortizeUpfront as jest.Mock).mockReturnValue(true);
      await load();
      await load();
      expect(document.querySelectorAll('#history-amortize-checkbox')).toHaveLength(1);
      expect(document.querySelectorAll('#approval-queue-amortize-checkbox')).toHaveLength(1);
      expect(checkbox('history-amortize-checkbox').checked).toBe(true);
      expect(checkbox('approval-queue-amortize-checkbox').checked).toBe(true);
    });

    test('changing a checkbox writes the new value to state', async () => {
      mountDOM();
      await load();
      const cb = checkbox('history-amortize-checkbox');
      cb.checked = true;
      cb.dispatchEvent(new Event('change', { bubbles: true }));
      expect(setAmortizeUpfront).toHaveBeenCalledWith(true);
      const qcb = checkbox('approval-queue-amortize-checkbox');
      qcb.checked = false;
      qcb.dispatchEvent(new Event('change', { bubbles: true }));
      expect(setAmortizeUpfront).toHaveBeenLastCalledWith(false);
    });

    test('a toggle flip re-renders both tables from the cache and syncs both checkboxes', async () => {
      mountDOM();
      await load();
      setupHistoryHandlers();
      expect(headers('history-list')).toContain('Monthly Cost');
      expect(headers('purchases-approval-queue')).toContain('Monthly Cost');
      const fetches = (api.getHistory as jest.Mock).mock.calls.length;

      (getAmortizeUpfront as jest.Mock).mockReturnValue(true);
      const redraw = (subscribeAmortizeUpfront as jest.Mock).mock.calls[0]![0] as () => void;
      redraw();

      expect(headers('history-list')).toContain('Monthly Cost (amortized)');
      expect(headers('purchases-approval-queue')).toContain('Monthly Cost (amortized)');
      expect(checkbox('history-amortize-checkbox').checked).toBe(true);
      expect(checkbox('approval-queue-amortize-checkbox').checked).toBe(true);
      expect((api.getHistory as jest.Mock).mock.calls.length).toBe(fetches);
    });
  });
});
