/**
 * Inventory sub-section switching and sub-tab addressing (QA A.4). Split from
 * inventory.test.ts to stay under the 500-line ceiling.
 */

// The harness registers the module mocks, so it must be imported first.
import { buildInventoryDOM, clearDOM } from './inventory-harness';
import { loadInventory, switchInventorySubSection } from '../inventory';
import { switchInventorySubTab } from '../navigation';
import { loadRIExchange } from '../riexchange';
import { loadTrends } from '../trends';
import * as api from '../api';
import * as state from '../state';

describe('Inventory & Coverage sub-section switching', () => {
  beforeEach(() => {
    buildInventoryDOM();
    (loadRIExchange as jest.Mock).mockClear();
    // listActiveCommitments is invoked when switching to the
    // active-commitments sub-tab; default to a resolved-empty so the
    // switching tests don't need to care about the fetch outcome.
    (api.listActiveCommitments as jest.Mock).mockReset();
    (api.listActiveCommitments as jest.Mock).mockResolvedValue([]);
    // getCoverageBreakdown is invoked when switching to the coverage sub-tab.
    (api.getCoverageBreakdown as jest.Mock).mockReset();
    (api.getCoverageBreakdown as jest.Mock).mockResolvedValue({ providers: [] });
    // Reset state mocks to defaults. subscribeProvider/Account must keep
    // returning an unsubscribe function so inventory.ts can call it during
    // teardown without throwing.
    (state.subscribeProvider as jest.Mock).mockReset().mockReturnValue(jest.fn());
    (state.subscribeAccount as jest.Mock).mockReset().mockReturnValue(jest.fn());
    (state.getCurrentProvider as jest.Mock).mockReturnValue('');
    (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([]);
  });

  afterEach(() => {
    clearDOM();
  });

  test('switchInventorySubSection shows active-commitments and hides others', () => {
    switchInventorySubSection('active-commitments');

    expect(document.getElementById('inventory-active-commitments')?.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('inventory-coverage')?.classList.contains('hidden')).toBe(true);
    expect(document.getElementById('inventory-ri-exchange')?.classList.contains('hidden')).toBe(true);

    const activeBtn = document.querySelector('[data-inv-subtab="active-commitments"]');
    expect(activeBtn?.classList.contains('active')).toBe(true);
    expect(activeBtn?.getAttribute('aria-selected')).toBe('true');

    // Switching to active-commitments triggers the fetch — same shape
    // as the ri-exchange sub-tab triggers loadRIExchange.
    expect(api.listActiveCommitments).toHaveBeenCalledTimes(1);
  });

  test('switchInventorySubSection shows coverage and hides others', () => {
    switchInventorySubSection('coverage');

    expect(document.getElementById('inventory-coverage')?.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('inventory-active-commitments')?.classList.contains('hidden')).toBe(true);
    expect(document.getElementById('inventory-ri-exchange')?.classList.contains('hidden')).toBe(true);
  });

  test('switchInventorySubSection shows ri-exchange and calls loadRIExchange', () => {
    switchInventorySubSection('ri-exchange');

    expect(document.getElementById('inventory-ri-exchange')?.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('inventory-active-commitments')?.classList.contains('hidden')).toBe(true);
    expect(document.getElementById('inventory-coverage')?.classList.contains('hidden')).toBe(true);
    expect(loadRIExchange).toHaveBeenCalledTimes(1);
  });

  test('switchInventorySubSection shows trends and calls loadTrends', () => {
    const trends = document.createElement('section');
    trends.id = 'inventory-trends';
    trends.classList.add('hidden');
    document.getElementById('inventory-tab')?.appendChild(trends);
    (loadTrends as jest.Mock).mockClear();

    expect(switchInventorySubSection('trends')).toBe('trends');

    expect(trends.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('inventory-coverage')?.classList.contains('hidden')).toBe(true);
    expect(document.getElementById('inventory-active-commitments')?.classList.contains('hidden')).toBe(true);
    expect(loadTrends).toHaveBeenCalledTimes(1);
  });

  test('switchInventorySubSection falls back to active-commitments for unknown sub-section', () => {
    switchInventorySubSection('something-unknown');

    expect(document.getElementById('inventory-active-commitments')?.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('inventory-ri-exchange')?.classList.contains('hidden')).toBe(true);
    expect(api.listActiveCommitments).toHaveBeenCalledTimes(1);
  });

  test('loadInventory wires sub-nav click handlers and lands on default', () => {
    loadInventory();

    // Default landing is active-commitments (issue #751).
    expect(document.getElementById('inventory-active-commitments')?.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('inventory-ri-exchange')?.classList.contains('hidden')).toBe(true);

    // Clicking a sub-tab button routes through the router (QA A.4) so the
    // click both switches the view AND pushes /inventory/<subtab>. The
    // mocked router delegates to the real view switcher, so the DOM flips.
    const coverageBtn = document.querySelector<HTMLButtonElement>('[data-inv-subtab="coverage"]')!;
    coverageBtn.click();
    expect(switchInventorySubTab).toHaveBeenCalledWith('coverage');
    expect(document.getElementById('inventory-coverage')?.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('inventory-active-commitments')?.classList.contains('hidden')).toBe(true);
  });

});

// QA A.4: Inventory sub-tabs are URL-addressable (/inventory/<subtab>),
// default-first, and shareable. inventory.ts owns the view switch + the
// click->router wiring; the URL push itself lives in navigation.ts and is
// covered in navigation.test.ts. Here we assert:
//   - the pure switcher returns the resolved (validated) sub-section,
//   - loadInventory honours an explicit sub-section, defaults when absent,
//     and falls back when unknown (default-first),
//   - a sub-nav click routes through navigation.switchInventorySubTab so
//     the URL gets updated (no hidden session state).
describe('Inventory & Coverage sub-tab addressing (QA A.4)', () => {
  beforeEach(() => {
    buildInventoryDOM();
    (loadRIExchange as jest.Mock).mockClear();
    (switchInventorySubTab as jest.Mock).mockClear();
    (api.listActiveCommitments as jest.Mock).mockReset().mockResolvedValue([]);
    (api.getCoverageBreakdown as jest.Mock).mockReset().mockResolvedValue({ providers: [] });
    (state.subscribeProvider as jest.Mock).mockReset().mockReturnValue(jest.fn());
    (state.subscribeAccount as jest.Mock).mockReset().mockReturnValue(jest.fn());
    (state.getCurrentProvider as jest.Mock).mockReturnValue('');
    (state.getCurrentAccountIDs as jest.Mock).mockReturnValue([]);
  });

  afterEach(() => {
    clearDOM();
  });

  test('switchInventorySubSection returns the resolved sub-section', () => {
    expect(switchInventorySubSection('coverage')).toBe('coverage');
    // Unknown input resolves to the default (default-first).
    expect(switchInventorySubSection('bogus')).toBe('active-commitments');
  });

  test('(a) loadInventory(undefined) -> default sub-section (active-commitments)', () => {
    loadInventory(undefined);
    expect(document.getElementById('inventory-active-commitments')?.classList.contains('hidden')).toBe(false);
    expect(api.listActiveCommitments).toHaveBeenCalled();
  });

  test('(b) loadInventory(<subtab>) -> that sub-section', () => {
    loadInventory('coverage');
    expect(document.getElementById('inventory-coverage')?.classList.contains('hidden')).toBe(false);
    expect(document.getElementById('inventory-active-commitments')?.classList.contains('hidden')).toBe(true);
    expect(api.getCoverageBreakdown).toHaveBeenCalled();
  });

  test('(d) loadInventory(<unknown>) -> falls back to default', () => {
    loadInventory('bogus-subtab');
    expect(document.getElementById('inventory-active-commitments')?.classList.contains('hidden')).toBe(false);
  });
});
