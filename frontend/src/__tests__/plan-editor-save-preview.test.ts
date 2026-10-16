import { mountPlanReview, PlanReviewHandle } from '../plan-editor-save';
import { previewPlanEditor } from '../api/plan-editor';
import type { PlanEditorPreviewResponse, PlanEditorResponse } from '../api/plan-editor';
import { adaptPlanEditor } from '../plan-editor-adapter';
import { setDraftDate } from '../lib/plan-editor-draft';
import type { EditorDraft } from '../lib/plan-editor-draft';
import type { EditFormPatch } from '../lib/plan-editor-form';
import { response as baseResponse } from './fixtures/plan-editor';

jest.mock('../api/plan-editor', () => ({ ...jest.requireActual('../api/plan-editor'), previewPlanEditor: jest.fn() }));
const preview = previewPlanEditor as jest.Mock;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

const dated = (): PlanEditorResponse => {
  const r = baseResponse(), s2 = r.graph!.steps[1]!;
  return { ...r, graph: { ...r.graph!, steps: r.graph!.steps.map(s => s.step_number === 4 ? { ...s2, step_number: 4, execution_id: 'root-4', scheduled_date: '2026-11-12', cumulative_target_percent: 80 } : s) } };
};
const result = (moved: number): PlanEditorPreviewResponse => ({
  reviewed_diff: 'sha256:' + 'a'.repeat(64), required_cancellation_ids: [], required_reapproval_ids: [],
  diff: { plan: [], services: [], accounts: { added: [], removed: [] }, steps: [], totals: { moved, created: 0, canceled: 1, approvals_affected: 2 } },
});
const deferred = <T>(): { promise: Promise<T>; resolve: (v: T) => void; reject: (e: unknown) => void } => {
  let resolve!: (v: T) => void, reject!: (e: unknown) => void;
  const promise = new Promise<T>((a, b) => { resolve = a; reject = b; });
  return { promise, resolve, reject };
};
const flush = (): Promise<void> => new Promise(resolve => setTimeout(resolve, 0));

let r: PlanEditorResponse, draft: EditorDraft | null, patch: EditFormPatch, generation: number;
let editListeners: Set<() => void>, formListeners: Set<() => void>, handle: PlanReviewHandle, button: HTMLButtonElement, host: HTMLElement;
const readOnlySpy = jest.fn(), lockAllSpy = jest.fn();
const mount = (response: PlanEditorResponse = r): void => {
  handle = mountPlanReview({ response, host, button, generation: () => generation, onSaved: jest.fn(), reload: async () => true,
    edit: { dispose: () => undefined, setReadOnly: readOnlySpy, draft: () => draft, subscribe: l => { editListeners.add(l); return () => editListeners.delete(l); } },
    form: { patch: () => patch, lockAll: lockAllSpy, onChange: l => { formListeners.add(l); return () => formListeners.delete(l); } } });
};
const moveStep2 = (date: string): void => { draft = setDraftDate(draft!, 1, date); editListeners.forEach(l => l()); };
const status = (): string => document.querySelector('#plan-review-feedback [role="status"]')!.textContent ?? '';
const reasonText = (): string => document.getElementById('plan-review-reason')!.textContent ?? '';
const alertText = (): string => document.querySelector('#plan-review-feedback [role="alert"]')!.textContent ?? '';

beforeEach(() => {
  preview.mockReset(); generation = 1; editListeners = new Set(); formListeners = new Set();
  document.body.innerHTML = '<div id="host"><div class="schedule-preview-scroll"></div><p data-edit="status"></p></div><button id="b" type="submit">Save Plan</button>';
  host = document.getElementById('host')!; button = document.getElementById('b') as HTMLButtonElement;
  r = dated(); draft = adaptPlanEditor(r).graphDraft; patch = { plan: {}, services: {} };
});

describe('review gating', () => {
  it('is disabled with a reason until something changed, then enabled', () => {
    mount();
    expect(button.textContent).toBe('Review changes');
    expect(button.disabled).toBe(true);
    expect(reasonText()).toBe('No changes to review');
    expect(button.getAttribute('aria-describedby')).toBe('plan-review-reason');
    expect(document.getElementById('plan-review-reason')!.textContent).toBe('No changes to review');
    moveStep2('2026-11-07');
    expect(button.disabled).toBe(false); expect(reasonText()).toBe(''); expect(button.hasAttribute('aria-describedby')).toBe(false);
    patch = { plan: { name: 'New' }, services: {} }; formListeners.forEach(l => l());
    expect(button.disabled).toBe(false);
  });
  it('enables for a form-only change on a plan without a draft', () => {
    const zero = { ...r, graph: null, plan: { ...r.plan, ramp_schedule: { ...r.plan.ramp_schedule, total_steps: 0, current_step: 0 } } };
    draft = null; mount(zero);
    expect(button.disabled).toBe(true);
    patch = { plan: { enabled: true }, services: {} }; formListeners.forEach(l => l());
    expect(button.disabled).toBe(false);
  });
  it('shows the unavailable reason and the panel status sentence', () => {
    draft = null; mount();
    expect(button.disabled).toBe(true); expect(reasonText()).toMatch(/schedule/i);
    handle.dispose();
    draft = adaptPlanEditor(baseResponse()).graphDraft; patch = { plan: { name: 'x' }, services: {} };
    host.querySelector('[data-edit="status"]')!.textContent = 'Needs consent first.';
    mount(baseResponse());
    expect(button.disabled).toBe(true); expect(reasonText()).toBe('Choose a date for step 4.');
  });
  it('blocks an invalid draft with the panel sentence, not a request', () => {
    host.querySelector('[data-edit="status"]')!.textContent = 'Choose a future UTC date.';
    draft = { ...draft!, dates: draft!.dates.map((date, i) => i === 1 ? '2026-10-10' : date) };
    mount();
    expect(button.disabled).toBe(true); expect(reasonText()).toBe('Choose a future UTC date.');
    handle.review(); expect(preview).not.toHaveBeenCalled();
  });
  it('does not count an approval toggle alone as a change', () => {
    draft = { ...draft!, approvedEdits: true }; mount();
    expect(reasonText()).toBe('No changes to review');
  });
  it('restores the button on dispose', () => {
    mount(); handle.dispose();
    expect(button.textContent).toBe('Save Plan'); expect(button.disabled).toBe(false);
    expect(document.getElementById('plan-review-feedback')).toBeNull();
  });
});

describe('preview lifecycle', () => {
  it('sends the serialized request once for double clicks and resends identical bytes on retry', async () => {
    moveStep2('2026-11-07');
    const gate = deferred<PlanEditorPreviewResponse>();
    preview.mockReturnValueOnce(gate.promise);
    mount(); handle.review(); handle.review();
    expect(preview).toHaveBeenCalledTimes(1);
    const [planId, body, signal] = preview.mock.calls[0]!;
    expect(planId).toBe('plan-id'); expect(signal).toBeInstanceOf(AbortSignal);
    const sent = JSON.parse(body as string);
    expect(sent.command_id).toMatch(UUID); expect(sent.expected_revision).toBe('opaque-revision');
    expect(sent.plan).toEqual({}); expect(sent.steps[0]).toEqual({ step_number: 2, scheduled_date: '2026-11-07' });
    gate.reject(Object.assign(new Error('x'), { status: 0 })); await flush();
    preview.mockResolvedValueOnce(result(3)); handle.review(); await flush();
    expect(status()).toBe('3 dates moved; 1 step removed; 2 approvals affected');
    expect(preview.mock.calls[1]![1]).toBe(body);
  });
  it('drops a late preview A after the intent changed and shows B', async () => {
    const a = deferred<PlanEditorPreviewResponse>(), b = deferred<PlanEditorPreviewResponse>();
    preview.mockReturnValueOnce(a.promise).mockReturnValueOnce(b.promise);
    moveStep2('2026-11-07'); mount(); handle.review();
    const signalA = preview.mock.calls[0]![2] as AbortSignal;
    moveStep2('2026-11-08');
    expect(signalA.aborted).toBe(true);
    handle.review();
    expect(JSON.parse(preview.mock.calls[1]![1] as string).command_id).not.toBe(JSON.parse(preview.mock.calls[0]![1] as string).command_id);
    a.resolve(result(1)); await flush();
    expect(status()).toBe('');
    b.resolve(result(2)); await flush();
    expect(status()).toBe('2 dates moved; 1 step removed; 2 approvals affected');
    a.resolve(result(9)); await flush();
    expect(status()).toContain('2 dates moved');
  });
  it('drops a late response after the dialog session changed or the module was disposed', async () => {
    const a = deferred<PlanEditorPreviewResponse>();
    preview.mockReturnValueOnce(a.promise);
    moveStep2('2026-11-07'); mount(); handle.review();
    generation = 2; a.resolve(result(5)); await flush();
    expect(status()).toBe('');
    const b = deferred<PlanEditorPreviewResponse>();
    preview.mockReturnValueOnce(b.promise); handle.review();
    handle.dispose(); expect((preview.mock.calls[1]![2] as AbortSignal).aborted).toBe(true); b.resolve(result(5)); await flush();
    expect(document.getElementById('plan-review-feedback')).toBeNull();
  });
  it('clears a shown result as soon as the intent changes', async () => {
    preview.mockResolvedValueOnce(result(4));
    moveStep2('2026-11-07'); mount(); handle.review(); await flush();
    expect(status()).toContain('4 dates moved');
    moveStep2('2026-11-08');
    expect(status()).toBe('');
  });
  it('ignores review and notifications after dispose', () => {
    moveStep2('2026-11-07'); mount(); handle.dispose();
    expect(editListeners.size).toBe(0); expect(formListeners.size).toBe(0);
    handle.review(); expect(preview).not.toHaveBeenCalled();
  });
  it('does not call the API while blocked', () => {
    mount(); handle.review();
    expect(preview).not.toHaveBeenCalled();
  });
});

describe('preview errors', () => {
  const fail = async (error: unknown): Promise<string> => {
    preview.mockRejectedValueOnce(error); moveStep2('2026-11-07'); mount(); handle.review(); await flush();
    return alertText();
  };
  it('classifies server answers and keeps the draft', async () => {
    const text = await fail(Object.assign(new Error('plan changed'), { status: 409, details: { code: 'plan_changed' } }));
    expect(text).toBe('plan changed');
    expect(draft!.dates[1]).toBe('2026-11-07'); expect(button.disabled).toBe(false);
  });
  it('words an uncertain preview as read-only, never "saved"', async () => {
    for (const error of [Object.assign(new Error('x'), { status: 504 }), new TypeError('network'), Object.assign(new Error('t'), { status: 0 })]) {
      document.body.innerHTML = '<div id="host"><div class="schedule-preview-scroll"></div></div><button id="b" type="submit">Save Plan</button>';
      host = document.getElementById('host')!; button = document.getElementById('b') as HTMLButtonElement; draft = adaptPlanEditor(r).graphDraft;
      const text = await fail(error);
      expect(text).toMatch(/Nothing was saved/); expect(text).not.toMatch(/may have been saved/);
    }
  });
  it('is silent for the AbortError of a superseded preview', async () => {
    const a = deferred<PlanEditorPreviewResponse>();
    preview.mockReturnValueOnce(a.promise);
    moveStep2('2026-11-07'); mount(); handle.review();
    moveStep2('2026-11-08');
    a.reject(Object.assign(new Error('aborted'), { name: 'AbortError' })); await flush();
    expect(alertText()).toBe('');
  });
});
