jest.mock('chart.js', () => ({ Chart: Object.assign(jest.fn().mockImplementation(() => ({ destroy: jest.fn() })), { register: jest.fn() }), registerables: [] }));
jest.mock('../api/accounts', () => ({ listAccountsMinimal: jest.fn() }));
jest.mock('../api/ladder', () => ({ getLadderEvents: jest.fn(), getLadderRuns: jest.fn(), amendLadderEvent: jest.fn() }));
jest.mock('../permissions', () => ({ canAccess: jest.fn(() => true) }));

import { listAccountsMinimal } from '../api/accounts';
import { getLadderEvents, getLadderRuns, amendLadderEvent, type LadderEvent, type LadderRun } from '../api/ladder';
import { initLadderTimeline } from '../ladder-timeline';
import { canAccess } from '../permissions';
import { Chart } from 'chart.js';

const event: LadderEvent = { id: 'one', config_id: 'config', run_id: 'run', layer_type: 'compute-sp', term: '1yr', payment_option: 'no-upfront', status: 'scheduled', run_status: 'planned', revision: 0, amount_usd_hr: '1.000000', scheduled_date: '2030-01-01T00:00:00Z', created_at: '2026-01-01T00:00:00Z' };
const run: LadderRun = { id: 'run', config_id: 'config', status: 'planned', started_at: '2026-01-01T00:00:00Z', created_at: '2026-01-01T00:00:00Z', baseline_usd_hr: null, existing_usd_hr: null, target_usd_hr: null, gap_usd_hr: null, total_hourly_commit: '1.000000', actions: [] };

beforeEach(() => {
  jest.clearAllMocks();
  jest.mocked(canAccess).mockReturnValue(true);
  document.body.innerHTML = '<section id="timeline"></section>';
  jest.mocked(listAccountsMinimal).mockResolvedValue([{ id: 'account', name: 'Account', external_id: '123', provider: 'aws' }]);
  jest.mocked(getLadderEvents).mockResolvedValue([event]);
  jest.mocked(getLadderRuns).mockResolvedValue([run]);
});

test('real selection opens editor, UTC draft resets and cancel preserves authoritative event', async () => {
  await initLadderTimeline(document.getElementById('timeline')!, [], false);
  document.querySelector<HTMLButtonElement>('[data-event]')!.click();
  const amount = document.getElementById('ladder-event-amount') as HTMLInputElement;
  amount.value = '0.5'; amount.dispatchEvent(new Event('input'));
  expect(document.getElementById('ladder-event-preview')!.textContent).toContain('0.500000');
  document.querySelector<HTMLButtonElement>('.ladder-event-editor [data-reset]')!.click();
  expect(amount.value).toBe('1.000000');
  document.querySelector<HTMLButtonElement>('.ladder-event-editor [data-cancel]')!.click();
  expect(document.querySelector('.ladder-event-editor')).toBeNull();
  expect(amendLadderEvent).not.toHaveBeenCalled();
  expect(event.amount_usd_hr).toBe('1.000000');
});

test('combined filters reset and unsupported providers have distinct states', async () => {
  await initLadderTimeline(document.getElementById('timeline')!, [], false);
  const filter = document.querySelector<HTMLSelectElement>('[data-filter="term"]')!;
  filter.value = '1yr'; filter.dispatchEvent(new Event('change'));
  expect(document.querySelector('[data-summary]')!.textContent).toContain('1 of 1');
  document.querySelector<HTMLButtonElement>('#timeline [data-reset]')!.click();
  expect(filter.value).toBe('');
  const provider = document.querySelector<HTMLSelectElement>('[data-provider]')!;
  provider.value = 'azure'; provider.dispatchEvent(new Event('change'));
  expect(document.querySelector('[data-content]')!.hasAttribute('hidden')).toBe(true);
  expect(getLadderEvents).toHaveBeenCalledTimes(1);
  expect(document.querySelector('[data-state]')!.textContent).toContain('only for AWS');
});

test('graph selection saves once while pending, then reloads authoritative state', async () => {
  let finish!: (value: Awaited<ReturnType<typeof amendLadderEvent>>) => void;
  jest.mocked(amendLadderEvent).mockImplementation(() => new Promise(resolve => { finish = resolve; }));
  await initLadderTimeline(document.getElementById('timeline')!, [], false);
  const options = jest.mocked(Chart).mock.calls[0]?.[1].options;
  const click = options?.onClick as (...args: unknown[]) => void;
  click({}, [{ index: 0, datasetIndex: 0 }]);
  const form = document.querySelector<HTMLFormElement>('.ladder-event-editor form')!;
  form.dispatchEvent(new Event('submit', { cancelable: true }));
  form.dispatchEvent(new Event('submit', { cancelable: true }));
  expect(amendLadderEvent).toHaveBeenCalledTimes(1);
  finish({ id: 'one', revision: 1, scheduled_date: event.scheduled_date, amount_usd_hr: '1.000000', run_total_usd_hr: '1.000000' });
  await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
  expect(document.querySelector('.ladder-event-editor')).toBeNull();
  expect(getLadderEvents).toHaveBeenCalledTimes(2);
});

test('read-only selection never opens an editor', async () => {
  jest.mocked(canAccess).mockReturnValue(false);
  await initLadderTimeline(document.getElementById('timeline')!, [], false);
  document.querySelector<HTMLButtonElement>('[data-event]')!.click();
  expect(document.querySelector('.ladder-event-editor')).toBeNull();
});

test('account discovery retries and old initialization cannot replace the newer view', async () => {
  jest.mocked(listAccountsMinimal).mockRejectedValueOnce(new Error('offline'));
  await initLadderTimeline(document.getElementById('timeline')!, [], false);
  expect(document.querySelector('[data-state]')!.textContent).toContain('offline');
  document.querySelector<HTMLButtonElement>('[data-reload]')!.click();
  await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
  expect(listAccountsMinimal).toHaveBeenCalledTimes(2);
  let release!: (value: Awaited<ReturnType<typeof listAccountsMinimal>>) => void;
  jest.mocked(listAccountsMinimal).mockImplementationOnce(() => new Promise(resolve => { release = resolve; }));
  const older = initLadderTimeline(document.getElementById('timeline')!, [], false);
  await initLadderTimeline(document.getElementById('timeline')!, [], false);
  const calls = jest.mocked(getLadderEvents).mock.calls.length;
  release([{ id: 'stale', name: 'Stale', external_id: '123', provider: 'aws' }]);
  await older;
  expect(getLadderEvents).toHaveBeenCalledTimes(calls);
  expect(document.querySelector('[data-account]')!.textContent).not.toContain('Stale');
});

test('scope changes clear visible dates and active cascading selections stay visible', async () => {
  jest.mocked(listAccountsMinimal).mockResolvedValue([{ id: 'account', name: 'Account', external_id: '123', provider: 'aws' }, { id: 'other', name: 'Other', external_id: '456', provider: 'aws' }]);
  await initLadderTimeline(document.getElementById('timeline')!, [], false);
  const term = document.querySelector<HTMLSelectElement>('[data-filter="term"]')!;
  term.value = '1yr'; term.dispatchEvent(new Event('change'));
  const from = document.querySelector<HTMLInputElement>('[data-date="from"]')!;
  from.value = '2031-01-01T00:00:00'; from.dispatchEvent(new Event('change'));
  expect(term.value).toBe('1yr');
  expect(document.querySelector('[data-summary]')!.textContent).toContain('0 of 1');
  const account = document.querySelector<HTMLSelectElement>('[data-account]')!;
  account.value = 'other'; account.dispatchEvent(new Event('change'));
  expect(from.value).toBe('');
});

test('amount-only amendment preserves the original schedule precision', async () => {
  const precise = { ...event, scheduled_date: '2030-01-01T00:00:00.123456Z' };
  jest.mocked(getLadderEvents).mockResolvedValue([precise]);
  jest.mocked(amendLadderEvent).mockResolvedValue({ id: 'one', revision: 1, amount_usd_hr: '0.5', scheduled_date: precise.scheduled_date, run_total_usd_hr: '0.500000' });
  await initLadderTimeline(document.getElementById('timeline')!, [], false);
  document.querySelector<HTMLButtonElement>('[data-event]')!.click();
  const amount = document.getElementById('ladder-event-amount') as HTMLInputElement;
  amount.value = '0.5'; amount.dispatchEvent(new Event('input'));
  document.querySelector<HTMLFormElement>('.ladder-event-editor form')!.dispatchEvent(new Event('submit', { cancelable: true }));
  expect(amendLadderEvent).toHaveBeenCalledWith('one', { expected_revision: 0, amount_usd_hr: '0.5', scheduled_date: precise.scheduled_date });
  await Promise.resolve(); await Promise.resolve();
});

test('graph legend filters share visible counts and stable layer colors', async () => {
  jest.mocked(getLadderEvents).mockResolvedValue([event, { ...event, id: 'two', layer_type: 'ec2-instance-sp' }]);
  await initLadderTimeline(document.getElementById('timeline')!, [], false);
  const chartConfig = jest.mocked(Chart).mock.calls[0]?.[1];
  const legendClick = chartConfig?.options?.plugins?.legend?.onClick as (...args: unknown[]) => void;
  const color = chartConfig?.data.datasets[1]?.backgroundColor;
  expect(color).toBe('#0891b2');
  legendClick({}, { datasetIndex: 1 });
  expect(document.querySelector('[data-summary]')!.textContent).toContain('1 of 2');
  expect(document.querySelector<HTMLSelectElement>('[data-filter="layer_type"]')!.value).toBe('ec2-instance-sp');
  expect(jest.mocked(Chart).mock.calls[1]?.[1].data.datasets[0]?.backgroundColor).toBe(color);
});

test('save errors retain draft and invalid dates do not reach the API', async () => {
  jest.mocked(amendLadderEvent).mockRejectedValue(new Error('HTTP 409'));
  await initLadderTimeline(document.getElementById('timeline')!, [], false);
  document.querySelector<HTMLButtonElement>('[data-event]')!.click();
  const form = document.querySelector<HTMLFormElement>('.ladder-event-editor form')!;
  const date = document.getElementById('ladder-event-date') as HTMLInputElement;
  date.value = '2020-01-01T00:00:00';
  form.dispatchEvent(new Event('submit', { cancelable: true }));
  expect(amendLadderEvent).not.toHaveBeenCalled();
  date.value = '2030-01-02T00:00:00';
  form.dispatchEvent(new Event('submit', { cancelable: true }));
  await Promise.resolve(); await Promise.resolve();
  expect(amendLadderEvent).toHaveBeenCalledWith('one', { expected_revision: 0, scheduled_date: '2030-01-02T00:00:00.000Z', amount_usd_hr: '1.000000' });
  expect(document.getElementById('ladder-event-error')!.textContent).toContain('draft is retained');
  expect(date.value).toBe('2030-01-02T00:00');
});

const flush = async (): Promise<void> => { for (let i = 0; i < 6; i++) await Promise.resolve(); };

test('a 409 offers reload, which discards the draft, reloads and refocuses the purchase', async () => {
  jest.mocked(amendLadderEvent).mockRejectedValue(Object.assign(new Error('Plan changed'), { status: 409 }));
  await initLadderTimeline(document.getElementById('timeline')!, [], false);
  document.querySelector<HTMLButtonElement>('[data-event]')!.click();
  const reload = document.querySelector<HTMLButtonElement>('.ladder-event-editor [data-reload]')!;
  expect(reload.hidden).toBe(true);
  document.querySelector<HTMLFormElement>('.ladder-event-editor form')!.dispatchEvent(new Event('submit', { cancelable: true }));
  await flush();
  expect(reload.hidden).toBe(false);
  expect(document.getElementById('ladder-event-error')!.textContent).toContain('reload it');
  reload.click();
  await flush();
  expect(document.querySelector('.ladder-event-editor')).toBeNull();
  expect(getLadderEvents).toHaveBeenCalledTimes(2);
  expect(document.activeElement).toBe(document.querySelector('[data-event="one"]'));
});

test('a non-conflict save error does not offer reload', async () => {
  jest.mocked(amendLadderEvent).mockRejectedValue(Object.assign(new Error('Server error'), { status: 500 }));
  await initLadderTimeline(document.getElementById('timeline')!, [], false);
  document.querySelector<HTMLButtonElement>('[data-event]')!.click();
  document.querySelector<HTMLFormElement>('.ladder-event-editor form')!.dispatchEvent(new Event('submit', { cancelable: true }));
  await flush();
  expect(document.querySelector<HTMLButtonElement>('.ladder-event-editor [data-reload]')!.hidden).toBe(true);
});

test('focus returns to the edited purchase after a successful save and the canvas is not a tab stop', async () => {
  jest.mocked(amendLadderEvent).mockResolvedValue({ id: 'one', revision: 1, amount_usd_hr: '1.000000', scheduled_date: '2030-01-02T00:00:00.000Z', run_total_usd_hr: '1.000000' });
  await initLadderTimeline(document.getElementById('timeline')!, [], false);
  expect(document.querySelector('canvas')!.hasAttribute('tabindex')).toBe(false);
  document.querySelector<HTMLButtonElement>('[data-event]')!.click();
  (document.getElementById('ladder-event-date') as HTMLInputElement).value = '2030-01-02T00:00:00';
  document.querySelector<HTMLFormElement>('.ladder-event-editor form')!.dispatchEvent(new Event('submit', { cancelable: true }));
  await flush();
  expect(document.querySelector('.ladder-event-editor')).toBeNull();
  expect(document.activeElement).toBe(document.querySelector('[data-event="one"]'));
});
