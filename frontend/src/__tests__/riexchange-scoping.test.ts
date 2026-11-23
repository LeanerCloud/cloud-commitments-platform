/**
 * RI Exchange module tests — global Provider/Account filter scoping (issue #871)
 */

import { _accountListeners, _providerListeners, createModal } from './riexchange-harness';
import { loadReshapeRecommendations, loadRIExchange, openExchangeModal, setupRIExchangeHandlers } from '../riexchange';
import * as api from '../api';

// issue #871: RI Exchange must honour the Main Header global Provider/Account
// filter, matching the Active Commitments + Coverage sub-tabs (#866/#881).
describe('RI Exchange global filter scoping (issue #871)', () => {
  let instancesEl: HTMLDivElement;
  let recsEl: HTMLDivElement;
  let historyEl: HTMLDivElement;
  let riExchangePanel: HTMLDivElement;

  const stateMod = (): {
    getCurrentProvider: jest.Mock;
    getCurrentAccountIDs: jest.Mock;
    subscribeProvider: jest.Mock;
    subscribeAccount: jest.Mock;
  } => jest.requireMock('../state');

  beforeEach(() => {
    instancesEl = document.createElement('div');
    instancesEl.id = 'ri-exchange-instances-list';
    recsEl = document.createElement('div');
    recsEl.id = 'ri-exchange-recommendations-list';
    historyEl = document.createElement('div');
    historyEl.id = 'ri-exchange-history-list';
    riExchangePanel = document.createElement('div');
    riExchangePanel.id = 'inventory-ri-exchange';
    document.body.append(instancesEl, recsEl, historyEl, riExchangePanel);

    (api.listConvertibleRIs as jest.Mock).mockResolvedValue([]);
    (api.listExchangeableAzureRIs as jest.Mock).mockResolvedValue([]);
    (api.listAccountsMinimal as jest.Mock).mockResolvedValue([
      { id: 'uuid-1', name: 'Prod', external_id: '123456789012', provider: 'aws' },
      { id: 'uuid-2', name: 'Other', external_id: '210987654321', provider: 'aws' },
      { id: 'uuid-az', name: 'Sub', external_id: 'aaaaaaaa-0000-0000-0000-bbbbbbbbbbbb', provider: 'azure' },
    ]);
    (api.getRIUtilization as jest.Mock).mockResolvedValue([]);
    (api.getReshapeRecommendations as jest.Mock).mockResolvedValue({ recommendations: [], recs_staleness: '', recs_collected_at: null });
    (api.getRIExchangeHistoryResponse as jest.Mock).mockResolvedValue({ records: [], direct_executions: [] });

    _providerListeners.length = 0;
    _accountListeners.length = 0;
    const s = stateMod();
    s.subscribeProvider.mockImplementation((cb: () => void) => { _providerListeners.push(cb); return () => undefined; });
    s.subscribeAccount.mockImplementation((cb: () => void) => { _accountListeners.push(cb); return () => undefined; });
    s.getCurrentProvider.mockReturnValue('aws');
    s.getCurrentAccountIDs.mockReturnValue([]);
    // Restore column-filter slices wiped by a prior resetAllMocks() so
    // renderRIsTable / renderRecommendations don't throw on non-empty data.
    (s as unknown as { getActiveRiColumnFilters: jest.Mock }).getActiveRiColumnFilters?.mockReturnValue({});
    (s as unknown as { getRiExchangeColumnFilters: jest.Mock }).getRiExchangeColumnFilters?.mockReturnValue({});
  });

  afterEach(() => {
    document.body.innerHTML = '';
    jest.clearAllMocks();
  });

  it('forwards the single selected account to the AWS list endpoint', async () => {
    stateMod().getCurrentAccountIDs.mockReturnValue(['uuid-1']);
    await loadRIExchange();
    expect(api.listConvertibleRIs).toHaveBeenCalledWith('123456789012');
  });

  it('keeps the account scope and refreshes reshape recs after a successful exchange (closes #285)', async () => {
    stateMod().getCurrentAccountIDs.mockReturnValue(['uuid-1']);
    (api.listTargetOfferings as jest.Mock).mockResolvedValue([]);
    const modal = createModal();
    (api.getExchangeQuote as jest.Mock).mockResolvedValueOnce({
      IsValidExchange: true, ValidationFailureReason: '', CurrencyCode: 'USD', PaymentDueRaw: '12.50',
      SourceHourlyPriceRaw: '', SourceRemainingUpfrontRaw: '', SourceRemainingTotalRaw: '',
      TargetHourlyPriceRaw: '', TargetRemainingUpfrontRaw: '', TargetRemainingTotalRaw: '', Region: 'us-west-2',
    });
    (api.executeExchange as jest.Mock).mockResolvedValueOnce({ exchange_id: 'exch-1', quote: {} });
    openExchangeModal('ri-abc', 3, 'm5.large', [
      { instance_type: 'm5.large', offering_id: '4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91', effective_monthly_cost: 42.5 },
    ]);
    Array.from(modal.querySelectorAll('button')).find((b) => b.textContent === 'Get Quote')?.click();
    await new Promise((resolve) => setTimeout(resolve, 0));
    Array.from(modal.querySelectorAll('button')).find((b) => b.textContent === 'Execute Exchange')?.click();
    document.querySelector<HTMLButtonElement>('.modal-confirm-actions .btn-destructive')?.click();
    await new Promise((resolve) => setTimeout(resolve, 0));
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(api.executeExchange).toHaveBeenCalledTimes(1);
    expect(api.listConvertibleRIs).toHaveBeenCalledWith('123456789012');
    expect(api.listConvertibleRIs).not.toHaveBeenCalledWith(undefined);
    expect(api.getReshapeRecommendations).toHaveBeenCalled();
  });

  it('forwards the Azure subscription GUID (external_id), not the account UUID', async () => {
    stateMod().getCurrentProvider.mockReturnValue('azure');
    stateMod().getCurrentAccountIDs.mockReturnValue(['uuid-az']);
    await loadRIExchange();
    expect(api.listExchangeableAzureRIs).toHaveBeenCalledWith('aaaaaaaa-0000-0000-0000-bbbbbbbbbbbb');
  });

  it('shows an error and calls no list endpoint when the selected account is unknown', async () => {
    stateMod().getCurrentAccountIDs.mockReturnValue(['uuid-missing']);
    await loadRIExchange();
    expect(api.listConvertibleRIs).not.toHaveBeenCalled();
    expect(instancesEl.textContent).toContain('uuid-missing');
    expect(instancesEl.querySelector('.error')).not.toBeNull();
  });

  it('a pending utilization fetch from a prior scope cannot overwrite the resolve error', async () => {
    const sampleRI = {
      reserved_instance_id: 'ri-old', instance_type: 'm5.large', availability_zone: 'us-east-1a',
      instance_count: 1, start: '2026-01-01T00:00:00Z', end: '2027-01-01T00:00:00Z',
      offering_type: 'Convertible', fixed_price: 0, usage_price: 0, state: 'active', normalization_factor: 4,
    };
    let releaseUtil: (v: unknown) => void = () => undefined;
    (api.getRIUtilization as jest.Mock).mockReturnValueOnce(new Promise(r => { releaseUtil = r; }));
    (api.listConvertibleRIs as jest.Mock).mockResolvedValue([sampleRI]);
    stateMod().getCurrentAccountIDs.mockReturnValue(['uuid-1']);
    await loadRIExchange();
    stateMod().getCurrentAccountIDs.mockReturnValue(['uuid-missing']);
    await loadRIExchange();
    releaseUtil([]);
    await new Promise(r => setTimeout(r, 0));
    expect(instancesEl.querySelector('.error')).not.toBeNull();
    expect(instancesEl.textContent).not.toContain('ri-old');
    expect(recsEl.textContent).toContain('Select a valid account');
    expect(historyEl.textContent).toContain('Select a valid account');
  });

  it('drops a superseded failing resolve instead of rendering its error', async () => {
    let rejectLookup: (e: Error) => void = () => undefined;
    (api.listAccountsMinimal as jest.Mock).mockReturnValueOnce(new Promise((_, rej) => { rejectLookup = rej; }));
    stateMod().getCurrentAccountIDs.mockReturnValue(['uuid-1']);
    const first = loadRIExchange();
    stateMod().getCurrentAccountIDs.mockReturnValue([]);
    await loadRIExchange();
    rejectLookup(new Error('boom'));
    await first;
    expect(instancesEl.querySelector('.error')).toBeNull();
    expect(instancesEl.textContent).not.toContain('boom');
  });

  it('shows the request failure and makes no list call when listAccountsMinimal rejects', async () => {
    (api.listAccountsMinimal as jest.Mock).mockRejectedValueOnce(new Error('HTTP 500'));
    stateMod().getCurrentAccountIDs.mockReturnValue(['uuid-1']);
    await loadRIExchange();
    expect(api.listConvertibleRIs).not.toHaveBeenCalled();
    expect(instancesEl.textContent).toContain('HTTP 500');
  });

  it('fails loudly when the selected account has no external_id', async () => {
    (api.listAccountsMinimal as jest.Mock).mockResolvedValueOnce([
      { id: 'uuid-1', name: 'Prod', external_id: '', provider: 'aws' },
    ]);
    stateMod().getCurrentAccountIDs.mockReturnValue(['uuid-1']);
    await loadRIExchange();
    expect(api.listConvertibleRIs).not.toHaveBeenCalled();
    expect(instancesEl.querySelector('.error')).not.toBeNull();
  });

  it('in-flight loaders from a superseded load cannot overwrite a later resolve error', async () => {
    const pending = <T,>() => {
      let resolve: (v: T) => void = () => undefined;
      const promise = new Promise<T>(r => { resolve = r; });
      return { promise, resolve };
    };
    const ris = pending<unknown[]>();
    const recs = pending<unknown>();
    const hist = pending<unknown[]>();
    (api.listConvertibleRIs as jest.Mock).mockReturnValueOnce(ris.promise);
    (api.getReshapeRecommendations as jest.Mock).mockReturnValueOnce(recs.promise);
    (api.getRIExchangeHistoryResponse as jest.Mock).mockReturnValueOnce(hist.promise.then(records => ({ records, direct_executions: [] })));
    const loadA = loadRIExchange();
    await new Promise(r => setTimeout(r, 0));
    stateMod().getCurrentAccountIDs.mockReturnValue(['uuid-missing']);
    await loadRIExchange();
    ris.resolve([{
      reserved_instance_id: 'ri-old', instance_type: 'm5.large', availability_zone: 'us-east-1a',
      instance_count: 1, start: '2026-01-01T00:00:00Z', end: '2027-01-01T00:00:00Z',
      offering_type: 'Convertible', fixed_price: 0, usage_price: 0, state: 'active', normalization_factor: 4,
    }]);
    recs.resolve({ recommendations: [], recs_staleness: '', recs_collected_at: null });
    hist.resolve([]);
    await loadA;
    expect(instancesEl.querySelector('.error')).not.toBeNull();
    expect(instancesEl.textContent).not.toContain('ri-old');
    expect(recsEl.textContent).toContain('Select a valid account');
    expect(historyEl.textContent).toContain('Select a valid account');
  });

  it('forwards undefined and skips the account lookup when no account is selected', async () => {
    await loadRIExchange();
    // No scope lookup (an unfiltered listAccountsMinimal()); the only call is
    // the AWS account-name lookup for the account label (issue #1692).
    expect(api.listAccountsMinimal).not.toHaveBeenCalledWith();
    expect(api.listAccountsMinimal).toHaveBeenCalledWith({ provider: 'aws' });
    expect(api.listConvertibleRIs).toHaveBeenCalledWith(undefined);
  });

  it('drops a superseded load when the account chip changes mid-lookup', async () => {
    let release: (v: unknown) => void = () => undefined;
    (api.listAccountsMinimal as jest.Mock).mockReturnValueOnce(new Promise(r => { release = r; }));
    stateMod().getCurrentAccountIDs.mockReturnValue(['uuid-1']);
    const first = loadRIExchange();
    stateMod().getCurrentAccountIDs.mockReturnValue(['uuid-2']);
    const second = loadRIExchange();
    await second;
    release([{ id: 'uuid-1', name: 'Prod', external_id: '123456789012', provider: 'aws' }]);
    await first;
    expect(api.listConvertibleRIs).toHaveBeenCalledTimes(1);
    expect(api.listConvertibleRIs).toHaveBeenCalledWith('210987654321');
  });

  it('does not forward an account id when more than one account is selected', async () => {
    stateMod().getCurrentAccountIDs.mockReturnValue(['111111111111', '222222222222']);
    await loadRIExchange();
    expect(api.listConvertibleRIs).toHaveBeenCalledWith(undefined);
  });

  it('renders a scoped empty-state naming the AWS account', async () => {
    stateMod().getCurrentAccountIDs.mockReturnValue(['uuid-1']);
    (api.listConvertibleRIs as jest.Mock).mockResolvedValue([]);
    await loadRIExchange();
    expect(instancesEl.textContent).toContain('123456789012');
  });

  it('loads Azure reservations (not AWS RIs) when provider is azure', async () => {
    stateMod().getCurrentProvider.mockReturnValue('azure');
    (api.listExchangeableAzureRIs as jest.Mock).mockResolvedValue([
      { reservation_order_id: 'o1', reservation_id: 'r1', sku: 'Standard_D2s_v3', quantity: 2, region: 'eastus', term: 'P1Y', instance_flexibility: 'On', display_name: 'web-rsv' },
    ]);
    await loadRIExchange();
    expect(api.listExchangeableAzureRIs).toHaveBeenCalled();
    expect(api.listConvertibleRIs).not.toHaveBeenCalled();
    expect(instancesEl.textContent).toContain('Standard_D2s_v3');
    expect(instancesEl.textContent).toContain('web-rsv');
    // Reshape recommendations are AWS-only -> provider-aware not-applicable copy.
    expect(recsEl.textContent).toContain('not available for Azure');
  });

  it('lets a user who can preview purchases select Azure reservations for an exchange (issue #81)', async () => {
    stateMod().getCurrentProvider.mockReturnValue('azure');
    // An Administrators-group member (admin:* covers view:purchases).
    (jest.requireMock('../state') as { getCurrentUser: jest.Mock }).getCurrentUser
      .mockReturnValue({ id: 'u', email: 'u@example.com', groups: ['00000000-0000-5000-8000-000000000001'] });
    (api.listExchangeableAzureRIs as jest.Mock).mockResolvedValue([
      { reservation_order_id: 'o1', reservation_id: 'r1', sku: 'Standard_D2s_v3', quantity: 2, region: 'eastus', term: 'P1Y', instance_flexibility: 'On', billing_scope_id: '/subscriptions/s1' },
      { reservation_order_id: 'o2', reservation_id: 'r2', sku: 'Standard_E2s_v3', quantity: 1, region: 'eastus', term: 'P1Y', instance_flexibility: 'On', billing_scope_id: '/subscriptions/s1' },
    ]);
    await loadRIExchange();
    expect(instancesEl.querySelectorAll('tbody input.azure-ri-select')).toHaveLength(2);
    expect(instancesEl.querySelectorAll('thead th')).toHaveLength(7);
    // The checkbox column's header has an accessible name, visually hidden.
    const selectHeader = instancesEl.querySelector('thead th')!;
    expect(selectHeader.querySelector('.sr-only')?.textContent).toBe('Select for exchange');
    const btn = instancesEl.querySelector<HTMLButtonElement>('#azure-exchange-selected-btn')!;
    expect(btn.disabled).toBe(true);
  });

  it('shows a provider-aware empty state for Azure with no reservations', async () => {
    stateMod().getCurrentProvider.mockReturnValue('azure');
    stateMod().getCurrentAccountIDs.mockReturnValue(['uuid-az']);
    (api.listExchangeableAzureRIs as jest.Mock).mockResolvedValue([]);
    await loadRIExchange();
    expect(api.listExchangeableAzureRIs).toHaveBeenCalledWith('aaaaaaaa-0000-0000-0000-bbbbbbbbbbbb');
    expect(instancesEl.textContent).toContain('No exchangeable reservations for Azure subscription aaaaaaaa-0000-0000-0000-bbbbbbbbbbbb');
  });

  it('shows the not-available empty state for GCP and calls no list endpoint', async () => {
    stateMod().getCurrentProvider.mockReturnValue('gcp');
    await loadRIExchange();
    expect(api.listConvertibleRIs).not.toHaveBeenCalled();
    expect(api.listExchangeableAzureRIs).not.toHaveBeenCalled();
    expect(instancesEl.textContent).toContain("isn't available for GCP");
    expect(recsEl.textContent).toContain("isn't available for GCP");
    expect(historyEl.textContent).toContain("isn't available for GCP");
  });

  it('does not leave stale AWS rows when switching to GCP', async () => {
    // First load AWS with a row present.
    (api.listConvertibleRIs as jest.Mock).mockResolvedValue([{
      reserved_instance_id: 'ri-1', instance_type: 'm5.large', availability_zone: 'us-east-1a',
      instance_count: 1, start: '2026-01-01T00:00:00Z', end: '2027-01-01T00:00:00Z',
      offering_type: 'Convertible', fixed_price: 0, usage_price: 0, state: 'active', normalization_factor: 4,
    }]);
    await loadRIExchange();
    expect(instancesEl.textContent).toContain('m5.large');
    // Switch to GCP -> table must be replaced by the empty state.
    stateMod().getCurrentProvider.mockReturnValue('gcp');
    await loadRIExchange();
    expect(instancesEl.textContent).not.toContain('m5.large');
    expect(instancesEl.textContent).toContain("isn't available for GCP");
  });

  it('closes an in-progress exchange modal when the filter changes', async () => {
    const modal = createModal();
    modal.classList.remove('hidden'); // simulate an open exchange modal
    setupRIExchangeHandlers();
    _providerListeners.forEach(cb => cb());
    // Modal is closed synchronously on the chip event.
    expect(modal.classList.contains('hidden')).toBe(true);
  });

  describe('excluded-RI notice lifecycle (issue #1181)', () => {
    const noticeText = (): string => document.getElementById('ri-exchange-recommendations-excluded')?.textContent ?? '';

    beforeEach(async () => {
      (api.getReshapeRecommendations as jest.Mock).mockResolvedValue({
        recommendations: [],
        excluded_ris: [{ ri_id: 'ri-noterm', reason: 'no term' }],
      });
      await loadRIExchange();
      expect(noticeText()).toContain('ri-noterm');
    });

    it('clears after a failed reload', async () => {
      (api.getReshapeRecommendations as jest.Mock).mockRejectedValue(new Error('boom'));
      await loadReshapeRecommendations();
      expect(noticeText()).toBe('');
    });

    it('clears when switching to GCP', async () => {
      stateMod().getCurrentProvider.mockReturnValue('gcp');
      await loadRIExchange();
      expect(noticeText()).toBe('');
    });

    it('clears when switching to Azure', async () => {
      stateMod().getCurrentProvider.mockReturnValue('azure');
      stateMod().getCurrentAccountIDs.mockReturnValue(['uuid-az']);
      await loadRIExchange();
      expect(noticeText()).toBe('');
    });

    it('clears on an account-resolution error', async () => {
      stateMod().getCurrentAccountIDs.mockReturnValue(['uuid-missing']);
      await loadRIExchange();
      expect(noticeText()).toBe('');
    });
  });

  // Money and count columns carry th.num/td.num (right-aligned, tabular
  // digits) in the header and in every row at the same index (issue #739).
  function numColumns(root: ParentNode): { headers: string[]; rowsMatch: boolean } {
    const ths = Array.from(root.querySelectorAll('thead th'));
    const numIdx = ths.flatMap((th, i) => (th.classList.contains('num') ? [i] : []));
    const rows = Array.from(root.querySelectorAll('tbody tr'));
    const rowsMatch = rows.length > 0 && rows.every((tr) => {
      const tds = Array.from(tr.querySelectorAll('td'));
      return tds.flatMap((td, i) => (td.classList.contains('num') ? [i] : [])).join() === numIdx.join();
    });
    return { headers: numIdx.map((i) => (ths[i]!.textContent ?? '').trim()), rowsMatch };
  }

  it('right-aligns the count and payment columns of the RI tables (issue #739)', async () => {
    (api.listConvertibleRIs as jest.Mock).mockResolvedValue([{
      reserved_instance_id: 'ri-1', instance_type: 'm5.large', availability_zone: 'us-east-1a',
      instance_count: 3, start: '2026-01-01T00:00:00Z', end: '2027-01-01T00:00:00Z',
      offering_type: 'Convertible', fixed_price: 0, usage_price: 0, state: 'active', normalization_factor: 4,
    }]);
    (api.getRIExchangeHistoryResponse as jest.Mock).mockResolvedValue({ records: [{
      id: 'h1', account_id: 'a', exchange_id: '', region: 'us-east-1', source_ri_ids: ['ri-1'],
      source_instance_type: 'm5.large', source_count: 1, target_offering_id: 'o', target_instance_type: 'm5.xlarge',
      target_count: 1, payment_due: '1.25', status: 'failed', mode: 'manual',
      created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
    }], direct_executions: [] });
    await loadRIExchange();
    expect(numColumns(instancesEl)).toEqual({ headers: ['Count'], rowsMatch: true });
    expect(numColumns(historyEl)).toEqual({ headers: ['Payment'], rowsMatch: true });
  });

  it('right-aligns the Azure reservation quantity column (issue #739)', async () => {
    stateMod().getCurrentProvider.mockReturnValue('azure');
    (api.listExchangeableAzureRIs as jest.Mock).mockResolvedValue([
      { reservation_order_id: 'o1', reservation_id: 'r1', sku: 'Standard_D2s_v3', quantity: 2, region: 'eastus', term: 'P1Y', instance_flexibility: 'On', display_name: 'web-rsv' },
    ]);
    await loadRIExchange();
    expect(numColumns(instancesEl)).toEqual({ headers: ['Quantity'], rowsMatch: true });
  });
});
