/**
 * RI Exchange module tests — openExchangeModal (pre-fill, quotes and
 * invalidation) and fillQuoteFromRI
 */

import { createModal } from './riexchange-harness';
import { fillQuoteFromRI, openExchangeModal } from '../riexchange';
import * as api from '../api';

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

  it('removes hidden class to show the modal', () => {
    openExchangeModal('ri-abc123', 2);
    expect(modal.classList.contains('hidden')).toBe(false);
  });

  it('renders a header close-X labelled "Close dialog" that closes the dialog', () => {
    openExchangeModal('ri-abc123', 2);
    const x = modal.querySelector<HTMLButtonElement>('.modal-header button.modal-close[data-modal-dismiss]');
    expect(x).not.toBeNull();
    expect(x!.getAttribute('aria-label')).toBe('Close dialog');
    expect(modal.querySelectorAll('.modal-header .modal-close')).toHaveLength(1);
    x!.click();
    expect(modal.classList.contains('hidden')).toBe(true);
  });

  it('displays the RI ID in the modal content', () => {
    openExchangeModal('ri-abc123', 2);
    expect(modal.textContent).toContain('ri-abc123');
  });

  it('pre-fills count input with given count', () => {
    openExchangeModal('ri-abc123', 5);
    // Multi-target refactor: count + target inputs now live inside
    // per-row containers; selectors changed from #id to class.
    const countInput = modal.querySelector<HTMLInputElement>('.modal-exchange-count');
    expect(countInput?.value).toBe('5');
  });

  it('pre-fills hidden target input with offering_id when suggestedTargetType matches an alternativeTarget', () => {
    // suggestedTargetType is resolved to an offering_id via alternativeTargets lookup.
    openExchangeModal('ri-abc123', 2, 'm5.large', [
      { instance_type: 'm5.large', offering_id: '4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91', effective_monthly_cost: 42.5 },
    ]);
    const targetInput = modal.querySelector<HTMLInputElement>('.modal-exchange-target');
    // Hidden input must contain the UUID, not the instance type string.
    expect(targetInput?.value).toBe('4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91');
  });

  it('leaves hidden target input empty when suggestedTargetType is not provided', () => {
    openExchangeModal('ri-abc123', 2);
    const targetInput = modal.querySelector<HTMLInputElement>('.modal-exchange-target');
    expect(targetInput?.value).toBe('');
  });

  it('leaves hidden target input empty when suggestedTargetType has no matching alternativeTarget', () => {
    openExchangeModal('ri-abc123', 2, 'm5.large');
    const targetInput = modal.querySelector<HTMLInputElement>('.modal-exchange-target');
    // No alternativeTargets provided -- cannot resolve instance type to UUID.
    expect(targetInput?.value).toBe('');
  });

  it('starts with exactly one target row', () => {
    openExchangeModal('ri-abc123', 2);
    const rows = modal.querySelectorAll<HTMLDivElement>('.exchange-target-row');
    expect(rows.length).toBe(1);
  });

  it('adds a second target row when "+ Add target" is clicked', () => {
    openExchangeModal('ri-abc123', 2);
    const addBtn = modal.querySelector<HTMLButtonElement>('#modal-exchange-add-target');
    expect(addBtn).not.toBeNull();
    addBtn?.click();
    const rows = modal.querySelectorAll<HTMLDivElement>('.exchange-target-row');
    expect(rows.length).toBe(2);
  });

  it('posts singleton target_offering_id/target_count when exactly one row is present', async () => {
    const mockGetQuote = api.getExchangeQuote as jest.Mock;
    mockGetQuote.mockResolvedValueOnce({
      IsValidExchange: false,
      ValidationFailureReason: 'test',
      CurrencyCode: 'USD',
      PaymentDueRaw: '0',
      SourceHourlyPriceRaw: '',
      SourceRemainingUpfrontRaw: '',
      SourceRemainingTotalRaw: '',
      TargetHourlyPriceRaw: '',
      TargetRemainingUpfrontRaw: '',
      TargetRemainingTotalRaw: '',
    });
    // Pre-seed with a CE alternative so suggestedTargetType resolves to a UUID.
    const offeringUUID = '4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91';
    openExchangeModal('ri-abc', 3, 'm5.large', [
      { instance_type: 'm5.large', offering_id: offeringUUID, effective_monthly_cost: 42.5 },
    ]);
    const quoteBtn = Array.from(modal.querySelectorAll('button')).find((b) => b.textContent === 'Get Quote');
    quoteBtn?.click();
    // Wait for the async submit handler to settle.
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(mockGetQuote).toHaveBeenCalledTimes(1);
    const req = mockGetQuote.mock.calls[0][0];
    expect(req.ri_ids).toEqual(['ri-abc']);
    // Singleton shape: target_offering_id must be the UUID, not the instance type.
    expect(req.target_offering_id).toBe(offeringUUID);
    expect(req.target_count).toBe(3);
    expect(req.targets).toBeUndefined();
  });

  it('carries the quote-resolved region onto the execute request (issue #238)', async () => {
    const mockGetQuote = api.getExchangeQuote as jest.Mock;
    const mockExecute = api.executeExchange as jest.Mock;
    mockGetQuote.mockResolvedValueOnce({
      IsValidExchange: true,
      ValidationFailureReason: '',
      CurrencyCode: 'USD',
      PaymentDueRaw: '12.50',
      SourceHourlyPriceRaw: '',
      SourceRemainingUpfrontRaw: '',
      SourceRemainingTotalRaw: '',
      TargetHourlyPriceRaw: '',
      TargetRemainingUpfrontRaw: '',
      TargetRemainingTotalRaw: '',
      // Region the backend's SDK chain resolved for this quote.
      Region: 'us-west-2',
    });
    mockExecute.mockResolvedValueOnce({ exchange_id: 'exch-1', quote: {} });

    const offeringUUID = '4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91';
    openExchangeModal('ri-abc', 3, 'm5.large', [
      { instance_type: 'm5.large', offering_id: offeringUUID, effective_monthly_cost: 42.5 },
    ]);
    const quoteBtn = Array.from(modal.querySelectorAll('button')).find((b) => b.textContent === 'Get Quote');
    quoteBtn?.click();
    await new Promise((resolve) => setTimeout(resolve, 0));

    const executeBtn = Array.from(modal.querySelectorAll('button')).find((b) => b.textContent === 'Execute Exchange');
    expect(executeBtn?.classList.contains('hidden')).toBe(false);
    executeBtn?.click();
    expect(mockExecute).not.toHaveBeenCalled();
    expect(document.querySelector('.modal-confirm-body')?.textContent).toContain('USD 12.50');
    document.querySelector<HTMLButtonElement>('.modal-confirm-actions .btn-destructive')?.click();
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(mockExecute).toHaveBeenCalledTimes(1);
    const execReq = mockExecute.mock.calls[0][0];
    // Pre-fix, execute never carried a region at all, so the backend's
    // validateExecuteExchangeBody rejected every UI-initiated exchange
    // with a 400. This must equal the region the quote resolved.
    expect(execReq.region).toBe('us-west-2');
    // The whole body the backend's ExchangeExecuteRequestBody decodes: the
    // singleton target, the quoted payment as the spend cap, and the region.
    expect(execReq).toEqual({
      ri_ids: ['ri-abc'],
      target_offering_id: offeringUUID,
      target_count: 3,
      max_payment_due_usd: '12.50',
      region: 'us-west-2',
    });
  });
});

describe('fillQuoteFromRI', () => {
  let modal: HTMLDivElement;

  beforeEach(() => {
    modal = createModal();
    (api.listTargetOfferings as jest.Mock).mockResolvedValue([]);
  });

  afterEach(() => {
    document.body.innerHTML = '';
  });

  it('opens the exchange modal', () => {
    fillQuoteFromRI('ri-abc123', 2);
    expect(modal.classList.contains('hidden')).toBe(false);
  });

  it('displays the RI ID in the modal', () => {
    fillQuoteFromRI('ri-xyz', 3);
    expect(modal.textContent).toContain('ri-xyz');
  });

  it('does not throw when modal is missing', () => {
    const body = document.body;
    while (body.firstChild) body.removeChild(body.firstChild);
    expect(() => fillQuoteFromRI('ri-abc123', 1)).not.toThrow();
  });
});
