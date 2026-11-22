/**
 * Shared column-filter popover lifecycle (issue #18): open/toggle/swap,
 * outside click, Escape, commits, Clear, inline expression errors, and
 * re-sync after the owning table re-renders.
 */

import {
  closeColumnFilterPopover,
  closeColumnFilterPopoverForScope,
  isColumnFilterPopoverOpen,
  openColumnFilterPopover,
  positionPopover,
  rebindColumnFilterPopover,
  sortedDistinctValues,
  type ColumnFilterPopoverConfig,
} from '../lib/column-filter-popover';
import type { ColumnFilterKind } from '../lib/column-filters';

let filters: Record<string, ColumnFilterKind | null> = {};
let onCommit: jest.Mock;

function mountTable(scope: string): void {
  const host = document.createElement('div');
  host.id = scope;
  host.innerHTML = ['provider', 'count']
    .map((c) => `<button class="column-filter-btn" data-column="${c}" aria-expanded="false">F</button>`)
    .join('');
  document.getElementById(scope)?.remove();
  document.body.appendChild(host);
}

const trigger = (scope: string, column: string): HTMLElement =>
  document.querySelector<HTMLElement>(`#${scope} .column-filter-btn[data-column="${column}"]`)!;

function config(scope: string, column: 'provider' | 'count'): ColumnFilterPopoverConfig {
  return {
    scope,
    column,
    label: column === 'provider' ? 'Provider' : 'Count',
    kind: column === 'count' ? 'numeric' : 'categorical',
    distinctValues: ['', 'aws', 'azure'],
    getFilter: () => filters[`${scope}:${column}`],
    onCommit: (f) => {
      filters[`${scope}:${column}`] = f;
      onCommit(scope, column, f);
    },
    findTrigger: () => trigger(scope, column),
  };
}

function openFor(scope: string, column: 'provider' | 'count'): void {
  openColumnFilterPopover(config(scope, column), trigger(scope, column));
}

const popovers = (): HTMLElement[] => Array.from(document.querySelectorAll<HTMLElement>('.column-filter-popover'));
const box = (value: string): HTMLInputElement =>
  document.querySelector<HTMLInputElement>(`.column-filter-popover input[data-value="${value}"]`)!;
const allBox = (): HTMLInputElement =>
  document.querySelector<HTMLInputElement>('.column-filter-popover input[data-role="all"]')!;
const exprInput = (): HTMLInputElement =>
  document.querySelector<HTMLInputElement>('.column-filter-popover .column-filter-numeric-input')!;

beforeEach(() => {
  filters = {};
  onCommit = jest.fn();
  mountTable('plans');
  mountTable('riex');
});

afterEach(() => {
  closeColumnFilterPopover();
  document.body.innerHTML = '';
});

describe('open / close lifecycle', () => {
  test('opens an accessible dialog anchored to the trigger and focuses the first control', () => {
    openFor('plans', 'provider');
    const [pop] = popovers();
    expect(pop?.getAttribute('role')).toBe('dialog');
    expect(pop?.querySelector('h3')?.textContent).toBe('Filter Provider');
    expect(pop?.getAttribute('aria-labelledby')).toBe('plans-column-filter-provider-heading');
    expect(trigger('plans', 'provider').getAttribute('aria-expanded')).toBe('true');
    expect(document.activeElement).toBe(allBox());
  });

  test('clicking the same trigger again closes and returns focus to it', () => {
    openFor('plans', 'provider');
    openFor('plans', 'provider');
    expect(popovers()).toHaveLength(0);
    expect(document.activeElement).toBe(trigger('plans', 'provider'));
    expect(trigger('plans', 'provider').getAttribute('aria-expanded')).toBe('false');
  });

  test('opening another column, or the same column in another table, swaps popovers', () => {
    openFor('plans', 'provider');
    openFor('plans', 'count');
    expect(popovers()).toHaveLength(1);
    expect(popovers()[0]?.textContent).toContain('Filter Count');
    openFor('riex', 'count');
    expect(popovers()).toHaveLength(1);
    expect(trigger('plans', 'count').getAttribute('aria-expanded')).toBe('false');
    expect(trigger('riex', 'count').getAttribute('aria-expanded')).toBe('true');
  });

  test('outside mousedown closes; mousedown inside the popover does not', () => {
    openFor('plans', 'provider');
    box('aws').dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    expect(popovers()).toHaveLength(1);
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    expect(popovers()).toHaveLength(0);
  });

  test('Escape closes and restores focus to the trigger', () => {
    openFor('riex', 'provider');
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(popovers()).toHaveLength(0);
    expect(document.activeElement).toBe(trigger('riex', 'provider'));
  });

  test('a popover removed by someone else is forgotten instead of toggled', () => {
    openFor('plans', 'provider');
    popovers().forEach((p) => p.remove());
    openFor('plans', 'provider');
    expect(popovers()).toHaveLength(1);
  });
});

describe('categorical commits', () => {
  test('unchecking a value commits the remaining allow-list; rechecking all clears', () => {
    openFor('plans', 'provider');
    expect(box('aws').checked).toBe(true);
    box('aws').click();
    expect(onCommit).toHaveBeenLastCalledWith('plans', 'provider', { kind: 'set', values: ['', 'azure'] });
    expect(allBox().indeterminate).toBe(true);
    box('aws').click();
    expect(onCommit).toHaveBeenLastCalledWith('plans', 'provider', null);
  });

  test('Select none writes an explicit empty allow-list', () => {
    openFor('plans', 'provider');
    const btn = document.querySelector<HTMLButtonElement>('.column-filter-clear')!;
    // #727 (UX-28): it hides every row, so it is not labelled "Clear".
    expect(btn.textContent).toBe('Select none');
    btn.click();
    expect(onCommit).toHaveBeenLastCalledWith('plans', 'provider', { kind: 'set', values: [] });
    expect(box('azure').checked).toBe(false);
  });

  test('empty value renders as (empty) unless displayLabel overrides it', () => {
    openFor('plans', 'provider');
    expect(box('').parentElement?.textContent).toBe('(empty)');
  });
});

describe('numeric commits', () => {
  test('Enter commits a valid expression', () => {
    openFor('plans', 'count');
    exprInput().value = '>2';
    exprInput().dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    expect(onCommit).toHaveBeenLastCalledWith('plans', 'count', { kind: 'expr', expr: '>2' });
  });

  test('an invalid expression shows an inline error and does not commit', () => {
    openFor('plans', 'count');
    exprInput().value = '>>';
    exprInput().dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    expect(onCommit).not.toHaveBeenCalled();
    const err = document.getElementById('plans-column-filter-count-error');
    expect(err?.textContent).not.toBe('');
    expect(exprInput().getAttribute('aria-describedby')).toBe('plans-column-filter-count-error');
  });

  test('Clear empties the input and clears the filter', () => {
    filters['plans:count'] = { kind: 'expr', expr: '5' };
    openFor('plans', 'count');
    expect(exprInput().value).toBe('5');
    const btn = document.querySelector<HTMLButtonElement>('.column-filter-clear')!;
    expect(btn.textContent).toBe('Clear');
    btn.click();
    expect(exprInput().value).toBe('');
    expect(onCommit).toHaveBeenLastCalledWith('plans', 'count', null);
  });
});

describe('rebind after re-render', () => {
  test('re-anchors to the fresh trigger and re-syncs from external state', () => {
    openFor('plans', 'provider');
    filters['plans:provider'] = { kind: 'set', values: ['azure'] };
    mountTable('plans');
    rebindColumnFilterPopover('plans');
    expect(trigger('plans', 'provider').getAttribute('aria-expanded')).toBe('true');
    expect(box('aws').checked).toBe(false);
    expect(box('azure').checked).toBe(true);
  });

  test('ignores other scopes and closes when the column disappears', () => {
    openFor('plans', 'provider');
    rebindColumnFilterPopover('riex');
    expect(popovers()).toHaveLength(1);
    document.getElementById('plans')!.innerHTML = '';
    rebindColumnFilterPopover('plans');
    expect(popovers()).toHaveLength(0);
  });

  test('does not overwrite the expression input while it has focus', () => {
    openFor('plans', 'count');
    exprInput().value = '>1';
    filters['plans:count'] = { kind: 'expr', expr: '9' };
    rebindColumnFilterPopover('plans');
    expect(exprInput().value).toBe('>1');
  });
});

describe('opt-in trigger selector, scroll following and open state', () => {
  const scroll = (): void => { window.dispatchEvent(new Event('scroll')); };

  test('isColumnFilterPopoverOpen is keyed by scope and column', () => {
    openFor('plans', 'provider');
    expect(isColumnFilterPopoverOpen('plans', 'provider')).toBe(true);
    expect(isColumnFilterPopoverOpen('plans', 'count')).toBe(false);
    expect(isColumnFilterPopoverOpen('riex', 'provider')).toBe(false);
    closeColumnFilterPopover();
    expect(isColumnFilterPopoverOpen('plans', 'provider')).toBe(false);
  });

  test('scrolling leaves the popover alone unless followOnScroll is set', () => {
    openFor('plans', 'provider');
    scroll();
    expect(popovers()).toHaveLength(1);
  });

  test('followOnScroll closes the popover once the trigger is off screen', () => {
    openColumnFilterPopover({ ...config('plans', 'provider'), followOnScroll: true }, trigger('plans', 'provider'));
    scroll();
    expect(popovers()).toHaveLength(0);
  });

  test('followOnScroll keeps the popover while the trigger is on screen', () => {
    const btn = trigger('plans', 'provider');
    btn.getBoundingClientRect = () => ({ left: 10, right: 40, top: 10, bottom: 30, width: 30, height: 20, x: 10, y: 10, toJSON: () => ({}) });
    openColumnFilterPopover({ ...config('plans', 'provider'), followOnScroll: true }, btn);
    scroll();
    expect(popovers()).toHaveLength(1);
  });

  test('triggerSelector decides which outside clicks are left to the trigger', () => {
    document.getElementById('plans')!.innerHTML += '<button class="other-btn">x</button>';
    openColumnFilterPopover({ ...config('plans', 'provider'), triggerSelector: '.other-btn' }, trigger('plans', 'provider'));
    document.querySelector('.other-btn')!.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    expect(popovers()).toHaveLength(1);
    trigger('plans', 'count').dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    expect(popovers()).toHaveLength(0);
  });
});

test('sortedDistinctValues puts the empty value first, then locale order', () => {
  expect(sortedDistinctValues(['b', '', 'a', 'b'], (v) => v)).toEqual(['', 'a', 'b']);
});

describe('valueGroups', () => {
  const groups = [{ role: 'sp-group', label: 'All Savings Plans', members: ['aws', 'azure'] }];
  const groupBox = (): HTMLInputElement | null =>
    document.querySelector<HTMLInputElement>('.column-filter-popover input[data-role="sp-group"]');
  const openGrouped = (): void => {
    openColumnFilterPopover({ ...config('plans', 'provider'), valueGroups: groups }, trigger('plans', 'provider'));
  };

  test('no group element without valueGroups', () => {
    openFor('plans', 'provider');
    expect(document.querySelector('.column-filter-group')).toBeNull();
    expect(document.querySelector('[data-role="sp-group"]')).toBeNull();
  });

  test('renders one labelled row between (All) and the list', () => {
    openGrouped();
    const row = document.querySelector<HTMLElement>('.column-filter-group')!;
    expect(row.tagName).toBe('LABEL');
    expect(row.textContent).toBe('All Savings Plans');
    expect(groupBox()!.type).toBe('checkbox');
    expect(row.previousElementSibling!.className).toBe('column-filter-all');
    expect(row.nextElementSibling!.className).toBe('column-filter-list');
  });

  test('is not rendered for a numeric column', () => {
    openColumnFilterPopover({ ...config('plans', 'count'), valueGroups: groups }, trigger('plans', 'count'));
    expect(groupBox()).toBeNull();
  });

  test('tri-state follows the selected values: all, some, none', () => {
    openGrouped();
    expect(groupBox()!.checked).toBe(true);
    expect(groupBox()!.indeterminate).toBe(false);
    box('aws').click();
    expect(groupBox()!.checked).toBe(false);
    expect(groupBox()!.indeterminate).toBe(true);
    box('azure').click();
    expect(groupBox()!.checked).toBe(false);
    expect(groupBox()!.indeterminate).toBe(false);
  });

  test('tri-state is recomputed by rebind from external filter state', () => {
    openGrouped();
    filters['plans:provider'] = { kind: 'set', values: ['', 'aws'] };
    rebindColumnFilterPopover('plans');
    expect(groupBox()!.indeterminate).toBe(true);
    filters['plans:provider'] = { kind: 'set', values: ['aws', 'azure'] };
    rebindColumnFilterPopover('plans');
    expect(groupBox()!.checked).toBe(true);
    expect(groupBox()!.indeterminate).toBe(false);
  });

  test('clicking the group unticks every member, leaves others, and commits', () => {
    openGrouped();
    groupBox()!.click();
    expect(box('aws').checked).toBe(false);
    expect(box('azure').checked).toBe(false);
    expect(box('').checked).toBe(true);
    expect(onCommit).toHaveBeenLastCalledWith('plans', 'provider', { kind: 'set', values: [''] });
    groupBox()!.click();
    expect(box('aws').checked).toBe(true);
    expect(box('azure').checked).toBe(true);
    expect(onCommit).toHaveBeenLastCalledWith('plans', 'provider', null);
    expect(onCommit).toHaveBeenCalledTimes(2);
  });

  test('(All) and Select none keep the group in step', () => {
    openGrouped();
    document.querySelector<HTMLButtonElement>('.column-filter-clear')!.click();
    expect(groupBox()!.checked).toBe(false);
    expect(groupBox()!.indeterminate).toBe(false);
    allBox().click();
    expect(groupBox()!.checked).toBe(true);
  });
});

describe('positionPopover', () => {
  const rectOf = (left: number, top: number, width: number, height: number): DOMRect =>
    ({ left, top, width, height, right: left + width, bottom: top + height, x: left, y: top, toJSON: () => ({}) });
  const place = (anchor: DOMRect, pop: DOMRect): HTMLElement => {
    const a = document.createElement('button');
    a.getBoundingClientRect = () => anchor;
    const el = document.createElement('div');
    el.getBoundingClientRect = () => pop;
    positionPopover(el, a);
    return el;
  };

  beforeEach(() => {
    Object.defineProperty(window, 'innerHeight', { value: 600, configurable: true });
    Object.defineProperty(window, 'innerWidth', { value: 1000, configurable: true });
  });

  test('places the popover 4px below the anchor', () => {
    const el = place(rectOf(100, 50, 20, 20), rectOf(0, 0, 200, 100));
    expect(el.style.display).toBe('block');
    expect(el.style.position).toBe('absolute');
    expect(el.style.top).toBe('74px');
    expect(el.style.left).toBe('100px');
  });

  test('flips above when it would overflow the bottom', () => {
    const el = place(rectOf(100, 500, 20, 20), rectOf(0, 0, 200, 100));
    expect(el.style.top).toBe('396px');
  });

  test('clamps against the right edge', () => {
    const el = place(rectOf(950, 50, 20, 20), rectOf(0, 0, 200, 100));
    expect(el.style.left).toBe('792px');
  });
});

describe('closeColumnFilterPopoverForScope', () => {
  test('closes the open popover of that scope and restores focus on request', () => {
    openFor('plans', 'provider');
    closeColumnFilterPopoverForScope('plans', true);
    expect(popovers()).toHaveLength(0);
    expect(trigger('plans', 'provider').getAttribute('aria-expanded')).toBe('false');
    expect(document.activeElement).toBe(trigger('plans', 'provider'));
  });

  test('is a no-op for another scope and when nothing is open', () => {
    closeColumnFilterPopoverForScope('plans');
    openFor('plans', 'provider');
    closeColumnFilterPopoverForScope('riex');
    expect(popovers()).toHaveLength(1);
    expect(isColumnFilterPopoverOpen('plans', 'provider')).toBe(true);
  });
});
