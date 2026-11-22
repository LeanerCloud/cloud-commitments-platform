/**
 * Characterization of the Opportunities column-filter popover filters (issue #18, T0): pushdown, persistence, modes, SP group, position.
 * Pins CURRENT observable behavior so the migration onto lib/column-filter-popover
 * can be checked against it. Real DOM, real state and recsflow code; only the
 * network modules are mocked.
 */
import { loadRecommendations } from '../recommendations';
import { closePopover, rebindOpenPopoverAnchor, rerenderRecommendations } from '../recsflow/popover';
import { openColumnFilterPopover, closeColumnFilterPopover } from '../lib/column-filter-popover';
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
const FILTERS_KEY = 'cudly.recs.columnFilters.v1';

// The tab observer is a module singleton bound to the first #opportunities-tab
// it sees, so every test must reuse this one element.
const tab = document.createElement('div');
tab.id = 'opportunities-tab';

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
const rectAt = (left: number) => () => ({ left, top: 300, width: 24, height: 24, right: left + 24, bottom: 324, x: left, y: 300, toJSON: () => ({}) }) as DOMRect;
const fetches = (): number => (api.getRecommendations as jest.Mock).mock.calls.length;
const saved = (): unknown => {
  const writes = (localStorage.setItem as jest.Mock).mock.calls.filter(([k]) => k === FILTERS_KEY);
  return writes.length === 0 ? null : JSON.parse(writes[writes.length - 1][1]);
};

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
});
afterEach(() => {
  closePopover();
  closeColumnFilterPopover();
});

describe('SP All Savings Plans group', () => {
  test('present for the service scope with two or more SP slugs and absent otherwise', async () => {
    await render();
    open('service');
    const group = pop()!.querySelector('.column-filter-group');
    expect(group!.textContent).toBe('All Savings Plans');
    trigger('service').click();
    open('resource_type');
    expect(pop()!.querySelector('input[data-role="sp-group"]')).toBeNull();
    trigger('resource_type').click();
    await render([RECS[0], RECS[1], RECS[2]]);
    open('service');
    expect(pop()!.querySelector('input[data-role="sp-group"]')).toBeNull();
  });

  test('ticking the group adds every SP slug to the committed set filter', async () => {
    state.setRecommendationsColumnFilter('service', { kind: 'set', values: ['ec2'] });
    await render();
    open('service');
    const group = pop()!.querySelector<HTMLInputElement>('input[data-role="sp-group"]')!;
    expect(group.checked).toBe(false);
    toggle(group, true);
    const f = state.getRecommendationsColumnFilters().service as { kind: string; values: string[] };
    expect(f.kind).toBe('set');
    expect([...f.values].sort()).toEqual(['ec2', SP_COMPUTE, 'savings-plans-ec2instance']);
  });
});

describe('commit side effects: pushdown refetch and persistence', () => {
  test.each(['service', 'region'])('committing a %s filter refetches from the server and persists it', async (col) => {
    await render();
    const value = col === 'service' ? 'rds' : 'us-west-2';
    open(col);
    const before = fetches();
    toggle(box(value), false);
    await flushPromises();
    expect(fetches()).toBe(before + 1);
    expect((state.getRecommendationsColumnFilters() as Record<string, { kind: string }>)[col]!.kind).toBe('set');
    expect((saved() as { filters: Record<string, { values: string[] }> }).filters[col]!.values).not.toContain(value);
  });

  test('committing a non-pushdown column re-renders client side without a refetch and persists', async () => {
    await render();
    const old = open('resource_type');
    const before = fetches();
    toggle(box('db.t3'), false);
    await flushPromises();
    expect(fetches()).toBe(before);
    expect(trigger('resource_type')).not.toBe(old);
    expect((saved() as { filters: Record<string, { values: string[] }> }).filters['resource_type']!.values.sort()).toEqual(['sp', 'sp2', 't3.medium']);
  });

  test('Select none stores an explicit empty allow-list and refetches for a pushdown column', async () => {
    await render();
    open('service');
    const before = fetches();
    pop()!.querySelector<HTMLButtonElement>('.column-filter-clear')!.click();
    await flushPromises();
    expect(fetches()).toBe(before + 1);
    expect(state.getRecommendationsColumnFilters().service).toEqual({ kind: 'set', values: [] });
    expect(pop()!.querySelector<HTMLInputElement>('input[data-role="all"]')!.checked).toBe(false);
  });

  test('re-checking every value clears the filter (null) and persists the removal', async () => {
    await render();
    open('resource_type');
    toggle(box('db.t3'), false);
    toggle(box('db.t3'), true);
    expect(state.getRecommendationsColumnFilters().resource_type).toBeUndefined();
    expect((saved() as { filters: Record<string, unknown> }).filters['resource_type']).toBeUndefined();
  });
});

describe('numeric and set modes', () => {
  test('numeric column shows an expression input with a Clear button and no checkboxes', async () => {
    await render();
    open('savings');
    expect(pop()!.querySelector('.column-filter-numeric-input')).not.toBeNull();
    expect(pop()!.querySelector('input[type="checkbox"]')).toBeNull();
    expect(pop()!.querySelector('.column-filter-clear')!.textContent).toBe('Clear');
  });

  test('set column shows (All), one checkbox per value and a Select none button, no text input', async () => {
    await render();
    open('resource_type');
    expect(pop()!.querySelector('input[type="text"]')).toBeNull();
    expect(pop()!.querySelector('.column-filter-all')!.textContent).toBe('(All)');
    expect(pop()!.querySelectorAll('.column-filter-item input[type="checkbox"]')).toHaveLength(4);
    expect(pop()!.querySelector('.column-filter-clear')!.textContent).toBe('Select none');
  });

  test('Enter commits a valid expression; an invalid one shows an error and writes nothing', async () => {
    await render();
    open('savings');
    const input = pop()!.querySelector<HTMLInputElement>('.column-filter-numeric-input')!;
    const err = pop()!.querySelector('.column-filter-error')!;
    input.value = '>150';
    expect(press('Enter', input).defaultPrevented).toBe(true);
    expect(state.getRecommendationsColumnFilters().savings).toEqual({ kind: 'expr', expr: '>150' });
    expect(err.textContent).toBe('');
    expect((saved() as { filters: Record<string, unknown> }).filters['savings']).toEqual({ kind: 'expr', expr: '>150' });
    const bad = pop()!.querySelector<HTMLInputElement>('.column-filter-numeric-input')!;
    bad.value = 'abc';
    press('Enter', bad);
    expect(pop()!.querySelector('.column-filter-error')!.textContent).not.toBe('');
    expect(state.getRecommendationsColumnFilters().savings).toEqual({ kind: 'expr', expr: '>150' });
  });

  test('Clear on a numeric filter empties the input and removes the filter', async () => {
    state.setRecommendationsColumnFilter('savings', { kind: 'expr', expr: '>150' });
    await render();
    open('savings');
    const input = pop()!.querySelector<HTMLInputElement>('.column-filter-numeric-input')!;
    expect(input.value).toBe('>150');
    pop()!.querySelector<HTMLButtonElement>('.column-filter-clear')!.click();
    expect(state.getRecommendationsColumnFilters().savings).toBeUndefined();
    expect(pop()!.querySelector<HTMLInputElement>('.column-filter-numeric-input')!.value).toBe('');
  });
});


describe('persistence of clear and select-none paths', () => {
  const filtersSaved = (): Record<string, unknown> => (saved() as { filters: Record<string, unknown> }).filters;

  test('Select none persists an explicit empty allow-list', async () => {
    await render();
    open('resource_type');
    pop()!.querySelector<HTMLButtonElement>('.column-filter-clear')!.click();
    expect(filtersSaved()['resource_type']).toEqual({ kind: 'set', values: [] });
  });

  test('numeric Clear persists the removal of the filter', async () => {
    state.setRecommendationsColumnFilter('savings', { kind: 'expr', expr: '>150' });
    await render();
    open('savings');
    pop()!.querySelector<HTMLButtonElement>('.column-filter-clear')!.click();
    expect(filtersSaved()['savings']).toBeUndefined();
  });

  test('committing an emptied numeric input persists the removal of the filter', async () => {
    state.setRecommendationsColumnFilter('savings', { kind: 'expr', expr: '>150' });
    await render();
    open('savings');
    const input = pop()!.querySelector<HTMLInputElement>('.column-filter-numeric-input')!;
    input.value = '';
    press('Enter', input);
    expect(state.getRecommendationsColumnFilters().savings).toBeUndefined();
    expect(filtersSaved()['savings']).toBeUndefined();
  });
});

describe('popover position', () => {
  test('opens 4px below the trigger bottom, at the trigger left', async () => {
    await render();
    const t = trigger('service');
    t.getBoundingClientRect = rectAt(400);
    t.click();
    expect(pop()!.style.top).toBe('328px');
    expect(pop()!.style.left).toBe('400px');
  });

  test('flips above the trigger when there is no room below the viewport', async () => {
    await render();
    const t = trigger('service');
    t.getBoundingClientRect = () => ({ left: 400, top: 790, width: 24, height: 24, right: 424, bottom: 814, x: 400, y: 790, toJSON: () => ({}) }) as DOMRect;
    t.click();
    expect(pop()!.style.top).toBe('786px');
  });
});

describe('recs scope isolation', () => {
  const openForeign = (scope: string, findTrigger: (b: HTMLButtonElement) => HTMLElement | null): HTMLButtonElement => {
    const b = document.createElement('button');
    b.className = `${scope}-column-filter-btn`;
    document.body.appendChild(b);
    openColumnFilterPopover({
      scope, column: 'provider', label: 'Provider', kind: 'categorical', distinctValues: ['aws', 'gcp'],
      getFilter: () => undefined, onCommit: () => undefined, findTrigger: () => findTrigger(b),
      triggerSelector: `.${scope}-column-filter-btn`,
    }, b);
    return b;
  };

  test.each(['history', 'plans'])('recs closePopover and rebind leave an open %s popover alone', async (scope) => {
    await render();
    const b = openForeign(scope, (x) => x);
    const el = pop()!;
    closePopover(true);
    rebindOpenPopoverAnchor();
    rerenderRecommendations();
    expect(pop()).toBe(el);
    expect(el.isConnected).toBe(true);
    expect(b.getAttribute('aria-expanded')).toBe('true');
  });

  test('rebind does not close another scope whose trigger cannot be found', async () => {
    await render();
    openForeign('history', () => null);
    rebindOpenPopoverAnchor();
    expect(pop()).not.toBeNull();
  });

  test('the tab observer is registered at most once across renders and opens', async () => {
    const observe = jest.spyOn(MutationObserver.prototype, 'observe');
    try {
      await render();
      await render();
      rerenderRecommendations();
      open('service');
      await render();
      expect(observe.mock.calls.filter(([t]) => t === tab).length).toBeLessThanOrEqual(1);
    } finally {
      observe.mockRestore();
    }
  });
});
