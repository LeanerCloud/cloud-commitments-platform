import { planEditorFailure, previewPlanEditor, savePlanEditor, type PlanEditorFailure, type PlanEditorRequest, type PlanEditorResponse, type PlanEditorSaveResponse } from './api/plan-editor';
import type { PlanEditorEditHandle } from './plan-editor-edit-panel';
import type { EditFormModel, EditFormPatch } from './lib/plan-editor-form';
import { isSavingsPlanSlug, resolvePlanService } from './lib/plan-service-ids';
import { canReviewEditorDraft, draftDateChanges } from './lib/plan-editor-draft';
import { buildEditRequest, serializeRequest, withReview } from './lib/plan-editor-request';
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
  // Called once a Save succeeded; `open` is false when the dialog changed while it was in flight (then nothing else runs).
  readonly onSaved: (saved: PlanEditorSaveResponse, open: boolean) => void;
  // Rebuilds the dialog from a fresh GET (which disposes this module); resolves false when the GET failed.
  readonly reload: (saved: PlanEditorSaveResponse) => Promise<boolean>;
  // [summary, mapping] shown in the footer of a freshly rebuilt dialog after a Save.
  readonly notice?: readonly string[];
}
// ready() is the acknowledged "ready to save" state E4b-3 reads; it is null until every required acknowledgment is checked.
export interface PlanReviewHandle { review(): void; ready(): ReviewAcknowledgments | null; dispose(): void }

const previewUncertainText = 'The preview did not complete. Nothing was saved; try Review changes again.';
const reloadFailedText = 'Saved, but reloading failed. Reload to keep editing.';
// Summary line, then one compact line of what became of each purchase (dates only, never raw ids).
export const savedLines = (saved: PlanEditorSaveResponse): string[] => {
  const t = saved.diff.totals, changes = saved.mapping.flatMap(item => item.new_execution_id !== null
    ? [`step ${item.step_number} ${item.old_execution_id !== null ? 'replaced' : 'created'}, now ${item.scheduled_date ?? 'undated'}`]
    : item.old_execution_id !== null ? [`step ${item.step_number} canceled`] : []);
  return [`${saved.replayed ? 'Already applied.' : 'Saved.'} ${plural(t.moved, 'date', 'dates')} moved; ${plural(t.canceled, 'step', 'steps')} removed; ${plural(t.approvals_affected, 'approval', 'approvals')} affected.`,
    changes.length ? `${plural(changes.length, 'purchase', 'purchases')} changed: ${changes.join('; ')}.` : ''];
};
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
  const feedback = document.createElement('div'), mapping = document.createElement('p'), totals = document.createElement('p'), alert = document.createElement('p'), reason = document.createElement('p');
  feedback.id = 'plan-review-feedback'; totals.setAttribute('role', 'status'); alert.setAttribute('role', 'alert'); reason.className = 'help-text'; reason.id = 'plan-review-reason';
  mapping.className = 'help-text'; mapping.id = 'plan-review-mapping'; feedback.append(totals, mapping, alert, reason);
  (button.closest('.modal-buttons') ?? host).before(feedback);
  let intentKey = '', commandId = newUuid(), pending: { key: string; abort: AbortController } | undefined;
  let disposed = false;

  const current = (): { key: string; body: string; request: PlanEditorRequest } | { blocked: string } => {
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
    return { key: serializeRequest({ ...built, command_id: '' }), body: serializeRequest(built), request: built };
  };
  const showLines = (lines: readonly string[]): void => { totals.textContent = lines[0] ?? ''; mapping.textContent = lines[1] ?? ''; };
  if (deps.notice) showLines(deps.notice);
  // The reviewed request and, once Save starts, the one immutable command (bytes included) every retry of it resends.
  let region: ReviewRegion | undefined, reviewed: PlanEditorRequest | undefined, saving = false;
  let frozen: { readonly bodyJson: string } | undefined, restoreInert: (() => void) | undefined;
  const clearRegion = (): void => {
    if (!region) return;
    const hadFocus = region.element.contains(document.activeElement);
    region.element.remove(); region = undefined;
    if (hadFocus) button.focus();
  };
  const supersede = (): void => { pending?.abort.abort(); pending = undefined; totals.textContent = ''; mapping.textContent = ''; alert.textContent = ''; clearRegion(); frozen = undefined; };
  const sync = (): void => {
    if (disposed || saving) return;
    const now = current(), key = 'key' in now ? now.key : '';
    if (key !== intentKey) { supersede(); intentKey = key; commandId = newUuid(); }
    button.textContent = region?.ready() ? 'Save changes' : 'Review changes';
    button.disabled = 'blocked' in now;
    reason.textContent = 'blocked' in now ? now.blocked : '';
    if ('blocked' in now) button.setAttribute('aria-describedby', reason.id); else button.removeAttribute('aria-describedby');
  };
  // The Save is done; the dialog stays read-only until a fresh GET rebuilds it (which disposes this module).
  const retry = document.createElement('button');
  retry.type = 'button'; retry.textContent = 'Reload'; retry.id = 'plan-review-reload';
  const reload = async (saved: PlanEditorSaveResponse): Promise<void> => {
    retry.disabled = true;
    retry.onclick = () => void reload(saved);
    const ok = await deps.reload(saved);
    if (ok || disposed) return;
    button.textContent = 'Review changes'; button.disabled = true; alert.textContent = reloadFailedText;
    retry.disabled = false; alert.after(retry); feedback.classList.add('needs-reload');
  };
  const save = (): void => {
    const acks = region?.ready(), layout = host.closest<HTMLElement>('.plan-editor-layout') ?? host;
    if (!acks || !reviewed || saving) return;
    frozen ??= Object.freeze({ bodyJson: serializeRequest(withReview(reviewed, acks.reviewedDiff, acks.cancellationIds, acks.reapprovalIds)) });
    const generation = deps.generation(), wasInert = layout.hasAttribute('inert'), sent = frozen;
    saving = true; layout.setAttribute('inert', ''); button.disabled = true; button.textContent = 'Saving...'; alert.textContent = '';
    const unlock = (): void => { saving = false; restoreInert?.(); restoreInert = undefined; };
    restoreInert = () => { layout.toggleAttribute('inert', wasInert); };
    void savePlanEditor(response.plan.id, sent.bodyJson).then(saved => {
      const open = !disposed && generation === deps.generation();
      if (!open) { deps.onSaved(saved, false); return; }
      showLines(savedLines(saved)); clearRegion(); frozen = undefined; reviewed = undefined;
      deps.onSaved(saved, true);
      void reload(saved);
    }, (error: unknown) => {
      if (disposed) return;
      unlock();
      const failure = planEditorFailure(error);
      alert.textContent = failure.message;
      // An uncertain outcome keeps the frozen command so the retry is byte-identical; any other failure needs a new review.
      if (failure.kind !== 'uncertain') { clearRegion(); frozen = undefined; }
      sync();
    });
  };
  const review = (): void => {
    if (disposed || saving) return;
    if (region?.ready()) { save(); return; }
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
      reviewed = now.request;
      region = createReviewRegion(result, sync);
      const anchor = host.querySelector('.schedule-preview-scroll');
      if (anchor) anchor.after(region.element); else host.append(region.element);
      region.heading.focus({ preventScroll: true });
      // Bring the actions (checkboxes, status) into view; jsdom has no scrollIntoView.
      region.element.scrollIntoView?.({ block: 'end' });
      sync();
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
      disposed = true; restoreInert?.(); region?.element.remove(); pending?.abort.abort(); unsubscribe.forEach(off => off()); feedback.remove();
      button.textContent = original.label; button.disabled = original.disabled;
      if (original.describedBy === null) button.removeAttribute('aria-describedby'); else button.setAttribute('aria-describedby', original.describedBy);
    },
  };
}
