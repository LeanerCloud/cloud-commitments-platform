/**
 * Unit tests for API module (plans and history)
 */
import { fetchMock } from './setup';
import {
  getPlans,
  getPlan,
  createPlan,
  updatePlan,
  patchPlan,
  deletePlan,
  getHistory
} from '../api';
import type { CreatePlanRequest } from '../api';

describe('Plans API', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({})
    });
  });

  describe('getPlans', () => {
    test('fetches all plans', async () => {
      await getPlans();
      expect(fetchMock).toHaveBeenCalledWith('/api/plans', expect.anything());
    });
  });

  describe('getPlan', () => {
    test('fetches single plan by ID', async () => {
      await getPlan('plan-123');
      expect(fetchMock).toHaveBeenCalledWith('/api/plans/plan-123', expect.anything());
    });
  });

  describe('createPlan', () => {
    test('sends POST with plan data', async () => {
      const plan: CreatePlanRequest = {
        name: 'Test Plan',
        enabled: true,
        auto_purchase: false,
        notification_days_before: 3,
        services: { 'aws:ec2': { provider: 'aws', service: 'ec2', enabled: true, term: 3, payment: 'all-upfront', coverage: 80 } },
        ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0, current_step: 0, total_steps: 1 },
        target_accounts: ['11111111-1111-1111-1111-111111111111'],
      };
      await createPlan(plan);

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/plans',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify(plan)
        })
      );
    });
  });

  describe('updatePlan', () => {
    test('sends PUT with plan data', async () => {
      const plan: CreatePlanRequest = {
        name: 'Updated Plan',
        enabled: true,
        auto_purchase: true,
        notification_days_before: 5,
        services: { 'aws:rds': { provider: 'aws', service: 'rds', enabled: true, term: 1, payment: 'no-upfront', coverage: 70 } },
        ramp_schedule: { type: 'weekly', percent_per_step: 25, step_interval_days: 7, current_step: 0, total_steps: 4 },
        target_accounts: ['22222222-2222-2222-2222-222222222222'],
      };
      await updatePlan('plan-123', plan);

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/plans/plan-123',
        expect.objectContaining({
          method: 'PUT',
          body: JSON.stringify(plan)
        })
      );
    });
  });

  describe('patchPlan', () => {
    test('sends PATCH with partial data', async () => {
      await patchPlan('plan-123', { enabled: false });

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/plans/plan-123',
        expect.objectContaining({
          method: 'PATCH',
          body: JSON.stringify({ enabled: false })
        })
      );
    });
  });

  describe('deletePlan', () => {
    test('sends DELETE request', async () => {
      await deletePlan('plan-123');

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/plans/plan-123',
        expect.objectContaining({ method: 'DELETE' })
      );
    });
  });
});

describe('History API', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({})
    });
  });

  describe('getHistory', () => {
    test('fetches with no filters', async () => {
      await getHistory();
      expect(fetchMock).toHaveBeenCalledWith('/api/history', expect.anything());
    });

    test('applies date filters', async () => {
      await getHistory({ start: '2024-01-01', end: '2024-03-31' });

      const url = fetchMock.mock.calls[0][0] as string;
      expect(url).toContain('start=2024-01-01');
      expect(url).toContain('end=2024-03-31');
    });

    test('applies provider and plan filters', async () => {
      await getHistory({ provider: 'aws', planId: 'plan-123' });

      const url = fetchMock.mock.calls[0][0] as string;
      expect(url).toContain('provider=aws');
      expect(url).toContain('plan_id=plan-123');
    });
  });
});
