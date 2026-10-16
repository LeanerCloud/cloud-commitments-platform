import { planEditorFailure, previewPlanEditor, type PlanEditorFailure, type PlanEditorResponse } from './api/plan-editor';
import type { PlanEditorEditHandle } from './plan-editor-edit-panel';
import type { EditFormModel } from './lib/plan-editor-form';
import { canReviewEditorDraft, draftDateChanges } from './lib/plan-editor-draft';
import { buildEditRequest, serializeRequest } from './lib/plan-editor-request';
import { newUuid } from './lib/uuid';

export interface PlanReviewDeps {
  readonly response: PlanEditorResponse;
  readonly host: HTMLElement;
  readonly button: HTMLButtonElement;
  readonly edit: PlanEditorEditHandle;
  readonly form: EditFormModel;
  // The dialog session counter: any open, close or reload bumps it, which drops a late preview.
  readonly generation: () => number;
}
export interface PlanReviewHandle { review(): void; dispose(): void }

const previewUncertainText = 'The preview did not complete. Nothing was saved; try Review changes again.';
export const previewFailureText = (failure: PlanEditorFailure): string => failure.kind === 'uncertain' ? previewUncertainText : failure.message;

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
  const supersede = (): void => { pending?.abort.abort(); pending = undefined; totals.textContent = ''; alert.textContent = ''; };
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
    const generation = deps.generation(), abort = new AbortController();
    const fresh = (): boolean => !disposed && !abort.signal.aborted && generation === deps.generation();
    void previewPlanEditor(response.plan.id, now.body, abort.signal).then(result => {
      if (!fresh()) return;
      const t = result.diff.totals;
      totals.textContent = `${t.moved} dates moved; ${t.canceled} steps removed; ${t.approvals_affected} approvals affected`;
    }, (error: unknown) => {
      if (!fresh()) return;
      alert.textContent = previewFailureText(planEditorFailure(error));
    }).finally(() => { if (pending?.abort === abort) pending = undefined; });
    pending = { key: now.key, abort };
  };

  const unsubscribe = [edit.subscribe(sync), form.onChange(sync)];
  sync();
  return {
    review,
    dispose: () => {
      disposed = true; pending?.abort.abort(); unsubscribe.forEach(off => off()); feedback.remove();
      button.textContent = original.label; button.disabled = original.disabled;
      if (original.describedBy === null) button.removeAttribute('aria-describedby'); else button.setAttribute('aria-describedby', original.describedBy);
    },
  };
}
