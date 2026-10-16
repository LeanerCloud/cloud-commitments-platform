import { mountPlanReview, PlanReviewHandle } from '../plan-editor-save';
import { previewPlanEditor } from '../api/plan-editor';
import type { PlanEditorPreviewResponse, PlanEditorResponse, PlanEditorStepChange } from '../api/plan-editor';
import { adaptPlanEditor } from '../plan-editor-adapter';
import { setDraftDate } from '../lib/plan-editor-draft';
import type { EditorDraft } from '../lib/plan-editor-draft';
import type { EditFormPatch } from '../lib/plan-editor-form';
import { response as baseResponse } from './fixtures/plan-editor';

jest.mock('../api/plan-editor', () => ({ ...jest.requireActual('../api/plan-editor'), previewPlanEditor: jest.fn() }));
const preview = previewPlanEditor as jest.Mock;

const step = (n: number, change: PlanEditorStepChange['change'], before: string | null, after: string | null, id: string | null, effect: PlanEditorStepChange['approval_effect'] = 'none'): PlanEditorStepChange =>
  ({ step_number: n, before_date: before, after_date: after, change, old_execution_id: id, approval_effect: effect });
const result = (over: Partial<PlanEditorPreviewResponse> = {}): PlanEditorPreviewResponse => ({
  reviewed_diff: 'sha256:' + 'b'.repeat(64), required_cancellation_ids: [], required_reapproval_ids: [],
  diff: { plan: [], services: [], accounts: { added: [], removed: [] }, steps: [], totals: { moved: 0, created: 0, canceled: 0, approvals_affected: 0 } }, ...over,
});
const impactful = (): PlanEditorPreviewResponse => result({
  required_cancellation_ids: ['c-2', 'c-1'], required_reapproval_ids: ['r-3'],
  diff: {
    plan: [{ field: 'name', before: 'Old', after: 'New' }, { field: 'notification_days_before', before: null, after: 3 }],
    services: [{ key: 'azure/vm', new_key: 'azure/compute-vm', fields: [{ field: 'coverage', before: 70, after: 80 }] }],
    accounts: { added: ['acct-a'], removed: ['acct-r'] },
    steps: [step(1, 'unchanged', '2026-11-01', '2026-11-01', null), step(2, 'canceled', '2026-11-05', null, 'c-1'), step(3, 'moved', '2026-11-09', '2026-11-12', 'r-3', 'reapproval_required'),
      step(4, 'replaced', '2026-11-20', '2026-11-22', 'c-2', 'fresh_veto_window')],
    totals: { moved: 1, created: 0, canceled: 1, approvals_affected: 1 },
  },
});
const flush = (): Promise<void> => new Promise(resolve => setTimeout(resolve, 0));

let r: PlanEditorResponse, draft: EditorDraft | null, patch: EditFormPatch, handle: PlanReviewHandle, button: HTMLButtonElement, host: HTMLElement, editListeners: Set<() => void>;
const move = (date: string): void => { draft = setDraftDate(draft!, 1, date); editListeners.forEach(l => l()); };
const region = (): HTMLElement | null => document.getElementById('plan-review');
const boxes = (): HTMLInputElement[] => Array.from(document.querySelectorAll<HTMLInputElement>('#plan-review input[type="checkbox"]'));
const tick = (box: HTMLInputElement): void => { box.checked = !box.checked; box.dispatchEvent(new Event('change', { bubbles: true })); };
const start = async (value: PlanEditorPreviewResponse): Promise<void> => {
  preview.mockResolvedValueOnce(value); move('2026-11-07'); handle.review(); await flush();
};

beforeEach(() => {
  preview.mockReset(); editListeners = new Set();
  document.body.innerHTML = '<div id="host"><div class="schedule-preview-scroll"></div><p data-edit="status"></p></div><button id="b" type="submit">Save Plan</button>';
  host = document.getElementById('host')!; button = document.getElementById('b') as HTMLButtonElement;
  const s2 = baseResponse().graph!.steps[1]!, b = baseResponse();
  r = { ...b, graph: { ...b.graph!, steps: b.graph!.steps.map(s => s.step_number === 4 ? { ...s2, step_number: 4, execution_id: 'root-4', scheduled_date: '2026-11-12', cumulative_target_percent: 80 } : s) } };
  draft = adaptPlanEditor(r).graphDraft; patch = { plan: {}, services: {} };
  handle = mountPlanReview({ response: r, host, button, generation: () => 1, onSaved: jest.fn(), reload: async () => true,
    edit: { dispose: () => undefined, setReadOnly: () => undefined, draft: () => draft, subscribe: l => { editListeners.add(l); return () => editListeners.delete(l); } },
    form: { patch: () => patch, lockAll: () => undefined, onChange: () => () => undefined } });
});

describe('acknowledgment gating', () => {
  it('keeps the footer on Review changes until every cancellation and re-approval is checked, then returns the server sets', async () => {
    await start(impactful());
    expect(button.textContent).toBe('Review changes'); expect(handle.ready()).toBeNull();
    expect(boxes()).toHaveLength(3);
    tick(boxes()[0]!); tick(boxes()[1]!);
    expect(button.textContent).toBe('Review changes'); expect(handle.ready()).toBeNull();
    expect(region()!.querySelector('[role="status"]')!.textContent).toBe('Acknowledge 1 more item to continue.');
    tick(boxes()[2]!);
    expect(button.textContent).toBe('Save changes');
    expect(handle.ready()).toEqual({ reviewedDiff: 'sha256:' + 'b'.repeat(64), cancellationIds: ['c-2', 'c-1'], reapprovalIds: ['r-3'] });
    expect(region()!.querySelector('[role="status"]')!.textContent).toBe('Ready to save. Use Save changes.');
    tick(boxes()[0]!);
    expect(button.textContent).toBe('Review changes'); expect(handle.ready()).toBeNull();
  });
  it('is ready at once for a zero-impact preview', async () => {
    await start(result());
    expect(boxes()).toHaveLength(0); expect(button.textContent).toBe('Save changes');
    expect(handle.ready()).toEqual({ reviewedDiff: 'sha256:' + 'b'.repeat(64), cancellationIds: [], reapprovalIds: [] });
    expect(region()!.textContent).toContain('No step changes.');
  });
  it('labels each checkbox by step and date inside named groups, and moves focus to the heading', async () => {
    await start(impactful());
    const labels = boxes().map(box => box.closest('label')!.textContent!.trim());
    expect(labels).toEqual(['I understand step 4 (2026-11-20 -> 2026-11-22) will be canceled.', 'I understand step 2 (2026-11-05 -> no date) will be canceled.',
      'I understand step 3 (2026-11-09 -> 2026-11-12) will need approval again.']);
    expect(Array.from(region()!.querySelectorAll('fieldset > legend')).map(l => l.textContent)).toEqual(['2 purchases will be canceled', '1 approved purchase needs approval again']);
    expect(region()!.getAttribute('aria-labelledby')).toBe('plan-review-heading');
    expect(document.activeElement).toBe(document.getElementById('plan-review-heading'));
    expect(region()!.parentElement).toBe(host);
  });
});

describe('singular and plural wording', () => {
  it('uses singular for one and plural for many, in the legends, the note and the footer totals', async () => {
    await start(impactful());
    expect(document.querySelector('#plan-review-feedback [role="status"]')!.textContent).toBe('1 date moved; 1 step removed; 1 approval affected');
    expect(region()!.querySelector('[role="status"]')!.textContent).toBe('Acknowledge 3 more items to continue.');
    tick(boxes()[0]!); tick(boxes()[1]!);
    expect(region()!.querySelector('[role="status"]')!.textContent).toBe('Acknowledge 1 more item to continue.');
  });
  it('says purchases for two or more, purchase for exactly one', async () => {
    await start(result({ required_cancellation_ids: ['a'], required_reapproval_ids: ['b', 'c'] }));
    expect(Array.from(region()!.querySelectorAll('legend')).map(l => l.textContent)).toEqual(['1 purchase will be canceled', '2 approved purchases need approval again']);
  });
});

describe('ids with no matching step', () => {
  it('still renders, gates on and returns an acknowledgment for an unmatched id in each set', async () => {
    await start(result({ required_cancellation_ids: ['ghost-c'], required_reapproval_ids: ['ghost-r'] }));
    expect(boxes().map(box => box.closest('label')!.textContent!.trim())).toEqual([
      'I understand a purchase without a listed step will be canceled.', 'I understand a purchase without a listed step will need approval again.']);
    expect(button.textContent).toBe('Review changes');
    tick(boxes()[0]!); expect(button.textContent).toBe('Review changes'); expect(handle.ready()).toBeNull();
    tick(boxes()[1]!); expect(button.textContent).toBe('Save changes');
    expect(handle.ready()).toMatchObject({ cancellationIds: ['ghost-c'], reapprovalIds: ['ghost-r'] });
  });
});

describe('diff rendering', () => {
  it('shows field, rename, account and step rows with approval effects', async () => {
    await start(impactful());
    const text = region()!.textContent!;
    for (const line of ['name: Old -> New', 'notification_days_before: none -> 3', 'Service azure/vm becomes azure/compute-vm', 'azure/compute-vm coverage: 70 -> 80',
      'Account added: acct-a', 'Account removed: acct-r', 'Step 2: canceled, 2026-11-05 -> no date', 'Step 3: moved, 2026-11-09 -> 2026-11-12, needs approval again',
      'Step 4: replaced, 2026-11-20 -> 2026-11-22, new veto window']) expect(text).toContain(line);
    expect(text).not.toContain('Step 1:');
    expect(text).not.toContain('c-1');
  });
  it('renders server strings as text only', async () => {
    const hostile = '<img src=x onerror=alert(1)>';
    await start(result({ diff: { ...result().diff, plan: [{ field: hostile, before: hostile, after: ['<b>x</b>'] }] } }));
    expect(region()!.querySelector('img, b')).toBeNull();
    expect(region()!.textContent).toContain(hostile);
  });
});

describe('consent lifecycle', () => {
  it('clears the region and acknowledgments when the intent changes and returns focus to the button', async () => {
    await start(impactful());
    boxes().forEach(tick);
    expect(handle.ready()).not.toBeNull();
    move('2026-11-08');
    expect(region()).toBeNull(); expect(handle.ready()).toBeNull(); expect(document.activeElement).toBe(button);
    preview.mockResolvedValueOnce(impactful()); handle.review(); await flush();
    expect(boxes().every(box => !box.checked)).toBe(true); expect(button.textContent).toBe('Review changes');
  });
  it('clears a previous region when a new review starts, and a re-review never inherits ticks', async () => {
    await start(impactful());
    tick(boxes()[0]!);
    preview.mockReturnValueOnce(new Promise(() => undefined)); handle.review();
    expect(region()).toBeNull(); expect(handle.ready()).toBeNull();
  });
  it('never shows a preview that resolves after the intent changed', async () => {
    let resolve!: (v: PlanEditorPreviewResponse) => void;
    preview.mockReturnValueOnce(new Promise<PlanEditorPreviewResponse>(done => { resolve = done; }));
    move('2026-11-07'); handle.review(); move('2026-11-08');
    resolve(impactful()); await flush();
    expect(region()).toBeNull(); expect(handle.ready()).toBeNull();
  });
  it('removes the region on dispose', async () => {
    await start(impactful());
    handle.dispose(); expect(region()).toBeNull();
  });
});
