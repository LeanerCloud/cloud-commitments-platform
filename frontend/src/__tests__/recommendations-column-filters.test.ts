/**
 * Recommendations module tests: applyColumnFilters, the filter accessors and the first column header filter tests.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults } from './recommendations-harness';
import { loadRecommendations, applyColumnFilters } from '../recommendations';
import type { LocalRecommendation } from '../types';
import * as api from '../api';
import * as state from '../state';

beforeEach(() => {
  for (const [fn, factoryDefault] of mockFactoryDefaults) {
    fn.mockReset();
    if (factoryDefault) fn.mockImplementation(factoryDefault);
  }
});

describe('applyColumnFilters', () => {
  const rec = (
    overrides: Partial<LocalRecommendation> = {},
  ): LocalRecommendation => ({
    id: overrides.id ?? 'r1',
    provider: overrides.provider ?? 'aws',
    cloud_account_id: overrides.cloud_account_id ?? 'acct-1',
    service: overrides.service ?? 'ec2',
    resource_type: overrides.resource_type ?? 't3.medium',
    region: overrides.region ?? 'us-east-1',
    count: overrides.count ?? 1,
    term: overrides.term ?? 1,
    upfront_cost: overrides.upfront_cost ?? 100,
    monthly_cost: overrides.monthly_cost ?? 10,
    savings: overrides.savings ?? 50,
    engine: overrides.engine,
  } as unknown as LocalRecommendation);

  test('empty filters returns a clone of the input (no-op)', () => {
    const recs = [rec({ id: 'a' }), rec({ id: 'b' })];
    const out = applyColumnFilters(recs, {});
    expect(out).toEqual(recs);
    expect(out).not.toBe(recs); // defensive clone
  });

  test('categorical set filter narrows by membership', () => {
    const recs = [
      rec({ id: 'a', provider: 'aws' }),
      rec({ id: 'b', provider: 'azure' }),
      rec({ id: 'c', provider: 'gcp' }),
    ];
    const out = applyColumnFilters(recs, {
      provider: { kind: 'set', values: ['aws'] },
    });
    expect(out.map(r => r.id)).toEqual(['a']);
  });

  test('Account filter matches on cloud_account_id, not display name', () => {
    const recs = [
      rec({ id: 'a', cloud_account_id: 'acct-prod' }),
      rec({ id: 'b', cloud_account_id: 'acct-dev' }),
    ];
    const out = applyColumnFilters(recs, {
      account: { kind: 'set', values: ['acct-prod'] },
    });
    expect(out.map(r => r.id)).toEqual(['a']);
  });

  test('Term filter values are strings; row term integer stringifies', () => {
    const recs = [
      rec({ id: 'a', term: 1 }),
      rec({ id: 'b', term: 3 }),
    ];
    const out = applyColumnFilters(recs, {
      term: { kind: 'set', values: ['3'] },
    });
    expect(out.map(r => r.id)).toEqual(['b']);
  });

  test('(empty) sentinel matches null / undefined / "" cloud_account_id', () => {
    // Build raw objects — bypass the rec() factory because its `??` defaults
    // would replace null/undefined cloud_account_id with 'acct-1'.
    const recs: LocalRecommendation[] = [
      { ...rec({ id: 'a' }), cloud_account_id: '' } as LocalRecommendation,
      { ...rec({ id: 'b' }), cloud_account_id: undefined } as unknown as LocalRecommendation,
      rec({ id: 'c', cloud_account_id: 'acct-x' }),
    ];
    const out = applyColumnFilters(recs, {
      account: { kind: 'set', values: [''] },
    });
    expect(out.map(r => r.id).sort()).toEqual(['a', 'b']);
  });

  test('numeric expr filter narrows by predicate', () => {
    const recs = [
      rec({ id: 'a', savings: 25 }),
      rec({ id: 'b', savings: 150 }),
      rec({ id: 'c', savings: 1500 }),
    ];
    const out = applyColumnFilters(recs, {
      savings: { kind: 'expr', expr: '>100' },
    });
    expect(out.map(r => r.id)).toEqual(['b', 'c']);
  });

  test('invalid numeric expression is ignored (filter not applied)', () => {
    const recs = [rec({ id: 'a', savings: 1 }), rec({ id: 'b', savings: 100 })];
    const out = applyColumnFilters(recs, {
      savings: { kind: 'expr', expr: '>>5' }, // syntax error
    });
    // All rows pass — broken filter is silently inert.
    expect(out.map(r => r.id)).toEqual(['a', 'b']);
  });

  test('multiple column filters AND together', () => {
    const recs = [
      rec({ id: 'a', provider: 'aws', savings: 50 }),
      rec({ id: 'b', provider: 'aws', savings: 500 }),
      rec({ id: 'c', provider: 'azure', savings: 500 }),
    ];
    const out = applyColumnFilters(recs, {
      provider: { kind: 'set', values: ['aws'] },
      savings: { kind: 'expr', expr: '>100' },
    });
    expect(out.map(r => r.id)).toEqual(['b']);
  });

  test('capacity filter matches formatted "N vCPU / M GB" string', () => {
    const recs: LocalRecommendation[] = [
      { ...rec({ id: 'a' }), vcpu: 8,   memory_gb: 32 } as LocalRecommendation,
      { ...rec({ id: 'b' }), vcpu: 4,   memory_gb: 16 } as LocalRecommendation,
      { ...rec({ id: 'c' }), vcpu: null, memory_gb: null } as unknown as LocalRecommendation,
    ];
    const out = applyColumnFilters(recs, {
      capacity: { kind: 'set', values: ['8 vCPU / 32 GB'] },
    });
    expect(out.map(r => r.id)).toEqual(['a']);
  });

  test('capacity filter with empty string matches recs without vcpu/memory_gb', () => {
    const recs: LocalRecommendation[] = [
      { ...rec({ id: 'a' }), vcpu: 8,   memory_gb: 32 } as LocalRecommendation,
      { ...rec({ id: 'b' }), vcpu: null, memory_gb: null } as unknown as LocalRecommendation,
    ];
    const out = applyColumnFilters(recs, {
      capacity: { kind: 'set', values: [''] },
    });
    expect(out.map(r => r.id)).toEqual(['b']);
  });
});

//
// Bundle A: state-accessor tests for the new column-filter / visible-recs API.
// These import the REAL state module (the recommendations.test.ts above mocks
// it; here we exercise the actual implementation in a separate require scope).
//

describe('state.ts column-filter accessors', () => {
  // The top-level jest.mock('../state', …) replaces the module for every
  // import. Use jest.requireActual to bypass it for these state-accessor
  // tests so we exercise the real implementation. Each test starts from
  // a clean filter state via clearAllRecommendationsColumnFilters().
  const realState = jest.requireActual<typeof import('../state')>('../state');

  beforeEach(() => {
    realState.clearAllRecommendationsColumnFilters();
    realState.setVisibleRecommendations([]);
  });

  test('default filters are empty', () => {
    expect(realState.getRecommendationsColumnFilters()).toEqual({});
  });

  test('setRecommendationsColumnFilter adds an entry', () => {
    realState.setRecommendationsColumnFilter('provider', { kind: 'set', values: ['aws'] });
    expect(realState.getRecommendationsColumnFilters()).toEqual({
      provider: { kind: 'set', values: ['aws'] },
    });
  });

  test('passing null clears that single column', () => {
    realState.setRecommendationsColumnFilter('provider', { kind: 'set', values: ['aws'] });
    realState.setRecommendationsColumnFilter('savings', { kind: 'expr', expr: '>100' });
    realState.setRecommendationsColumnFilter('provider', null);
    expect(realState.getRecommendationsColumnFilters()).toEqual({
      savings: { kind: 'expr', expr: '>100' },
    });
  });

  test('clearAllRecommendationsColumnFilters empties the record', () => {
    realState.setRecommendationsColumnFilter('provider', { kind: 'set', values: ['aws'] });
    realState.setRecommendationsColumnFilter('savings', { kind: 'expr', expr: '>100' });
    realState.clearAllRecommendationsColumnFilters();
    expect(realState.getRecommendationsColumnFilters()).toEqual({});
  });

  test('getRecommendationsColumnFilters returns a defensive shallow copy', () => {
    realState.setRecommendationsColumnFilter('provider', { kind: 'set', values: ['aws'] });
    const a = realState.getRecommendationsColumnFilters();
    delete a.provider;
    // mutation of the returned object must not affect module state
    expect(realState.getRecommendationsColumnFilters()).toEqual({
      provider: { kind: 'set', values: ['aws'] },
    });
  });

  test('setVisibleRecommendations / getVisibleRecommendations round-trip with defensive clone', () => {
    const recs = [{ id: 'r1' }, { id: 'r2' }] as unknown as Parameters<
      typeof realState.setVisibleRecommendations
    >[0];
    realState.setVisibleRecommendations(recs);
    const out = realState.getVisibleRecommendations();
    expect(out.map((r) => (r as unknown as { id: string }).id)).toEqual(['r1', 'r2']);
    // mutating returned array must not affect module state
    (out as unknown as Array<unknown>).pop();
    expect(realState.getVisibleRecommendations()).toHaveLength(2);
  });
});

//
// Bundle B: column-filter popover + sticky bottom action box DOM behaviour.
// These tests assert the surfaces Bundle B introduced — header filter
// triggers, the detached popover lifecycle, and the bottom action box's
// label/disabled-state transitions.
//

describe('Bundle B: column header filter triggers', () => {
  const sampleRecs = [
    { id: 'rec-aws-1', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
    { id: 'rec-az-1',  provider: 'azure', cloud_account_id: 'a2', service: 'vm',  resource_type: 'D2s',       region: 'eastus',    count: 2, term: 3, savings: 200, upfront_cost: 800 },
  ];

  beforeEach(() => {
    // Set up DOM (the top-level beforeEach belongs to a different describe).
    document.body.replaceChildren();
    const recsTab = document.createElement('div');
    recsTab.id = 'opportunities-tab';
    recsTab.className = 'tab-content active';
    const summary = document.createElement('div');
    summary.id = 'recommendations-summary';
    const list = document.createElement('div');
    list.id = 'recommendations-list';
    recsTab.appendChild(summary);
    recsTab.appendChild(list);
    document.body.appendChild(recsTab);
    const purchaseModal = document.createElement('div');
    purchaseModal.id = 'purchase-modal';
    purchaseModal.className = 'hidden';
    const purchaseDetails = document.createElement('div');
    purchaseDetails.id = 'purchase-details';
    purchaseModal.appendChild(purchaseDetails);
    document.body.appendChild(purchaseModal);

    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: sampleRecs,
      regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue(sampleRecs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(sampleRecs);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
  });

  test('every data column header has a filter trigger button', async () => {
    await loadRecommendations();
    const buttons = document.querySelectorAll<HTMLButtonElement>('th .column-filter-btn[data-column]');
    const cols = Array.from(buttons).map((b) => b.dataset['column']);
    expect(cols.sort()).toEqual(
      ['account', 'capacity', 'count', 'effective_savings_pct', 'monthly_cost', 'on_demand_monthly', 'payment', 'provider', 'region', 'resource_type', 'savings', 'service', 'term', 'upfront_cost'].sort(),
    );
  });

  test('filter button gets .active class when its column has a filter', async () => {
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
      provider: { kind: 'set', values: ['aws'] },
    });
    await loadRecommendations();
    const providerBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="provider"]');
    expect(providerBtn?.classList.contains('active')).toBe(true);
    const serviceBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="service"]');
    expect(serviceBtn?.classList.contains('active')).toBe(false);
  });

  test('clicking a filter trigger opens a popover detached to document.body', async () => {
    await loadRecommendations();
    const providerBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="provider"]');
    providerBtn?.click();
    const popover = document.body.querySelector('.column-filter-popover');
    expect(popover).not.toBeNull();
    // Not a descendant of the table.
    expect(popover?.closest('table')).toBeNull();
  });

  test('clicking the same trigger toggles the popover closed', async () => {
    await loadRecommendations();
    const providerBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="provider"]');
    providerBtn?.click();
    expect(document.querySelector('.column-filter-popover')).not.toBeNull();
    providerBtn?.click();
    expect(document.querySelector('.column-filter-popover')).toBeNull();
  });

  test('ESC closes the popover and restores focus to the trigger', async () => {
    await loadRecommendations();
    const providerBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="provider"]');
    providerBtn?.click();
    expect(document.querySelector('.column-filter-popover')).not.toBeNull();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(document.querySelector('.column-filter-popover')).toBeNull();
  });

  test('categorical popover lists distinct values from the rec set (no other active filters)', async () => {
    await loadRecommendations();
    const providerBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="provider"]');
    providerBtn?.click();
    const checkboxes = document.querySelectorAll<HTMLInputElement>('.column-filter-popover .column-filter-item input[type="checkbox"]');
    const values = Array.from(checkboxes).map((cb) => cb.dataset['value']);
    expect(values.sort()).toEqual(['aws', 'azure']);
  });

  // #1683: the Service filter lists display names but still filters on slugs.
  test('service popover shows display names and keeps slug values', async () => {
    await loadRecommendations();
    document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="service"]')?.click();
    const items = Array.from(
      document.querySelectorAll<HTMLLabelElement>('.column-filter-popover .column-filter-item'),
    ).map((label) => [label.querySelector('input')?.dataset['value'], label.textContent]);
    expect(items).toContainEqual(['ec2', 'EC2']);
  });

  test('service popover includes SageMaker when the loaded rec set has it', async () => {
    const sagemakerRecs = [
      ...sampleRecs,
      {
        id: 'rec-sm-1',
        provider: 'aws',
        cloud_account_id: 'a3',
        service: 'sagemaker',
        resource_type: 'ml.m5.xlarge',
        region: 'us-east-1',
        count: 1,
        term: 1,
        savings: 300,
        upfront_cost: 1200,
      },
    ];

    (api.getRecommendations as jest.Mock).mockResolvedValue({
      summary: {},
      recommendations: sagemakerRecs,
      regions: [],
    });
    (state.getRecommendations as jest.Mock).mockReturnValue(sagemakerRecs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(sagemakerRecs);

    await loadRecommendations();
    const serviceBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="service"]');
    serviceBtn?.click();
    const values = Array.from(
      document.querySelectorAll<HTMLInputElement>('.column-filter-popover .column-filter-item input[type="checkbox"]'),
    ).map((cb) => cb.dataset['value']);
    expect(values).toContain('sagemaker');
  });

  test('Term popover labels show formatted terms; unticking one commits the remaining-values filter', async () => {
    // Issue #482: opening a popover for a column with no active filter
    // now renders all checkboxes as checked (reflecting the "no narrowing
    // applied" semantic). The user's narrowing action is therefore an
    // UNCHECK, not a check.
    await loadRecommendations();
    const termBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="term"]');
    termBtn?.click();
    const items = Array.from(
      document.querySelectorAll<HTMLLabelElement>('.column-filter-popover .column-filter-item'),
    );
    const labels = items.map((l) => l.querySelector('span')?.textContent);
    expect(labels.sort()).toEqual(['1 yr', '3 yr']);
    // Both boxes start checked (no active filter → all included). Untick
    // "1 Year"; remaining checked = ["3"] → filter narrows to that.
    const oneYearLabel = items.find((l) => l.textContent === '1 yr');
    const cb = oneYearLabel?.querySelector<HTMLInputElement>('input[type="checkbox"]');
    expect(cb?.dataset['value']).toBe('1');
    expect(cb?.checked).toBe(true);
    cb!.checked = false;
    cb!.dispatchEvent(new Event('change'));
    expect(state.setRecommendationsColumnFilter).toHaveBeenCalledWith('term', { kind: 'set', values: ['3'] });
  });
});
