import { buildLocalScheduleIntent, PlanScheduleIntentInput } from '../plan-schedule-intent';
import { suggestedPurchaseStartDate } from '../lib/calendar-date';
import { renderSchedulePlot, updateSchedulePlot, schedulePlotFrame, schedulePlotX } from '../plan-schedule-plot';

const draft: PlanScheduleIntentInput = { name: 'Compute ramp', preset: 'weekly-25pct', coverage: '80', stepPercent: '25', intervalDays: '7', notificationDays: '3', term: '1', payment: 'monthly' };
beforeEach(() => { jest.useFakeTimers({ now: new Date('2026-10-15T23:59:00Z') }); });
afterEach(() => { jest.useRealTimers(); });

test.each([
  ['weekly-25pct', 4, [25, 50, 75, 100], ['2026-10-20', '2026-10-27', '2026-11-03', '2026-11-10']],
  ['monthly-10pct', 10, [10, 20, 30, 40, 50, 60, 70, 80, 90, 100], ['2026-10-20', '2026-11-19', '2026-12-19']],
  ['immediate', 1, [100], ['2026-10-20']],
] as const)('builds the %s preset schedule with real UTC dates', (preset, count, shares, dates) => {
  const intent = buildLocalScheduleIntent({ ...draft, preset });
  expect(intent.coverage).toBe(80);
  expect(intent.notificationDays).toBe(3);
  expect(intent.shares).toEqual(shares);
  expect(intent.dates).toHaveLength(count);
  expect(intent.dates.slice(0, dates.length)).toEqual(dates);
});

test('counts custom steps with ceil and ends the last share at exactly 100', () => {
  const custom = buildLocalScheduleIntent({ ...draft, preset: 'custom', stepPercent: '33' });
  expect(custom.shares).toEqual([33, 66, 99, 100]);
  expect(custom.dates).toEqual(['2026-10-20', '2026-10-27', '2026-11-03', '2026-11-10']);
  const dense = buildLocalScheduleIntent({ ...draft, preset: 'custom', stepPercent: '1', intervalDays: '1' });
  expect(dense.dates).toHaveLength(100);
  expect(dense.shares).toHaveLength(100);
  expect(dense.shares[99]).toBe(100);
  expect(buildLocalScheduleIntent({ ...draft, preset: 'custom', stepPercent: '100' }).shares).toEqual([100]);
  expect(buildLocalScheduleIntent({ ...draft, coverage: '0' }).coverage).toBe(0);
});

test('every whole percent 1..100 has ceil(100/p) points and ends at 100', () => {
  for (let percent = 1; percent <= 100; percent++) {
    const { shares, dates } = buildLocalScheduleIntent({ ...draft, preset: 'custom', stepPercent: String(percent), intervalDays: '1' });
    expect([percent, shares.length, dates.length, shares[shares.length - 1]]).toEqual([percent, Math.ceil(100 / percent), Math.ceil(100 / percent), 100]);
    expect(shares.slice(0, -1)).toEqual(Array.from({ length: shares.length - 1 }, (_, i) => (i + 1) * percent));
  }
});

test.each([
  { coverage: '' }, { coverage: '101' }, { coverage: '1.5' }, { notificationDays: '0' }, { notificationDays: 'Infinity' },
  { preset: 'toString' }, { preset: 'custom', stepPercent: '0' }, { preset: 'custom', intervalDays: '366' },
])('rejects invalid draft %j', change => {
  expect(() => buildLocalScheduleIntent({ ...draft, ...change })).toThrow();
});

test('uses UTC calendar dates across a year boundary for both real callers', () => {
  expect(suggestedPurchaseStartDate(3, new Date('2026-12-31T23:59:00Z'))).toBe('2027-01-05');
});

test('plots logical ordinals without bridging missing dates or relabeling retained steps', () => {
  const points = [{ step: 2, date: '2026-10-22', target: 33 }, { step: 4, date: '2026-10-29', target: 99 }];
  const frame = schedulePlotFrame(points.map(point => point.date));
  const host = document.createElement('div');
  host.innerHTML = renderSchedulePlot(points, frame, 3, false, null);
  const svg = host.querySelector('svg')!;
  expect(svg.querySelector('[data-preview-step="1"]')?.getAttribute('aria-label')).toContain('Step 2,');
  expect(svg.querySelector('[data-preview-step="3"]')?.hasAttribute('data-selected')).toBe(true);
  expect(svg.querySelector('[data-preview-step="0"]')).toBeNull();
  expect(svg.querySelector('.preview-staircase')?.getAttribute('d')).toBe(` M ${schedulePlotX(points[0]!.date, frame)} ${260 - 33 * 2.2} M ${schedulePlotX(points[1]!.date, frame)} ${260 - 99 * 2.2}`);
  expect(svg.querySelector('[role="button"]')).toBeNull();
  const retained = svg.querySelector('[data-preview-step="3"]');
  const updated = [points[0]!, { ...points[1]!, step: 3, target: 66 }];
  updateSchedulePlot(svg, updated, frame, 2, false, null);
  expect(svg.querySelector('[data-preview-step="2"]')).toBe(retained);
  expect(retained?.getAttribute('aria-label')).toContain('Step 3,');
  expect(svg.querySelector('.preview-staircase')?.getAttribute('d')).toBe(` M ${schedulePlotX(points[0]!.date, frame)} ${260 - 33 * 2.2} H ${schedulePlotX(points[1]!.date, frame)} V ${260 - 66 * 2.2}`);
});

test('preserves contiguous plot geometry and uses only the known calendar extent', () => {
  const points = [{ step: 1, date: '2026-10-22', target: 25 }, { step: 2, date: '2026-10-29', target: 50 }];
  const frame = schedulePlotFrame(points.map(point => point.date));
  const host = document.createElement('div');
  host.innerHTML = renderSchedulePlot(points, frame, 0, true, null);
  expect(host.querySelector('.preview-staircase')?.getAttribute('d')).toBe(`M 48 260 H ${schedulePlotX(points[0]!.date, frame)} V 205 H ${schedulePlotX(points[1]!.date, frame)} V 150`);
  expect(host.querySelectorAll('[data-preview-boundary]')).toHaveLength(2);
  expect(host.querySelector('[data-preview-step="0"]')?.getAttribute('tabindex')).toBe('0');
  expect(schedulePlotFrame([...points.map(point => point.date)].reverse())).toEqual(frame);
  expect(() => schedulePlotFrame([])).toThrow('No dated steps');
  expect(schedulePlotFrame(['2026-10-22']).span).toBe(8);
  expect(schedulePlotFrame(Array.from({ length: 100 }, () => '2026-10-22')).width).toBe(4896);
});
