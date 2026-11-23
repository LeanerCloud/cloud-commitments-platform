/**
 * History module: setupHistoryHandlers wiring (issue #701), stale-load and
 * date-format guards (issue #1681), the absent-summary KPI cards (H-2) and the
 * amortize toggle (issue #1957). Split from history.test.ts to stay under the
 * 500-line ceiling.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockGetCurrentProvider, mockGetCurrentAccountIDs } from './history-harness';
import { loadHistory, setupHistoryHandlers } from '../history';
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
});
