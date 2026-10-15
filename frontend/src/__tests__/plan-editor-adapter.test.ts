import { getPlanEditor } from '../api/plan-editor';
import { clearAuth, setAuthToken } from '../api/client';
import { adaptPlanEditor, editorServiceTargets } from '../plan-editor-adapter';
import { canDraftStep, canReviewEditorDraft, draftDateChanges, resetEditorDraft, setApprovedDraftEdits, setDraftDate } from '../lib/plan-editor-draft';
import { fetchMock } from './setup';
import { response, service, display } from './fixtures/plan-editor';

function freeze(value: unknown): void {
  if (!value || typeof value !== 'object') return;
  Object.values(value).forEach(freeze); Object.freeze(value);
}

describe('complete editor read boundary', () => {
  beforeEach(() => { clearAuth(); jest.useFakeTimers(); jest.setSystemTime(new Date('2026-10-15T12:00:00Z')); });
  afterEach(() => { clearAuth(); jest.useRealTimers(); });
  test('uses authenticated GET and preserves the complete response without normalization', async () => {
    const original = response(); freeze(original); const before = JSON.stringify(original);
    fetchMock.mockResolvedValue({ ok: true, json: async () => original }); setAuthToken('test-token');
    expect(await getPlanEditor('plan/id?#')).toBe(original);
    expect(fetchMock).toHaveBeenCalledWith('/api/plans/plan%2Fid%3F%23/editor', expect.objectContaining({
      headers: expect.objectContaining({ 'X-Authorization': 'Bearer test-token' }),
    }));
    expect(fetchMock.mock.calls[0]![1]).not.toHaveProperty('body');
    expect(fetchMock.mock.calls[0]![1].method).toBeUndefined();
    expect(JSON.stringify(original)).toBe(before);
  });
  test.each([403, 409, 413, 503])('propagates HTTP %s without manufacturing a draft response', async status => {
    fetchMock.mockResolvedValue({ ok: false, status, json: async () => ({ error: 'Read denied' }) });
    await expect(getPlanEditor('plan')).rejects.toMatchObject({ status, message: 'Read denied' });
  });
  test('rejects empty or malformed successful JSON returned as null by apiRequest', async () => {
    fetchMock.mockResolvedValue({ ok: true, json: async () => { throw new SyntaxError('Empty JSON'); } });
    await expect(getPlanEditor('plan')).rejects.toThrow('Plan editor response is unavailable');
  });
  test('keeps original settings, history, policy and wire nulls separate from date proposals', () => {
    const original = response(); freeze(original); const before = JSON.stringify(original);
    const adapted = adaptPlanEditor(original), draft = adapted.graphDraft!;
    expect(adapted.response).toBe(original);
    expect(draft.context).toMatchObject({ kind: 'edit', planId: 'plan-id', revision: 'opaque-revision', totalSteps: 5 });
    expect(draft.context.kind === 'edit' && Object.keys(draft.context.slots[0]!).sort()).toEqual([
      'capability', 'cumulative_target_percent', 'editable', 'execution_id', 'logical_completed',
      'requires_approved_edits', 'root_state', 'scheduled_date', 'step_number',
    ]);
    expect(draft.dates).toEqual(['2026-10-01', '2026-11-06', '2026-11-09', null, '2026-11-15']);
    expect(draft.approvedEdits).toBe(false);
    expect([0, 1, 2, 3, 4].map(i => canDraftStep(draft, i))).toEqual([false, true, false, true, false]);
    expect(canReviewEditorDraft(draft)).toBe(false);
    const approved = setApprovedDraftEdits(draft, true);
    expect(canDraftStep(approved, 2)).toBe(true);
    const changed = setDraftDate(approved, 2, '2026-11-10');
    expect(draftDateChanges(changed)).toEqual([{ step: 3, before: '2026-11-09', after: '2026-11-10', executionId: 'root-3', requiresReapproval: true }]);
    expect(resetEditorDraft(changed).dates).toEqual(draft.dates);
    expect(resetEditorDraft(changed).approvedEdits).toBe(false);
    expect(JSON.stringify(original)).toBe(before);
  });
  test('uses server capability even when raw status suggests a pending purchase', () => {
    const original = response();
    expect(original.graph!.steps[4]!.status).toBe('pending');
    expect(canDraftStep(setApprovedDraftEdits(adaptPlanEditor(original).graphDraft!, true), 4)).toBe(false);
  });
  test('uses selected stored service coverage without relabeling aggregate amounts', () => {
    const original = response();
    expect(editorServiceTargets(original, 'first')).toEqual([8, 16, 24, 32, 40]);
    expect(editorServiceTargets(original, 'disabledOther')).toEqual([15, 30, 45, 60, 75]);
    expect(original.graph!.steps[0]!.display).toBe(display);
    expect(original.history[0]!.display.quantity).toBeNull();
    expect(() => editorServiceTargets(original, 'missing')).toThrow('stored service');
    expect(() => editorServiceTargets(original, 'toString')).toThrow('stored service');
    expect(() => editorServiceTargets({ ...original, plan: { ...original.plan, services: null } }, 'first')).toThrow('stored service');
    expect(editorServiceTargets({ ...original, plan: { ...original.plan, services: { toString: service(10) } } }, 'toString')).toEqual([2, 4, 6, 8, 10]);
  });
  test('preserves graph-unavailable ordinary settings without a formula schedule', () => {
    const original = { ...response(), graph: null, graph_unavailable_reason: 'unsupported_schedule' as const };
    const adapted = adaptPlanEditor(original);
    expect(adapted.response).toBe(original); expect(adapted.graphDraft).toBeNull();
    expect(adapted.response.field_policy.controls['name']!.editable).toBe(true);
    expect(editorServiceTargets(original, 'first')).toBeNull();
  });
  test('propagates malformed supported graph errors instead of hiding them as unavailable', () => {
    const original = response();
    expect(() => adaptPlanEditor({ ...original, graph: { ...original.graph!, total_steps: 6 } })).toThrow('Incomplete schedule projection');
    expect(() => adaptPlanEditor({ ...original, revision: '' })).toThrow('Missing plan revision');
  });
});
