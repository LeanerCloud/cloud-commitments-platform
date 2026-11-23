/**
 * RI Exchange module tests — openExchangeModal (target rows, cost chip,
 * picker states and the exchange result announcement)
 */

import { createModal } from './riexchange-harness';
import { openExchangeModal } from '../riexchange';
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

  it('removes a target row when the × button is clicked, but keeps at least one', () => {
    openExchangeModal('ri-abc123', 2);
    const addBtn = modal.querySelector<HTMLButtonElement>('#modal-exchange-add-target');
    addBtn?.click();
    addBtn?.click();
    // Three rows now; removing one should leave 2.
    const removes = modal.querySelectorAll<HTMLButtonElement>('.exchange-remove-target');
    removes[0]?.click();
    let rows = modal.querySelectorAll<HTMLDivElement>('.exchange-target-row');
    expect(rows.length).toBe(2);

    // Remove down to 1 row; further clicks are ignored.
    modal.querySelector<HTMLButtonElement>('.exchange-remove-target')?.click();
    rows = modal.querySelectorAll<HTMLDivElement>('.exchange-target-row');
    expect(rows.length).toBe(1);
    modal.querySelector<HTMLButtonElement>('.exchange-remove-target')?.click();
    rows = modal.querySelectorAll<HTMLDivElement>('.exchange-target-row');
    expect(rows.length).toBe(1);
  });

  it('hides modal when cancel button is clicked', () => {
    openExchangeModal('ri-abc123', 2);
    const cancelBtn = Array.from(modal.querySelectorAll('button')).find(b => b.textContent === 'Cancel');
    cancelBtn?.click();
    expect(modal.classList.contains('hidden')).toBe(true);
  });

  it('shows a cost chip when the selected offering_id matches an alternative', () => {
    // suggestedTargetType='m5.large' resolves to offering_id 'off-m5' via alternativeTargets.
    // The chip should show the cost for that offering.
    openExchangeModal('ri-abc', 2, 'm5.large', [
      { instance_type: 'm5.large', offering_id: 'off-m5', effective_monthly_cost: 42.5 },
      { instance_type: 'm6i.large', offering_id: 'off-m6i', effective_monthly_cost: 35.0 },
    ]);
    const chip = modal.querySelector<HTMLSpanElement>('.cost-chip');
    expect(chip).not.toBeNull();
    expect(chip?.textContent).toBe('$42.50/mo each');
  });

  it('shows an em-dash in the cost chip when the selected offering_id has no CE pricing match', () => {
    // 'unknown.shape' cannot be resolved to an offering_id from alternativeTargets,
    // so the hidden input stays empty and the chip shows "—".
    openExchangeModal('ri-abc', 2, 'unknown.shape', [
      { instance_type: 'm5.large', offering_id: 'off-m5', effective_monthly_cost: 42.5 },
    ]);
    const chip = modal.querySelector<HTMLSpanElement>('.cost-chip');
    expect(chip?.textContent).toBe('—');
  });

  it('computes the running total across two rows using per-row count', () => {
    openExchangeModal('ri-multi', 1, 'm5.large', [
      { instance_type: 'm5.large', offering_id: 'off-m5', effective_monthly_cost: 40.0 },
      { instance_type: 'm6i.large', offering_id: 'off-m6i', effective_monthly_cost: 30.0 },
    ]);
    const firstCountInput = modal.querySelector<HTMLInputElement>('.modal-exchange-count');
    if (firstCountInput) {
      firstCountInput.value = '2';
      firstCountInput.dispatchEvent(new Event('input'));
    }
    modal.querySelector<HTMLButtonElement>('#modal-exchange-add-target')?.click();
    const rows = modal.querySelectorAll<HTMLDivElement>('.exchange-target-row');
    const secondOffering = rows[1]?.querySelector<HTMLInputElement>('.modal-exchange-target');
    const secondCount = rows[1]?.querySelector<HTMLInputElement>('.modal-exchange-count');
    if (secondOffering) {
      // Inject the offering_id UUID directly into the hidden input and
      // trigger an input event so updateRunningTotal fires.
      secondOffering.value = 'off-m6i';
      secondOffering.dispatchEvent(new Event('input'));
    }
    if (secondCount) {
      secondCount.value = '3';
      secondCount.dispatchEvent(new Event('input'));
    }
    const total = modal.querySelector<HTMLDivElement>('#modal-exchange-running-total');
    expect(total?.textContent).toContain('$170.00/mo');
    expect(total?.textContent).not.toContain('incomplete');
  });

  it('marks the running total as incomplete when some rows have no pricing match', () => {
    openExchangeModal('ri-incomplete', 1, 'm5.large', [
      { instance_type: 'm5.large', offering_id: 'off-m5', effective_monthly_cost: 40.0 },
    ]);
    modal.querySelector<HTMLButtonElement>('#modal-exchange-add-target')?.click();
    const rows = modal.querySelectorAll<HTMLDivElement>('.exchange-target-row');
    const secondOffering = rows[1]?.querySelector<HTMLInputElement>('.modal-exchange-target');
    if (secondOffering) {
      // 'unknown-offering' does not match any CE alternative offering_id.
      secondOffering.value = 'unknown-offering';
      secondOffering.dispatchEvent(new Event('input'));
    }
    const total = modal.querySelector<HTMLDivElement>('#modal-exchange-running-total');
    expect(total?.textContent).toContain('$40.00/mo');
    expect(total?.textContent).toContain('incomplete');
  });

  it('hides the running total when called without alternativeTargets', () => {
    openExchangeModal('ri-no-alts', 2, 'm5.large');
    const total = modal.querySelector<HTMLDivElement>('#modal-exchange-running-total');
    expect(total?.classList.contains('hidden')).toBe(true);
  });

  it('does not throw when modal element is missing', () => {
    document.body.innerHTML = '';
    expect(() => openExchangeModal('ri-abc123', 2)).not.toThrow();
  });

  // Defect 1 -- picker tests
  it('renders a select picker (not a free-text input) for the target offering', () => {
    openExchangeModal('ri-abc123', 2);
    // There must be a <select> for the picker.
    const picker = modal.querySelector<HTMLSelectElement>('.modal-exchange-target-select');
    expect(picker).not.toBeNull();
    // There must NOT be a visible text input (the hidden field has type="hidden").
    const textInput = modal.querySelector<HTMLInputElement>('input[type="text"].modal-exchange-target');
    expect(textInput).toBeNull();
  });

  it('populates the select with CE recommendation options when alternativeTargets are provided', async () => {
    openExchangeModal('ri-abc', 2, undefined, [
      { instance_type: 'm5.large', offering_id: 'off-m5', effective_monthly_cost: 42.5 },
      { instance_type: 'm6i.large', offering_id: 'off-m6i', effective_monthly_cost: 35.0 },
    ]);
    // CE recommendations land in the select synchronously (no async fetch needed).
    const picker = modal.querySelector<HTMLSelectElement>('.modal-exchange-target-select');
    const options = picker ? Array.from(picker.querySelectorAll('option')) : [];
    const optionValues = options.map((o) => o.value);
    expect(optionValues).toContain('off-m5');
    expect(optionValues).toContain('off-m6i');
  });

  it('populates the select with AWS offerings after async load completes', async () => {
    const mockListOfferings = api.listTargetOfferings as jest.Mock;
    mockListOfferings.mockResolvedValueOnce([
      {
        offering_id: '4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91',
        instance_type: 'm5.xlarge',
        offering_type: 'No Upfront',
        product_description: 'Linux/UNIX',
        duration: 31536000,
        fixed_price: 0,
        usage_price: 0.12,
        currency_code: 'USD',
        scope: 'Region',
        normalization_factor: 8,
      },
    ]);
    openExchangeModal('ri-abc', 2);
    // Let the async populateAwsOfferings() settle.
    await flushPromises();

    const picker = modal.querySelector<HTMLSelectElement>('.modal-exchange-target-select');
    const options = picker ? Array.from(picker.querySelectorAll('option')) : [];
    const optionValues = options.map((o) => o.value);
    expect(optionValues).toContain('4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91');
    expect(mockListOfferings).toHaveBeenCalledWith('ri-abc');
  });

  it('shows an error with Retry, not a "type a UUID" dead end, when offerings fail to load (issue #740)', async () => {
    const mockListOfferings = api.listTargetOfferings as jest.Mock;
    mockListOfferings.mockRejectedValueOnce(new Error('throttled'));
    openExchangeModal('ri-abc', 2);
    await flushPromises();

    const picker = modal.querySelector<HTMLSelectElement>('.modal-exchange-target-select');
    expect(picker?.options[0]?.textContent).toBe('Could not load offerings');
    expect(modal.textContent).not.toContain('UUID');
    const errorRow = modal.querySelector<HTMLElement>('.exchange-offerings-error');
    expect(errorRow?.classList.contains('hidden')).toBe(false);
    expect(errorRow?.querySelector('[role="alert"]')?.textContent).toBe('Could not load target offerings: throttled');

    mockListOfferings.mockResolvedValueOnce([{
      offering_id: '4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91', instance_type: 'm5.xlarge', offering_type: 'No Upfront',
      product_description: 'Linux/UNIX', duration: 31536000, fixed_price: 0, usage_price: 0.12,
      currency_code: 'USD', scope: 'Region', normalization_factor: 8,
    }]);
    Array.from(errorRow?.querySelectorAll('button') ?? []).find((b) => b.textContent === 'Retry')?.click();
    expect(picker?.options[0]?.textContent).toBe('Loading offerings...');
    await flushPromises();

    expect(mockListOfferings).toHaveBeenCalledTimes(2);
    expect(errorRow?.classList.contains('hidden')).toBe(true);
    expect(Array.from(picker?.options ?? []).map((o) => o.value)).toContain('4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91');
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
