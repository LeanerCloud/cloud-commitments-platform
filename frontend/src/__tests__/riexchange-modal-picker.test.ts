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
});
