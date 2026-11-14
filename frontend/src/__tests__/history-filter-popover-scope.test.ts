/**
 * Issue #281: the Purchase History and Approval Queue tables render filter
 * buttons with the same bare column ids. The shared popover must key the
 * open popover and resolve its anchor by (scope, column) so the two tables
 * do not toggle, refocus or leave aria-expanded stuck on each other.
 */

import {
  closeOpenHistoryPopover,
  openHistoryColumnPopover,
  renderHistoryFilterButton,
} from '../lib/history-filter-popover';

function mountTables(): { queueBtn: HTMLElement; historyBtn: HTMLElement } {
  // Queue first in the DOM, mirroring the Purchases tab layout, so a bare
  // document-wide `[data-column="provider"]` lookup would hit the queue.
  document.body.innerHTML = `
    <div id="queue">${renderHistoryFilterButton('provider', 'Provider', false, 'queue')}</div>
    <div id="history">${renderHistoryFilterButton('provider', 'Provider', false, 'history')}</div>
  `;
  return {
    queueBtn: document.querySelector<HTMLElement>('#queue .history-column-filter-btn')!,
    historyBtn: document.querySelector<HTMLElement>('#history .history-column-filter-btn')!,
  };
}

function open(scope: string, anchor: HTMLElement): void {
  openHistoryColumnPopover({
    scope,
    column: 'provider',
    anchor,
    currentFilter: undefined,
    headerLabel: `Provider (${scope})`,
    kind: 'categorical',
    distinctValues: ['aws', 'azure'],
    onCommit: jest.fn(),
  });
}

const popovers = (): NodeListOf<HTMLElement> =>
  document.querySelectorAll<HTMLElement>('.column-filter-popover');

describe('history filter popover scoping (issue #281)', () => {
  afterEach(() => {
    closeOpenHistoryPopover();
    document.body.innerHTML = '';
  });

  test('opening the same column in the other table swaps instead of closing', () => {
    const { queueBtn, historyBtn } = mountTables();
    open('queue', queueBtn);
    expect(popovers()).toHaveLength(1);

    open('history', historyBtn);
    expect(popovers()).toHaveLength(1);
    expect(popovers()[0]?.textContent).toContain('Provider (history)');
    expect(queueBtn.getAttribute('aria-expanded')).toBe('false');
    expect(historyBtn.getAttribute('aria-expanded')).toBe('true');
  });

  test('re-opening the same scope and column still toggles closed', () => {
    const { historyBtn } = mountTables();
    open('history', historyBtn);
    open('history', historyBtn);
    expect(popovers()).toHaveLength(0);
    expect(historyBtn.getAttribute('aria-expanded')).toBe('false');
  });

  test('Escape returns focus to the owning table button', () => {
    const { queueBtn, historyBtn } = mountTables();
    open('history', historyBtn);
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));

    expect(popovers()).toHaveLength(0);
    expect(document.activeElement).toBe(historyBtn);
    expect(historyBtn.getAttribute('aria-expanded')).toBe('false');
    expect(queueBtn.getAttribute('aria-expanded')).toBe('false');
  });

  test('a commit that re-renders the table keeps the new button expanded and re-anchors to it', () => {
    mountTables();
    const historyBox = document.getElementById('history')!;
    const onCommit = jest.fn(() => {
      // The caller re-renders its table, replacing the trigger button.
      historyBox.innerHTML = renderHistoryFilterButton('provider', 'Provider', true, 'history');
    });
    const oldBtn = historyBox.querySelector<HTMLElement>('.history-column-filter-btn')!;
    openHistoryColumnPopover({
      scope: 'history',
      column: 'provider',
      anchor: oldBtn,
      currentFilter: undefined,
      headerLabel: 'Provider',
      kind: 'categorical',
      distinctValues: ['aws', 'azure'],
      onCommit,
    });
    const box = popovers()[0]!.querySelector<HTMLInputElement>('input[data-value="aws"]')!;
    box.checked = false;
    box.dispatchEvent(new Event('change'));

    expect(onCommit).toHaveBeenCalledWith({ kind: 'set', values: ['azure'] });
    const newBtn = historyBox.querySelector<HTMLElement>('.history-column-filter-btn')!;
    expect(newBtn).not.toBe(oldBtn);
    expect(popovers()).toHaveLength(1);
    expect(newBtn.getAttribute('aria-expanded')).toBe('true');
    // Closing still clears the new button's state.
    closeOpenHistoryPopover();
    expect(newBtn.getAttribute('aria-expanded')).toBe('false');
  });

  test('a button rendered while its popover is open starts expanded; others do not', () => {
    const { historyBtn } = mountTables();
    open('history', historyBtn);
    expect(renderHistoryFilterButton('provider', 'Provider', false, 'history')).toContain('aria-expanded="true"');
    expect(renderHistoryFilterButton('provider', 'Provider', false, 'queue')).toContain('aria-expanded="false"');
    expect(renderHistoryFilterButton('service', 'Service', false, 'history')).toContain('aria-expanded="false"');
  });

  test('popover element ids are unique per table', () => {
    const { queueBtn } = mountTables();
    open('queue', queueBtn);
    const heading = popovers()[0]?.querySelector('h3');
    expect(heading?.id).toBe('queue-column-filter-provider-heading');
  });

  test('mousedown on a history trigger is left to the trigger (no outside-click close)', () => {
    const { historyBtn } = mountTables();
    open('history', historyBtn);
    historyBtn.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    expect(popovers()).toHaveLength(1);
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    expect(popovers()).toHaveLength(0);
  });

  test('a commit that re-renders the table re-anchors and re-expands the popover', () => {
    const { historyBtn } = mountTables();
    const historyBox = document.getElementById('history')!;
    openHistoryColumnPopover({
      scope: 'history', column: 'provider', anchor: historyBtn, currentFilter: undefined,
      headerLabel: 'Provider', kind: 'categorical', distinctValues: ['aws', 'azure'],
      onCommit: () => {
        // Re-render with a plain (not expanded) button: the adapter must fix it.
        historyBox.innerHTML = '<button class="history-column-filter-btn" data-filter-scope="history" data-column="provider" aria-expanded="false"></button>';
      },
    });
    const cb = document.querySelector<HTMLInputElement>('.column-filter-popover input[data-value="aws"]')!;
    cb.checked = false;
    cb.dispatchEvent(new Event('change'));
    const newBtn = historyBox.querySelector<HTMLElement>('.history-column-filter-btn')!;
    expect(newBtn.getAttribute('aria-expanded')).toBe('true');
  });

  describe('scroll handling', () => {
    const onScreen = (el: HTMLElement): void => {
      el.getBoundingClientRect = () => ({ left: 10, right: 40, top: 10, bottom: 30, width: 30, height: 20, x: 10, y: 10, toJSON: () => ({}) });
    };
    const scroll = (): void => { window.dispatchEvent(new Event('scroll')); };

    test('closes when the trigger scrolls out of view, stays while visible', () => {
      const { historyBtn } = mountTables();
      onScreen(historyBtn);
      open('history', historyBtn);
      popovers()[0]!.style.top = '999px';
      scroll();
      expect(popovers()).toHaveLength(1);
      // Followed the trigger: repositioned just below it (bottom 30 + 4).
      expect(popovers()[0]!.style.top).toBe('34px');
      historyBtn.getBoundingClientRect = () => new DOMRect(0, 0, 0, 0);
      scroll();
      expect(popovers()).toHaveLength(0);
    });

    test('removes the scroll listener, with the same options, when the popover closes', () => {
      const { historyBtn } = mountTables();
      const add = jest.spyOn(window, 'addEventListener');
      const remove = jest.spyOn(window, 'removeEventListener');
      try {
        open('history', historyBtn);
        closeOpenHistoryPopover();
        const scrollCalls = (spy: jest.SpyInstance): unknown[][] => spy.mock.calls.filter((c) => c[0] === 'scroll');
        expect(scrollCalls(add)).toHaveLength(1);
        expect(scrollCalls(remove)).toEqual(scrollCalls(add));
      } finally {
        add.mockRestore();
        remove.mockRestore();
      }
    });
  });
});
