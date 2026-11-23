/**
 * RI Exchange module tests — openExchangeModal and fillQuoteFromRI
 */

import { _accountListeners, _providerListeners, createModal } from './riexchange-harness';
import {
  loadReshapeRecommendations,
  loadRIExchange,
  openExchangeModal,
} from '../riexchange';
import * as api from '../api';
import { flushPromises } from './flush-promises';

describe('openExchangeModal', () => {
  let modal: HTMLDivElement;

  beforeEach(() => {
    modal = createModal();
    // Other describes call jest.resetAllMocks(), which wipes the default
    // listTargetOfferings resolution; restore it so test order does not matter.
    (api.listTargetOfferings as jest.Mock).mockResolvedValue([]);
  });

  afterEach(() => {
    document.body.innerHTML = '';
  });

  it('shows an empty state, not a bare placeholder, when the offerings list is empty (issue #1696)', async () => {
    (api.listTargetOfferings as jest.Mock).mockResolvedValueOnce([]);
    openExchangeModal('ri-abc', 2);
    await flushPromises();

    const picker = modal.querySelector<HTMLSelectElement>('.modal-exchange-target-select');
    expect(picker?.options[0]?.textContent).toBe('No compatible offerings found');
    const empty = modal.querySelector<HTMLElement>('.exchange-offerings-empty');
    expect(empty?.classList.contains('hidden')).toBe(false);
    expect(empty?.textContent).toContain('AWS returned no convertible offerings');
    expect(modal.querySelector<HTMLElement>('.exchange-offerings-error')?.classList.contains('hidden')).toBe(true);
  });

  it('hides the empty state when CE recommendations remain selectable (issue #1696)', async () => {
    (api.listTargetOfferings as jest.Mock).mockResolvedValueOnce([]);
    openExchangeModal('ri-abc', 2, undefined, [
      { instance_type: 'm5.large', offering_id: '4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91', effective_monthly_cost: 42.5 },
    ]);
    await flushPromises();
    expect(modal.querySelector<HTMLElement>('.exchange-offerings-empty')?.classList.contains('hidden')).toBe(true);
    expect(modal.querySelector<HTMLSelectElement>('.modal-exchange-target-select')?.options[0]?.textContent).toBe('Select a target offering');
  });

  it('shows the error row and hides the empty state on failure, and tracks Retry transitions (issue #1696)', async () => {
    const mock = api.listTargetOfferings as jest.Mock;
    mock.mockRejectedValueOnce(new Error('throttled'));
    openExchangeModal('ri-abc', 2);
    await flushPromises();
    const empty = modal.querySelector<HTMLElement>('.exchange-offerings-empty');
    const err = modal.querySelector<HTMLElement>('.exchange-offerings-error');
    const retry = (): void => Array.from(err?.querySelectorAll('button') ?? []).find((b) => b.textContent === 'Retry')?.click();
    expect(err?.classList.contains('hidden')).toBe(false);
    expect(empty?.classList.contains('hidden')).toBe(true);

    mock.mockResolvedValueOnce([]);
    retry();
    await flushPromises();
    expect(err?.classList.contains('hidden')).toBe(true);
    expect(empty?.classList.contains('hidden')).toBe(false);
    expect(empty?.textContent).toContain('AWS returned no convertible offerings');

    mock.mockRejectedValueOnce(new Error('boom'));
    modal.querySelector<HTMLElement>('.exchange-offerings-error button')?.click();
    await flushPromises();
    expect(err?.classList.contains('hidden')).toBe(false);
    expect(empty?.classList.contains('hidden')).toBe(true);
    expect(empty?.textContent).toBe('');
  });

  it('keeps CE recommendations selectable when the AWS offerings fail to load', async () => {
    (api.listTargetOfferings as jest.Mock).mockRejectedValueOnce(new Error('throttled'));
    openExchangeModal('ri-abc', 2, undefined, [
      { instance_type: 'm5.large', offering_id: '4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91', effective_monthly_cost: 42.5 },
    ]);
    await flushPromises();
    const picker = modal.querySelector<HTMLSelectElement>('.modal-exchange-target-select');
    expect(picker?.options[0]?.textContent).toBe('AWS offerings unavailable -- pick a recommendation or retry');
    expect(Array.from(picker?.options ?? []).map((o) => o.value)).toContain('4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91');
  });

  it('selecting a picker option drives the hidden offering input with a UUID', () => {
    openExchangeModal('ri-abc', 2, undefined, [
      { instance_type: 'm5.large', offering_id: '4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91', effective_monthly_cost: 42.5 },
    ]);
    const picker = modal.querySelector<HTMLSelectElement>('.modal-exchange-target-select');
    const hiddenInput = modal.querySelector<HTMLInputElement>('.modal-exchange-target');
    if (picker) {
      picker.value = '4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91';
      picker.dispatchEvent(new Event('change'));
    }
    expect(hiddenInput?.value).toBe('4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91');
  });

  it('rejects submission when the hidden offering input contains a non-UUID value', async () => {
    const mockGetQuote = api.getExchangeQuote as jest.Mock;
    openExchangeModal('ri-abc', 1);
    // Force a non-UUID value into the hidden input (simulates a bypass attempt).
    const hiddenInput = modal.querySelector<HTMLInputElement>('.modal-exchange-target');
    if (hiddenInput) hiddenInput.value = 't3.medium';

    const quoteBtn = Array.from(modal.querySelectorAll('button')).find((b) => b.textContent === 'Get Quote');
    quoteBtn?.click();
    await new Promise((resolve) => setTimeout(resolve, 0));

    // The quote API must NOT have been called -- the frontend guard fires first.
    expect(mockGetQuote).not.toHaveBeenCalled();
    // The result container must show an error mentioning the invalid value.
    const result = modal.querySelector('#modal-exchange-result');
    expect(result?.textContent).toContain('t3.medium');
  });

  describe('exchange result announcement and receipt (issue #738)', () => {
    const offeringUUID = '4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91';
    const flush = (): Promise<void> => new Promise((resolve) => setTimeout(resolve, 0));
    const button = (label: string): HTMLButtonElement | undefined =>
      Array.from(modal.querySelectorAll('button')).find((b) => b.textContent === label);

    async function executeExchange(): Promise<void> {
      (api.getExchangeQuote as jest.Mock).mockResolvedValueOnce({
        IsValidExchange: true, ValidationFailureReason: '', CurrencyCode: 'USD', PaymentDueRaw: '12.50',
        SourceHourlyPriceRaw: '', SourceRemainingUpfrontRaw: '', SourceRemainingTotalRaw: '',
        TargetHourlyPriceRaw: '', TargetRemainingUpfrontRaw: '', TargetRemainingTotalRaw: '', Region: 'us-west-2',
      });
      (api.executeExchange as jest.Mock).mockResolvedValueOnce({ exchange_id: 'exch-42', quote: {} });
      openExchangeModal('ri-abc', 1, 'm5.large', [
        { instance_type: 'm5.large', offering_id: offeringUUID, effective_monthly_cost: 42.5 },
      ]);
      button('Get Quote')?.click();
      await flush();
      button('Execute Exchange')?.click();
      document.querySelector<HTMLButtonElement>('.modal-confirm-actions .btn-destructive')?.click();
      await flush();
      await flush();
    }

    it('makes the result area a polite live region and announces errors as alerts', async () => {
      (api.getExchangeQuote as jest.Mock).mockRejectedValueOnce(new Error('throttled'));
      openExchangeModal('ri-abc', 1, 'm5.large', [
        { instance_type: 'm5.large', offering_id: offeringUUID, effective_monthly_cost: 42.5 },
      ]);
      const result = modal.querySelector('#modal-exchange-result');
      expect(result?.getAttribute('role')).toBe('status');
      expect(result?.getAttribute('aria-live')).toBe('polite');
      button('Get Quote')?.click();
      await flush();
      const alert = result?.querySelector('[role="alert"]');
      expect(alert?.textContent).toBe('Quote failed: throttled');
    });

    it('keeps the exchange ID on screen until the user closes the modal', async () => {
      // Fake timers so every timer the exchange schedules (the old bug was a
      // 2s auto-close) runs to completion without a real wall-clock wait.
      // runAllTimersAsync also fires executeExchange's zero-delay flushes
      // and lets the promise callbacks between them settle.
      jest.useFakeTimers();
      try {
        const done = executeExchange();
        await jest.runAllTimersAsync();
        await done;
        await jest.runAllTimersAsync();
      } finally {
        jest.useRealTimers();
      }
      expect(modal.classList.contains('hidden')).toBe(false);
      expect(modal.querySelector('#modal-exchange-result')?.textContent).toBe('Exchange completed. ID: exch-42');
      // The form is frozen so an edit cannot wipe the receipt, and only Close remains.
      expect(button('Get Quote')?.classList.contains('hidden')).toBe(true);
      expect(modal.querySelector<HTMLSelectElement>('.modal-exchange-target-select')?.disabled).toBe(true);
      const close = button('Close');
      expect(document.activeElement).toBe(close);
      close?.click();
      expect(modal.classList.contains('hidden')).toBe(true);
    });

    it('shows a success toast with the exchange ID and a link to the history', async () => {
      const section = document.createElement('section');
      const list = document.createElement('div');
      list.id = 'ri-exchange-history-list';
      section.appendChild(list);
      section.scrollIntoView = jest.fn();
      document.body.appendChild(section);

      await executeExchange();
      const toast = document.querySelector('.toast--success');
      expect(toast?.querySelector('.toast-message')?.textContent).toBe('RI exchange completed. Exchange ID: exch-42');
      Array.from(toast?.querySelectorAll('button') ?? []).find((b) => b.textContent === 'View history')?.click();
      expect(modal.classList.contains('hidden')).toBe(true);
      expect(section.scrollIntoView).toHaveBeenCalled();
      expect(document.activeElement).toBe(list);
    });
  });
});

// Reshape-table rendering tests. Kept in a separate describe block so
// the shared modal-element fixture doesn't leak in.
describe('reshape recommendations table', () => {
  let tableContainer: HTMLDivElement;

  beforeEach(() => {
    tableContainer = document.createElement('div');
    tableContainer.id = 'ri-exchange-recommendations-list';
    document.body.appendChild(tableContainer);
    // Re-apply the column-filter mock impls after a prior test's
    // jest.resetAllMocks(); without this the renderers blow up on
    // Object.entries(undefined) when reading filter state.
    const stateMod = jest.requireMock('../state') as {
      getRiExchangeColumnFilters: jest.Mock;
      getActiveRiColumnFilters: jest.Mock;
    };
    stateMod.getRiExchangeColumnFilters.mockReturnValue({});
    stateMod.getActiveRiColumnFilters.mockReturnValue({});
  });

  afterEach(() => {
    const body = document.body;
    while (body.firstChild) body.removeChild(body.firstChild);
    jest.resetAllMocks();
  });

  // Baseline rec with the reshape fields the renderer expects. Tests
  // extend this with alternative_targets when needed.
  const baseRec = {
    source_ri_id: 'ri-abc',
    source_instance_type: 'm5.xlarge',
    source_count: 1,
    target_instance_type: 'm5.large',
    target_count: 2,
    utilization_percent: 50,
    normalized_used: 4,
    normalized_purchased: 8,
    reason: 'underutilized',
  };

  it('renders the Alternatives column with cost chips when the rec carries alternative_targets', async () => {
    const mockGet = api.getReshapeRecommendations as jest.Mock;
    mockGet.mockResolvedValueOnce({
      recommendations: [
        {
          ...baseRec,
          alternative_targets: [
            { instance_type: 'm7g.large', offering_id: 'off-m7g', effective_monthly_cost: 30.0 },
            { instance_type: 'm6i.large', offering_id: 'off-m6i', effective_monthly_cost: 35.0 },
          ],
        },
      ],
      recs_staleness: '',
      recs_collected_at: null,
    });

    await loadReshapeRecommendations();

    const chips = tableContainer.querySelectorAll<HTMLSpanElement>('.cost-chip');
    expect(chips.length).toBe(2);
    // Chips include the instance type + formatted cost; the backend
    // emits them in ascending-cost order (commit 7378ceaa5 sorts
    // fillAlternativesFromOfferings output by EffectiveMonthlyCost)
    // so the UI shows cheapest first.
    expect(chips[0]?.textContent).toContain('m7g.large');
    expect(chips[0]?.textContent).toContain('$30.00/mo');
    expect(chips[1]?.textContent).toContain('m6i.large');
    expect(chips[1]?.textContent).toContain('$35.00/mo');
  });

  it('renders an unknown utilization as a neutral em-dash instead of throwing (#136)', async () => {
    const mockGet = api.getReshapeRecommendations as jest.Mock;
    const { utilization_percent: _omit, ...withoutUtil } = baseRec;
    mockGet.mockResolvedValueOnce({
      recommendations: [withoutUtil, { ...baseRec, source_ri_id: 'ri-def', utilization_percent: 97 }],
      recs_staleness: '',
      recs_collected_at: null,
    });

    await loadReshapeRecommendations();

    const rows = tableContainer.querySelectorAll<HTMLTableRowElement>('tbody tr');
    expect(rows).toHaveLength(2);
    const unknown = rows[0]!.querySelectorAll('td')[4]!;
    expect(unknown.textContent).toBe('—');
    expect(unknown.className).toBe('');
    const known = rows[1]!.querySelectorAll('td')[4]!;
    expect(known.textContent).toBe('97.0%');
    expect(known.className).toBe('util-green');
  });

  it('renders an em-dash in the Alternatives column when the rec has no alternative_targets', async () => {
    const mockGet = api.getReshapeRecommendations as jest.Mock;
    mockGet.mockResolvedValueOnce({ recommendations: [{ ...baseRec }], recs_staleness: '', recs_collected_at: null }); // no alternative_targets

    await loadReshapeRecommendations();

    // The Alternatives <td> is the 4th column (Source RI, Current,
    // Suggested, Alternatives, Utilization, Normalized Units, Reason,
    // Actions). Select the single data row and pull the 4th cell.
    const row = tableContainer.querySelector<HTMLTableRowElement>('tbody tr');
    expect(row).not.toBeNull();
    const cells = row?.querySelectorAll<HTMLTableCellElement>('td');
    expect(cells?.[3]?.textContent).toBe('—');
    expect(tableContainer.querySelectorAll('.cost-chip').length).toBe(0);
  });
});

// Empty-state copy varies with RI-fleet presence. Prior to commit P4 the
// "All convertible RIs are well-utilized" copy ran on every zero-rec state
// including a totally empty fleet — a truthy claim about an empty set.
describe('reshape recommendations empty state', () => {
  let instancesEl: HTMLDivElement;
  let recsEl: HTMLDivElement;
  let historyEl: HTMLDivElement;

  const sampleRI = {
    reserved_instance_id: 'ri-1',
    instance_type: 'm5.large',
    availability_zone: 'us-east-1a',
    instance_count: 1,
    start: '2026-01-01T00:00:00Z',
    end: '2027-01-01T00:00:00Z',
    offering_type: 'Convertible',
    fixed_price: 0,
    usage_price: 0,
    state: 'active',
    normalization_factor: 4,
  };

  const utilFor = (id: string) => ({
    reserved_instance_id: id,
    utilization_percent: 99,
    purchased_hours: 720,
    total_actual_hours: 713,
    unused_hours: 7,
  });

  const utilizationCell = (): string =>
    instancesEl.querySelector('tbody tr td:nth-child(7)')?.textContent?.trim() ?? '';

  beforeEach(() => {
    instancesEl = document.createElement('div');
    instancesEl.id = 'ri-exchange-instances-list';
    recsEl = document.createElement('div');
    recsEl.id = 'ri-exchange-recommendations-list';
    historyEl = document.createElement('div');
    historyEl.id = 'ri-exchange-history-list';
    document.body.append(instancesEl, recsEl, historyEl);

    (api.getRIUtilization as jest.Mock).mockResolvedValue([]);
    (api.getRIExchangeHistoryResponse as jest.Mock).mockResolvedValue({ records: [], direct_executions: [] });
    (api.getReshapeRecommendations as jest.Mock).mockResolvedValue({ recommendations: [], recs_staleness: '', recs_collected_at: null });
    // resetAllMocks() in afterEach wipes the state mock implementations;
    // loadRIExchange reads the chips to scope the request (issue #871), so
    // restore the AWS/all-accounts default here. Also restore the column-
    // filter slices so renderRIsTable / renderRecommendations don't crash
    // on Object.entries(undefined) for non-empty tables.
    const stateMod = jest.requireMock('../state') as {
      getCurrentProvider: jest.Mock;
      getCurrentAccountIDs: jest.Mock;
      getRiExchangeColumnFilters: jest.Mock;
      getActiveRiColumnFilters: jest.Mock;
    };
    stateMod.getCurrentProvider.mockReturnValue('aws');
    stateMod.getCurrentAccountIDs.mockReturnValue([]);
    stateMod.getRiExchangeColumnFilters.mockReturnValue({});
    stateMod.getActiveRiColumnFilters.mockReturnValue({});
  });

  afterEach(() => {
    const body = document.body;
    while (body.firstChild) body.removeChild(body.firstChild);
    jest.resetAllMocks();
  });

  it('advises the user their accounts have no convertible RIs yet when both lists are empty', async () => {
    (api.listConvertibleRIs as jest.Mock).mockResolvedValue([]);

    await loadRIExchange();

    expect(recsEl.textContent).toContain('none are registered yet');
    expect(recsEl.textContent).not.toContain('well-utilized');
    expect(recsEl.textContent).not.toContain('utilization threshold');
  });

  it('claims all RIs meet the threshold only when RIs actually exist', async () => {
    (api.listConvertibleRIs as jest.Mock).mockResolvedValue([sampleRI]);
    (api.getRIUtilization as jest.Mock).mockResolvedValue([utilFor('ri-1')]);

    await loadRIExchange();
    await flushPromises();

    expect(recsEl.textContent).toContain('meet your utilization threshold');
    expect(recsEl.textContent).toContain('1 convertible RI ');
    expect(recsEl.textContent).not.toContain('none are registered');
  });

  // Issue #1135: the three loads race. An empty reshape response that lands
  // before the RI list must not keep the previous scope's count.
  it('re-renders the empty-state count when recommendations resolve before the RI list', async () => {
    (api.getRIUtilization as jest.Mock).mockResolvedValue(['ri-1', 'ri-2', 'ri-3'].map(utilFor));
    (api.listConvertibleRIs as jest.Mock).mockResolvedValueOnce([sampleRI]);
    await loadRIExchange();
    await flushPromises();
    expect(recsEl.textContent).toContain('All 1 convertible RI ');

    let releaseRIs: (v: unknown) => void = () => undefined;
    (api.listConvertibleRIs as jest.Mock).mockReturnValueOnce(new Promise(r => { releaseRIs = r; }));
    const reload = loadRIExchange();
    await new Promise(r => setTimeout(r, 0));
    // Recommendations are in, RIs are not: no stale "All 1" claim.
    expect(api.getReshapeRecommendations).toHaveBeenCalledTimes(2);
    expect(recsEl.textContent).not.toContain('All 1 convertible RI');
    expect(recsEl.textContent).toContain('No reshape recommendations right now.');

    releaseRIs([sampleRI, { ...sampleRI, reserved_instance_id: 'ri-2' }, { ...sampleRI, reserved_instance_id: 'ri-3' }]);
    await reload;
    await flushPromises();
    expect(recsEl.textContent).toContain('All 3 convertible RIs meet your utilization threshold');
  });

  it('does not quote a count from a failed RI load', async () => {
    (api.listConvertibleRIs as jest.Mock).mockResolvedValueOnce([sampleRI]);
    await loadRIExchange();
    (api.listConvertibleRIs as jest.Mock).mockRejectedValueOnce(new Error('boom'));
    await loadRIExchange();
    expect(recsEl.textContent).toContain('No reshape recommendations right now.');
    expect(recsEl.textContent).not.toContain('convertible RI');
  });

  it('keeps the error from a failed reshape load after the RI list arrives', async () => {
    let releaseRIs: (v: unknown) => void = () => undefined;
    (api.listConvertibleRIs as jest.Mock).mockReturnValueOnce(new Promise(r => { releaseRIs = r; }));
    (api.getReshapeRecommendations as jest.Mock).mockRejectedValueOnce(new Error('ce down'));
    const reload = loadRIExchange();
    await new Promise(r => setTimeout(r, 0));
    releaseRIs([sampleRI]);
    await reload;
    expect(recsEl.querySelector('.error')?.textContent).toContain('Failed to load recommendations: ce down');
    expect(recsEl.textContent).not.toContain('utilization threshold');
  });

  // Issue #1687: an RI with no utilization data was shown as "..." forever
  // while the reshape panel claimed it met the utilization threshold.
  describe('unknown utilization (issue #1687)', () => {
    it('shows "No data" and makes no threshold claim when utilization has no entry for the RI', async () => {
      (api.listConvertibleRIs as jest.Mock).mockResolvedValue([sampleRI]);
      (api.getRIUtilization as jest.Mock).mockResolvedValue([]);

      await loadRIExchange();
      await flushPromises();

      expect(utilizationCell()).toBe('No data');
      expect(instancesEl.querySelector('.loading-inline')).toBeNull();
      expect(recsEl.textContent).not.toContain('meet your utilization threshold');
      expect(recsEl.textContent).toContain('Utilization data is unavailable for 1 of 1 convertible RI');
    });

    it('shows "No data" and makes no threshold claim when the utilization load fails', async () => {
      (api.listConvertibleRIs as jest.Mock).mockResolvedValue([sampleRI]);
      (api.getRIUtilization as jest.Mock).mockRejectedValue(new Error('ce down'));
      jest.spyOn(console, 'error').mockImplementation(() => undefined);

      await loadRIExchange();
      await flushPromises();

      expect(utilizationCell()).toBe('No data');
      expect(recsEl.textContent).not.toContain('meet your utilization threshold');
      expect(recsEl.textContent).toContain('Utilization data is unavailable for 1 of 1 convertible RI');
    });

    it('counts only the RIs without utilization when some have it', async () => {
      const ri2 = { ...sampleRI, reserved_instance_id: 'ri-2' };
      (api.listConvertibleRIs as jest.Mock).mockResolvedValue([sampleRI, ri2]);
      (api.getRIUtilization as jest.Mock).mockResolvedValue([utilFor('ri-1')]);

      await loadRIExchange();
      await flushPromises();

      expect(recsEl.textContent).toContain('Utilization data is unavailable for 1 of 2 convertible RIs');
      expect(recsEl.textContent).not.toContain('meet your utilization threshold');
    });

    it('keeps the loading placeholder and makes no claim while utilization is still loading', async () => {
      // A fresh id: module state keeps the last known utilization per RI.
      (api.listConvertibleRIs as jest.Mock).mockResolvedValue([{ ...sampleRI, reserved_instance_id: 'ri-pending' }]);
      (api.getRIUtilization as jest.Mock).mockReturnValue(new Promise(() => undefined));

      await loadRIExchange();
      await flushPromises();

      expect(instancesEl.querySelector('.loading-inline')).not.toBeNull();
      expect(recsEl.textContent).not.toContain('meet your utilization threshold');
      expect(recsEl.textContent).not.toContain('unavailable');
      expect(recsEl.textContent).toContain('No reshape recommendations right now.');
    });

    it('keeps a reshape load error when utilization arrives afterwards', async () => {
      (api.listConvertibleRIs as jest.Mock).mockResolvedValue([sampleRI]);
      (api.getReshapeRecommendations as jest.Mock).mockRejectedValueOnce(new Error('ce down'));
      (api.getRIUtilization as jest.Mock).mockResolvedValue([utilFor('ri-1')]);

      await loadRIExchange();
      await flushPromises();

      expect(recsEl.querySelector('.error')?.textContent).toContain('Failed to load recommendations: ce down');
    });

    // Slow Cost Explorer: utilization lands after loadRIExchange has already
    // rendered the neutral reshape copy.
    const deferredUtilization = () => {
      let resolve: (v: unknown) => void = () => undefined;
      let reject: (e: unknown) => void = () => undefined;
      (api.getRIUtilization as jest.Mock).mockReturnValueOnce(new Promise((res, rej) => { resolve = res; reject = rej; }));
      return { resolve: (v: unknown) => resolve(v), reject: (e: unknown) => reject(e) };
    };

    it('re-renders the reshape copy when utilization lands after the page loaded', async () => {
      (api.listConvertibleRIs as jest.Mock).mockResolvedValue([{ ...sampleRI, reserved_instance_id: 'ri-late' }]);
      const util = deferredUtilization();

      await loadRIExchange();
      expect(recsEl.textContent).toContain('No reshape recommendations right now.');

      util.resolve([utilFor('ri-late')]);
      await flushPromises();
      expect(recsEl.textContent).toContain('All 1 convertible RI meet your utilization threshold');
    });

    it('re-renders the reshape copy when a late utilization load fails', async () => {
      (api.listConvertibleRIs as jest.Mock).mockResolvedValue([{ ...sampleRI, reserved_instance_id: 'ri-late-fail' }]);
      jest.spyOn(console, 'error').mockImplementation(() => undefined);
      const util = deferredUtilization();

      await loadRIExchange();
      util.reject(new Error('ce timeout'));
      await flushPromises();

      expect(recsEl.textContent).toContain('Utilization data is unavailable for 1 of 1 convertible RI');
      expect(utilizationCell()).toBe('No data');
      expect(instancesEl.querySelector('td span[title]')?.getAttribute('title')).toBe('Utilization could not be loaded');
    });

    it('ignores a superseded utilization failure', async () => {
      (api.listConvertibleRIs as jest.Mock).mockResolvedValue([{ ...sampleRI, reserved_instance_id: 'ri-superseded' }]);
      jest.spyOn(console, 'error').mockImplementation(() => undefined);
      const first = deferredUtilization();
      await loadRIExchange();
      deferredUtilization(); // the second load's utilization stays pending
      await loadRIExchange();

      first.reject(new Error('stale ce timeout'));
      await flushPromises();

      expect(instancesEl.querySelector('.loading-inline')).not.toBeNull();
      expect(recsEl.textContent).not.toContain('unavailable');
    });

    it.each([
      ['fails', (u: ReturnType<typeof deferredUtilization>) => u.reject(new Error('ce timeout'))],
      ['succeeds', (u: ReturnType<typeof deferredUtilization>) => u.resolve([utilFor('ri-switch')])],
    ])('a late AWS utilization load that %s leaves the Azure view alone', async (_label, settle) => {
      const stateMod = jest.requireMock('../state') as { getCurrentProvider: jest.Mock };
      (api.listConvertibleRIs as jest.Mock).mockResolvedValue([{ ...sampleRI, reserved_instance_id: 'ri-switch' }]);
      jest.spyOn(console, 'error').mockImplementation(() => undefined);
      const util = deferredUtilization();
      await loadRIExchange();

      stateMod.getCurrentProvider.mockReturnValue('azure');
      (api.listExchangeableAzureRIs as jest.Mock).mockResolvedValue([
        { reservation_order_id: 'o1', reservation_id: 'r1', sku: 'Standard_D2s_v3', quantity: 2, region: 'eastus', term: 'P1Y', instance_flexibility: 'On', display_name: 'web-rsv' },
      ]);
      await loadRIExchange();
      settle(util);
      await flushPromises();

      expect(instancesEl.textContent).toContain('web-rsv');
      expect(recsEl.textContent).toContain('not available for Azure');
    });

    it('a late failed AWS utilization load leaves the GCP view alone', async () => {
      const stateMod = jest.requireMock('../state') as { getCurrentProvider: jest.Mock };
      (api.listConvertibleRIs as jest.Mock).mockResolvedValue([{ ...sampleRI, reserved_instance_id: 'ri-gcp' }]);
      jest.spyOn(console, 'error').mockImplementation(() => undefined);
      const util = deferredUtilization();
      await loadRIExchange();

      stateMod.getCurrentProvider.mockReturnValue('gcp');
      await loadRIExchange();
      util.reject(new Error('ce timeout'));
      await flushPromises();

      expect(instancesEl.textContent).toContain("isn't available for GCP");
      expect(recsEl.textContent).toContain("isn't available for GCP");
    });

    it('drops the previous values when a same-scope reload fails to load utilization', async () => {
      (api.listConvertibleRIs as jest.Mock).mockResolvedValue([{ ...sampleRI, reserved_instance_id: 'ri-reload' }]);
      (api.getRIUtilization as jest.Mock).mockResolvedValueOnce([utilFor('ri-reload')]);
      await loadRIExchange();
      await flushPromises();
      expect(utilizationCell()).toBe('99.0%');

      jest.spyOn(console, 'error').mockImplementation(() => undefined);
      (api.getRIUtilization as jest.Mock).mockRejectedValueOnce(new Error('ce timeout'));
      await loadRIExchange();
      await flushPromises();

      expect(utilizationCell()).toBe('No data');
      expect(recsEl.textContent).toContain('Utilization data is unavailable for 1 of 1 convertible RI');
      expect(recsEl.textContent).not.toContain('meet your utilization threshold');
    });

    it('ignores a utilization result that lands while the next scope resolves its account', async () => {
      const stateMod = jest.requireMock('../state') as { getCurrentAccountIDs: jest.Mock };
      (api.listConvertibleRIs as jest.Mock).mockResolvedValue([{ ...sampleRI, reserved_instance_id: 'ri-resolving' }]);
      const util = deferredUtilization();
      await loadRIExchange();

      let releaseAccounts: (v: unknown) => void = () => undefined;
      (api.listAccountsMinimal as jest.Mock).mockReturnValueOnce(new Promise(r => { releaseAccounts = r; }));
      stateMod.getCurrentAccountIDs.mockReturnValue(['uuid-1']);
      const next = loadRIExchange();
      util.resolve([utilFor('ri-resolving')]);
      await flushPromises();

      // The previous scope's late result must not render while the new scope is pending.
      expect(instancesEl.querySelector('.loading-inline')).not.toBeNull();

      releaseAccounts([{ id: 'uuid-1', name: 'Payer', external_id: '123456789012', provider: 'aws' }]);
      await next;
    });
  });
});
