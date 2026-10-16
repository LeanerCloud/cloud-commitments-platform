import { describeFailure } from './plan-editor-errors';
import { planEditorFailure, previewPlanEditor, savePlanEditor, type PlanEditorRequest, type PlanEditorResponse, type PlanEditorSaveResponse } from './api/plan-editor';
import type { PlanEditorEditHandle } from './plan-editor-edit-panel';
import type { EditFormModel, EditFormPatch } from './lib/plan-editor-form';
import { isSavingsPlanSlug, resolvePlanService } from './lib/plan-service-ids';
import { canReviewEditorDraft, draftDateChanges } from './lib/plan-editor-draft';
import { buildEditRequest, serializeRequest, withReview } from './lib/plan-editor-request';
import { newUuid } from './lib/uuid';
import { createReviewRegion, plural, type ReviewAcknowledgments, type ReviewRegion } from './plan-editor-review';

// What the review/Save lifecycle needs; Edit and Create each supply their own request source.
export interface ReviewCore {
  readonly host: HTMLElement;
  readonly button: HTMLButtonElement;
  readonly planId: () => string;
  // The request for the current form and draft, or the reason it cannot be built. `commandId` is the frozen id of this intent.
  readonly build: (commandId: string) => { readonly request: PlanEditorRequest } | { readonly blocked: string };
  readonly subscribe: (listener: () => void) => Array<() => void>;
  // Create has no plan to reload and its 404 is a missing account, not a missing plan.
  readonly create?: boolean;
  // Edit only: what a reload would discard, as notice lines for the rebuilt dialog.
  readonly discarded?: () => readonly string[];
  // Edit only: re-fetches the editor GET and rebuilds the dialog (which disposes this module), listing `notice`; resolves false when the GET failed.
  readonly reloadFresh?: (notice: readonly string[]) => Promise<boolean>;
  // Edit only: the plan is gone, so the list needs a reload.
  readonly onGone?: () => void;
  // The dialog session counter: any open, close or reload bumps it, which drops a late preview.
  readonly generation: () => number;
  // Called once a Save succeeded; `open` is false when the dialog changed while it was in flight (then nothing else runs).
  readonly onSaved: (saved: PlanEditorSaveResponse, open: boolean) => void;
  // Rebuilds the dialog from a fresh GET (which disposes this module); resolves false when the GET failed.
  readonly reload: (saved: PlanEditorSaveResponse) => Promise<boolean>;
  // [summary, mapping] shown in the footer of a freshly rebuilt dialog after a Save.
  readonly notice?: readonly string[];
}
export interface PlanReviewDeps extends Omit<ReviewCore, 'planId' | 'build' | 'subscribe' | 'create' | 'discarded'> {
  readonly response: PlanEditorResponse;
  readonly edit: PlanEditorEditHandle;
  readonly form: EditFormModel;
}
// ready() is the acknowledged "ready to save" state E4b-3 reads; it is null until every required acknowledgment is checked.
export interface PlanReviewHandle { review(): void; ready(): ReviewAcknowledgments | null; refresh(): void; dispose(): void }

const reloadFailedText = 'Saved, but reloading failed. Reload to keep editing.';
// Summary line, then one compact line of what became of each purchase (dates only, never raw ids).
export const savedLines = (saved: PlanEditorSaveResponse): string[] => {
  const t = saved.diff.totals, changes = saved.mapping.flatMap(item => item.new_execution_id !== null
    ? [`step ${item.step_number} ${item.old_execution_id !== null ? 'replaced' : 'created'}, now ${item.scheduled_date ?? 'undated'}`]
    : item.old_execution_id !== null ? [`step ${item.step_number} canceled`] : []);
  return [`${saved.replayed ? 'Already applied.' : 'Saved.'} ${totalsText(t)}.`,
    changes.length ? `${plural(changes.length, 'purchase', 'purchases')} changed: ${changes.join('; ')}.` : ''];
};
// Created steps lead the line only when there are any, so an Edit without new steps reads as before.
const totalsText = (t: { moved: number; created: number; canceled: number; approvals_affected: number }): string =>
  `${t.created > 0 ? `${plural(t.created, 'step', 'steps')} created; ` : ''}${plural(t.moved, 'date', 'dates')} moved; ${plural(t.canceled, 'step', 'steps')} removed; ${plural(t.approvals_affected, 'approval', 'approvals')} affected`;

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
  const { response, host, edit, form } = deps;
  const readOnly = unsupportedServiceBlocker(response);
  if (readOnly) { edit.setReadOnly(readOnly); form.lockAll(readOnly); }
  // What a reload would throw away, so the new dialog can say so instead of dropping it silently.
  const discarded = (): string[] => {
    const dates = edit.draft(), count = dates ? draftDateChanges(dates).length : 0, patch = form.patch();
    const others = [Object.keys(patch.plan).length > 0 ? 'plan settings' : '', Object.values(patch.services).some(fields => Object.keys(fields).length > 0) ? 'service settings' : '',
      patch.target_accounts !== undefined ? 'target accounts' : ''].filter(Boolean);
    const named = others.length > 1 ? `${others.slice(0, -1).join(', ')} and ${others[others.length - 1]}` : others.join('');
    const parts = [count > 0 ? plural(count, 'date change', 'date changes') : '', named ? `${count > 0 ? 'plus ' : ''}changes to ${named}` : ''].filter(Boolean);
    return parts.length ? [`Not applied: ${parts.join(', ')}.`] : [];
  };
  return mountReview({ ...deps, planId: () => response.plan.id, discarded,
    subscribe: sync => [edit.subscribe(sync), form.onChange(sync)],
    build: commandId => {
      const blocker = saveBlockers(response, form.patch());
      if (blocker) return { blocked: blocker };
      const draft = edit.draft();
      const built = buildEditRequest({ response, formPatch: form.patch(), draft, commandId });
      if ('unavailable' in built) return { blocked: built.unavailable };
      if (draft && !canReviewEditorDraft(draft)) {
        return { blocked: host.querySelector('[data-edit="status"]')?.textContent || host.querySelector('[data-edit="error"]')?.textContent || 'Fix the draft dates before reviewing.' };
      }
      const unchanged = Object.keys(built.plan).length === 0 && (draft === null || draftDateChanges(draft).length === 0);
      return unchanged ? { blocked: 'No changes to review' } : { request: built };
    } });
}

export function mountReview(core: ReviewCore): PlanReviewHandle {
  const { host, button } = core, deps = core;
  const original = { label: button.textContent, disabled: button.disabled, describedBy: button.getAttribute('aria-describedby') };
  // In the dialog footer, which stays on screen while the editor scrolls.
  const feedback = document.createElement('div'), mapping = document.createElement('p'), totals = document.createElement('p'), alert = document.createElement('p'), reason = document.createElement('p');
  feedback.id = 'plan-review-feedback'; totals.setAttribute('role', 'status'); alert.setAttribute('role', 'alert'); feedback.tabIndex = 0; feedback.setAttribute('role', 'group'); feedback.setAttribute('aria-label', 'Review and save feedback'); reason.className = 'help-text'; reason.id = 'plan-review-reason'; reason.setAttribute('aria-live', 'polite');
  mapping.className = 'help-text'; mapping.id = 'plan-review-mapping'; feedback.append(totals, mapping, alert, reason);
  (button.closest('.modal-buttons') ?? host).before(feedback);
  // The one action button under the alert: Reload after a Save, Reload changes after a refusal.
  const retry = document.createElement('button');
  retry.type = 'button'; retry.textContent = 'Reload'; retry.id = 'plan-review-reload';
  let halted = false;
  let intentKey = '', commandId = newUuid(), pending: { key: string; abort: AbortController } | undefined;
  let disposed = false;

  const current = (): { key: string; body: string; request: PlanEditorRequest } | { blocked: string } => {
    const built = core.build(commandId);
    if ('blocked' in built) return built;
    return { key: serializeRequest({ ...built.request, command_id: '' }), body: serializeRequest(built.request), request: built.request };
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
  const supersede = (): void => { pending?.abort.abort(); pending = undefined; totals.textContent = ''; mapping.textContent = ''; alert.textContent = ''; retry.remove(); clearRegion(); frozen = undefined; };
  const sync = (): void => {
    if (disposed || saving || halted) return;
    const now = current(), key = 'key' in now ? now.key : '';
    if (key !== intentKey) { supersede(); intentKey = key; commandId = newUuid(); }
    button.textContent = region?.ready() ? 'Save changes' : 'Review changes';
    button.disabled = 'blocked' in now;
    reason.textContent = 'blocked' in now ? now.blocked : '';
    if ('blocked' in now) button.setAttribute('aria-describedby', reason.id); else button.removeAttribute('aria-describedby');
  };
  const reload = async (saved: PlanEditorSaveResponse): Promise<void> => {
    retry.disabled = true; retry.textContent = 'Reload';
    retry.onclick = () => void reload(saved);
    const ok = await deps.reload(saved);
    if (ok || disposed) return;
    button.textContent = 'Review changes'; button.disabled = true; alert.textContent = reloadFailedText;
    retry.disabled = false; alert.after(retry); feedback.classList.add('needs-reload');
  };
  // Shows the table's sentence, then does its action. The frozen request survives only where a same-bytes resend is safe.
  const fail = (error: unknown, phase: 'preview' | 'save'): void => {
    const failure = planEditorFailure(error), view = describeFailure(failure, phase, core.create === true);
    alert.textContent = view.text; retry.remove(); feedback.classList.remove('needs-reload');
    if (view.action !== 'retry') { clearRegion(); frozen = undefined; }
    const halt = (): void => { halted = true; button.textContent = 'Review changes'; button.disabled = true; };
    if (view.action === 'sign-in') halt();
    else if (view.action === 'gone') { halt(); (host.closest<HTMLElement>('.plan-editor-layout') ?? host).setAttribute('inert', ''); core.onGone?.(); }
    else if (view.action === 'reload' && core.reloadFresh) {
      retry.textContent = 'Reload changes'; retry.disabled = false; alert.after(retry); feedback.classList.add('needs-reload');
      retry.onclick = () => { retry.disabled = true; void core.reloadFresh!(core.discarded?.() ?? []).then(ok => { if (!ok && !disposed) retry.disabled = false; }); };
    } else if (view.action === 'focus-date') host.querySelector<HTMLElement>('[data-edit="date"], [data-editor="date"]')?.focus();
    if (!halted) sync();
    if (view.action === 'review') { commandId = newUuid(); start(true); }
  };
  const save = (): void => {
    const acks = region?.ready(), layout = host.closest<HTMLElement>('.plan-editor-layout') ?? host;
    if (!acks || !reviewed || saving) return;
    frozen ??= Object.freeze({ bodyJson: serializeRequest(withReview(reviewed, acks.reviewedDiff, acks.cancellationIds, acks.reapprovalIds)) });
    const generation = deps.generation(), wasInert = layout.hasAttribute('inert'), sent = frozen;
    saving = true; layout.setAttribute('inert', ''); button.disabled = true; button.textContent = 'Saving...'; alert.textContent = '';
    const unlock = (): void => { saving = false; restoreInert?.(); restoreInert = undefined; };
    restoreInert = () => { layout.toggleAttribute('inert', wasInert); };
    void savePlanEditor(core.planId(), sent.bodyJson).then(saved => {
      const open = !disposed && generation === deps.generation();
      if (!open) { deps.onSaved(saved, false); return; }
      showLines(savedLines(saved)); clearRegion(); frozen = undefined; reviewed = undefined;
      deps.onSaved(saved, true);
      void reload(saved);
    }, (error: unknown) => {
      if (disposed) return;
      unlock();
      fail(error, 'save');
    });
  };
  const review = (): void => { start(false); };
  const start = (keepAlert: boolean): void => {
    if (disposed || saving || halted) return;
    if (!keepAlert && region?.ready()) { save(); return; }
    sync();
    const now = current();
    if ('blocked' in now || pending?.key === now.key) return;
    clearRegion(); totals.textContent = ''; if (!keepAlert) { alert.textContent = ''; retry.remove(); }
    const generation = deps.generation(), abort = new AbortController();
    const fresh = (): boolean => !disposed && !abort.signal.aborted && generation === deps.generation();
    void previewPlanEditor(core.planId(), now.body, abort.signal).then(result => {
      if (!fresh()) return;
      const t = result.diff.totals;
      totals.textContent = totalsText(t);
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
      fail(error, 'preview');
    }).finally(() => { if (pending?.abort === abort) pending = undefined; });
    pending = { key: now.key, abort };
  };

  const unsubscribe = core.subscribe(sync);
  sync();
  return {
    review, refresh: sync,
    ready: () => region?.ready() ?? null,
    dispose: () => {
      disposed = true; restoreInert?.(); region?.element.remove(); pending?.abort.abort(); unsubscribe.forEach(off => off()); feedback.remove();
      button.textContent = original.label; button.disabled = original.disabled;
      if (original.describedBy === null) button.removeAttribute('aria-describedby'); else button.setAttribute('aria-describedby', original.describedBy);
    },
  };
}
