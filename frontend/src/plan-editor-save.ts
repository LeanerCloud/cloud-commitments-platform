import { planEditorFailure, previewPlanEditor, type PlanEditorFailure, type PlanEditorResponse } from './api/plan-editor';
import type { PlanEditorEditHandle } from './plan-editor-edit-panel';
import type { EditFormModel, EditFormPatch } from './lib/plan-editor-form';
import { isSavingsPlanSlug, resolvePlanService } from './lib/plan-service-ids';
import { canReviewEditorDraft, draftDateChanges } from './lib/plan-editor-draft';
import { buildEditRequest, serializeRequest } from './lib/plan-editor-request';
import { newUuid } from './lib/uuid';
import { createReviewRegion, plural, type ReviewAcknowledgments, type ReviewRegion } from './plan-editor-review';

export interface PlanReviewDeps {
  readonly response: PlanEditorResponse;
  readonly host: HTMLElement;
  readonly button: HTMLButtonElement;
  readonly edit: PlanEditorEditHandle;
  readonly form: EditFormModel;
  // The dialog session counter: any open, close or reload bumps it, which drops a late preview.
  readonly generation: () => number;
}
// ready() is the acknowledged "ready to save" state E4b-3 reads; it is null until every required acknowledgment is checked.
export interface PlanReviewHandle { review(): void; ready(): ReviewAcknowledgments | null; dispose(): void }

const previewUncertainText = 'The preview did not complete. Nothing was saved; try Review changes again.';
export const previewFailureText = (failure: PlanEditorFailure): string => failure.kind === 'uncertain' ? previewUncertainText : failure.message;

// An enabled stored service the editor cannot handle makes the whole view read-only (root ACK Q4). Disabled entries are preserved untouched.
export function unsupportedServiceBlocker(response: PlanEditorResponse): string | null {
  for (const item of Object.values(response.plan.services ?? {})) {
    if (item.enabled && !isSavingsPlanSlug(item.service) && resolvePlanService(item.provider, item.service).state === 'unsupported') {
      return `This plan uses a service the editor does not support (${item.provider}/${item.service}). It cannot be saved here.`;
    }
  }
  return null;
}

export function saveBlockers(response: PlanEditorResponse, patch: EditFormPatch): string | null {
  const unsupported = unsupportedServiceBlocker(response);
  if (unsupported) return unsupported;
  const services = Object.values(response.plan.services ?? {});
  if (response.plan.ramp_schedule.total_steps > 1 && services.some(item => item.enabled && isSavingsPlanSlug(item.service))) {
    return 'Savings Plans cannot be laddered, so a plan with more than one step cannot be saved here.';
  }
  if ((patch.target_accounts ?? response.account_ids).length === 0) return 'Choose at least one target account.';
  if (patch.plan['auto_purchase'] === true && response.plan.max_step_commitment === null) return 'Automatic purchase needs a per-step cap, which cannot be set yet';
  return null;
}

export function mountPlanReview(deps: PlanReviewDeps): PlanReviewHandle {
  const { response, host, button, edit, form } = deps;
  const original = { label: button.textContent, disabled: button.disabled, describedBy: button.getAttribute('aria-describedby') };
  // In the dialog footer, which stays on screen while the editor scrolls.
  const feedback = document.createElement('div'), totals = document.createElement('p'), alert = document.createElement('p'), reason = document.createElement('p');
  feedback.id = 'plan-review-feedback'; totals.setAttribute('role', 'status'); alert.setAttribute('role', 'alert'); reason.className = 'help-text'; reason.id = 'plan-review-reason';
  feedback.append(totals, alert, reason);
  (button.closest('.modal-buttons') ?? host).before(feedback);
  let intentKey = '', commandId = newUuid(), pending: { key: string; abort: AbortController } | undefined;
  let disposed = false;

  const current = (): { key: string; body: string } | { blocked: string } => {
    const blocker = saveBlockers(response, form.patch());
    if (blocker) return { blocked: blocker };
    const draft = edit.draft();
    const built = buildEditRequest({ response, formPatch: form.patch(), draft, commandId });
    if ('unavailable' in built) return { blocked: built.unavailable };
    if (draft && !canReviewEditorDraft(draft)) {
      return { blocked: host.querySelector('[data-edit="status"]')?.textContent || host.querySelector('[data-edit="error"]')?.textContent || 'Fix the draft dates before reviewing.' };
    }
    const unchanged = Object.keys(built.plan).length === 0 && (draft === null || draftDateChanges(draft).length === 0);
    if (unchanged) return { blocked: 'No changes to review' };
    return { key: serializeRequest({ ...built, command_id: '' }), body: serializeRequest(built) };
  };
  let region: ReviewRegion | undefined;
  const clearRegion = (): void => {
    if (!region) return;
    const hadFocus = region.element.contains(document.activeElement);
    region.element.remove(); region = undefined;
    if (hadFocus) button.focus();
  };
  const supersede = (): void => { pending?.abort.abort(); pending = undefined; totals.textContent = ''; alert.textContent = ''; clearRegion(); };
  const sync = (): void => {
    if (disposed) return;
    const now = current(), key = 'key' in now ? now.key : '';
    if (key !== intentKey) { supersede(); intentKey = key; commandId = newUuid(); }
    button.textContent = 'Review changes';
    button.disabled = 'blocked' in now;
    reason.textContent = 'blocked' in now ? now.blocked : '';
    if ('blocked' in now) button.setAttribute('aria-describedby', reason.id); else button.removeAttribute('aria-describedby');
  };
  const review = (): void => {
    if (disposed) return;
    sync();
    const now = current();
    if ('blocked' in now || pending?.key === now.key) return;
    clearRegion(); totals.textContent = ''; alert.textContent = '';
    const generation = deps.generation(), abort = new AbortController();
    const fresh = (): boolean => !disposed && !abort.signal.aborted && generation === deps.generation();
    void previewPlanEditor(response.plan.id, now.body, abort.signal).then(result => {
      if (!fresh()) return;
      const t = result.diff.totals;
      totals.textContent = `${plural(t.moved, 'date', 'dates')} moved; ${plural(t.canceled, 'step', 'steps')} removed; ${plural(t.approvals_affected, 'approval', 'approvals')} affected`;
      region = createReviewRegion(result);
      const anchor = host.querySelector('.schedule-preview-scroll');
      if (anchor) anchor.after(region.element); else host.append(region.element);
      region.heading.focus({ preventScroll: true });
      // Bring the actions (checkboxes, Confirm, status) into view; jsdom has no scrollIntoView.
      region.element.scrollIntoView?.({ block: 'end' });
    }, (error: unknown) => {
      if (!fresh()) return;
      alert.textContent = previewFailureText(planEditorFailure(error));
    }).finally(() => { if (pending?.abort === abort) pending = undefined; });
    pending = { key: now.key, abort };
  };

  const readOnly = unsupportedServiceBlocker(response);
  if (readOnly) { edit.setReadOnly(readOnly); form.lockAll(readOnly); }
  const unsubscribe = [edit.subscribe(sync), form.onChange(sync)];
  sync();
  return {
    review,
    ready: () => region?.ready() ?? null,
    dispose: () => {
      disposed = true; region?.element.remove(); pending?.abort.abort(); unsubscribe.forEach(off => off()); feedback.remove();
      button.textContent = original.label; button.disabled = original.disabled;
      if (original.describedBy === null) button.removeAttribute('aria-describedby'); else button.setAttribute('aria-describedby', original.describedBy);
    },
  };
}
