/**
 * History module: viewPlanHistory tab switching and the purchases access gate
 * (issue #1775). Split from history.test.ts to stay under the 500-line ceiling.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockGetCurrentProvider, mockGetCurrentAccountIDs } from './history-harness';
import { viewPlanHistory } from '../history';
import * as api from '../api';
import { getAmortizeUpfront } from '../state';
import { switchTab } from '../navigation';
import { canAccess } from '../permissions';

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

  describe('viewPlanHistory', () => {
    // Issue #1775: 'history' was the pre-#340 tab name, so this silently fell
    // back to Home and the Plans page's "View history" button rendered the
    // Home dashboard.
    test('switches to the Purchases tab without its default load', async () => {
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: {},
        purchases: []
      });

      await viewPlanHistory('plan-123');

      expect(switchTab).toHaveBeenCalledWith('purchases', { skipDefaultLoad: true });
    });

    test('does not fetch when the session cannot view purchases', async () => {
      (canAccess as jest.Mock).mockReturnValueOnce(false);

      await viewPlanHistory('plan-123');

      expect(switchTab).toHaveBeenCalledWith('purchases', { skipDefaultLoad: true });
      expect(api.getHistory).not.toHaveBeenCalled();
    });

    test('calls getHistory with planId filter', async () => {
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: {},
        purchases: []
      });

      await viewPlanHistory('plan-123');

      expect(api.getHistory).toHaveBeenCalledWith({ planId: 'plan-123' });
    });

    test('renders history summary', async () => {
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: {
          total_purchases: 5,
          total_completed: 5,
          total_upfront: 1000,
          total_monthly_savings: 200,
          total_annual_savings: 2400
        },
        purchases: []
      });

      await viewPlanHistory('plan-123');

      const summary = document.getElementById('history-summary');
      expect(summary?.innerHTML).toContain('Total Purchases');
      expect(summary?.innerHTML).toContain('5');
    });

    test('absent total_completed renders the unknown marker, not total_purchases (#136 A12-060)', async () => {
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: { total_purchases: 10, total_pending: 3, total_upfront: 1, total_monthly_savings: 1, total_annual_savings: 12 },
        purchases: []
      });

      await viewPlanHistory('plan-123');

      const card = document.getElementById('history-summary')?.querySelector('.card');
      expect(card?.textContent).toContain('Total Purchases');
      expect(card?.textContent).not.toContain('10');
      expect(card?.textContent).toContain('—');
    });

    test('handles API errors gracefully', async () => {
      (api.getHistory as jest.Mock).mockRejectedValue(new Error('API Error'));
      console.error = jest.fn();

      await viewPlanHistory('plan-123');

      expect(console.error).toHaveBeenCalledWith('Failed to load plan history:', expect.any(Error));
    });

    test('snaps From/To inputs to min/max purchase timestamps after fetch', async () => {
      // Reproduces the scenario from PR #139's CodeRabbit nitpick: the
      // plan-history endpoint returns the full history regardless of
      // date range, so the inputs should reflect the rendered data, not
      // the tab's generic default range.
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: {},
        purchases: [
          { timestamp: '2025-12-15T10:00:00Z' },
          { timestamp: '2026-02-04T10:00:00Z' },
          { timestamp: '2026-01-20T10:00:00Z' },
        ],
      });

      const startInput = document.getElementById('history-start') as HTMLInputElement;
      const endInput = document.getElementById('history-end') as HTMLInputElement;
      // Pre-seed with the tab default so we can prove the snap overwrites.
      startInput.value = '2026-04-20';
      endInput.value = '2026-04-27';

      await viewPlanHistory('plan-123');

      expect(startInput.value).toBe('2025-12-15');
      expect(endInput.value).toBe('2026-02-04');
    });

    test('snaps to the LOCAL day of a purchase made after UTC midnight', async () => {
      // 02:00 UTC on Feb 5 is 21:00 EST on Feb 4: the table shows Feb 4,
      // so the To input must too (issue #51).
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: {},
        purchases: [{ timestamp: '2026-02-01T12:00:00Z' }, { timestamp: '2026-02-05T02:00:00Z' }],
      });

      await viewPlanHistory('plan-123');

      expect((document.getElementById('history-start') as HTMLInputElement).value).toBe('2026-02-01');
      expect((document.getElementById('history-end') as HTMLInputElement).value).toBe('2026-02-04');
    });

    test('leaves From/To inputs untouched when the plan has no purchases', async () => {
      // No data → no honest range to display. Clobbering the inputs with
      // `today` would lie to the user; preserve whatever was there.
      (api.getHistory as jest.Mock).mockResolvedValue({
        summary: {},
        purchases: [],
      });

      const startInput = document.getElementById('history-start') as HTMLInputElement;
      const endInput = document.getElementById('history-end') as HTMLInputElement;
      startInput.value = '2026-04-20';
      endInput.value = '2026-04-27';

      await viewPlanHistory('plan-123');

      expect(startInput.value).toBe('2026-04-20');
      expect(endInput.value).toBe('2026-04-27');
    });
  });
});
