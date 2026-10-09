/**
 * Tests for the reusable confirmDialog helper.
 */

import { confirmDialog } from '../confirmDialog';

describe('confirmDialog', () => {
  afterEach(() => {
    const body = document.body;
    while (body.firstChild) body.removeChild(body.firstChild);
  });

  it('renders title + body text + close-X + cancel + confirm buttons', () => {
    void confirmDialog({ title: 'Delete?', body: 'Permanent action.', destructive: true });
    const dialog = document.querySelector('.modal-confirm')!;
    expect(dialog.querySelector('.modal-confirm-title')?.textContent).toBe('Delete?');
    expect(dialog.querySelector('.modal-confirm-body')?.textContent).toBe('Permanent action.');
    // close-X + cancel + confirm = 3 buttons in the default layout.
    const buttons = dialog.querySelectorAll('button');
    expect(buttons.length).toBe(3);
    expect(dialog.querySelector('.modal-confirm-close')).not.toBeNull();
  });

  it('hides the dismiss button when hideCancelButton is true but still renders close-X', () => {
    void confirmDialog({ title: 't', body: 'b', hideCancelButton: true });
    const dialog = document.querySelector('.modal-confirm')!;
    expect(dialog.querySelector('button.btn-secondary')).toBeNull();
    expect(dialog.querySelector('.modal-confirm-close')).not.toBeNull();
    expect(dialog.querySelector('button.btn-primary')).not.toBeNull();
  });

  it('resolves false when the close-X is clicked', async () => {
    const promise = confirmDialog({ title: 't', body: 'b', hideCancelButton: true });
    const closeBtn = document.querySelector<HTMLButtonElement>('.modal-confirm-close')!;
    closeBtn.click();
    await expect(promise).resolves.toBe(false);
    expect(document.querySelector('.modal-confirm')).toBeNull();
  });

  it('applies .btn-destructive to the confirm button when destructive is true', () => {
    void confirmDialog({ title: 't', body: 'b', destructive: true });
    const confirmBtn = document.querySelector('.modal-confirm button.btn-destructive');
    expect(confirmBtn).not.toBeNull();
  });

  it('applies .btn-primary to the confirm button when destructive is false', () => {
    void confirmDialog({ title: 't', body: 'b' });
    const confirmBtn = document.querySelector('.modal-confirm button.btn-primary');
    expect(confirmBtn).not.toBeNull();
  });

  it('resolves true when the confirm button is clicked', async () => {
    const promise = confirmDialog({ title: 't', body: 'b' });
    const confirmBtn = document.querySelector<HTMLButtonElement>('.modal-confirm button.btn-primary')!;
    confirmBtn.click();
    await expect(promise).resolves.toBe(true);
    expect(document.querySelector('.modal-confirm')).toBeNull();
  });

  it('resolves false when the cancel button is clicked', async () => {
    const promise = confirmDialog({ title: 't', body: 'b' });
    const cancelBtn = document.querySelector<HTMLButtonElement>('.modal-confirm button.btn-secondary')!;
    cancelBtn.click();
    await expect(promise).resolves.toBe(false);
    expect(document.querySelector('.modal-confirm')).toBeNull();
  });

  it('resolves false when ESC is pressed', async () => {
    const promise = confirmDialog({ title: 't', body: 'b' });
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await expect(promise).resolves.toBe(false);
  });

  it('resolves false when the backdrop is clicked', async () => {
    const promise = confirmDialog({ title: 't', body: 'b' });
    const backdrop = document.querySelector<HTMLDivElement>('.modal-confirm-backdrop')!;
    backdrop.click();
    await expect(promise).resolves.toBe(false);
  });

  it('honours custom confirm + cancel labels', () => {
    void confirmDialog({
      title: 't',
      body: 'b',
      confirmLabel: 'Reset all',
      cancelLabel: 'Keep them',
    });
    expect(document.querySelector('.btn-primary')?.textContent).toBe('Reset all');
    expect(document.querySelector('.btn-secondary')?.textContent).toBe('Keep them');
  });

  it('names the dialog by its title and describes it by its body', () => {
    void confirmDialog({ title: 'Delete?', body: 'Permanent action.' });
    const backdrop = document.querySelector('.modal-confirm-backdrop')!;
    const titleId = backdrop.getAttribute('aria-labelledby')!;
    const bodyId = backdrop.getAttribute('aria-describedby')!;
    expect(document.getElementById(titleId)?.textContent).toBe('Delete?');
    expect(document.getElementById(bodyId)?.textContent).toBe('Permanent action.');
  });

  it('focuses Cancel for destructive dialogs and Confirm otherwise', () => {
    void confirmDialog({ title: 't', body: 'b', destructive: true });
    expect(document.activeElement?.textContent).toBe('Cancel');
    document.body.replaceChildren();
    void confirmDialog({ title: 't', body: 'b' });
    expect(document.activeElement?.textContent).toBe('Confirm');
  });

  it('focuses Cancel when initialFocus is cancel even if not destructive', () => {
    void confirmDialog({ title: 't', body: 'b', initialFocus: 'cancel' });
    expect(document.activeElement?.textContent).toBe('Cancel');
  });

  it('focuses the close-X when the cancel button is hidden and focus is cancel', () => {
    void confirmDialog({ title: 't', body: 'b', destructive: true, hideCancelButton: true });
    expect(document.activeElement?.classList.contains('modal-confirm-close')).toBe(true);
  });

  it('cycles focus over all focusables in DOM order, including body inputs', () => {
    const body = document.createElement('div');
    const ta = document.createElement('textarea');
    body.appendChild(ta);
    void confirmDialog({ title: 't', body, destructive: true });
    const tab = (shiftKey = false): void => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey }));
    };
    // DOM order: close-X, textarea, Cancel, Confirm; focus starts on Cancel.
    tab(true);
    expect(document.activeElement).toBe(ta);
    tab(true);
    expect(document.activeElement?.classList.contains('modal-confirm-close')).toBe(true);
    tab();
    expect(document.activeElement).toBe(ta);
    tab(); tab();
    expect(document.activeElement?.textContent).toBe('Confirm');
    tab();
    expect(document.activeElement?.classList.contains('modal-confirm-close')).toBe(true);
  });
});
