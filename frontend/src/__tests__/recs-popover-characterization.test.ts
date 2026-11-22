/**
 * Characterization of the Opportunities column-filter popover (issue #18, T0): open/close, focus, aria, listeners, keyboard, resize/scroll/rebind, tab observer.
 * Pins CURRENT observable behavior so the migration onto lib/column-filter-popover
 * can be checked against it. Real DOM, real state and recsflow code; only the
 * network modules are mocked.
 */
import { loadRecommendations } from '../recommendations';
import { closePopover, rerenderRecommendations } from '../recsflow/popover';
import { closeVisibilityPopover } from '../recsflow/visibility';
import { resetColumnFiltersState } from '../recsflow/columnPrefs';
import * as api from '../api';
import * as state from '../state';
import { flushPromises } from './flush-promises';

jest.mock('../api', () => ({
  getRecommendations: jest.fn(),
  refreshRecommendations: jest.fn(),
  listAccounts: jest.fn().mockResolvedValue([]),
  listAccountsMinimal: jest.fn().mockResolvedValue([]),
  getConfig: jest.fn().mockResolvedValue({ global: {} }),
  listAccountServiceOverrides: jest.fn().mockResolvedValue([]),
}));
jest.mock('../api/recommendations', () => ({
  getRecommendationDetail: jest.fn().mockResolvedValue({ id: 'x', usage_history: [], confidence_bucket: 'low', provenance_note: '' }),
  getRecommendationsFreshness: jest.fn().mockImplementation(() => Promise.resolve({
    last_collected_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
    last_collection_error: null,
  })),
  refreshRecommendations: jest.fn().mockResolvedValue({}),
}));
jest.mock('../toast', () => ({ showToast: jest.fn(() => ({ dismiss: jest.fn() })) }));

const rec = (id: string, service: string, resource_type: string, region: string, savings: number) => (
  { id, provider: 'aws', cloud_account_id: 'a1', service, resource_type, region, count: 1, term: 1, savings, upfront_cost: 500 }
);
const RECS = [
  rec('r1', 'ec2', 't3.medium', 'us-east-1', 100),
  rec('r2', 'rds', 'db.t3', 'us-west-2', 200),
  rec('r3', 'savings-plans-compute', 'sp', 'us-east-1', 300),
  rec('r4', 'savings-plans-ec2instance', 'sp2', 'us-east-1', 400),
];
const SP_COMPUTE = 'savings-plans-compute';

// The tab observer is a module singleton bound to the first #opportunities-tab
// it sees, so every test must reuse this one element.
const tab = document.createElement('div');
tab.id = 'opportunities-tab';

// Ledger of document/window listeners so we can assert add/remove symmetry.
type Entry = { type: string; fn: unknown; cap: boolean };
let ledger: Entry[] = [];
const spies: jest.SpyInstance[] = [];
const isCap = (o: unknown): boolean => o === true || (typeof o === 'object' && o !== null && (o as { capture?: boolean }).capture === true);
function trackListeners(target: Document | Window): void {
  const add = target.addEventListener;
  const remove = target.removeEventListener;
  spies.push(jest.spyOn(target, 'addEventListener').mockImplementation(function (this: unknown, type: string, fn: unknown, o?: unknown) {
    if (!ledger.some((e) => e.type === type && e.fn === fn && e.cap === isCap(o))) ledger.push({ type, fn, cap: isCap(o) });
    return (add as (...a: unknown[]) => void).call(this, type, fn, o);
  }));
  spies.push(jest.spyOn(target, 'removeEventListener').mockImplementation(function (this: unknown, type: string, fn: unknown, o?: unknown) {
    ledger = ledger.filter((e) => !(e.type === type && e.fn === fn && e.cap === isCap(o)));
    return (remove as (...a: unknown[]) => void).call(this, type, fn, o);
  }));
}
const counts = (): Record<string, number> => {
  const out: Record<string, number> = { mousedown: 0, keydown: 0, scroll: 0, resize: 0 };
  for (const e of ledger) if (e.type in out) out[e.type]!++;
  return out;
};
const ZERO = { mousedown: 0, keydown: 0, scroll: 0, resize: 0 };
const ONE_SET = { mousedown: 1, keydown: 1, scroll: 1, resize: 1 };

async function render(recs: unknown[] = RECS): Promise<void> {
  (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
  await loadRecommendations();
}
const trigger = (col: string): HTMLButtonElement => document.querySelector<HTMLButtonElement>(`th .column-filter-btn[data-column="${col}"]`)!;
const pop = (): HTMLElement | null => document.querySelector<HTMLElement>('.column-filter-popover');
const open = (col: string): HTMLButtonElement => { const t = trigger(col); t.click(); return t; };
const box = (value: string): HTMLInputElement => pop()!.querySelector<HTMLInputElement>(`input[data-value="${value}"]`)!;
const toggle = (cb: HTMLInputElement, checked: boolean): void => { cb.checked = checked; cb.dispatchEvent(new Event('change', { bubbles: true })); };
const press = (key: string, target: EventTarget = document.body): KeyboardEvent => {
  const e = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true });
  target.dispatchEvent(e);
  return e;
};
const mouseDown = (target: EventTarget): void => { target.dispatchEvent(new MouseEvent('mousedown', { bubbles: true })); };
const rectAt = (left: number) => () => ({ left, top: 300, width: 24, height: 24, right: left + 24, bottom: 324, x: left, y: 300, toJSON: () => ({}) }) as DOMRect;

beforeEach(() => {
  document.body.replaceChildren(tab);
  tab.className = 'tab-content active';
  const list = document.createElement('div');
  list.id = 'recommendations-list';
  const summary = document.createElement('div');
  summary.id = 'recommendations-summary';
  tab.replaceChildren(summary, list);
  state.clearAllRecommendationsColumnFilters();
  state.setHiddenColumns(new Set());
  resetColumnFiltersState();
  jest.clearAllMocks();
  ledger = [];
  trackListeners(document);
  trackListeners(window);
});
afterEach(() => {
  closePopover();
  closeVisibilityPopover();
  spies.splice(0).forEach((s) => s.mockRestore());
});

describe('aria, roles and focus on open', () => {
  test('trigger aria-expanded flips true then false; popover is a non-modal dialog labelled by its heading', async () => {
    await render();
    expect(trigger('service').getAttribute('aria-expanded')).toBe('false');
    open('service');
    expect(trigger('service').getAttribute('aria-expanded')).toBe('true');
    const p = pop()!;
    expect(p.getAttribute('role')).toBe('dialog');
    expect(p.getAttribute('aria-modal')).toBe('false');
    expect(document.getElementById(p.getAttribute('aria-labelledby')!)!.textContent).toBe('Filter Service');
    trigger('service').click();
    expect(trigger('service').getAttribute('aria-expanded')).toBe('false');
    expect(pop()).toBeNull();
  });

  test('focus moves into the popover: first checkbox for set mode, the input for numeric mode', async () => {
    await render();
    open('service');
    expect(document.activeElement).toBe(pop()!.querySelector('input[type="checkbox"]'));
    expect(pop()!.contains(document.activeElement)).toBe(true);
    trigger('service').click();
    open('savings');
    expect(document.activeElement).toBe(pop()!.querySelector('.column-filter-numeric-input'));
  });
});

describe('focus on each close path', () => {
  test('Escape restores focus to the trigger and prevents default', async () => {
    await render();
    const t = open('service');
    const e = press('Escape');
    expect(e.defaultPrevented).toBe(true);
    expect(pop()).toBeNull();
    expect(t.getAttribute('aria-expanded')).toBe('false');
    expect(document.activeElement).toBe(t);
  });

  test('toggle click restores focus to the trigger', async () => {
    await render();
    const t = open('service');
    t.click();
    expect(pop()).toBeNull();
    expect(document.activeElement).toBe(t);
  });

  test('outside mousedown closes without moving focus to the trigger', async () => {
    await render();
    const t = open('service');
    mouseDown(document.body);
    expect(pop()).toBeNull();
    expect(t.getAttribute('aria-expanded')).toBe('false');
    expect(document.activeElement).not.toBe(t);
  });

  test('a commit does not close the popover or move focus out of it', async () => {
    await render();
    open('resource_type');
    toggle(box('db.t3'), false);
    expect(pop()).not.toBeNull();
    expect(trigger('resource_type').getAttribute('aria-expanded')).toBe('true');
  });

  test('switching to another column closes the first, leaves one popover and focuses inside the new one', async () => {
    await render();
    const first = open('service');
    const second = open('region');
    expect(document.querySelectorAll('.column-filter-popover')).toHaveLength(1);
    expect(first.getAttribute('aria-expanded')).toBe('false');
    expect(second.getAttribute('aria-expanded')).toBe('true');
    expect(pop()!.contains(document.activeElement)).toBe(true);
    expect(document.activeElement).not.toBe(first);
  });

  test('scroll-out close does not steal focus to the trigger', async () => {
    await render();
    const t = open('service');
    document.body.dispatchEvent(new Event('scroll', { bubbles: true }));
    expect(pop()).toBeNull();
    expect(document.activeElement).not.toBe(t);
  });
});

describe('outside-click rules', () => {
  test('mousedown inside the popover or on another filter trigger does not close it', async () => {
    await render();
    open('service');
    mouseDown(pop()!.querySelector('.column-filter-list')!);
    expect(pop()).not.toBeNull();
    mouseDown(trigger('region'));
    expect(pop()).not.toBeNull();
  });
});

describe('listener symmetry', () => {
  test('open registers one mousedown, keydown, scroll and resize handler; open-close-open does not accumulate', async () => {
    await render();
    expect(counts()).toEqual(ZERO);
    const t = open('service');
    expect(counts()).toEqual(ONE_SET);
    t.click();
    expect(counts()).toEqual(ZERO);
    open('service');
    expect(counts()).toEqual(ONE_SET);
    open('region');
    expect(counts()).toEqual(ONE_SET);
  });

  const closers: Array<[string, () => Promise<void>]> = [
    ['Escape', async () => { press('Escape'); }],
    ['outside mousedown', async () => { mouseDown(document.body); }],
    ['toggle click', async () => { trigger('service').click(); }],
    ['scroll with trigger out of view', async () => { document.body.dispatchEvent(new Event('scroll', { bubbles: true })); }],
    ['resize after the trigger is removed', async () => { trigger('service').remove(); window.dispatchEvent(new Event('resize')); }],
    ['tab hidden', async () => { tab.classList.remove('active'); await flushPromises(); }],
    ['swap column then Escape', async () => { open('region'); press('Escape'); }],
    ['rebind after the column is hidden', async () => { state.setHiddenColumns(new Set(['region'])); rerenderRecommendations(); }],
  ];
  test.each(closers)('after %s the registered handler count returns to its start', async (name, close) => {
    await render();
    open(name === 'rebind after the column is hidden' ? 'region' : 'service');
    expect(counts()).toEqual(ONE_SET);
    await close();
    expect(pop()).toBeNull();
    expect(counts()).toEqual(ZERO);
  });

  test('after close, Escape, outside mousedown, scroll and resize do nothing', async () => {
    await render();
    const t = open('service');
    t.click();
    t.getBoundingClientRect = jest.fn(rectAt(10));
    const before = document.activeElement;
    expect(press('Escape').defaultPrevented).toBe(false);
    mouseDown(document.body);
    document.body.dispatchEvent(new Event('scroll', { bubbles: true }));
    window.dispatchEvent(new Event('resize'));
    expect(t.getBoundingClientRect).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(before);
    expect(t.getAttribute('aria-expanded')).toBe('false');
  });
});

describe('resize, scroll and rebind', () => {
  test('resize repositions to the trigger; closes when the trigger is gone', async () => {
    await render();
    const t = trigger('service');
    let left = 400;
    t.getBoundingClientRect = () => rectAt(left)();
    t.click();
    expect(pop()!.style.left).toBe('400px');
    left = 120;
    window.dispatchEvent(new Event('resize'));
    expect(pop()!.style.left).toBe('120px');
    t.remove();
    window.dispatchEvent(new Event('resize'));
    expect(pop()).toBeNull();
  });

  test('scroll follows the visible trigger on every scroll; closes once it leaves the screen; no updates after close', async () => {
    await render();
    const t = trigger('service');
    let rect = rectAt(400);
    t.getBoundingClientRect = () => rect();
    t.click();
    rect = rectAt(250);
    document.body.dispatchEvent(new Event('scroll', { bubbles: true }));
    expect(pop()!.style.left).toBe('250px');
    rect = rectAt(180);
    document.dispatchEvent(new Event('scroll'));
    expect(pop()!.style.left).toBe('180px');
    rect = () => ({ left: 0, top: 0, width: 0, height: 0, right: 0, bottom: 0, x: 0, y: 0, toJSON: () => ({}) }) as DOMRect;
    document.body.dispatchEvent(new Event('scroll', { bubbles: true }));
    expect(pop()).toBeNull();
    const spy = jest.fn(rect);
    t.getBoundingClientRect = spy;
    document.body.dispatchEvent(new Event('scroll', { bubbles: true }));
    expect(spy).not.toHaveBeenCalled();
  });

  test('re-render rebinds to the new trigger, keeps aria-expanded and one popover', async () => {
    await render();
    const old = open('service');
    const p = pop();
    rerenderRecommendations();
    const fresh = trigger('service');
    expect(fresh).not.toBe(old);
    expect(fresh.getAttribute('aria-expanded')).toBe('true');
    expect(pop()).toBe(p);
    expect(counts()).toEqual(ONE_SET);
  });

  test('re-render resyncs checkboxes and the (All) and SP-group tri-states from state', async () => {
    await render();
    open('service');
    const all = pop()!.querySelector<HTMLInputElement>('input[data-role="all"]')!;
    const group = pop()!.querySelector<HTMLInputElement>('input[data-role="sp-group"]')!;
    expect([all.checked, all.indeterminate, group.checked]).toEqual([true, false, true]);
    state.setRecommendationsColumnFilter('service', { kind: 'set', values: [SP_COMPUTE] });
    rerenderRecommendations();
    expect(box(SP_COMPUTE).checked).toBe(true);
    expect(box('ec2').checked).toBe(false);
    expect([all.checked, all.indeterminate]).toEqual([false, true]);
    expect([group.checked, group.indeterminate]).toEqual([false, true]);
    state.setRecommendationsColumnFilter('service', { kind: 'set', values: [SP_COMPUTE, 'savings-plans-ec2instance'] });
    rerenderRecommendations();
    expect([group.checked, group.indeterminate]).toEqual([true, false]);
    state.setRecommendationsColumnFilter('service', { kind: 'set', values: [] });
    rerenderRecommendations();
    expect([all.checked, all.indeterminate, group.checked, group.indeterminate]).toEqual([false, false, false, false]);
  });

  test('re-render does not overwrite a focused numeric input but does once it is unfocused', async () => {
    await render();
    open('savings');
    const input = pop()!.querySelector<HTMLInputElement>('.column-filter-numeric-input')!;
    expect(document.activeElement).toBe(input);
    state.setRecommendationsColumnFilter('savings', { kind: 'expr', expr: '>100' });
    rerenderRecommendations();
    expect(input.value).toBe('');
    input.blur();
    state.setRecommendationsColumnFilter('savings', { kind: 'expr', expr: '>100' });
    rerenderRecommendations();
    expect(pop()!.querySelector<HTMLInputElement>('.column-filter-numeric-input')!.value).toBe('>100');
  });

  test('re-render closes the popover when its column is no longer in the table', async () => {
    await render();
    open('region');
    state.setHiddenColumns(new Set(['region']));
    rerenderRecommendations();
    expect(pop()).toBeNull();
    expect(trigger('region')).toBeNull();
  });
});

describe('keyboard inside the popover', () => {
  test('Escape pressed on a checkbox closes it; other keys leave it open and are not prevented', async () => {
    await render();
    open('service');
    const cb = pop()!.querySelector('input[type="checkbox"]')!;
    expect(press('a', cb).defaultPrevented).toBe(false);
    expect(press('Tab', cb).defaultPrevented).toBe(false);
    expect(pop()).not.toBeNull();
    expect(press('Escape', cb).defaultPrevented).toBe(true);
    expect(pop()).toBeNull();
  });
});

describe('tab switch and the visibility popover', () => {
  test('removing .active from the tab closes the popover; unrelated class changes do not', async () => {
    await render();
    const t = open('service');
    tab.classList.add('other');
    await flushPromises();
    expect(pop()).not.toBeNull();
    tab.classList.remove('active');
    await flushPromises();
    expect(pop()).toBeNull();
    expect(t.getAttribute('aria-expanded')).toBe('false');
    tab.classList.add('active');
    await flushPromises();
    open('service');
    tab.classList.remove('active');
    await flushPromises();
    expect(pop()).toBeNull();
  });

  test('hiding the tab also closes an open column-visibility popover', async () => {
    await render();
    document.querySelector<HTMLButtonElement>('.column-visibility-btn')!.click();
    expect(document.querySelector('.column-visibility-popover')).not.toBeNull();
    tab.classList.remove('active');
    await flushPromises();
    expect(document.querySelector('.column-visibility-popover')).toBeNull();
  });

  test('opening the visibility popover closes an open column popover and resets its trigger', async () => {
    await render();
    const t = open('service');
    const vis = document.querySelector<HTMLButtonElement>('.column-visibility-btn')!;
    vis.click();
    expect(pop()).toBeNull();
    expect(t.getAttribute('aria-expanded')).toBe('false');
    expect(vis.getAttribute('aria-expanded')).toBe('true');
    expect(document.querySelector('.column-visibility-popover')!.getAttribute('aria-label')).toBe('Show/hide columns');
  });

  test('listeners added by opening the visibility popover are removed when it closes', async () => {
    await render();
    const base = counts();
    const vis = document.querySelector<HTMLButtonElement>('.column-visibility-btn')!;
    vis.click();
    await flushPromises();
    expect(counts()).not.toEqual(base);
    vis.click();
    expect(document.querySelector('.column-visibility-popover')).toBeNull();
    expect(counts()).toEqual(base);
  });

  test('opening a column popover leaves an open visibility popover open', async () => {
    await render();
    document.querySelector<HTMLButtonElement>('.column-visibility-btn')!.click();
    open('service');
    expect(pop()).not.toBeNull();
    expect(document.querySelector('.column-visibility-popover')).not.toBeNull();
  });
});
