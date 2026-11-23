/**
 * Recommendations module tests: the last column header filter tests and the sticky bottom action box.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults } from './recommendations-harness';
import { loadRecommendations } from '../recommendations';
import * as api from '../api';
import * as state from '../state';

beforeEach(() => {
  for (const [fn, factoryDefault] of mockFactoryDefaults) {
    fn.mockReset();
    if (factoryDefault) fn.mockImplementation(factoryDefault);
  }
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

  // Issue #1684: Opportunities group rows, payback card and action-bar wording.
  describe('Issue #1684: Opportunities group rows, payback and wording', () => {
    const base = { provider: 'aws', cloud_account_id: 'a1684', service: 'rds', resource_type: 'db.t4g.micro', engine: 'postgresql', region: 'eu-west-1', count: 1 };
    const variants = [
      { ...base, id: 'v1684-nu', term: 1, payment: 'no-upfront', savings: 10, upfront_cost: 0 },
      { ...base, id: 'v1684-au', term: 1, payment: 'all-upfront', savings: 20, upfront_cost: 120 },
    ];

    async function load(recs: Array<Record<string, unknown>>, selected: string[] = []): Promise<void> {
      (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
      (state.getRecommendations as jest.Mock).mockReturnValue(recs);
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
      (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(selected));
      await loadRecommendations();
    }

    const paybackText = (): string =>
      document.querySelector('#recommendations-summary [data-kpi="payback"]')?.textContent ?? '';

    test('group summary skips blank resource type and region', async () => {
      const sp = { provider: 'azure', cloud_account_id: 'sub1684', service: 'savingsplans', resource_type: '', region: '', count: 1, upfront_cost: 0 };
      await load([
        { ...sp, id: 'sp1684-1', term: 1, payment: 'monthly', savings: 30 },
        { ...sp, id: 'sp1684-3', term: 3, payment: 'monthly', savings: 50 },
      ]);
      const identity = document.querySelector('tr.rec-cell-summary-row .rec-cell-identity')?.textContent ?? '';
      expect(identity.trim()).toBe('2 variants');
    });

    test('payback card never shows a 0.0-month lower bound', async () => {
      await load(variants);
      // no-upfront has no payback; all-upfront pays back in 120 / 20 = 6 months.
      expect(paybackText()).toContain('Up to 6.0 months');
      expect(paybackText()).not.toContain('0.0');
    });

    test('payback card shows the no-value dash when nothing is paid upfront', async () => {
      await load(variants, ['v1684-nu']);
      expect(paybackText()).toContain('—');
      expect(paybackText()).not.toContain('months');
    });

    test('action bar talks about rows, not cells', async () => {
      await load(variants);
      expect(document.getElementById('recommendations-action-summary')?.textContent).toBe('(Select rows to act on)');
      await load(variants, ['v1684-au']);
      expect(document.getElementById('recommendations-action-summary')?.textContent).toMatch(/across 1 row\)$/);
    });

    // Issue #1705: the Savings Plans child rows use the same nesting rules.
    test('Savings Plans children: a single plan keeps its type, variants of a multi-variant plan stay blank', async () => {
      const sp = { provider: 'aws', cloud_account_id: 'a1705', region: 'us-east-1', count: 1, upfront_cost: 0 };
      await load([
        { ...sp, id: 'sp1705-compute', service: 'savings-plans-compute', resource_type: 'Compute SP', term: 1, payment: 'no-upfront', savings: 40 },
        { ...sp, id: 'sp1705-ec2-1', service: 'savings-plans-ec2instance', resource_type: '', term: 1, payment: 'no-upfront', savings: 30 },
        { ...sp, id: 'sp1705-ec2-3', service: 'savings-plans-ec2instance', resource_type: '', term: 3, payment: 'no-upfront', savings: 50 },
      ]);
      const spChevron = document.querySelector<HTMLButtonElement>('.rec-sp-group-chevron');
      if (spChevron?.getAttribute('aria-expanded') !== 'true') spChevron!.click();

      // The single-plan child is not a variant: it keeps its resource type.
      const single = document.querySelector<HTMLTableRowElement>('tr.recommendation-row[data-rec-id="sp1705-compute"]')!;
      expect(single.textContent).toContain('Compute SP');

      // The multi-variant child's summary skips its blank resource type.
      const childSummary = document.querySelector<HTMLTableRowElement>('tr.rec-sp-group-child')!;
      expect(childSummary.querySelector('.rec-cell-identity')?.textContent?.trim()).toBe('us-east-1 \u2014 2 variants');

      const childChevron = childSummary.querySelector<HTMLButtonElement>('.rec-cell-chevron')!;
      if (childChevron.getAttribute('aria-expanded') !== 'true') childChevron.click();
      const variant = document.querySelector<HTMLTableRowElement>('tr.rec-variant-row[data-rec-id="sp1705-ec2-3"]')!;
      expect(Array.from(variant.cells).slice(1, 5).map((td) => td.textContent?.trim())).toEqual(['', '', '', '']);
      expect(variant.textContent).not.toContain('us-east-1');
    });

    test('payback card treats a lower bound that rounds to 0.0 as no payback', async () => {
      const tiny = { ...base, resource_type: 'db.r6g.large' };
      // Best end: 1 / 100 = 0.01 months, which rounds to "0.0"; worst end: 600 / 100 = 6 months.
      await load([
        { ...tiny, id: 'v1705-tiny', term: 1, payment: 'partial-upfront', savings: 100, upfront_cost: 1 },
        { ...tiny, id: 'v1705-full', term: 1, payment: 'all-upfront', savings: 100, upfront_cost: 600 },
      ]);
      expect(paybackText()).toContain('Up to 6.0 months');
      expect(paybackText()).not.toContain('0.0 \u2013');
    });

    test('payback card treats a negative lower bound as no payback', async () => {
      const credit = { ...base, resource_type: 'db.r6g.xlarge' };
      // A negative upfront (a credit) gives -1 / 100 = -0.01 months, which rounds to "-0.0".
      await load([
        { ...credit, id: 'v1705-credit', term: 1, payment: 'partial-upfront', savings: 100, upfront_cost: -1 },
        { ...credit, id: 'v1705-paid', term: 1, payment: 'all-upfront', savings: 100, upfront_cost: 600 },
      ]);
      expect(paybackText()).toContain('Up to 6.0 months');
      expect(paybackText()).not.toContain('-0.0');
    });
  });

  // Issue #164: cross-column-aware (cascading) distinct values.
  // Each column popover must show only values from rows that pass all
  // OTHER active filters -- values that produce non-empty results when
  // combined with the existing selection.
  describe('Issue #164: cross-column-aware categorical filter distinct values', () => {
    const multiProviderRecs = [
      { id: 'r-aws-ec2',  provider: 'aws',   cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
      { id: 'r-aws-rds',  provider: 'aws',   cloud_account_id: 'a1', service: 'rds', resource_type: 'db.t3',    region: 'us-east-1', count: 1, term: 1, savings: 80,  upfront_cost: 400 },
      { id: 'r-az-vm',   provider: 'azure',  cloud_account_id: 'a2', service: 'vm',  resource_type: 'D2s',      region: 'eastus',    count: 2, term: 3, savings: 200, upfront_cost: 800 },
    ];

    beforeEach(() => {
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: multiProviderRecs,
        regions: [],
      });
      (state.getRecommendations as jest.Mock).mockReturnValue(multiProviderRecs);
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(multiProviderRecs);
    });

    test('service popover shows only AWS services when provider=aws filter is active', async () => {
      // Provider=aws is set; opening the service popover must list only
      // services from aws rows (ec2, rds), not the azure service (vm).
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
        provider: { kind: 'set', values: ['aws'] },
      });
      await loadRecommendations();
      const serviceBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="service"]');
      serviceBtn?.click();
      const values = Array.from(
        document.querySelectorAll<HTMLInputElement>('.column-filter-popover .column-filter-item input[type="checkbox"]'),
      ).map((cb) => cb.dataset['value']).sort();
      expect(values).toEqual(['ec2', 'rds']);
      expect(values).not.toContain('vm');
    });

    test('provider popover shows all providers when opening the provider column (own filter excluded)', async () => {
      // Provider=aws is active, but opening the provider popover must still
      // show all 3 providers because the column's own filter is excluded from
      // the cross-filter narrowing -- allowing the user to switch providers.
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
        provider: { kind: 'set', values: ['aws'] },
      });
      await loadRecommendations();
      const providerBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="provider"]');
      providerBtn?.click();
      const values = Array.from(
        document.querySelectorAll<HTMLInputElement>('.column-filter-popover .column-filter-item input[type="checkbox"]'),
      ).map((cb) => cb.dataset['value']).sort();
      // All providers must be visible so the user can switch from aws to azure.
      expect(values).toEqual(['aws', 'azure']);
    });
  });
});
