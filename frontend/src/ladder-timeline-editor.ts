import { amendLadderEvent, type LadderEvent, type LadderRun } from './api/ladder';
import { openModal, closeModal } from './modal';
import { escapeHtml } from './utils';
import { formatLadderUnits, ladderAmountUnits, ladderTotal } from './ladder-timeline-filters';

export function openLadderEventEditor(event: LadderEvent, run: LadderRun, siblings: readonly LadderEvent[], onSaved: () => void): void {
  const modal = document.createElement('div');
  modal.className = 'modal hidden ladder-event-editor';
  modal.setAttribute('role', 'dialog');
  modal.setAttribute('aria-modal', 'true');
  modal.setAttribute('aria-labelledby', 'ladder-event-title');
  modal.innerHTML = `<div class="modal-content"><div class="modal-header">
    <h3 id="ladder-event-title">Edit planned purchase</h3></div><form>
    <p>${escapeHtml(event.layer_type)} / ${escapeHtml(event.term)} / ${escapeHtml(event.payment_option)}</p>
    <p>Original plan: ${escapeHtml(run.started_at)}.
      ${run.actions.filter(action => action.layer === event.layer_type).map(action => `${escapeHtml(action.rationale)}${action.data_sources?.length ? ` Sources: ${escapeHtml(action.data_sources.join(', '))}.` : ''}`).join(' ')}</p>
    <p>These are planning budgets. Purchases are not executed by this editor.</p>
    <div class="form-row"><label for="ladder-event-date">Scheduled date and time (UTC)</label>
      <input id="ladder-event-date" type="datetime-local" step="1" required></div>
    <div class="form-row"><label for="ladder-event-amount">Planning budget (USD/hour)</label>
      <input id="ladder-event-amount" inputmode="decimal" required></div>
    <p id="ladder-event-preview" aria-live="polite"></p>
    <p>Original run ceiling: ${escapeHtml(run.total_hourly_commit)} USD/hour.
      The server also enforces the current per-run cap.</p>
    <p>Reducing a tranche can cause the next planning run to add it back.
      Change target coverage in account settings for a lasting target reduction.</p>
    <p id="ladder-event-error" role="alert"></p>
    <div class="modal-footer"><button type="button" class="btn btn-secondary" data-cancel>Cancel</button>
      <button type="button" class="btn btn-secondary" data-reset>Undo draft changes</button>
      <button type="submit" class="btn btn-primary">Save planned purchase</button></div>
    </form></div>`;
  document.body.append(modal);
  const form = modal.querySelector<HTMLFormElement>('form')!;
  const date = modal.querySelector<HTMLInputElement>('#ladder-event-date')!;
  const amount = modal.querySelector<HTMLInputElement>('#ladder-event-amount')!;
  const preview = modal.querySelector<HTMLElement>('#ladder-event-preview')!;
  const error = modal.querySelector<HTMLElement>('#ladder-event-error')!;
  const save = modal.querySelector<HTMLButtonElement>('button[type="submit"]')!;
  const cancel = modal.querySelector<HTMLButtonElement>('[data-cancel]')!;
  let pending = false;
  const initialDate = new Date(event.scheduled_date).toISOString().slice(0, 19);
  const originalTotal = ladderAmountUnits(ladderTotal(siblings));
  const restore = (): void => {
    date.value = initialDate;
    amount.value = event.amount_usd_hr;
    error.textContent = '';
    updatePreview();
  };
  const updatePreview = (): void => {
    try {
      const units = ladderAmountUnits(amount.value);
      const total = originalTotal - ladderAmountUnits(event.amount_usd_hr) + units;
      preview.textContent = `Current run total after this change: ${formatLadderUnits(total)} USD/hour.`;
      save.disabled = pending || units <= 0n || total > ladderAmountUnits(run.total_hourly_commit);
    } catch {
      preview.textContent = 'Enter a positive decimal with up to six fractional digits.';
      save.disabled = true;
    }
  };
  restore();
  const initialControlDate = date.value;
  amount.addEventListener('input', updatePreview);
  modal.querySelector('[data-reset]')!.addEventListener('click', restore);
  modal.querySelector('[data-cancel]')!.addEventListener('click', () => { if (!pending) closeModal(modal); });
  modal.addEventListener('keydown', e => {
    if (pending && e.key === 'Escape') {
      e.preventDefault();
      e.stopImmediatePropagation();
    }
  }, true);
  form.addEventListener('submit', async e => {
    e.preventDefault();
    if (pending || save.disabled || !form.reportValidity()) return;
    const proposedDate = date.value === initialControlDate ? event.scheduled_date : `${date.value}Z`;
    const proposed = new Date(proposedDate);
    if (!Number.isFinite(proposed.getTime()) || proposed.getTime() <= Date.now()) {
      error.textContent = 'Choose a future date and time in UTC.';
      return;
    }
    pending = true;
    form.querySelectorAll<HTMLInputElement | HTMLButtonElement>('input,button').forEach(input => { input.disabled = true; });
    cancel.disabled = false;
    cancel.setAttribute('aria-disabled', 'true');
    cancel.textContent = 'Save in progress';
    cancel.focus();
    error.textContent = 'Saving planned purchase...';
    try {
      await amendLadderEvent(event.id, { expected_revision: event.revision, scheduled_date: date.value === initialControlDate ? event.scheduled_date : proposed.toISOString(), amount_usd_hr: amount.value });
      closeModal(modal);
      onSaved();
    } catch (err) {
      error.textContent = `${err instanceof Error ? err.message : 'Save failed'}. Your draft is retained. Reload the timeline before retrying a changed plan.`;
    } finally {
      pending = false;
      form.querySelectorAll<HTMLInputElement | HTMLButtonElement>('input,button').forEach(input => { input.disabled = false; });
      cancel.removeAttribute('aria-disabled');
      cancel.textContent = 'Cancel';
      updatePreview();
    }
  });
  openModal(modal, { initialFocus: '#ladder-event-date', onClose: () => modal.remove() });
}
