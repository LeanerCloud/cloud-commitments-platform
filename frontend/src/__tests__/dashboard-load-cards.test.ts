/**
 * Dashboard module tests: loadDashboard upcoming purchase card and details dialog tests.
 * Split from dashboard.test.ts to stay under the 500-line ceiling; the
 * module mocks live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockShowToast, mockConfirmDialog, mockGroupRecsByCell, mockPageLevelRange, mockFormatSavingsRange } from './dashboard-harness';
import { loadDashboard } from '../dashboard';
import * as api from '../api';
import * as state from '../state';
import { flushPromises } from './flush-promises';

describe('Dashboard Module', () => {
  beforeEach(() => {
    // Reset DOM
    document.body.innerHTML = `
      <div id="summary"></div>
      <section id="savings-by-service-section"><h3>Potential savings range per service</h3>
        <canvas id="savings-by-service-chart"></canvas>
        <p id="savings-by-service-empty" class="empty hidden"></p>
      </section>
      <div id="upcoming-list"></div>
    `;

    jest.clearAllMocks();
    window.alert = jest.fn();
    window.confirm = jest.fn().mockReturnValue(true);
    mockShowToast.mockClear();
    mockConfirmDialog.mockReset();
    mockConfirmDialog.mockImplementation(() => Promise.resolve(true));
    // Re-initialize recommendation mocks after clearAllMocks().
    mockGroupRecsByCell.mockImplementation((recs: unknown[]) => new Map(recs.length ? [['cell-1', recs]] : []));
    mockPageLevelRange.mockImplementation((groups: Map<string, unknown[]>) => {
      if (groups.size === 0) return { savingsMin: 0, savingsMax: 0, cellCount: 0 };
      return { savingsMin: 300, savingsMax: 400, cellCount: groups.size };
    });
    mockFormatSavingsRange.mockImplementation((min: number, max: number) => min === max ? `$${min}` : `$${min} – $${max}`);
    // Default: empty recs so existing tests are unaffected by the range.
    (api.getRecommendations as jest.Mock).mockResolvedValue([]);
  });

  describe('loadDashboard', () => {
    test('uses current provider filter', async () => {
      (state.getCurrentProvider as jest.Mock).mockReturnValue('aws');
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 1000,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({
        purchases: []
      });

      await loadDashboard();

      expect(api.getDashboardSummary).toHaveBeenCalledWith('aws', []);
    });

    test('renders view details and cancel buttons for upcoming purchases', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 1000,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({
        purchases: [
          {
            execution_id: 'exec-1', plan_id: 'plan-1',
            plan_name: 'Test Plan',
            provider: 'aws',
            service: 'ec2',
            step_number: 1,
            total_steps: 4,
            estimated_savings: 100,
            scheduled_date: '2024-02-15'
          }
        ]
      });

      await loadDashboard();

      const upcomingList = document.getElementById('upcoming-list');
      expect(upcomingList?.innerHTML).toContain('data-action="view-purchase"');
      expect(upcomingList?.innerHTML).toContain('data-action="cancel-purchase"');
    });

    test('row-level cancel button is the outlined destructive variant, not the filled one', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({ potential_monthly_savings: 1000, by_service: {} });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({
        purchases: [{
          execution_id: 'exec-1', plan_id: 'plan-1', plan_name: 'Test Plan', provider: 'aws', service: 'ec2',
          step_number: 1, total_steps: 4, estimated_savings: 100, scheduled_date: '2024-02-15',
        }],
      });

      await loadDashboard();

      const cancelBtn = document.querySelector<HTMLButtonElement>('#upcoming-list [data-action="cancel-purchase"]');
      expect(cancelBtn?.classList.contains('btn-danger')).toBe(true);
      expect(cancelBtn?.classList.contains('danger')).toBe(false);
    });

    test('handles missing canvas element gracefully', async () => {
      document.body.innerHTML = `
        <div id="summary"></div>
        <div id="upcoming-list"></div>
      `;

      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 1000,
        by_service: { ec2: { potential_savings: 100, current_savings: 50 } }
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({
        purchases: []
      });

      await expect(loadDashboard()).resolves.not.toThrow();
    });

    test('view purchase button shows details', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 1000,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({
        purchases: [
          {
            execution_id: 'exec-123', plan_id: 'plan-123',
            plan_name: 'Test Plan',
            provider: 'aws',
            service: 'ec2',
            step_number: 1,
            total_steps: 4,
            estimated_savings: 100,
            scheduled_date: '2024-02-15'
          }
        ]
      });

      await loadDashboard();

      const viewBtn = document.querySelector('[data-action="view-purchase"]') as HTMLButtonElement;
      viewBtn?.click();

      // viewPurchaseDetails is sync now — renders from the in-memory
      // upcoming-purchases index (issues #204 + #205). No API call to
      // /api/purchases/{id}, since the row identifier is a plan_id and
      // there's no execution row yet for an upcoming purchase.
      expect(api.getPurchaseDetails).not.toHaveBeenCalled();
      const modal = document.getElementById('purchase-details-modal');
      expect(modal).toBeTruthy();
      expect(modal?.textContent).toContain('Test Plan');
      expect(modal?.textContent).toContain('exec-123');
    });

    // platform#609 / #620: a step with no recommendations must not render its
    // placeholder savings as data, on the card or in the details dialog.
    describe.each([
      ['false', false, true],
      ['true', true, false],
      ['absent', undefined, false],
    ])('has_recommendations %s', (_label, flag, bare) => {
      test('renders card and details dialog accordingly', async () => {
        (api.getDashboardSummary as jest.Mock).mockResolvedValue({
          potential_monthly_savings: 1000,
          by_service: {}
        });
        (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({
          purchases: [
            {
              execution_id: 'exec-620', plan_id: 'plan-620',
              plan_name: 'Test Plan',
              provider: 'aws',
              service: 'ec2',
              step_number: 1,
              total_steps: 4,
              estimated_savings: 100,
              scheduled_date: '2024-02-15',
              ...(flag === undefined ? {} : { has_recommendations: flag })
            }
          ]
        });

        await loadDashboard();

        const card = document.querySelector('.upcoming-savings');
        const viewBtn = document.querySelector('[data-action="view-purchase"]') as HTMLButtonElement;
        viewBtn.click();
        const modal = document.getElementById('purchase-details-modal');

        if (bare) {
          expect(card?.textContent).toContain('—');
          expect(card?.textContent).toContain('No recommendations attached');
          expect(card?.textContent).not.toContain('100');
          expect(modal?.textContent).toContain('No recommendations attached');
          expect(modal?.textContent).not.toContain('$100');
        } else {
          expect(card?.textContent).toContain('100');
          expect(card?.textContent).toContain('Est. monthly savings');
          expect(card?.textContent).not.toContain('No recommendations attached');
          expect(modal?.textContent).toContain('100');
          expect(modal?.textContent).not.toContain('No recommendations attached');
        }
        modal?.remove();
      });
    });

    // (The previous "shows error on failure" test exercised an API
    // rejection path that no longer exists — viewPurchaseDetails is
    // synchronous after the data-flow fix. The graceful-fallback for a
    // pruned-from-index plan_id is enforced by the `if (!purchase) {
    // showToast(...) }` guard in dashboard.ts but isn't easily reached
    // through the public surface, so it's covered by code-review only.)

    test('cancel purchase button cancels and reloads', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 1000,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({
        purchases: [
          {
            execution_id: 'exec-123', plan_id: 'plan-123',
            plan_name: 'Test Plan',
            provider: 'aws',
            service: 'ec2',
            step_number: 1,
            total_steps: 4,
            estimated_savings: 100,
            scheduled_date: '2024-02-15'
          }
        ]
      });
      (api.deletePlannedPurchase as jest.Mock).mockResolvedValue({});
      window.confirm = jest.fn().mockReturnValue(true);

      await loadDashboard();

      // Reset mocks to track the reload
      (api.getDashboardSummary as jest.Mock).mockClear();
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });

      const cancelBtn = document.querySelector('[data-action="cancel-purchase"]') as HTMLButtonElement;
      cancelBtn?.click();

      await flushPromises();

      expect(api.deletePlannedPurchase).toHaveBeenCalledWith('exec-123');
      // Cancel must NOT delete the entire plan — that was the wrong fix
      // in PR #207. deletePlan should not be called from this path.
      expect(api.deletePlan).not.toHaveBeenCalled();
      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
        message: 'Purchase cancelled successfully',
        kind: 'success',
      }));
    });

    test('cancel purchase does nothing if user declines confirmation', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 1000,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({
        purchases: [
          {
            execution_id: 'exec-123', plan_id: 'plan-123',
            plan_name: 'Test Plan',
            provider: 'aws',
            service: 'ec2',
            step_number: 1,
            total_steps: 4,
            estimated_savings: 100,
            scheduled_date: '2024-02-15'
          }
        ]
      });
      mockConfirmDialog.mockResolvedValueOnce(false);

      await loadDashboard();

      const cancelBtn = document.querySelector('[data-action="cancel-purchase"]') as HTMLButtonElement;
      cancelBtn?.click();

      await flushPromises();

      expect(api.deletePlannedPurchase).not.toHaveBeenCalled();
    });

    test('cancel purchase shows error on failure', async () => {
      (api.getDashboardSummary as jest.Mock).mockResolvedValue({
        potential_monthly_savings: 1000,
        by_service: {}
      });
      (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({
        purchases: [
          {
            execution_id: 'exec-123', plan_id: 'plan-123',
            plan_name: 'Test Plan',
            provider: 'aws',
            service: 'ec2',
            step_number: 1,
            total_steps: 4,
            estimated_savings: 100,
            scheduled_date: '2024-02-15'
          }
        ]
      });
      (api.deletePlannedPurchase as jest.Mock).mockRejectedValue(new Error('API Error'));
      window.confirm = jest.fn().mockReturnValue(true);
      console.error = jest.fn();

      await loadDashboard();

      const cancelBtn = document.querySelector('[data-action="cancel-purchase"]') as HTMLButtonElement;
      cancelBtn?.click();

      await flushPromises();

      expect(mockShowToast).toHaveBeenCalledWith(expect.objectContaining({
        message: 'Failed to cancel purchase',
        kind: 'error',
      }));
    });

    // #731: the details dialog goes through openModal and shares the card's
    // cancel path (same confirm copy, one DELETE per step at a time).
    describe('upcoming purchase details dialog (#731)', () => {
      const upcoming = {
        execution_id: 'exec-731', plan_id: 'plan-731', plan_name: 'Test Plan', provider: 'aws',
        service: 'ec2', step_number: 1, total_steps: 4, estimated_savings: 100, scheduled_date: '2024-02-15',
      };

      async function openDetails(): Promise<{ viewBtn: HTMLButtonElement; modal: HTMLElement }> {
        (api.getDashboardSummary as jest.Mock).mockResolvedValue({ potential_monthly_savings: 0, by_service: {} });
        (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [upcoming] });
        await loadDashboard();
        const viewBtn = document.querySelector('[data-action="view-purchase"]') as HTMLButtonElement;
        viewBtn.focus();
        viewBtn.click();
        return { viewBtn, modal: document.getElementById('purchase-details-modal') as HTMLElement };
      }

      test('is a named dialog that takes focus, closes on Escape and restores focus', async () => {
        const { viewBtn, modal } = await openDetails();
        expect(modal.getAttribute('role')).toBe('dialog');
        expect(modal.getAttribute('aria-modal')).toBe('true');
        const titleId = modal.getAttribute('aria-labelledby') ?? '';
        expect(document.getElementById(titleId)?.textContent).toBe('Upcoming Purchase Details');
        expect(modal.contains(document.activeElement)).toBe(true);

        modal.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
        expect(document.getElementById('purchase-details-modal')).toBeNull();
        expect(document.activeElement).toBe(viewBtn);
      });

      // #1653: opening the dialog must not put focus on the destructive action.
      test('initial focus lands on Close, not Cancel Purchase', async () => {
        const { modal } = await openDetails();
        expect(modal.querySelector('#cancel-purchase-detail-btn')).not.toBeNull();
        expect(document.activeElement?.id).toBe('close-purchase-details-btn');
      });

      test('Cancel Purchase uses the card confirm copy and sends one DELETE while in flight', async () => {
        const { modal } = await openDetails();
        let finishDelete: () => void = () => {};
        (api.deletePlannedPurchase as jest.Mock).mockReturnValue(new Promise<void>(r => { finishDelete = r; }));
        mockConfirmDialog.mockClear();

        const cancelBtn = modal.querySelector('#cancel-purchase-detail-btn') as HTMLButtonElement;
        cancelBtn.click();
        await new Promise(resolve => setTimeout(resolve, 0));
        expect(cancelBtn.disabled).toBe(true);
        cancelBtn.disabled = false; // force a second click through
        cancelBtn.click();
        (document.querySelector('[data-action="cancel-purchase"]') as HTMLButtonElement).click();
        await new Promise(resolve => setTimeout(resolve, 0));

        expect(mockConfirmDialog).toHaveBeenCalledTimes(1);
        const body = (mockConfirmDialog.mock.calls[0]![0] as { body: string }).body;
        expect(body).toContain('removes this upcoming step from the plan');
        expect(body).not.toContain('refunded');
        expect(api.deletePlannedPurchase).toHaveBeenCalledTimes(1);

        (api.getUpcomingPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
        finishDelete();
        await flushPromises();
        expect(document.getElementById('purchase-details-modal')).toBeNull();
        expect(api.deletePlannedPurchase).toHaveBeenCalledWith('exec-731');
      });
    });
  });
});
