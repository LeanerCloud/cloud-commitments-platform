/**
 * RI Exchange module tests — global Provider/Account filter scoping (issue #871)
 */

import { _accountListeners, _providerListeners, createModal } from './riexchange-harness';
import { loadRIExchange, openExchangeModal } from '../riexchange';
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
});
