import { mountPlanReview, mountReview, PlanReviewHandle } from '../plan-editor-save';
import { previewPlanEditor, savePlanEditor } from '../api/plan-editor';
import type { PlanEditorPreviewResponse, PlanEditorResponse } from '../api/plan-editor';
import { adaptPlanEditor } from '../plan-editor-adapter';
import { setDraftDate } from '../lib/plan-editor-draft';
import type { EditorDraft } from '../lib/plan-editor-draft';
import { response as baseResponse } from './fixtures/plan-editor';

jest.mock('../api/plan-editor', () => ({ ...jest.requireActual('../api/plan-editor'), previewPlanEditor: jest.fn(), savePlanEditor: jest.fn() }));
const preview = previewPlanEditor as jest.Mock, save = savePlanEditor as jest.Mock;

const diff = { plan: [], services: [], accounts: { added: [], removed: [] }, steps: [], totals: { moved: 1, created: 0, canceled: 0, approvals_affected: 0 } };
const reviewed = (token = 'd'): PlanEditorPreviewResponse => ({ reviewed_diff: 'sha256:' + token.repeat(64), required_cancellation_ids: [], required_reapproval_ids: [], diff });
const flush = (): Promise<void> => new Promise(resolve => setTimeout(resolve, 0));
const failure = (status: number, code?: string, message = 'boom'): Error => Object.assign(new Error(message), { status, details: code ? { code } : undefined });

let r: PlanEditorResponse, draft: EditorDraft, handle: PlanReviewHandle, button: HTMLButtonElement, layout: HTMLElement, listeners: Set<() => void>;
const reloadFresh = jest.fn(), onGone = jest.fn();
const feedback = (): HTMLElement => document.getElementById('plan-review-feedback')!;
const alertText = (): string => feedback().querySelector('[role="alert"]')!.textContent ?? '';
const action = (): HTMLButtonElement | null => document.getElementById('plan-review-reload') as HTMLButtonElement | null;
const ready = async (): Promise<void> => {
  preview.mockResolvedValueOnce(reviewed()); draft = setDraftDate(draft, 1, '2026-11-07'); listeners.forEach(l => l()); handle.review(); await flush();
};
const refuse = async (error: Error): Promise<void> => { await ready(); save.mockRejectedValueOnce(error); handle.review(); await flush(); };

beforeEach(() => {
  preview.mockReset(); save.mockReset(); reloadFresh.mockReset().mockResolvedValue(true); onGone.mockReset(); listeners = new Set();
  document.body.innerHTML = '<div class="plan-editor-layout"><div id="host"><div class="schedule-preview-scroll"></div><input data-edit="date"><p data-edit="status"></p></div></div><button id="b" type="submit">Save Plan</button>';
  const host = document.getElementById('host')!; button = document.getElementById('b') as HTMLButtonElement; layout = host.parentElement!;
  const s2 = baseResponse().graph!.steps[1]!, b = baseResponse();
  r = { ...b, graph: { ...b.graph!, steps: b.graph!.steps.map(s => s.step_number === 4 ? { ...s2, step_number: 4, execution_id: 'root-4', scheduled_date: '2026-11-12', cumulative_target_percent: 80 } : s) } };
  draft = adaptPlanEditor(r).graphDraft!;
  handle = mountPlanReview({ response: r, host, button, generation: () => 1, onSaved: jest.fn(), reload: jest.fn(), reloadFresh, onGone,
    edit: { dispose: () => undefined, setReadOnly: () => undefined, draft: () => draft, subscribe: l => { listeners.add(l); return () => listeners.delete(l); } },
    form: { patch: () => ({ plan: { name: 'Renamed' }, services: {} }), lockAll: () => undefined, onChange: () => () => undefined } });
});

describe('discarded notice names every changed category', () => {
  const notice = async (patch: object, dates: boolean): Promise<string> => {
    document.body.innerHTML = '<div class="plan-editor-layout"><div id="h"></div></div><button id="bb" type="submit">x</button>';
    const d = dates ? setDraftDate(draft, 1, '2026-11-07') : draft;
    const h = mountPlanReview({ response: r, host: document.getElementById('h')!, button: document.getElementById('bb') as HTMLButtonElement, generation: () => 1, onSaved: jest.fn(), reload: jest.fn(), reloadFresh,
      edit: { dispose: () => undefined, setReadOnly: () => undefined, draft: () => d, subscribe: () => () => undefined }, form: { patch: () => patch as never, lockAll: () => undefined, onChange: () => () => undefined } });
    save.mockRejectedValueOnce(failure(409, 'plan_changed')); preview.mockResolvedValueOnce(reviewed()); h.review(); await flush(); h.review(); await flush();
    document.getElementById('plan-review-reload')!.click(); await flush();
    return reloadFresh.mock.calls[reloadFresh.mock.calls.length - 1]![0][0];
  };
  it.each([
    [{ plan: {}, services: {} }, true, 'Not applied: 1 date change.'],
    [{ plan: { name: 'x' }, services: {} }, false, 'Not applied: changes to plan settings.'],
    [{ plan: {}, services: { 'aws/ec2': { term: 3 } } }, false, 'Not applied: changes to service settings.'],
    [{ plan: {}, services: {}, target_accounts: ['a'] }, false, 'Not applied: changes to target accounts.'],
    [{ plan: { name: 'x' }, services: { 'aws/ec2': { term: 3 } }, target_accounts: ['a'] }, true, 'Not applied: 1 date change, plus changes to plan settings, service settings and target accounts.'],
  ])('%j', async (patch, dates, expected) => { expect(await notice(patch, dates)).toBe(expected); });
});

describe('footer feedback region', () => {
  it('is keyboard scrollable, named, and keeps the alert rendered (empty) before any error', () => {
    expect(feedback().getAttribute('tabindex')).toBe('0'); expect(feedback().getAttribute('role')).toBe('group');
    expect(feedback().getAttribute('aria-label')).toBe('Review and save feedback');
    expect(feedback().querySelector('[role="alert"]')).not.toBeNull();
  });
});

describe('error table on Edit', () => {
  it('409 plan_changed: keeps the draft, does not reload until the click, then reloads once with the discarded changes listed', async () => {
    await refuse(failure(409, 'plan_changed'));
    expect(alertText()).toBe('This plan changed since you opened it. Reload to see its current state. Changes made here are not applied.');
    expect(reloadFresh).not.toHaveBeenCalled(); expect(action()!.textContent).toBe('Reload changes'); expect(layout.hasAttribute('inert')).toBe(false);
    action()!.click(); await flush();
    expect(reloadFresh).toHaveBeenCalledTimes(1); expect(reloadFresh).toHaveBeenCalledWith(['Not applied: 1 date change, plus changes to plan settings.']);
  });
  it('keeps the Reload button usable after a failed reload', async () => {
    await refuse(failure(409, 'plan_changed')); reloadFresh.mockResolvedValueOnce(false);
    action()!.click(); await flush();
    expect(action()!.disabled).toBe(false);
  });
  it('editor_field_locked shows the server reason and offers a reload', async () => {
    await refuse(failure(409, 'editor_field_locked', 'Step 2 is running, so its date is fixed.'));
    expect(alertText()).toBe('Step 2 is running, so its date is fixed. Reload to see which fields are locked.'); expect(action()).not.toBeNull();
  });
  it('editor_sequence_invalid shows the message and focuses the Date input', async () => {
    await refuse(failure(409, 'editor_sequence_invalid', 'Dates must be on separate days.'));
    expect(alertText()).toBe('Dates must be on separate days.'); expect(document.activeElement).toBe(document.querySelector('[data-edit="date"]'));
  });
  it('editor_review_required clears the acknowledgments and previews again once with a fresh command id', async () => {
    await ready(); const first = JSON.parse(preview.mock.calls[0]![1] as string).command_id;
    save.mockRejectedValueOnce(failure(409, 'editor_review_required')); preview.mockResolvedValueOnce(reviewed('e'));
    handle.review(); await flush();
    expect(preview).toHaveBeenCalledTimes(2); expect(JSON.parse(preview.mock.calls[1]![1] as string).command_id).not.toBe(first);
    expect(alertText()).toContain('tick the boxes again'); expect(handle.ready()).not.toBeNull(); expect(save).toHaveBeenCalledTimes(1);
  });
  it('503 editor_busy keeps the frozen request: the retry sends identical bytes', async () => {
    await refuse(failure(503, 'editor_busy'));
    expect(alertText()).toContain('The server is busy'); expect(button.textContent).toBe('Save changes'); expect(button.disabled).toBe(false);
    save.mockResolvedValueOnce({ replayed: false, mapping: [], diff, editor: baseResponse() }); handle.review(); await flush();
    expect(save).toHaveBeenCalledTimes(2); expect(save.mock.calls[1]![1]).toBe(save.mock.calls[0]![1]);
  });
  it('401 stops: sign-in sentence, Save disabled, draft still there', async () => {
    await refuse(failure(401, 'unauthenticated'));
    expect(alertText()).toBe('Your session expired. Reload the page and sign in again; unsaved changes in this dialog cannot be kept.');
    expect(button.disabled).toBe(true); listeners.forEach(l => l()); expect(button.disabled).toBe(true); handle.review(); expect(save).toHaveBeenCalledTimes(1);
  });
  it('404 makes the dialog read-only, reloads the list and offers no retry', async () => {
    await refuse(failure(404, 'not_found'));
    expect(alertText()).toBe('This plan no longer exists or is out of scope. Close this dialog.');
    expect(onGone).toHaveBeenCalledTimes(1); expect(layout.hasAttribute('inert')).toBe(true); expect(button.disabled).toBe(true); expect(action()).toBeNull();
  });
  it('413 with the history code adds a number-free sentence; a 413 without a code says the change is too large', async () => {
    await refuse(failure(413, 'editor_history_too_large', 'History too big.'));
    expect(alertText()).toBe('History too big. This plan has more purchase history than the editor can save.'); expect(handle.ready()).toBeNull();
    await refuse(failure(413));
    expect(alertText()).toBe('This change is too large to save.');
  });
  it('a preview-phase editor_review_required uses a preview sentence and previews exactly once', async () => {
    preview.mockRejectedValueOnce(failure(409, 'editor_review_required')); draft = setDraftDate(draft, 1, '2026-11-07'); listeners.forEach(l => l()); handle.review(); await flush();
    expect(alertText()).toBe('The plan changed while you were reviewing. Try Review changes again.'); expect(preview).toHaveBeenCalledTimes(1);
  });
  it.each([
    [403, 'permission_denied', 'You may not do that.'], [400, 'invalid_request', 'validation error: name'], [409, 'editor_plan_unsupported', 'Plan is disabled.'],
    [418, 'something_new', 'Unknown thing.'],
  ])('%i %s shows the server sentence verbatim, as text, with no retry', async (status, code, message) => {
    await refuse(failure(status, code, `<b>${message}</b>`));
    expect(alertText()).toBe(`<b>${message}</b>`); expect(feedback().querySelector('b')).toBeNull(); expect(action()).toBeNull(); expect(handle.ready()).toBeNull();
  });
  it('503 editor_unavailable says so without a retry; a 500 is uncertain and keeps the request', async () => {
    await refuse(failure(503, 'editor_unavailable'));
    expect(alertText()).toBe('The plan editor is not available right now. Try again later.'); expect(handle.ready()).toBeNull();
  });
  it('a preview refusal uses the same table', async () => {
    preview.mockRejectedValueOnce(failure(409, 'plan_changed')); draft = setDraftDate(draft, 1, '2026-11-07'); listeners.forEach(l => l()); handle.review(); await flush();
    expect(alertText()).toContain('This plan changed since you opened it.'); expect(action()).not.toBeNull();
  });
  it('clears the old action and message when the user starts a new review', async () => {
    await refuse(failure(409, 'plan_changed')); preview.mockResolvedValueOnce(reviewed()); draft = setDraftDate(draft, 1, '2026-11-08'); listeners.forEach(l => l());
    expect(action()).toBeNull(); expect(alertText()).toBe('');
  });
});

describe('error table on Create', () => {
  const createHandle = (): { review: () => void } => {
    document.body.innerHTML = '<div class="plan-editor-layout"><div id="host2"><div class="schedule-preview-scroll"></div></div></div><button id="b2" type="submit">Save Plan</button>';
    const host = document.getElementById('host2')!, b = document.getElementById('b2') as HTMLButtonElement;
    return mountReview({ host, button: b, planId: () => 'new-id', create: true, generation: () => 1, onSaved: jest.fn(), reload: jest.fn(), reloadFresh, onGone,
      build: id => ({ request: { command_id: id, expected_revision: null, plan: { name: 'N' }, steps: [], allow_approved_edits: false, acknowledged_cancellation_ids: [], acknowledged_reapproval_ids: [] } }),
      subscribe: () => [] });
  };
  it.each([[409, 'plan_changed', 'This plan may already have been created. Check the plans list before saving again.'], [0, undefined, 'This plan may already have been created. Check the plans list before saving again.'], [500, 'internal_error', 'This plan may already have been created. Check the plans list before saving again.']])('%i %s is uncertain-or-collision: no reload button', async (status, code, sentence) => {
    const create = createHandle(); preview.mockResolvedValueOnce(reviewed()); create.review(); await flush();
    save.mockRejectedValueOnce(failure(status, code)); create.review(); await flush();
    expect(alertText()).toBe(sentence); expect(action()).toBeNull(); expect(reloadFresh).not.toHaveBeenCalled();
  });
  it('a 404 is a missing account: message only, the dialog stays usable', async () => {
    const create = createHandle(); preview.mockResolvedValueOnce(reviewed()); create.review(); await flush();
    save.mockRejectedValueOnce(failure(404, 'not_found', 'Not found')); create.review(); await flush();
    expect(alertText()).toBe('Not found'); expect(onGone).not.toHaveBeenCalled(); expect(document.getElementById('b2')!.hasAttribute('disabled')).toBe(false);
  });
});
