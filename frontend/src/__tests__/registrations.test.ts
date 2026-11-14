/**
 * Registrations module — default-filter behaviour (refs #45).
 *
 * The Account Registrations status dropdown defaults to "Pending" in
 * index.html. `loadRegistrations` reads that select and forwards the value
 * to the API, so on first paint the panel must request only pending rows.
 */
import { initRegistrations, loadRegistrations } from '../modules/registrations';

jest.mock('../api', () => ({
  listRegistrations: jest.fn(),
  rejectRegistration: jest.fn(),
}));
jest.mock('../confirmDialog', () => ({ confirmDialog: jest.fn() }));
jest.mock('../toast', () => ({ showToast: jest.fn() }));
jest.mock('../settings', () => ({
  openAccountModal: jest.fn(),
  loadAccountsForProvider: jest.fn(),
}));

import * as api from '../api';
import { openAccountModal } from '../settings';
import { confirmDialog } from '../confirmDialog';
import { showToast } from '../toast';

describe('loadRegistrations default filter', () => {
  beforeEach(() => {
    document.body.replaceChildren();
    jest.clearAllMocks();
  });

  function buildDOM(initialFilterValue: string): void {
    const container = document.createElement('div');
    container.id = 'registrations-list';
    document.body.appendChild(container);

    const select = document.createElement('select');
    select.id = 'registrations-status-filter';
    for (const v of ['', 'pending', 'approved', 'rejected']) {
      const opt = document.createElement('option');
      opt.value = v;
      // Mirror index.html: "pending" carries the `selected` attribute.
      if (v === 'pending') opt.defaultSelected = true;
      select.appendChild(opt);
    }
    select.value = initialFilterValue;
    document.body.appendChild(select);
  }

  test('passes "pending" to listRegistrations on first load', async () => {
    buildDOM('pending');
    (api.listRegistrations as jest.Mock).mockResolvedValue([]);

    await loadRegistrations();

    expect(api.listRegistrations).toHaveBeenCalledTimes(1);
    expect(api.listRegistrations).toHaveBeenCalledWith('pending');
  });

  test('passes undefined when the user picks "All" (empty value)', async () => {
    buildDOM('');
    (api.listRegistrations as jest.Mock).mockResolvedValue([]);

    await loadRegistrations();

    expect(api.listRegistrations).toHaveBeenCalledWith(undefined);
  });

  test('repeated initRegistrations (one per Accounts visit) wires the filter only once', async () => {
    buildDOM('pending');
    (api.listRegistrations as jest.Mock).mockResolvedValue([]);
    for (let i = 0; i < 5; i++) initRegistrations();
    await Promise.resolve();
    (api.listRegistrations as jest.Mock).mockClear();

    const select = document.getElementById('registrations-status-filter') as HTMLSelectElement;
    select.value = '';
    select.dispatchEvent(new Event('change'));

    expect(api.listRegistrations).toHaveBeenCalledTimes(1);
    expect(api.listRegistrations).toHaveBeenCalledWith(undefined);
  });
});

describe('registration row actions', () => {
  beforeEach(() => {
    document.body.replaceChildren();
    jest.clearAllMocks();
  });

  test('Approve stays a text button; Reject and Delete are named icon buttons in one row', async () => {
    const container = document.createElement('div');
    container.id = 'registrations-list';
    document.body.appendChild(container);
    (api.listRegistrations as jest.Mock).mockResolvedValue([{
      id: 'r1', provider: 'aws', account_name: 'Checkout', external_id: '650412398877',
      contact_email: 'c@example.com', status: 'pending', created_at: '2026-10-28T12:00:00Z',
    }]);

    await loadRegistrations();

    const actions = container.querySelector('tbody tr td:last-child .row-actions')!;
    const buttons = Array.from(actions.querySelectorAll('button'));
    expect(buttons.map(b => b.getAttribute('aria-label'))).toEqual([
      'Approve registration Checkout',
      'Reject registration Checkout',
      'Delete registration Checkout',
    ]);
    expect(buttons[0]!.textContent).toBe('Approve');
    for (const [btn, icon, title] of [[buttons[1]!, 'x-circle', 'Reject registration'], [buttons[2]!, 'trash-2', 'Delete registration']] as const) {
      expect(btn.classList.contains('icon-btn')).toBe(true);
      expect(btn.querySelector(`svg.icon-${icon}`)).not.toBeNull();
      expect(btn.title).toBe(title);
    }
    expect(buttons[2]!.classList.contains('btn-danger')).toBe(true);
  });

  test('Approve opens the account modal with enabled preset to true, so the admin unchecks it to keep the account disabled', async () => {
    const container = document.createElement('div');
    container.id = 'registrations-list';
    document.body.appendChild(container);
    (api.listRegistrations as jest.Mock).mockResolvedValue([{
      id: 'r1', provider: 'aws', account_name: 'Checkout', external_id: '650412398877',
      contact_email: 'c@example.com', status: 'pending', created_at: '2026-10-28T12:00:00Z',
    }]);
    await loadRegistrations();

    (container.querySelector('button[aria-label="Approve registration Checkout"]') as HTMLButtonElement).click();

    expect(openAccountModal).toHaveBeenCalledWith('aws', expect.objectContaining({ enabled: true }), expect.anything());
  });
});

describe('registrations table polish', () => {
  const reg = (over: Record<string, unknown>) => ({
    id: 'r1', provider: 'aws', account_name: 'Checkout', external_id: '650412398877',
    contact_email: 'c@example.com', status: 'pending', created_at: '2026-10-28T12:00:00Z', ...over,
  });
  let container: HTMLElement;

  async function load(regs: unknown[]): Promise<void> {
    (api.listRegistrations as jest.Mock).mockResolvedValue(regs);
    await loadRegistrations();
  }
  const box = (name: string) =>
    container.querySelector(`input[aria-label="Select registration ${name}"]`) as HTMLInputElement;
  const bulkBtn = () => container.querySelector('#registrations-bulk-reject') as HTMLButtonElement;
  const flush = async () => { for (let i = 0; i < 10; i++) await Promise.resolve(); };

  beforeEach(() => {
    document.body.replaceChildren();
    jest.resetAllMocks();
    container = document.createElement('div');
    container.id = 'registrations-list';
    document.body.appendChild(container);
    (api.listRegistrations as jest.Mock).mockResolvedValue([]);
    return loadRegistrations();
  });

  test('truncates a long contact email and keeps the full address in the title', async () => {
    const long = 'a.very.long.contact.name.here@example-company.com';
    await load([reg({ contact_email: long })]);
    const td = container.querySelectorAll('tbody tr td')[4] as HTMLElement;
    expect(td.textContent!.length).toBeLessThan(long.length);
    expect(td.textContent!.endsWith('\u2026')).toBe(true);
    expect(td.title).toBe(long);
  });

  test('a short contact email is not truncated and has no title', async () => {
    await load([reg({})]);
    const td = container.querySelectorAll('tbody tr td')[4] as HTMLElement;
    expect(td.textContent).toBe('c@example.com');
    expect(td.title).toBe('');
  });

  test('copy button writes the external ID to the clipboard', async () => {
    const writeText = jest.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    await load([reg({})]);
    (container.querySelector('button[aria-label="Copy external ID 650412398877"]') as HTMLButtonElement).click();
    await flush();
    expect(writeText).toHaveBeenCalledWith('650412398877');
    expect(showToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'success' }));
  });

  test('copy falls back to execCommand when the clipboard API is missing', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
    const exec = jest.fn().mockReturnValue(true);
    (document as unknown as { execCommand: unknown }).execCommand = exec;
    await load([reg({})]);
    (container.querySelector('button[aria-label="Copy external ID 650412398877"]') as HTMLButtonElement).click();
    await flush();
    expect(exec).toHaveBeenCalledWith('copy');
    expect(showToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'success' }));
    expect(document.querySelector('textarea')).toBeNull();
  });

  test('copy failure is reported, not silent', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
    (document as unknown as { execCommand: unknown }).execCommand = jest.fn().mockReturnValue(false);
    await load([reg({})]);
    (container.querySelector('button[aria-label="Copy external ID 650412398877"]') as HTMLButtonElement).click();
    await flush();
    expect(showToast).toHaveBeenCalledWith(expect.objectContaining({ kind: 'error' }));
  });

  test('"Account created" sits in the Status cell, not the Actions cell', async () => {
    await load([reg({ status: 'approved' })]);
    const tds = container.querySelectorAll('tbody tr td');
    expect(tds[6]!.textContent).toContain('Account created');
    expect(tds[7]!.textContent).not.toContain('Account created');
  });

  test('bulk reject: partial failure names the failed rows and keeps them selected', async () => {
    await load([
      reg({ id: 'a', account_name: 'Alpha' }),
      reg({ id: 'b', account_name: 'Beta' }),
      reg({ id: 'c', account_name: 'Gamma' }),
    ]);
    box('Alpha').click();
    box('Beta').click();
    expect(bulkBtn().textContent).toBe('Reject 2');

    (confirmDialog as jest.Mock).mockResolvedValue(true);
    (api.rejectRegistration as jest.Mock).mockImplementation(async (id: string) => {
      if (id === 'b') throw new Error('boom');
    });
    // Server state after the call: only Beta (failed) and Gamma are still pending.
    (api.listRegistrations as jest.Mock).mockResolvedValue([
      reg({ id: 'b', account_name: 'Beta' }), reg({ id: 'c', account_name: 'Gamma' }),
    ]);
    bulkBtn().click();
    await flush();

    expect(api.rejectRegistration).toHaveBeenCalledTimes(2);
    expect(api.rejectRegistration).toHaveBeenNthCalledWith(1, 'a', undefined);
    expect(api.rejectRegistration).toHaveBeenNthCalledWith(2, 'b', undefined);
    expect(showToast).toHaveBeenCalledWith(expect.objectContaining({
      kind: 'error', message: expect.stringContaining('Beta'),
    }));
    expect((showToast as jest.Mock).mock.calls[0][0].message).not.toContain('Alpha');
    expect(box('Beta').checked).toBe(true);
    expect(box('Gamma').checked).toBe(false);
    expect(bulkBtn().textContent).toBe('Reject 1');
  });

  test('bulk reject sends the typed reason for every row and explains each failure', async () => {
    await load([reg({ id: 'a', account_name: 'Alpha' }), reg({ id: 'b', account_name: 'Beta' })]);
    box('Alpha').click();
    box('Beta').click();
    (confirmDialog as jest.Mock).mockImplementation(async (opts: { body: HTMLElement }) => {
      (opts.body.querySelector('textarea') as HTMLTextAreaElement).value = '  not ours ';
      return true;
    });
    (api.rejectRegistration as jest.Mock).mockImplementation(async (id: string) => {
      if (id === 'b') throw new Error('already reviewed');
    });
    bulkBtn().click();
    await flush();
    expect(api.rejectRegistration).toHaveBeenNthCalledWith(1, 'a', 'not ours');
    expect(api.rejectRegistration).toHaveBeenNthCalledWith(2, 'b', 'not ours');
    expect((showToast as jest.Mock).mock.calls[0][0].message).toContain('already reviewed');
  });

  test('toggling a checkbox keeps the same element (focus survives) and updates the bar', async () => {
    await load([reg({ id: 'a', account_name: 'Alpha' }), reg({ id: 'b', account_name: 'Beta' })]);
    const alpha = box('Alpha');
    const all = container.querySelector('input[data-select-all]') as HTMLInputElement;
    alpha.focus();
    alpha.click();
    expect(box('Alpha')).toBe(alpha);
    expect(document.activeElement).toBe(alpha);
    expect(bulkBtn().textContent).toBe('Reject 1');
    expect(all.checked).toBe(false);
    box('Beta').click();
    expect(all.checked).toBe(true);
    expect(bulkBtn().textContent).toBe('Reject 2');
  });

  test('bulk reject locks the button and checkboxes while requests are in flight', async () => {
    await load([reg({ id: 'a', account_name: 'Alpha' })]);
    box('Alpha').click();
    (confirmDialog as jest.Mock).mockResolvedValue(true);
    let release!: () => void;
    (api.rejectRegistration as jest.Mock).mockReturnValue(new Promise<void>(r => { release = r; }));
    (api.listRegistrations as jest.Mock).mockResolvedValue([]);
    bulkBtn().click();
    await flush();
    expect(bulkBtn().disabled).toBe(true);
    expect(box('Alpha').disabled).toBe(true);
    bulkBtn().click();
    await flush();
    expect(api.rejectRegistration).toHaveBeenCalledTimes(1);
    release();
    await flush();
    expect(api.rejectRegistration).toHaveBeenCalledTimes(1);
  });

  test('bulk reject does nothing when the confirm dialog is cancelled', async () => {
    await load([reg({ id: 'a', account_name: 'Alpha' })]);
    box('Alpha').click();
    (confirmDialog as jest.Mock).mockResolvedValue(false);
    bulkBtn().click();
    await flush();
    expect(api.rejectRegistration).not.toHaveBeenCalled();
    expect(box('Alpha').checked).toBe(true);
  });

  test('select-all ticks every pending row; non-pending rows have no checkbox', async () => {
    await load([reg({ id: 's1', account_name: 'Alpha' }), reg({ id: 'z', account_name: 'Zed', status: 'approved' })]);
    (container.querySelector('input[aria-label="Select all pending registrations"]') as HTMLInputElement).click();
    expect(box('Alpha').checked).toBe(true);
    expect(box('Zed')).toBeNull();
    expect(bulkBtn().textContent).toBe('Reject 1');
  });
});
