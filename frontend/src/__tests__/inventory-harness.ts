/**
 * Shared setup for the inventory suites: the module mocks, the inventory DOM
 * builder and the commitment and coverage fixtures. Import this module FIRST
 * so the mocks register before a suite loads ../inventory. This file is not a
 * test (jest only collects *.test.ts).
 */
import * as api from '../api';
import type { ProviderCoverageSection } from '../api';

// loadRIExchange is a side-effect import from a module that touches the
// network in real use. Mock it out so the test only exercises sub-section
// switching, not real RI fetch.
jest.mock('../riexchange', () => ({
  loadRIExchange: jest.fn(),
}));

// The Trends sub-tab's fetch/render is covered in trends.test.ts; here we
// only check that the sub-section switch and chip reload route to it.
jest.mock('../trends', () => ({
  loadTrends: jest.fn(),
}));

// The active-commitments and coverage load paths hit the API. Mock the
// entire api barrel so we don't need to stand up fetch — tests exercise
// the render machinery, not the network shape.
jest.mock('../api', () => ({
  listActiveCommitments: jest.fn(),
  getCoverageBreakdown: jest.fn(),
  getConfig: jest.fn(),
}));

// Mock state so chip subscription tests can control provider/account
// values and capture the registered callbacks. subscribeProvider and
// subscribeAccount must return a function (the unsubscribe handle) since
// inventory.ts calls the return value to tear down old subscriptions on
// repeated loadInventory() calls (feedback_event_listener_dedup pattern).
jest.mock('../state', () => ({
  subscribeProvider: jest.fn(() => jest.fn()),
  subscribeAccount: jest.fn(() => jest.fn()),
  getCurrentProvider: jest.fn(() => ''),
  getCurrentAccountIDs: jest.fn(() => []),
  getAmortizeUpfront: jest.fn(() => false),
  setAmortizeUpfront: jest.fn(),
  subscribeAmortizeUpfront: jest.fn(() => jest.fn()),
}));

// inventory.ts routes sub-nav clicks through navigation.switchInventorySubTab
// so the click both switches the view AND pushes /inventory/<subtab>
// (QA A.4). Mock it to delegate to the real (pure) view switcher: that
// keeps the click->DOM behaviour these tests assert, while letting us spy
// on the router call. The URL-push half of switchInventorySubTab is tested
// in navigation.test.ts where the router owns history.
jest.mock('../navigation', () => {
  const actual = jest.requireActual('../inventory');
  return {
    switchInventorySubTab: jest.fn((name: string) => actual.switchInventorySubSection(name)),
  };
});

export function buildInventoryDOM(): void {
  // Build the inventory tab + sub-nav via DOM methods rather than an
  // innerHTML string. Matches the no-innerHTML-with-interpolated-data
  // constraint from issue #340's plan.
  const tab = document.createElement('div');
  tab.id = 'inventory-tab';
  tab.classList.add('tab-content', 'active');

  const subnav = document.createElement('div');
  subnav.classList.add('inventory-subnav');

  for (const [name, label, isActive] of [
    ['active-commitments', 'Active commitments', true],
    ['coverage', 'Coverage', false],
    ['ri-exchange', 'RI Exchange', false],
  ] as const) {
    const btn = document.createElement('button');
    btn.classList.add('sub-tab-btn');
    if (isActive) btn.classList.add('active');
    btn.dataset['invSubtab'] = name;
    btn.setAttribute('role', 'tab');
    btn.setAttribute('aria-selected', isActive ? 'true' : 'false');
    btn.textContent = label;
    subnav.appendChild(btn);
  }
  tab.appendChild(subnav);

  // active-commitments section: contains the real refresh button + list
  // container (matches index.html shape).
  const ac = document.createElement('section');
  ac.id = 'inventory-active-commitments';
  const refresh = document.createElement('button');
  refresh.id = 'active-commitments-refresh-btn';
  refresh.textContent = 'Refresh';
  ac.appendChild(refresh);
  const list = document.createElement('div');
  list.id = 'active-commitments-list';
  ac.appendChild(list);
  tab.appendChild(ac);

  // coverage section: matches the real HTML structure with refresh button +
  // providers container that loadCoverageBreakdown renders into.
  const coverageSection = document.createElement('section');
  coverageSection.id = 'inventory-coverage';
  coverageSection.classList.add('hidden');
  const coverageRefresh = document.createElement('button');
  coverageRefresh.id = 'coverage-refresh-btn';
  coverageRefresh.textContent = 'Refresh';
  coverageSection.appendChild(coverageRefresh);
  const coverageProviders = document.createElement('div');
  coverageProviders.id = 'coverage-providers';
  coverageSection.appendChild(coverageProviders);
  tab.appendChild(coverageSection);

  const riSection = document.createElement('section');
  riSection.id = 'inventory-ri-exchange';
  riSection.classList.add('hidden');
  riSection.textContent = 'ri-exchange';
  tab.appendChild(riSection);

  document.body.appendChild(tab);
}

export function makeCommitment(overrides: Partial<api.InventoryCommitment> = {}): api.InventoryCommitment {
  return {
    id: 'acc-1:p-1',
    provider: 'aws',
    account_id: 'acc-1',
    account_name: 'Prod Account',
    service: 'ec2',
    resource_type: 'm5.large',
    region: 'us-east-1',
    count: 4,
    term_years: 1,
    payment_option: 'no-upfront',
    start_date: '2025-01-01T00:00:00Z',
    end_date: '2026-01-01T00:00:00Z',
    upfront_cost: 0,
    monthly_cost: 240.5,
    estimated_savings: 80.0,
    status: 'active',
    ...overrides,
  };
}

export function clearDOM(): void {
  while (document.body.firstChild) document.body.removeChild(document.body.firstChild);
}

// Money and count columns carry th.num/td.num (right-aligned, tabular
// digits) in the header and in every row at the same index (issue #739).
export function numColumns(root: ParentNode): { headers: string[]; rowsMatch: boolean } {
  const ths = Array.from(root.querySelectorAll('thead th'));
  const numIdx = ths.flatMap((th, i) => (th.classList.contains('num') ? [i] : []));
  const rows = Array.from(root.querySelectorAll('tbody tr'));
  const rowsMatch = rows.length > 0 && rows.every((tr) => {
    const tds = Array.from(tr.querySelectorAll('td'));
    return tds.flatMap((td, i) => (td.classList.contains('num') ? [i] : [])).join() === numIdx.join();
  });
  return { headers: numIdx.map((i) => (ths[i]!.textContent ?? '').trim()), rowsMatch };
}

export function makeProviderSection(
  provider: string,
  services: ProviderCoverageSection['services'],
  overallPct: number | null
): ProviderCoverageSection {
  return { provider, services, overall_coverage_pct: overallPct };
}
