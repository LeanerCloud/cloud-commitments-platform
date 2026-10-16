import { buildCreateRequest, cumulativeStepCount, CreateRequestInput } from '../lib/plan-editor-request';
import type { PlanEditorRequest } from '../api/plan-editor';
import vectors from './fixtures/cumulative-step-count.json';

const ID = '3f2b8c1e-5d4a-4e6f-8a9b-0c1d2e3f4a5b', A1 = '11111111-1111-4111-8111-111111111111', A2 = '22222222-2222-4222-8222-222222222222';
const build = (over: Partial<CreateRequestInput> = {}): PlanEditorRequest => {
  const out = buildCreateRequest(input(over));
  if ('unavailable' in out) throw new Error(out.unavailable);
  return out;
};
const unavailable = (over: Partial<CreateRequestInput>): string => {
  const out = buildCreateRequest(input(over));
  if (!('unavailable' in out)) throw new Error('expected unavailable');
  return out.unavailable;
};
const input = (over: Partial<CreateRequestInput> = {}): CreateRequestInput => ({
  name: 'Weekly EC2', enabled: true, autoPurchase: false, notifyDays: 3, accountIds: [A1, A2],
  service: { provider: 'aws', service: 'ec2', term: 3, payment: 'all-upfront', coverage: 80 },
  ramp: { preset: 'weekly-25pct', intervalDays: 7, percentPerStep: 25 },
  dates: ['2026-11-01', '2026-11-09', '2026-11-20', '2026-12-01'], commandId: ID, ...over,
});

describe('buildCreateRequest', () => {
  it('builds the cumulative-percent body from the dragged dates', () => {
    const request = build();
    expect(request).toEqual({
      command_id: ID, expected_revision: null,
      plan: {
        name: 'Weekly EC2', enabled: true, auto_purchase: false, notification_days_before: 3, target_accounts: [A1, A2],
        services: { 'aws/ec2': { provider: 'aws', service: 'ec2', enabled: true, term: 3, payment: 'all-upfront', coverage: 80, ramp_schedule: 'weekly-25pct' } },
        ramp_schedule: { type: 'weekly', percent_per_step: 25, step_interval_days: 7, start_date: '2026-11-01' },
      },
      steps: [{ step_number: 1, scheduled_date: '2026-11-01' }, { step_number: 2, scheduled_date: '2026-11-09' },
        { step_number: 3, scheduled_date: '2026-11-20' }, { step_number: 4, scheduled_date: '2026-12-01' }],
      allow_approved_edits: false, acknowledged_cancellation_ids: [], acknowledged_reapproval_ids: [],
    });
    for (const key of ['step_count', 'reviewed_diff', 'recommendations', 'allocation_mode', 'id']) expect(key in request).toBe(false);
  });
  it('maps each preset to its ramp type and scalar', () => {
    const imm = build({ ramp: { preset: 'immediate', intervalDays: 0, percentPerStep: 100 }, dates: ['2026-11-01'] });
    expect(imm.plan.ramp_schedule).toEqual({ type: 'immediate', percent_per_step: 100, step_interval_days: 0, start_date: '2026-11-01' });
    expect(imm.plan.services!['aws/ec2']!.ramp_schedule).toBe('immediate');
    const monthly = build({ ramp: { preset: 'monthly-10pct', intervalDays: 30, percentPerStep: 10 }, dates: Array.from({ length: 10 }, (_, i) => `2026-12-${String(i + 1).padStart(2, '0')}`) });
    expect(monthly.plan.ramp_schedule!.type).toBe('monthly');
    const custom = build({ ramp: { preset: 'custom', intervalDays: 5, percentPerStep: 50 }, dates: ['2026-11-01', '2026-11-06'] });
    expect(custom.plan.ramp_schedule).toEqual({ type: 'custom', percent_per_step: 50, step_interval_days: 5, start_date: '2026-11-01' });
    expect(custom.plan.services!['aws/ec2']!.ramp_schedule).toBe('custom');
  });
  it('carries every field from the input, not from fixture constants', () => {
    const request = build({ name: 'Other', enabled: false, autoPurchase: true, notifyDays: 9, accountIds: [A2],
      service: { provider: 'azure', service: 'compute', term: 1, payment: 'monthly', coverage: 55 },
      ramp: { preset: 'custom', intervalDays: 11, percentPerStep: 50 }, dates: ['2027-01-05', '2027-01-20'], commandId: A1 });
    expect(request).toEqual({
      command_id: A1, expected_revision: null,
      plan: {
        name: 'Other', enabled: false, auto_purchase: true, notification_days_before: 9, target_accounts: [A2],
        services: { 'azure/compute': { provider: 'azure', service: 'compute', enabled: true, term: 1, payment: 'monthly', coverage: 55, ramp_schedule: 'custom' } },
        ramp_schedule: { type: 'custom', percent_per_step: 50, step_interval_days: 11, start_date: '2027-01-05' },
      },
      steps: [{ step_number: 1, scheduled_date: '2027-01-05' }, { step_number: 2, scheduled_date: '2027-01-20' }],
      allow_approved_edits: false, acknowledged_cancellation_ids: [], acknowledged_reapproval_ids: [],
    });
  });
  it('takes start_date from the first date, not from today', () => {
    expect(build({ dates: ['2027-03-01', '2027-03-08', '2027-03-15', '2027-03-22'] }).plan.ramp_schedule!.start_date).toBe('2027-03-01');
  });
  it('returns a sentence for every user-reachable problem', () => {
    expect(unavailable({ accountIds: [] })).toMatch(/account/i);
    expect(unavailable({ dates: ['2026-11-01', '2026-11-09', '2026-11-20'] })).toMatch(/4 dates/);
    expect(unavailable({ service: { ...input().service, provider: 'azure', service: 'vm' } })).toMatch(/azure\/vm/);
    expect(unavailable({ service: { ...input().service, service: 'bogus' } })).toMatch(/aws\/bogus/);
    for (const percentPerStep of [0, -5, 101, NaN]) expect(unavailable({ ramp: { preset: 'custom', intervalDays: 7, percentPerStep } })).toMatch(/percent/i);
    expect(unavailable({ ramp: { preset: 'custom', intervalDays: 7, percentPerStep: 0.5 }, dates: ['2026-11-01'] })).toMatch(/200 steps.*100/);
    for (const intervalDays of [-1, 366, 1.5, NaN]) expect(unavailable({ ramp: { preset: 'weekly-25pct', intervalDays, percentPerStep: 25 } })).toMatch(/interval/i);
    expect(build({ ramp: { preset: 'custom', intervalDays: 365, percentPerStep: 25 } }).plan.ramp_schedule!.step_interval_days).toBe(365);
    expect(build({ ramp: { preset: 'custom', intervalDays: 0, percentPerStep: 25 } }).plan.ramp_schedule!.step_interval_days).toBe(0);
    expect(build({ ramp: { preset: 'custom', intervalDays: 1, percentPerStep: 1 }, dates: Array.from({ length: 100 }, (_, i) => `2027-0${1 + Math.floor(i / 28)}-${String(1 + i % 28).padStart(2, '0')}`) }).steps).toHaveLength(100);
  });
  it('lets only custom choose the percent', () => {
    expect(unavailable({ ramp: { preset: 'immediate', intervalDays: 0, percentPerStep: 50 }, dates: ['a', 'b'] })).toMatch(/preset/);
    expect(unavailable({ ramp: { preset: 'weekly-25pct', intervalDays: 7, percentPerStep: 50 }, dates: ['a', 'b'] })).toMatch(/preset/);
    expect(unavailable({ ramp: { preset: 'monthly-10pct', intervalDays: 30, percentPerStep: 25 } })).toMatch(/preset/);
  });
  it('throws for an unknown preset, including inherited property names', () => {
    for (const preset of ['nope', 'toString', '__proto__', 'constructor']) {
      expect(() => buildCreateRequest(input({ ramp: { preset, intervalDays: 7, percentPerStep: 25 } }))).toThrow(/Unknown ramp/);
    }
  });
});

describe('cumulativeStepCount', () => {
  it('matches the shared vectors (also read by the Go C1a test)', () => {
    expect(vectors.length).toBeGreaterThan(100);
    for (const row of vectors) expect([row.percent, cumulativeStepCount(row.percent)]).toEqual([row.percent, row.steps]);
  });
});
