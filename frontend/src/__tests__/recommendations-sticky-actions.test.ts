/**
 * Recommendations module tests: the last column header filter tests and the sticky bottom action box.
 * Split from recommendations.test.ts to stay under the 500-line ceiling; the module
 * mocks live in recommendations-harness.ts.
 */
// The harness registers the module mocks, so it must be imported first.
import { mockFactoryDefaults } from './recommendations-harness';
import { loadRecommendations } from '../recommendations';
import { localStorageMock } from './setup';
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

    test('service popover pins active-filter values even when cross-filtering would omit them (alwaysInclude)', async () => {
      // Contradictory filter state: provider=azure (only has vm) + service=ec2 (no azure rows).
      // Opening the service popover: cross-filtering by provider=azure yields only vm.
      // But ec2 is in the service column's own active filter, so alwaysInclude must keep it
      // visible so the user can deselect it without first clearing the provider filter.
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({
        provider: { kind: 'set', values: ['azure'] },
        service:  { kind: 'set', values: ['ec2'] },
      });
      await loadRecommendations();
      const serviceBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="service"]');
      serviceBtn?.click();
      const values = Array.from(
        document.querySelectorAll<HTMLInputElement>('.column-filter-popover .column-filter-item input[type="checkbox"]'),
      ).map((cb) => cb.dataset['value']).sort();
      // vm is visible because provider=azure cross-filter leaves only that azure row.
      expect(values).toContain('vm');
      // ec2 must also be visible even though no azure row has service=ec2:
      // the alwaysInclude set pins values from the column's own active filter.
      expect(values).toContain('ec2');
    });

    test('hostile payload in service value is not interpreted as HTML in the popover (XSS guard)', async () => {
      // A recommendation whose `service` field contains an HTML injection string
      // must not be parsed as markup in the filter popover -- the value goes through
      // textContent and dataset, never innerHTML.
      const hostileService = '<script>alert(1)</script>';
      const recsWithHostile = [
        { id: 'r-aws-ec2', provider: 'aws', cloud_account_id: 'a1', service: hostileService, resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
        { id: 'r-aws-rds', provider: 'aws', cloud_account_id: 'a1', service: 'rds',           resource_type: 'db.t3',    region: 'us-east-1', count: 1, term: 1, savings: 80,  upfront_cost: 400 },
      ];
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: recsWithHostile,
        regions: [],
      });
      (state.getRecommendations as jest.Mock).mockReturnValue(recsWithHostile);
      (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recsWithHostile);
      (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
      await loadRecommendations();
      const serviceBtn = document.querySelector<HTMLButtonElement>('th .column-filter-btn[data-column="service"]');
      serviceBtn?.click();

      // The popover must not contain a parsed <script> element.
      const popover = document.querySelector('.column-filter-popover');
      expect(popover).not.toBeNull();
      expect(popover!.querySelector('script')).toBeNull();

      // The raw payload must appear as a literal data-value attribute, not interpreted HTML.
      const hostileCb = Array.from(
        document.querySelectorAll<HTMLInputElement>('.column-filter-popover .column-filter-item input[type="checkbox"]'),
      ).find((cb) => cb.dataset['value'] === hostileService);
      expect(hostileCb).not.toBeUndefined();

      // The label span renders the payload as literal text, not HTML.
      const labelSpan = hostileCb!.closest('label')?.querySelector('span');
      expect(labelSpan?.textContent).toBe(hostileService);
      expect(labelSpan?.innerHTML).not.toContain('<script>');
    });
  });
});

describe('Bundle B: sticky bottom action box', () => {
  const recs = [
    { id: 'r1', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, savings: 100, upfront_cost: 500 },
    { id: 'r2', provider: 'aws', cloud_account_id: 'a1', service: 'ec2', resource_type: 't3.large', region: 'us-east-1', count: 2, term: 3, savings: 200, upfront_cost: 800 },
  ];

  beforeEach(() => {
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

    jest.clearAllMocks();
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: recs, regions: [] });
    (state.getRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(recs);
    (state.getRecommendationsColumnFilters as jest.Mock).mockReturnValue({});
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set());
  });

  test('bottom box no longer exposes the bulk Payment dropdown (#282)', async () => {
    await loadRecommendations();
    // Issue #282: global Payment dropdown removed — each rec carries its own
    // payment_option from the API fan-out; a global override was misleading.
    expect(document.getElementById('bulk-purchase-payment')).toBeNull();
    // Other controls remain.
    expect(document.getElementById('bulk-purchase-capacity')).not.toBeNull();
    expect(document.getElementById('bulk-purchase-btn')).not.toBeNull();
    expect(document.getElementById('create-plan-btn')).not.toBeNull();
    // Term selector is gone — each rec carries its own term through the API call.
    expect(document.getElementById('bulk-purchase-term')).toBeNull();
  });

  test('button labels reflect the selection (#273)', async () => {
    // No selection → buttons disabled, label is the static form. The
    // disabled-state explanation lives on a sibling hint span linked via
    // aria-describedby (CR follow-up): disabled <button> elements are
    // non-focusable per HTML spec and don't reliably show title tooltips
    // across browsers, so the sibling-element pattern is the
    // discoverable channel for both mouse and keyboard users.
    await loadRecommendations();
    let purchaseBtn = document.getElementById('bulk-purchase-btn') as HTMLButtonElement;
    let planBtn = document.getElementById('create-plan-btn') as HTMLButtonElement;
    let hint = document.getElementById('recommendations-action-disabled-hint') as HTMLSpanElement;
    expect(purchaseBtn.disabled).toBe(true);
    expect(purchaseBtn.textContent).toBe('Purchase');
    expect(purchaseBtn.hasAttribute('title')).toBe(false);
    expect(purchaseBtn.getAttribute('aria-describedby')).toBe('recommendations-action-disabled-hint');
    expect(planBtn.disabled).toBe(true);
    expect(planBtn.textContent).toBe('Create Plan');
    expect(planBtn.hasAttribute('title')).toBe(false);
    expect(planBtn.getAttribute('aria-describedby')).toBe('recommendations-action-disabled-hint');
    expect(hint.hidden).toBe(false);
    expect(hint.textContent).toContain('Select at least one row to enable');
    expect(hint.getAttribute('aria-live')).toBe('polite');

    // Selection populated → buttons enabled, label shows the selection
    // count, hint hidden, aria-describedby cleared, title restored.
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['r1']));
    await loadRecommendations();
    purchaseBtn = document.getElementById('bulk-purchase-btn') as HTMLButtonElement;
    planBtn = document.getElementById('create-plan-btn') as HTMLButtonElement;
    hint = document.getElementById('recommendations-action-disabled-hint') as HTMLSpanElement;
    expect(purchaseBtn.disabled).toBe(false);
    expect(purchaseBtn.textContent).toBe('Purchase 1 selected');
    expect(purchaseBtn.hasAttribute('aria-describedby')).toBe(false);
    expect(purchaseBtn.title).toContain('Buy these reservations now');
    expect(planBtn.disabled).toBe(false);
    expect(planBtn.textContent).toBe('Plan from 1 selected');
    expect(planBtn.hasAttribute('aria-describedby')).toBe(false);
    expect(hint.hidden).toBe(true);
  });

  test('buttons disabled when no rows visible at all', async () => {
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue([]);
    (api.getRecommendations as jest.Mock).mockResolvedValue({ summary: {}, recommendations: [], regions: [] });
    await loadRecommendations();
    const purchaseBtn = document.getElementById('bulk-purchase-btn') as HTMLButtonElement;
    const planBtn = document.getElementById('create-plan-btn') as HTMLButtonElement;
    expect(purchaseBtn.disabled).toBe(true);
    expect(planBtn.disabled).toBe(true);
  });

  test('a mixed selection enables the plan button (#1746)', async () => {
    // The fixture recs differ in term (1 vs 3): they become two plan groups.
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(['r1', 'r2']));
    await loadRecommendations();
    const planBtn = document.getElementById('create-plan-btn') as HTMLButtonElement;
    const hint = document.getElementById('recommendations-action-disabled-hint') as HTMLSpanElement;
    expect(planBtn.disabled).toBe(false);
    expect(planBtn.textContent).toBe('Plan from 2 selected');
    expect(hint.hidden).toBe(true);
  });

  const selectOversize = () => {
    const many = Array.from({ length: 1001 }, (_, i) => ({ id: `many-${i}`, provider: 'aws', service: 'ec2', resource_type: 't3.medium', region: 'us-east-1', count: 1, term: 1, payment: 'all-upfront', savings: 100, upfront_cost: 500 }));
    (state.getVisibleRecommendations as jest.Mock).mockReturnValue(many);
    (state.getSelectedRecommendationIDs as jest.Mock).mockReturnValue(new Set(many.map((r) => r.id)));
  };

  test('plan button disabled with a hint when the selection exceeds the plan size limit (#1746)', async () => {
    selectOversize();
    await loadRecommendations();
    const purchaseBtn = document.getElementById('bulk-purchase-btn') as HTMLButtonElement;
    const planBtn = document.getElementById('create-plan-btn') as HTMLButtonElement;
    const hint = document.getElementById('recommendations-action-disabled-hint') as HTMLSpanElement;
    expect(planBtn.disabled).toBe(true);
    expect(hint.hidden).toBe(false);
    expect(hint.textContent).toContain('at most 1000');
    expect(purchaseBtn.disabled).toBe(false);
    expect(purchaseBtn.textContent).toBe('Purchase 1001 selected');
  });

  test('no size-limit hint when the user cannot create plans (#1746)', async () => {
    const admin = (state.getCurrentUser as jest.Mock)();
    (state.getCurrentUser as jest.Mock).mockReturnValue({ id: 'u-ro', email: 'ro@example.com', groups: [] });
    try {
      selectOversize();
      await loadRecommendations();
      expect((document.getElementById('create-plan-btn') as HTMLButtonElement).hidden).toBe(true);
      expect((document.getElementById('recommendations-action-disabled-hint') as HTMLSpanElement).hidden).toBe(true);
    } finally {
      (state.getCurrentUser as jest.Mock).mockReturnValue(admin);
    }
  });

  test.each([
    [33.33, '100'],
    [50, '50'],
    [150, '100'],
    [0, '100'],
    ['abc', '100'],
  ])('stored bulk-purchase capacity %p seeds the Capacity input as %s (#136)', async (stored, expected) => {
    localStorageMock.getItem.mockImplementation((key: string) =>
      key === 'cudly.recommendations.bulkPurchase.v1' ? JSON.stringify({ capacity: stored }) : null);
    await loadRecommendations();
    expect((document.getElementById('bulk-purchase-capacity') as HTMLInputElement).value).toBe(expected);
  });

  test('Capacity input value persists across re-render (mount-once-then-update)', async () => {
    await loadRecommendations();
    const cap = document.getElementById('bulk-purchase-capacity') as HTMLInputElement;
    cap.value = '50';
    // Trigger an unrelated re-render via sort
    const header = document.querySelector<HTMLTableCellElement>('th[data-sort="service"]');
    header?.click();
    const cap2 = document.getElementById('bulk-purchase-capacity') as HTMLInputElement;
    // Same DOM node (mount-once); value preserved (no rebuild).
    expect(cap2).toBe(cap);
    expect(cap2.value).toBe('50');
  });
});
