/**
 * Inventory & Coverage section (issue #340 T4, #754).
 *
 * Umbrella section that folds the former top-level "RI Exchange" tab into
 * a sub-section of a broader Inventory & Coverage view. Sub-sections:
 *   - active-commitments — per-commitment list backed by
 *                          /api/inventory/commitments
 *   - coverage           — per-provider coverage breakdowns backed by
 *                          /api/inventory/coverage (issue #754)
 *   - trends             — savings-snapshot history backed by
 *                          /api/analytics/trends (issue #30, trends.ts)
 *   - ri-exchange        — hosts the existing RI Exchange UI unchanged
 */

import { loadRIExchange } from './riexchange';
import { loadTrends } from './trends';
import * as state from './state';
import { switchInventorySubTab } from './navigation';
import { syncTabStops } from './lib/tablist';
import { loadActiveCommitments } from './modules/inventory-commitments';
import { loadCoverageBreakdown } from './modules/inventory-coverage';

export { loadActiveCommitments };
export { loadCoverageBreakdown };

type InventorySubSection = 'active-commitments' | 'coverage' | 'trends' | 'ri-exchange';

const SUB_SECTION_IDS: Record<InventorySubSection, string> = {
  'active-commitments': 'inventory-active-commitments',
  'coverage': 'inventory-coverage',
  'trends': 'inventory-trends',
  'ri-exchange': 'inventory-ri-exchange',
};

export const DEFAULT_INVENTORY_SUB_SECTION: InventorySubSection = 'active-commitments';

let currentSubSection: InventorySubSection | undefined;
let listenersWired = false;

/**
 * Type guard for the Inventory sub-section identifiers. Exported so the
 * router (navigation.ts) can validate the `/inventory/<subtab>` path
 * segment without duplicating the closed set.
 */
export function isValidInventorySubSection(name: string): name is InventorySubSection {
  return name === 'active-commitments' || name === 'coverage' || name === 'trends' || name === 'ri-exchange';
}

/**
 * Show one sub-section, hide the others. Activates the matching sub-nav
 * button and (for ri-exchange) triggers the RI exchange data load so the
 * existing flow stays identical to its pre-#340 behaviour.
 *
 * This is the pure view switcher: it does NOT touch the URL. URL history
 * (the `/inventory/<subtab>` addressing from QA A.4) is owned by
 * navigation.ts' switchInventorySubTab, mirroring how switchSettingsSubTab
 * owns the `/admin/<subtab>` history so a single counter (historyId) stays
 * authoritative for the back/forward dirty-guard.
 *
 * Returns the resolved (validated, default-substituted) sub-section so the
 * caller can reflect the same value in the URL.
 */
export function switchInventorySubSection(name: string): InventorySubSection {
  const target: InventorySubSection = isValidInventorySubSection(name)
    ? name
    : DEFAULT_INVENTORY_SUB_SECTION;

  document.querySelectorAll<HTMLButtonElement>('#inventory-tab .sub-tab-btn').forEach((btn) => {
    const isActive = btn.dataset['invSubtab'] === target;
    btn.classList.toggle('active', isActive);
    btn.setAttribute('aria-selected', isActive ? 'true' : 'false');
  });
  syncTabStops(document.querySelector('#inventory-tab [role="tablist"]'));

  for (const key of Object.keys(SUB_SECTION_IDS) as InventorySubSection[]) {
    const el = document.getElementById(SUB_SECTION_IDS[key]);
    if (el) el.classList.toggle('hidden', key !== target);
  }

  if (target === 'ri-exchange') {
    void loadRIExchange();
  } else if (target === 'active-commitments') {
    void loadActiveCommitments();
  } else if (target === 'coverage') {
    void loadCoverageBreakdown();
  } else if (target === 'trends') {
    void loadTrends();
  }

  currentSubSection = target;
  return target;
}


/**
 * Wire sub-nav button clicks. Idempotent — calling this more than once
 * doesn't double-bind handlers.
 */
function wireSubNavListeners(): void {
  if (listenersWired) return;
  const buttons = document.querySelectorAll<HTMLButtonElement>('#inventory-tab .sub-tab-btn');
  if (buttons.length === 0) return;
  buttons.forEach((btn) => {
    btn.addEventListener('click', () => {
      const name = btn.dataset['invSubtab'] ?? DEFAULT_INVENTORY_SUB_SECTION;
      // Route through the router so the click both switches the view AND
      // pushes /inventory/<subtab> (QA A.4), keeping history consistent
      // with the Admin sub-tab flow.
      switchInventorySubTab(name);
    });
  });
  listenersWired = true;
}

/**
 * True when the Inventory & Coverage tab is the currently-visible top-level
 * tab. The chip-subscription reload skips the fetch when this returns false
 * so we don't burn an API call (or trigger a skeleton flash) for a section
 * the user isn't looking at — switchTab('inventory') runs loadInventory()
 * on next entry anyway, which re-fetches with the current chip state.
 */
function isInventoryTabActive(): boolean {
  return document.getElementById('inventory-tab')?.classList.contains('active') === true;
}

// Unsubscribe handles for the chip subscriptions. Re-assigned each time
// loadInventory() wires them so repeated tab-switches don't stack duplicate
// listeners -- the old pair is torn down before a new pair is registered.
let unsubscribeProvider: (() => void) | null = null;
let unsubscribeAccount: (() => void) | null = null;

/**
 * Wire provider + account chip subscriptions (issue #866).
 *
 * Mirrors the pattern from PR #741 (Purchases) and PR #747 (Home):
 *   - Active-tab guard: only fire when the Inventory tab is active.
 *   - queueMicrotask coalescing: topbar-filters.ts fires BOTH the
 *     account-clear AND the provider-set subscribers synchronously on a
 *     single chip change. Without coalescing the two back-to-back fires
 *     would kick off two fetches; with it they collapse into one.
 *   - Re-check the active-tab guard inside the microtask: a tab switch
 *     between the chip change and the microtask flush cancels the
 *     now-unneeded fetch.
 *
 * Called from loadInventory() on every Inventory tab-switch. Tears down
 * the previous subscription pair first so repeated switches don't stack
 * duplicate listeners.
 */
function wireChipSubscriptions(): void {
  // Tear down any existing subscriptions to avoid stacking on repeated
  // tab-switches.
  if (unsubscribeProvider) { unsubscribeProvider(); unsubscribeProvider = null; }
  if (unsubscribeAccount) { unsubscribeAccount(); unsubscribeAccount = null; }

  let reloadQueued = false;
  const scheduleReload = (): void => {
    if (!isInventoryTabActive() || reloadQueued) return;
    reloadQueued = true;
    queueMicrotask(() => {
      reloadQueued = false;
      if (!isInventoryTabActive()) return;
      if (currentSubSection === 'active-commitments') {
        void loadActiveCommitments();
      } else if (currentSubSection === 'coverage') {
        void loadCoverageBreakdown();
      } else if (currentSubSection === 'trends') {
        void loadTrends();
      }
    });
  };

  unsubscribeProvider = state.subscribeProvider(scheduleReload);
  unsubscribeAccount = state.subscribeAccount(scheduleReload);
}

/**
 * Initialize the Inventory & Coverage section. Called by navigation.ts'
 * switchTab when 'inventory' is selected, passing the sub-section parsed
 * from the `/inventory/<subtab>` URL path (QA A.4).
 *
 * The sub-section comes from the URL, not hidden session state: a fresh
 * `/inventory` with no sub-segment lands on the default (active-commitments)
 * and a `/inventory/<subtab>` deep link lands on that sub-section. The
 * switch is URL-driven (push: false) so re-entering the tab doesn't stack
 * a redundant history entry on top of the one switchTab already pushed.
 */
export function loadInventory(subSection?: string): void {
  wireSubNavListeners();
  wireChipSubscriptions();
  const target = subSection !== undefined && isValidInventorySubSection(subSection)
    ? subSection
    : DEFAULT_INVENTORY_SUB_SECTION;
  // Pure view switch (no history push): switchTab already pushed the
  // canonical /inventory/<subtab> URL when this tab was entered.
  switchInventorySubSection(target);
}
