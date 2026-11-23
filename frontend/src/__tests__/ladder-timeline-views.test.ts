/**
 * Ladder timeline: planned purchases layout and UTC date filters (issue #753),
 * chart teardown on navigation (issue #667) and run progress (S13). Split from
 * ladder-timeline.test.ts to stay under the 500-line ceiling.
 */

// The harness registers the module mocks, so it must be imported first.
import { event, run, tokenColors } from './ladder-timeline-harness';
import { listAccountsMinimal } from '../api/accounts';
import { getLadderEvents, getLadderRuns, type LadderEvent, type LadderRun } from '../api/ladder';
import { initLadderTimeline, disposeLadderTimeline } from '../ladder-timeline';
import { canAccess } from '../permissions';
import { Chart } from 'chart.js';

beforeEach(() => {
  jest.clearAllMocks();
  Object.entries(tokenColors).forEach(([name, value]) => document.documentElement.style.setProperty(name, value));
  jest.mocked(canAccess).mockReturnValue(true);
  document.body.innerHTML = '<section id="timeline"></section>';
  jest.mocked(listAccountsMinimal).mockResolvedValue([{ id: 'account', name: 'Account', external_id: '123', provider: 'aws' }]);
  jest.mocked(getLadderEvents).mockResolvedValue([event]);
  jest.mocked(getLadderRuns).mockResolvedValue([run]);
});

describe('planned purchases layout and UTC date filters (issue #753)', () => {
  const date = (edge: 'from' | 'to'): HTMLInputElement => document.querySelector<HTMLInputElement>(`[data-date="${edge}"]`)!;
  const type = (edge: 'from' | 'to', value: string): void => { date(edge).value = value; date(edge).dispatchEvent(new Event('change')); };
  const error = (): HTMLElement => document.getElementById('ladder-timeline-date-error')!;
  const summary = (): string => document.querySelector('[data-summary]')!.textContent ?? '';

  test('renders its own card with labelled controls and plain-text UTC dates', async () => {
    await initLadderTimeline(document.getElementById('timeline')!, [], false);
    const card = document.querySelector('#timeline > fieldset.settings-category')!;
    expect(card.querySelector('legend')).toHaveTextContent('Planned purchases');
    card.querySelectorAll('.ladder-timeline-controls select, .ladder-timeline-controls input').forEach(control => {
      expect(card.querySelector(`label[for="${control.id}"]`)).not.toBeNull();
      expect(control.closest('.form-group')).not.toBeNull();
    });
    expect(card.querySelector('input[type="datetime-local"]')).toBeNull();
    expect(date('from').type).toBe('text');
    expect(date('from').value).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}(:\d{2})?$/);
    expect(date('to')).toHaveAttribute('placeholder', 'YYYY-MM-DD HH:MM');
    expect(card.querySelector('[data-reset]')).toHaveClass('btn-tertiary');
  });

  test('valid dates filter and normalize; invalid dates are flagged and ignored until cleared', async () => {
    await initLadderTimeline(document.getElementById('timeline')!, [], false);
    type('from', '');
    type('to', '2029-12-31');
    expect(date('to').value).toBe('2029-12-31 23:59:59');
    expect(summary()).toContain('0 of 1');
    type('to', '2030-01-01');
    expect(summary()).toContain('1 of 1');
    type('to', '01.01.2029');
    expect(date('to')).toHaveAttribute('aria-invalid', 'true');
    expect(error()).not.toHaveClass('hidden');
    expect(error()).toHaveTextContent('To date "01.01.2029" is not a valid UTC date');
    expect(summary()).toContain('1 of 1');
    document.querySelector<HTMLButtonElement>('#timeline [data-reset]')!.click();
    expect(date('to')).not.toHaveAttribute('aria-invalid');
    expect(error()).toHaveClass('hidden');
    expect(date('to').value).toBe('');
  });
});

describe('chart teardown on navigation (issue #667)', () => {
  const chartInstance = (index: number): { destroy: jest.Mock } => jest.mocked(Chart).mock.results[index]!.value as { destroy: jest.Mock };

  test('dispose destroys the chart, stops pending loads and init builds a fresh one', async () => {
    await initLadderTimeline(document.getElementById('timeline')!, [], false);
    expect(Chart).toHaveBeenCalledTimes(1);
    let release!: (value: LadderEvent[]) => void;
    jest.mocked(getLadderEvents).mockImplementationOnce(() => new Promise(resolve => { release = resolve; }));
    document.querySelector<HTMLButtonElement>('[data-reload]')!.click();
    disposeLadderTimeline();
    expect(chartInstance(0).destroy).toHaveBeenCalled();
    release([event]);
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    expect(Chart).toHaveBeenCalledTimes(1);
    disposeLadderTimeline();
    await initLadderTimeline(document.getElementById('timeline')!, [], false);
    expect(Chart).toHaveBeenCalledTimes(2);
  });

  test('a hidden section is not initialized and releases the previous chart', async () => {
    await initLadderTimeline(document.getElementById('timeline')!, [], false);
    document.body.classList.add('hidden');
    const calls = jest.mocked(getLadderEvents).mock.calls.length;
    await initLadderTimeline(document.getElementById('timeline')!, [], false);
    document.body.classList.remove('hidden');
    expect(chartInstance(0).destroy).toHaveBeenCalled();
    expect(Chart).toHaveBeenCalledTimes(1);
    expect(listAccountsMinimal).toHaveBeenCalledTimes(1);
    expect(getLadderEvents).toHaveBeenCalledTimes(calls);
  });
});

describe('run progress (S13)', () => {
  const load = async (patch: Record<string, unknown>) => {
    jest.mocked(getLadderRuns).mockResolvedValue([{ ...run, ...patch } as LadderRun]);
    await initLadderTimeline(document.getElementById('timeline')!, [], false);
    const select = document.querySelector<HTMLSelectElement>('[data-filter="run_id"]')!;
    select.value = 'run'; select.dispatchEvent(new Event('change'));
    return document.querySelector<HTMLElement>('[data-run-progress]')!;
  };
  test('mixed counts omit zero buckets and highlight failed', async () => {
    const el = await load({ tranche_counts: { scheduled: 2, fired: 1, completed: 3, failed: 1, canceled: 0 } });
    expect(el.hidden).toBe(false);
    expect(el.textContent).toBe('Tranches: 2 scheduled, 1 fired, 3 completed, 1 failed.');
    expect(el.querySelector('.ladder-run-failed')!.textContent).toBe('1 failed');
    expect(el.querySelector('[data-status="canceled"]')).toBeNull();
  });
  test('all zero, missing and malformed render nothing', async () => {
    expect((await load({ tranche_counts: { scheduled: 0, fired: 0, completed: 0, failed: 0, canceled: 0 } })).hidden).toBe(true);
    expect((await load({})).hidden).toBe(true);
    const el = await load({ tranche_counts: { scheduled: '<img src=x onerror=alert(1)>', fired: -1, completed: 1.5, failed: NaN, evil: 5 } });
    expect(el.hidden).toBe(true);
    expect(el.querySelector('img')).toBeNull();
  });
  test('only failed is highlighted alone', async () => {
    const el = await load({ tranche_counts: { scheduled: 0, fired: 0, completed: 0, failed: 4, canceled: 0 } });
    expect(el.textContent).toBe('Tranches: 4 failed.');
    expect(el.querySelectorAll('.ladder-run-failed')).toHaveLength(1);
  });
  test('delayed first step true, false and missing', async () => {
    const on = await load({ delayed_first_step: true });
    expect(on.hidden).toBe(false);
    expect(on.querySelector('[data-delayed]')!.textContent).toBe('Delayed first step');
    expect(on.querySelector('[data-delayed]')!.getAttribute('title')).toBe('Delayed first step');
    expect(on.textContent).toBe('Delayed first step: The first tranche is scheduled later than the 24 hour fire window after the run started, so it will not be claimed and this ramp will not buy.');
    expect((await load({ delayed_first_step: false })).hidden).toBe(true);
    expect((await load({})).hidden).toBe(true);
  });
  test('counts and delayed flag combine', async () => {
    const el = await load({ tranche_counts: { scheduled: 1, fired: 0, completed: 0, failed: 0, canceled: 0 }, delayed_first_step: true });
    expect(el.textContent).toContain('Tranches: 1 scheduled. Delayed first step: ');
  });
  test('switching runs replaces then clears the paragraph', async () => {
    const run2 = { ...run, id: 'run2', tranche_counts: { scheduled: 0, fired: 0, completed: 5, failed: 0, canceled: 0 } };
    const run1 = { ...run, tranche_counts: { scheduled: 3, fired: 0, completed: 0, failed: 0, canceled: 0 }, delayed_first_step: true };
    jest.mocked(getLadderEvents).mockResolvedValue([event, { ...event, id: 'two', run_id: 'run2' }]);
    jest.mocked(getLadderRuns).mockResolvedValue([run1, run2] as LadderRun[]);
    await initLadderTimeline(document.getElementById('timeline')!, [], false);
    const select = document.querySelector<HTMLSelectElement>('[data-filter="run_id"]')!;
    const el = document.querySelector<HTMLElement>('[data-run-progress]')!;
    const pick = (value: string) => { select.value = value; select.dispatchEvent(new Event('change')); };
    pick('run');
    expect(el.textContent).toContain('3 scheduled');
    expect(el.querySelector('[data-delayed]')).not.toBeNull();
    pick('run2');
    expect(el.textContent).toBe('Tranches: 5 completed.');
    pick('');
    expect(el.hidden).toBe(true);
    expect(el.textContent).toBe('');
  });
});
