/**
 * RI Exchange module tests — filter subscriptions, staleness banner, history
 * table counts and the excluded-RIs notice
 */

import { _accountListeners, _providerListeners } from './riexchange-harness';
import {
  loadReshapeRecommendations,
  loadRIExchange,
  renderExcludedRIsNotice,
  renderReshapeStalenessBanner,
  setupRIExchangeHandlers,
} from '../riexchange';
import * as api from '../api';
import * as navigation from '../navigation';

describe('exchange history table counts (issue #286)', () => {
  beforeEach(() => {
    for (const id of ['ri-exchange-instances-list', 'ri-exchange-recommendations-list', 'ri-exchange-history-list']) {
      const el = document.createElement('div');
      el.id = id;
      document.body.appendChild(el);
    }
    const s = jest.requireMock('../state') as { getCurrentProvider: jest.Mock; getCurrentAccountIDs: jest.Mock; getRiExchangeColumnFilters: jest.Mock; getActiveRiColumnFilters: jest.Mock };
    s.getCurrentProvider.mockReturnValue('aws');
    s.getCurrentAccountIDs.mockReturnValue([]);
    s.getRiExchangeColumnFilters.mockReturnValue({});
    s.getActiveRiColumnFilters.mockReturnValue({});
    (api.listConvertibleRIs as jest.Mock).mockResolvedValue([]);
    (api.getRIUtilization as jest.Mock).mockResolvedValue([]);
    (api.getReshapeRecommendations as jest.Mock).mockResolvedValue({ recommendations: [], recs_staleness: '', recs_collected_at: null });
  });

  afterEach(() => {
    document.body.innerHTML = '';
    jest.clearAllMocks();
  });

  it('shows each count once, inside the type cells, with no duplicate Count column', async () => {
    (api.getRIExchangeHistoryResponse as jest.Mock).mockResolvedValue({ records: [{
      id: 'h1', account_id: 'a', exchange_id: 'exch-1', region: 'us-east-1', source_ri_ids: ['ri-1'],
      source_instance_type: 'm5.large', source_count: 4, target_offering_id: 'o', target_instance_type: 'm5.4xlarge',
      target_count: 1, payment_due: '1.00', status: 'completed', mode: 'manual',
      created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
    }], direct_executions: [] });
    await loadRIExchange();
    const table = document.querySelector('#ri-exchange-history-list table')!;
    const headers = Array.from(table.querySelectorAll('th')).map((h) => h.textContent);
    expect(headers).not.toContain('Count');
    const cells = Array.from(table.querySelectorAll('tbody tr td')).map((c) => c.textContent);
    expect(cells).toHaveLength(headers.length);
    expect(cells).toContain('4x m5.large');
    expect(cells).toContain('1x m5.4xlarge');
    expect(cells).not.toContain('4');
  });

  // Issue #737: the status cell used to print the raw lowercase value with a
  // page-local color (pending blue, processing orange). It now uses the shared
  // badge tones and display labels.
  it.each([
    ['pending', 'badge-warning', 'Pending'],
    ['processing', 'badge-warning', 'Processing'],
    ['completed', 'badge-success', 'Completed'],
    ['failed', 'badge-danger', 'Failed'],
    ['canceled', 'badge-muted', 'Cancelled'],
  ])('renders status %s as a %s badge reading %s', async (status, cls, label) => {
    (api.getRIExchangeHistoryResponse as jest.Mock).mockResolvedValue({ records: [{
      id: 'h1', account_id: 'a', exchange_id: 'exch-1', region: 'us-east-1', source_ri_ids: ['ri-1'],
      source_instance_type: 'm5.large', source_count: 1, target_offering_id: 'o', target_instance_type: 'm5.xlarge',
      target_count: 1, payment_due: '1.00', status, mode: 'manual',
      created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
    }], direct_executions: [] });
    await loadRIExchange();
    const badge = document.querySelector('#ri-exchange-history-list tbody .badge')!;
    expect(badge.className).toBe(`badge ${cls}`);
    expect(badge.textContent).toBe(label);
  });
});

// Issue #1181: RIs left out of reshape analysis must be visible in the UI.
describe('excluded RIs notice', () => {
  let listEl: HTMLDivElement;

  beforeEach(() => {
    const wrapper = document.createElement('div');
    listEl = document.createElement('div');
    listEl.id = 'ri-exchange-recommendations-list';
    wrapper.appendChild(listEl);
    document.body.appendChild(wrapper);
  });

  afterEach(() => {
    document.body.innerHTML = '';
    jest.resetAllMocks();
  });

  it('lists the excluded ids as text when excluded_ris is non-empty', async () => {
    (api.getReshapeRecommendations as jest.Mock).mockResolvedValue({
      recommendations: [],
      excluded_ris: [
        { ri_id: 'ri-0', reason: 'no term' },
        { ri_id: '<img src=x onerror=alert(1)>', reason: 'no term' },
      ],
    });

    await loadReshapeRecommendations();

    const notice = document.getElementById('ri-exchange-recommendations-excluded');
    expect(notice?.textContent).toContain('2 convertible RIs were left out because no term');
    expect(notice?.textContent).toContain('ri-0');
    expect(notice?.textContent).toContain('<img src=x onerror=alert(1)>');
    expect(notice?.querySelector('img')).toBeNull();
  });

  it('shows each excluded RI\'s server reason, grouped by reason', () => {
    renderExcludedRIsNotice([
      { ri_id: 'ri-dur', reason: 'non-positive duration' },
      { ri_id: 'ri-price', reason: 'no pricing available' },
    ]);
    const text = document.getElementById('ri-exchange-recommendations-excluded')?.textContent ?? '';
    expect(text).toContain('1 convertible RI was left out because non-positive duration: ri-dur');
    expect(text).toContain('1 convertible RI was left out because no pricing available: ri-price');
    expect(text).not.toContain('no term length');
  });

  it('falls back to the term-length text when reason is empty or missing', () => {
    renderExcludedRIsNotice([
      { ri_id: 'ri-a', reason: '' },
      { ri_id: 'ri-b' } as unknown as { ri_id: string; reason: string },
    ]);
    expect(document.getElementById('ri-exchange-recommendations-excluded')?.textContent).toContain(
      '2 convertible RIs were left out because AWS returned no term length: ri-a, ri-b',
    );
  });

  it('renders a reason containing HTML special characters as text', () => {
    renderExcludedRIsNotice([{ ri_id: 'ri-x', reason: '<b>bad</b> & "quoted"' }]);
    const notice = document.getElementById('ri-exchange-recommendations-excluded');
    expect(notice?.textContent).toContain('<b>bad</b> & "quoted"');
    expect(notice?.querySelector('b')).toBeNull();
  });

  it('shows no notice when excluded_ris is empty or absent, and clears a stale one', () => {
    renderExcludedRIsNotice([{ ri_id: 'ri-0', reason: 'x' }]);
    expect(document.getElementById('ri-exchange-recommendations-excluded')?.textContent).toContain('1 convertible RI was left out');
    renderExcludedRIsNotice(undefined);
    expect(document.getElementById('ri-exchange-recommendations-excluded')?.textContent).toBe('');
  });

  it('does not render a notice for an empty list on load', async () => {
    (api.getReshapeRecommendations as jest.Mock).mockResolvedValue({ recommendations: [], excluded_ris: [] });
    await loadReshapeRecommendations();
    expect(document.getElementById('ri-exchange-recommendations-excluded')?.textContent ?? '').toBe('');
  });
});

// Staleness banner tests for issue #150.
describe('renderReshapeStalenessBanner', () => {
  let listEl: HTMLDivElement;

  beforeEach(() => {
    const wrapper = document.createElement('div');
    listEl = document.createElement('div');
    listEl.id = 'ri-exchange-recommendations-list';
    wrapper.appendChild(listEl);
    document.body.appendChild(wrapper);
  });

  afterEach(() => {
    document.body.innerHTML = '';
  });

  it('renders no banner when staleness is empty', () => {
    renderReshapeStalenessBanner('', null);
    const banner = document.getElementById('ri-exchange-recommendations-freshness');
    expect(banner?.textContent).toBe('');
  });

  it('renders a soft-warning banner for staleness=soft', () => {
    renderReshapeStalenessBanner('soft', null);
    const banner = document.getElementById('ri-exchange-recommendations-freshness');
    expect(banner?.className).toContain('warning');
    expect(banner?.textContent).toContain('more than 12h old');
  });

  it('renders a hard-warning banner for staleness=hard', () => {
    renderReshapeStalenessBanner('hard', null);
    const banner = document.getElementById('ri-exchange-recommendations-freshness');
    expect(banner?.className).toContain('error');
    expect(banner?.textContent).toContain('older than 24h');
  });

  it('includes an age label when recs_collected_at is provided', () => {
    const twoHoursAgo = new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString();
    renderReshapeStalenessBanner('soft', twoHoursAgo);
    const banner = document.getElementById('ri-exchange-recommendations-freshness');
    expect(banner?.textContent).toContain('last collected 2h');
  });

  it('reports days once the data is older than 48h (#1691)', () => {
    const old = new Date(Date.now() - 203 * 60 * 60 * 1000).toISOString();
    renderReshapeStalenessBanner('hard', old);
    const text = document.getElementById('ri-exchange-recommendations-freshness')?.textContent;
    expect(text).toContain('last collected 8 days ago');
    expect(text).not.toContain('203h');
  });

  it('clears an existing banner when staleness becomes empty', () => {
    renderReshapeStalenessBanner('hard', null);
    expect(document.getElementById('ri-exchange-recommendations-freshness')?.className).toContain('error');
    renderReshapeStalenessBanner('', null);
    const banner = document.getElementById('ri-exchange-recommendations-freshness');
    expect(banner?.textContent).toBe('');
    expect(banner?.className).toBe('');
  });
});

describe('⚙︎ Exchange settings deep-link', () => {
  beforeEach(() => {
    const btn = document.createElement('button');
    btn.id = 'ri-exchange-settings-btn';
    document.body.appendChild(btn);
  });

  afterEach(() => {
    const body = document.body;
    while (body.firstChild) body.removeChild(body.firstChild);
    jest.resetAllMocks();
  });

  // Issue #1775: 'settings' was the pre-#340 tab name, so switchTab fell back
  // to Home while switchSettingsSubTab still set the Admin title and pushed
  // /admin/purchasing -- URL and title said Admin over the Home dashboard.
  it('switches to Admin → Purchasing when clicked', () => {
    setupRIExchangeHandlers();
    const btn = document.getElementById('ri-exchange-settings-btn')!;
    btn.click();
    expect(navigation.switchTab).toHaveBeenCalledWith('admin', { push: false, skipDefaultLoad: true });
    expect(navigation.switchSettingsSubTab).toHaveBeenCalledWith('purchasing');
  });
});

// issue #186: provider/account subscriptions on the RI Exchange tab
describe('RI Exchange filter subscriptions (issue #186)', () => {
  let instancesEl: HTMLDivElement;
  let recsEl: HTMLDivElement;
  let historyEl: HTMLDivElement;
  let riExchangePanel: HTMLDivElement;

  beforeEach(() => {
    instancesEl = document.createElement('div');
    instancesEl.id = 'ri-exchange-instances-list';
    recsEl = document.createElement('div');
    recsEl.id = 'ri-exchange-recommendations-list';
    historyEl = document.createElement('div');
    historyEl.id = 'ri-exchange-history-list';
    // The sub-tab panel must exist and be visible for the guard to pass.
    riExchangePanel = document.createElement('div');
    riExchangePanel.id = 'inventory-ri-exchange';
    document.body.append(instancesEl, recsEl, historyEl, riExchangePanel);

    (api.listConvertibleRIs as jest.Mock).mockResolvedValue([]);
    (api.getRIUtilization as jest.Mock).mockResolvedValue([]);
    (api.getReshapeRecommendations as jest.Mock).mockResolvedValue({ recommendations: [], recs_staleness: '', recs_collected_at: null });
    (api.getRIExchangeHistoryResponse as jest.Mock).mockResolvedValue({ records: [], direct_executions: [] });
    _providerListeners.length = 0;
    _accountListeners.length = 0;
    // Re-apply the implementation after jest.resetAllMocks() from a prior
    // describe block may have cleared it.
    const stateMod = jest.requireMock('../state') as {
      subscribeProvider: jest.Mock;
      subscribeAccount: jest.Mock;
      getCurrentProvider: jest.Mock;
      getCurrentAccountIDs: jest.Mock;
    };
    stateMod.subscribeProvider.mockImplementation((cb: () => void) => {
      _providerListeners.push(cb);
      return () => undefined;
    });
    stateMod.subscribeAccount.mockImplementation((cb: () => void) => {
      _accountListeners.push(cb);
      return () => undefined;
    });
    // loadRIExchange now reads the provider/account chips to scope the
    // request (issue #871); restore their implementations too.
    stateMod.getCurrentProvider.mockImplementation(() => 'aws');
    stateMod.getCurrentAccountIDs.mockImplementation(() => []);
  });

  afterEach(() => {
    document.body.innerHTML = '';
    // Use clearAllMocks rather than resetAllMocks so the subscribeProvider/
    // subscribeAccount mock implementations (which push to _providerListeners)
    // are preserved across tests in this block.
    jest.clearAllMocks();
  });

  it('setupRIExchangeHandlers registers subscribeProvider and subscribeAccount', () => {
    const stateMod = jest.requireMock('../state');
    setupRIExchangeHandlers();
    expect(stateMod.subscribeProvider).toHaveBeenCalled();
    expect(stateMod.subscribeAccount).toHaveBeenCalled();
  });

  it('a provider change triggers loadRIExchange when the sub-tab is active', async () => {
    setupRIExchangeHandlers();
    // Fire the provider listener (simulates topbar provider change).
    _providerListeners.forEach(cb => cb());
    // Flush the microtask queue: queueMicrotask fires after all pending
    // micro-ticks; wrapping in a resolved promise ensures we drain it.
    await new Promise(r => setTimeout(r, 0));
    expect(api.listConvertibleRIs).toHaveBeenCalled();
  });

  it('a provider change does NOT trigger loadRIExchange when the sub-tab is hidden', async () => {
    riExchangePanel.classList.add('hidden');
    setupRIExchangeHandlers();
    _providerListeners.forEach(cb => cb());
    await new Promise(r => setTimeout(r, 0));
    expect(api.listConvertibleRIs).not.toHaveBeenCalled();
  });

  it('an account change triggers loadRIExchange when the sub-tab is active', async () => {
    setupRIExchangeHandlers();
    _accountListeners.forEach(cb => cb());
    await new Promise(r => setTimeout(r, 0));
    expect(api.listConvertibleRIs).toHaveBeenCalled();
  });

  it('coalesces provider and account changes into a single reload', async () => {
    setupRIExchangeHandlers();
    // Simulate topbar filter cascade: provider change triggers account reset
    _providerListeners.forEach(cb => cb());
    _accountListeners.forEach(cb => cb());
    await new Promise(r => setTimeout(r, 0));
    // Should be called once, not twice
    expect(api.listConvertibleRIs).toHaveBeenCalledTimes(1);
  });
});
