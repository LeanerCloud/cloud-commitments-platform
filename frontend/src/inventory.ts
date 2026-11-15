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

import * as api from './api';
import type { ProviderCoverageSection, CoverageServiceRow } from './api';
import { loadRIExchange } from './riexchange';
import { loadTrends } from './trends';
import { showSkeletonRows, teardownSkeleton } from './lib/skeleton';
import { renderErrorCallout } from './lib/error-callout';
import {
  buildCoverageBar, coverageTargetFor, coverageTargetsFromConfig, type CoverageTargets,
} from './lib/coverage-bar';
import { formatCurrency } from './utils';
import * as state from './state';
import { switchInventorySubTab } from './navigation';
import { syncTabStops } from './lib/tablist';
import { ownValue } from './lib/own-property';
import { appendCell, clearChildren, renderEmptyParagraph } from './modules/inventory-dom';
import { loadActiveCommitments } from './modules/inventory-commitments';

export { loadActiveCommitments };

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

let coverageGen = 0;

/**
 * Build a context-aware empty-state message for a per-provider Coverage card.
 * When chip filters are active the message names the scope so the user knows
 * the result is filtered rather than genuinely absent usage.
 *
 * @param providerLabel - Display name of the provider (e.g. "AWS").
 * @param activeProvider - The provider chip value, if one is set.
 * @param activeAccountID - The account chip value, if exactly one is selected.
 */
function buildCoverageEmptyMessage(
  providerLabel: string,
  activeProvider?: string,
  activeAccountID?: string,
): string {
  if (activeProvider || activeAccountID) {
    return `No ${providerLabel} usage for the selected account/filter.`;
  }
  return `No usage detected for ${providerLabel}.`;
}

// ──────────────────────────────────────────────
// Coverage breakdown
// ──────────────────────────────────────────────

const COVERAGE_CONTAINER_ID = 'coverage-providers';
const COVERAGE_REFRESH_BTN_ID = 'coverage-refresh-btn';

const PROVIDER_DISPLAY_NAMES: Record<string, string> = {
  aws: 'AWS',
  azure: 'Azure',
  gcp: 'GCP',
};

/**
 * Fetch and render per-provider coverage breakdowns into #coverage-providers.
 * Shows a skeleton on entry, then either the rendered sections or an error.
 * Idempotent — safe to call on every sub-tab switch and on every refresh click.
 *
 * Reads the current provider/account chips from state so that changing a
 * chip while on this sub-tab re-fetches with the new scope (issue #866).
 */
export async function loadCoverageBreakdown(): Promise<void> {
  const container = document.getElementById(COVERAGE_CONTAINER_ID);
  if (!container) return;

  wireCoverageRefreshButton();

  const provider = state.getCurrentProvider();
  const accountIDs = state.getCurrentAccountIDs();
  const accountID = accountIDs.length === 1 ? accountIDs[0] : undefined;

  // One skeleton row per known provider while loading.
  showSkeletonRows(container, 3, 1);

  const gen = ++coverageGen;
  try {
    // The config supplies each service's coverage target for the bars
    // (issue #726). It is optional: a failed read only drops the target tick.
    const [data, cfg] = await Promise.all([
      api.getCoverageBreakdown({ provider: provider || undefined, accountID }),
      Promise.resolve().then(() => api.getConfig()).catch(() => null),
    ]);
    if (gen !== coverageGen) return;
    const targets = coverageTargetsFromConfig(cfg);
    renderCoverageBreakdown(container, data.providers, targets, provider || undefined, accountID);
  } catch (error) {
    if (gen !== coverageGen) return;
    teardownSkeleton(container);
    const err = error as Error;
    renderErrorCallout(container, {
      message: `Failed to load coverage data: ${err.message}`,
      onRetry: () => void loadCoverageBreakdown(),
    });
  }
}

function wireCoverageRefreshButton(): void {
  const btn = document.getElementById(COVERAGE_REFRESH_BTN_ID);
  if (!btn) return;
  if (btn.dataset['wired'] === '1') return;
  btn.addEventListener('click', () => {
    void loadCoverageBreakdown();
  });
  btn.dataset['wired'] = '1';
}

/**
 * Render coverage sections. Each provider gets its own card. Providers
 * with services=null show an empty-state paragraph. All text is set via
 * textContent -- no innerHTML -- so no escaping helper is needed (XSS posture
 * matches the active-commitments section per issue #340).
 */
function renderCoverageBreakdown(
  container: HTMLElement,
  providers: ProviderCoverageSection[],
  targets: CoverageTargets,
  activeProvider?: string,
  activeAccountID?: string,
): void {
  clearChildren(container);

  if (!providers || providers.length === 0) {
    renderEmptyParagraph(container, 'No coverage data available.');
    return;
  }

  for (const section of providers) {
    container.appendChild(buildProviderSection(section, targets, activeProvider, activeAccountID));
  }
}

function buildProviderSection(
  section: ProviderCoverageSection,
  targets: CoverageTargets,
  activeProvider?: string,
  activeAccountID?: string,
): HTMLElement {
  const card = document.createElement('section');
  card.className = 'card coverage-provider-card';

  // Header row: provider name + overall coverage badge.
  const header = document.createElement('div');
  header.className = 'section-header';

  const providerLabel = ownValue(PROVIDER_DISPLAY_NAMES, section.provider) ?? section.provider.toUpperCase();

  const title = document.createElement('h3');
  title.textContent = providerLabel;
  header.appendChild(title);

  if (section.overall_coverage_pct !== null && section.overall_coverage_pct !== undefined) {
    const badge = document.createElement('span');
    badge.className = 'coverage-overall-badge';
    badge.textContent = `Overall: ${section.overall_coverage_pct.toFixed(1)}% covered`;
    header.appendChild(badge);
  }
  card.appendChild(header);

  // Body: empty-state or per-service table.
  if (!section.services || section.services.length === 0) {
    const empty = document.createElement('p');
    empty.className = 'empty';
    empty.textContent = buildCoverageEmptyMessage(providerLabel, activeProvider, activeAccountID);
    card.appendChild(empty);
    return card;
  }

  card.appendChild(buildServiceTable(section.services, (service) => coverageTargetFor(targets, section.provider, service)));
  return card;
}

function buildServiceTable(
  rows: CoverageServiceRow[],
  targetFor: (service: string) => number | null,
): HTMLTableElement {
  const table = document.createElement('table');
  table.className = 'coverage-service-table table-compact';

  const thead = document.createElement('thead');
  const headerRow = document.createElement('tr');
  for (const label of ['Service', 'Covered/mo', 'On-Demand Gap/mo', 'Coverage %', 'Coverage Bar']) {
    const th = document.createElement('th');
    th.textContent = label;
    if (label.endsWith('/mo')) th.className = 'num';
    if (label === 'Coverage Bar') {
      th.setAttribute('aria-label', 'Coverage bar');
    }
    headerRow.appendChild(th);
  }
  thead.appendChild(headerRow);
  table.appendChild(thead);

  const tbody = document.createElement('tbody');
  for (const row of rows) {
    tbody.appendChild(buildServiceRow(row, targetFor(row.service)));
  }
  table.appendChild(tbody);
  return table;
}

function buildServiceRow(row: CoverageServiceRow, target: number | null): HTMLTableRowElement {
  const tr = document.createElement('tr');

  appendCell(tr, row.service);
  appendCell(tr, formatCurrency(row.covered_monthly), true);
  appendCell(tr, formatCurrency(row.on_demand_monthly), true);
  appendCell(tr, row.coverage_pct !== null && row.coverage_pct !== undefined
    ? `${row.coverage_pct.toFixed(1)}%`
    : 'N/A');
  // Bar cell: visual coverage indicator.
  const barTd = document.createElement('td');
  barTd.className = 'coverage-bar-cell';
  if (row.coverage_pct !== null && row.coverage_pct !== undefined) {
    barTd.appendChild(buildCoverageBar(row.coverage_pct, target));
  } else {
    // No coverage figure: say so rather than leaving the cell blank, which
    // reads as a rendering failure. A 0%-width bar is not an option -- it
    // would claim we measured zero coverage.
    const absent = document.createElement('span');
    absent.className = 'coverage-bar-absent';
    absent.textContent = 'N/A';
    barTd.appendChild(absent);
  }
  tr.appendChild(barTd);

  return tr;
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
