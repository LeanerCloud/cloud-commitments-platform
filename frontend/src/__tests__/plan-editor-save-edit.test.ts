import { mountPlanReview, savedLines, PlanReviewHandle } from '../plan-editor-save';
import { previewPlanEditor, savePlanEditor } from '../api/plan-editor';
import type { PlanEditorPreviewResponse, PlanEditorResponse, PlanEditorSaveResponse } from '../api/plan-editor';
import { adaptPlanEditor } from '../plan-editor-adapter';
import { setDraftDate } from '../lib/plan-editor-draft';
import type { EditorDraft } from '../lib/plan-editor-draft';
import type { EditFormPatch } from '../lib/plan-editor-form';
import { response as baseResponse } from './fixtures/plan-editor';

jest.mock('../api/plan-editor', () => ({ ...jest.requireActual('../api/plan-editor'), previewPlanEditor: jest.fn(), savePlanEditor: jest.fn() }));
const preview = previewPlanEditor as jest.Mock, save = savePlanEditor as jest.Mock;

const diff = { plan: [], services: [], accounts: { added: [], removed: [] }, steps: [], totals: { moved: 1, created: 0, canceled: 1, approvals_affected: 0 } };
const reviewed = (ids: string[] = ['c-2', 'c-1']): PlanEditorPreviewResponse => ({ reviewed_diff: 'sha256:' + 'd'.repeat(64), required_cancellation_ids: ids, required_reapproval_ids: [], diff });
const saved = (replayed = false, mapping: PlanEditorSaveResponse['mapping'] = []): PlanEditorSaveResponse => ({ replayed, mapping, diff, editor: baseResponse() });
const flush = (): Promise<void> => new Promise(resolve => setTimeout(resolve, 0));
const failure = (status: number, code?: string): Error => Object.assign(new Error('boom'), { status, details: code ? { code } : undefined });

let patch: EditFormPatch = { plan: {}, services: {} };
let r: PlanEditorResponse, draft: EditorDraft, handle: PlanReviewHandle, button: HTMLButtonElement, layout: HTMLElement, generation: number, editListeners: Set<() => void>;
const onSaved = jest.fn(), reload = jest.fn();
const move = (date: string): void => { draft = setDraftDate(draft, 1, date); editListeners.forEach(l => l()); };
const boxes = (): HTMLInputElement[] => Array.from(document.querySelectorAll<HTMLInputElement>('#plan-review input[type="checkbox"]'));
const tick = (box: HTMLInputElement): void => { box.checked = true; box.dispatchEvent(new Event('change', { bubbles: true })); };
const text = (role: string): string => document.querySelector(`#plan-review-feedback [role="${role}"]`)!.textContent ?? '';
// Preview, acknowledge everything, leave the footer on "Save changes".
const ready = async (ids?: string[]): Promise<void> => {
  preview.mockResolvedValueOnce(reviewed(ids)); move('2026-11-07'); handle.review(); await flush(); boxes().forEach(tick);
};

beforeEach(() => {
  patch = { plan: {}, services: {} }; preview.mockReset(); save.mockReset(); onSaved.mockReset(); reload.mockReset().mockResolvedValue(true); generation = 1; editListeners = new Set();
  document.body.innerHTML = '<div class="plan-editor-layout"><div id="host"><div class="schedule-preview-scroll"></div><p data-edit="status"></p></div></div><button id="b" type="submit">Save Plan</button>';
  const host = document.getElementById('host')!; button = document.getElementById('b') as HTMLButtonElement; layout = host.parentElement!;
  const s2 = baseResponse().graph!.steps[1]!, b = baseResponse();
  r = { ...b, graph: { ...b.graph!, steps: b.graph!.steps.map(s => s.step_number === 4 ? { ...s2, step_number: 4, execution_id: 'root-4', scheduled_date: '2026-11-12', cumulative_target_percent: 80 } : s) } };
  draft = adaptPlanEditor(r).graphDraft!;
  handle = mountPlanReview({ response: r, host, button, generation: () => generation, onSaved, reload,
    edit: { dispose: () => undefined, setReadOnly: () => undefined, draft: () => draft, subscribe: l => { editListeners.add(l); return () => editListeners.delete(l); } },
    form: { patch: () => patch, lockAll: () => undefined, onChange: () => () => undefined } });
});

describe('one primary Save action', () => {
  it('shows Review changes until every acknowledgment is ticked, then Save changes, and has no second confirm button', async () => {
    preview.mockResolvedValueOnce(reviewed()); move('2026-11-07'); handle.review(); await flush();
    expect(button.textContent).toBe('Review changes'); expect(document.getElementById('plan-review-confirm')).toBeNull();
    expect(boxes().every(box => !box.checked)).toBe(true); expect(save).not.toHaveBeenCalled();
    boxes().forEach(tick);
    expect(button.textContent).toBe('Save changes'); expect(button.disabled).toBe(false);
  });
});

describe('frozen command', () => {
  it('sends the reviewed request once with sorted acknowledgments and the preview command id, and suppresses double submits', async () => {
    await ready();
    const previewBody = JSON.parse(preview.mock.calls[0]![1] as string);
    save.mockReturnValueOnce(new Promise(() => undefined));
    handle.review(); handle.review();
    expect(save).toHaveBeenCalledTimes(1);
    const [planId, body] = save.mock.calls[0]!;
    expect(planId).toBe('plan-id');
    expect(JSON.parse(body as string)).toEqual({ ...previewBody, reviewed_diff: 'sha256:' + 'd'.repeat(64), acknowledged_cancellation_ids: ['c-1', 'c-2'], acknowledged_reapproval_ids: [] });
    expect(button.textContent).toBe('Saving...'); expect(button.disabled).toBe(true); expect(layout.hasAttribute('inert')).toBe(true);
  });
  it('resends the identical bytes and command id after an uncertain failure, without rebuilding', async () => {
    await ready();
    save.mockRejectedValueOnce(failure(0)).mockResolvedValueOnce(saved(true));
    handle.review(); await flush();
    expect(text('alert')).toContain('may have been saved'); expect(layout.hasAttribute('inert')).toBe(false);
    expect(button.textContent).toBe('Save changes'); expect(handle.ready()).not.toBeNull();
    patch = { plan: { name: 'Edited without a change event' }, services: {} }; // a rebuild at retry time would pick this up
    handle.review(); await flush();
    expect(save).toHaveBeenCalledTimes(2);
    expect(save.mock.calls[1]![1]).toBe(save.mock.calls[0]![1]);
    expect(JSON.parse(save.mock.calls[1]![1] as string).command_id).toBe(JSON.parse(preview.mock.calls[0]![1] as string).command_id);
    expect(text('status')).toMatch(/^Already applied\./);
  });
  it('drops the frozen command when the intent changes after an uncertain failure, and uses a new command id on the next review', async () => {
    await ready();
    save.mockRejectedValueOnce(failure(503));
    handle.review(); await flush();
    move('2026-11-08');
    expect(handle.ready()).toBeNull(); expect(button.textContent).toBe('Review changes');
    preview.mockResolvedValueOnce(reviewed()); handle.review(); await flush(); boxes().forEach(tick); save.mockResolvedValueOnce(saved()); handle.review(); await flush();
    expect(JSON.parse(save.mock.calls[1]![1] as string).command_id).not.toBe(JSON.parse(save.mock.calls[0]![1] as string).command_id);
  });
  it('requires a new review after a definite refusal', async () => {
    await ready();
    save.mockRejectedValueOnce(failure(409, 'editor_plan_unsupported'));
    handle.review(); await flush();
    expect(text('alert')).toBe('boom'); expect(handle.ready()).toBeNull(); expect(button.textContent).toBe('Review changes');
    expect(document.getElementById('plan-review')).toBeNull(); expect(layout.hasAttribute('inert')).toBe(false);
  });
});

describe('re-review after a refusal', () => {
  it('sends the new preview reviewed_diff and acknowledgments, never the frozen first command', async () => {
    await ready();
    save.mockRejectedValueOnce(failure(409, 'editor_review_required'));
    preview.mockResolvedValueOnce({ ...reviewed(['n-1']), reviewed_diff: 'sha256:' + 'e'.repeat(64) });
    handle.review(); await flush();
    expect(button.textContent).toBe('Review changes'); boxes().forEach(tick);
    save.mockResolvedValueOnce(saved()); handle.review(); await flush();
    expect(save).toHaveBeenCalledTimes(2);
    expect(JSON.parse(save.mock.calls[1]![1] as string)).toMatchObject({ reviewed_diff: 'sha256:' + 'e'.repeat(64), acknowledged_cancellation_ids: ['n-1'] });
  });
});

describe('saved result', () => {
  it('lists what became of each purchase, compactly and pluralized, as text only', async () => {
    await ready();
    save.mockResolvedValueOnce(saved(false, [
      { step_number: 2, old_execution_id: 'o2', new_execution_id: 'n2', scheduled_date: '2026-11-07' },
      { step_number: 3, old_execution_id: 'o3', new_execution_id: null, scheduled_date: null },
      { step_number: 5, old_execution_id: null, new_execution_id: 'n5', scheduled_date: '<b>x</b>' }]));
    handle.review(); await flush();
    const line = document.querySelector('#plan-review-feedback p.help-text')!;
    expect(line.textContent).toBe('3 purchases changed: step 2 replaced, now 2026-11-07; step 3 canceled; step 5 created, now <b>x</b>.');
    expect(line.querySelector('b')).toBeNull(); expect(line.textContent).not.toContain('o2');
  });
  it('says purchase for exactly one and shows no line when nothing changed', async () => {
    expect(savedLines(saved(false, [{ step_number: 2, old_execution_id: 'o', new_execution_id: 'n', scheduled_date: '2026-11-07' }]))[1]).toBe('1 purchase changed: step 2 replaced, now 2026-11-07.');
    expect(savedLines(saved())[1]).toBe('');
  });
});

describe('reload after a Save when the GET fails', () => {
  it('keeps the saved result, restores the button, stays read-only and offers a working Reload', async () => {
    await ready();
    save.mockResolvedValueOnce(saved()); reload.mockResolvedValueOnce(false).mockResolvedValueOnce(false).mockResolvedValueOnce(true);
    handle.review(); await flush();
    expect(text('status')).toMatch(/^Saved\./); expect(text('alert')).toBe('Saved, but reloading failed. Reload to keep editing.');
    expect(button.textContent).toBe('Review changes'); expect(button.disabled).toBe(true); expect(layout.hasAttribute('inert')).toBe(true);
    const retry = document.getElementById('plan-review-reload') as HTMLButtonElement;
    expect(retry.disabled).toBe(false);
    retry.click(); await flush();
    expect(reload).toHaveBeenCalledTimes(2); expect(document.querySelectorAll('#plan-review-reload')).toHaveLength(1); expect(retry.disabled).toBe(false);
    retry.click(); await flush();
    expect(reload).toHaveBeenCalledTimes(3); expect(text('status')).toMatch(/^Saved\./);
  });
});

describe('success', () => {
  it('reports the saved state, clears the review and hands the result to the caller once', async () => {
    await ready();
    const result = saved(); save.mockResolvedValueOnce(result);
    handle.review(); await flush();
    expect(text('status')).toBe('Saved. 1 date moved; 1 step removed; 0 approvals affected.');
    expect(onSaved).toHaveBeenCalledTimes(1); expect(onSaved).toHaveBeenCalledWith(result, true); expect(reload).toHaveBeenCalledWith(result);
    expect(document.getElementById('plan-review')).toBeNull();
  });
  it('tells the caller the dialog is gone when the session changed in flight, and leaves the dialog untouched', async () => {
    await ready();
    const result = saved(); save.mockResolvedValueOnce(result);
    handle.review(); generation = 2; await flush();
    expect(onSaved).toHaveBeenCalledWith(result, false); expect(reload).not.toHaveBeenCalled();
    expect(text('status')).not.toMatch(/^Saved/);
  });
  it('restores the interactive layout when disposed mid-save', async () => {
    await ready();
    save.mockReturnValueOnce(new Promise(() => undefined)); handle.review();
    expect(layout.hasAttribute('inert')).toBe(true); handle.dispose(); expect(layout.hasAttribute('inert')).toBe(false);
  });
});
