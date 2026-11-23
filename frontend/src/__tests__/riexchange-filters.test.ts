/**
 * RI Exchange module tests — filter subscriptions, staleness banner, history
 * table counts and the excluded-RIs notice
 */

import './riexchange-harness';
import { loadReshapeRecommendations, loadRIExchange, renderExcludedRIsNotice } from '../riexchange';
import * as api from '../api';

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
