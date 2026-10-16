import { planEditorFailure, previewPlanEditor, savePlanEditor, PlanEditorErrorCode, PlanEditorFailureKind, PlanEditorPreviewResponse, PlanEditorSaveResponse } from '../api/plan-editor';

const id = '0b6e5d8c-6f0a-4f5e-9a55-111111111111';
const bodyJson = '{"command_id":"c","steps":[]}';
// Contract fixtures: shapes pinned by api-preview-save section 1.2 and 1.3, not recorded server output.
const diff = { plan: [{ field: 'name', before: 'a', after: 'b' }], services: [{ key: 'azure/vm', new_key: 'azure/compute', fields: [] }],
  accounts: { added: [], removed: [] }, steps: [], totals: { moved: 3, created: 0, canceled: 0, approvals_affected: 0 } };
const preview: PlanEditorPreviewResponse = { reviewed_diff: `sha256:${'a'.repeat(64)}`, required_cancellation_ids: [], required_reapproval_ids: [], diff };

beforeEach(() => { global.fetch = jest.fn(); localStorage.setItem('auth_token', 'tok'); });
afterEach(() => { jest.restoreAllMocks(); localStorage.clear(); });

const respond = (status: number, body: unknown): void => {
  (global.fetch as jest.Mock).mockResolvedValue({ ok: status < 400, status, json: async () => { if (body === undefined) throw new Error('no body'); return body; } });
};
const failureOf = async (status: number, body: unknown): Promise<ReturnType<typeof planEditorFailure>> => {
  respond(status, body);
  try { await savePlanEditor(id, bodyJson); } catch (error) { return planEditorFailure(error); }
  throw new Error('expected a failure');
};

describe('clients', () => {
  test('preview POSTs the exact body string to the encoded id', async () => {
    respond(200, preview);
    await expect(previewPlanEditor('a/b', bodyJson)).resolves.toEqual(preview);
    const [url, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect(url).toBe('/api/plans/a%2Fb/editor/preview');
    expect(init.method).toBe('POST');
    expect(init.body).toBe(bodyJson);
  });
  test('sends the body byte for byte, never re-serialized', async () => {
    const raw = '{ "steps": [],  "command_id": "c" }';
    respond(200, preview);
    await previewPlanEditor(id, raw);
    await savePlanEditor(id, raw);
    const calls = (global.fetch as jest.Mock).mock.calls;
    expect(calls.map(call => call[1].body)).toEqual([raw, raw]);
  });
  test('preview passes the caller signal through', async () => {
    respond(200, preview);
    const controller = new AbortController();
    await previewPlanEditor(id, bodyJson, controller.signal);
    expect((global.fetch as jest.Mock).mock.calls[0][1].signal.aborted).toBe(false);
    controller.abort();
    expect((global.fetch as jest.Mock).mock.calls[0][1].signal.aborted).toBe(true);
  });
  test('Save PUTs the exact body string and keeps replayed', async () => {
    const saved = { replayed: true, mapping: [], diff, editor: {} } as unknown as PlanEditorSaveResponse;
    respond(200, saved);
    await expect(savePlanEditor(id, bodyJson)).resolves.toMatchObject({ replayed: true });
    const [url, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect(url).toBe(`/api/plans/${id}/editor`);
    expect(init.method).toBe('PUT');
    expect(init.body).toBe(bodyJson);
  });
  test('a 2xx with no body is an uncertain outcome, never a saved result', async () => {
    respond(200, undefined);
    await expect(savePlanEditor(id, bodyJson)).rejects.toMatchObject({ status: 0 });
    await expect(previewPlanEditor(id, bodyJson)).rejects.toMatchObject({ status: 0 });
    expect(planEditorFailure(await savePlanEditor(id, bodyJson).catch(error => error)).kind).toBe('uncertain');
  });
});

describe('planEditorFailure', () => {
  const table: [number, PlanEditorErrorCode, PlanEditorFailureKind][] = [
    [400, 'invalid_request', 'invalid'], [401, 'unauthenticated', 'unauthenticated'], [403, 'permission_denied', 'forbidden'],
    [404, 'not_found', 'not-found'], [409, 'plan_changed', 'stale'], [409, 'editor_field_locked', 'locked'],
    [409, 'editor_sequence_invalid', 'sequence'], [409, 'editor_review_required', 'review'], [409, 'editor_plan_unsupported', 'unsupported'],
    [409, 'editor_state_unsupported', 'stale'], [409, 'editor_attribution_unresolved', 'stale'], [413, 'editor_history_too_large', 'too-large'],
    [500, 'internal_error', 'uncertain'], [503, 'editor_busy', 'busy'], [503, 'editor_unavailable', 'unavailable'],
  ];
  test.each(table)('%s %s is %s', async (status, code, kind) => {
    const failure = await failureOf(status, { error: 'server words', code });
    expect(failure).toMatchObject({ kind, code });
    if (kind !== 'uncertain' && kind !== 'unauthenticated') expect(failure.message).toBe('server words');
  });
  test('statuses without a code', async () => {
    expect((await failureOf(413, { error: 'too big' })).kind).toBe('too-large');
    expect((await failureOf(415, { error: 'bad type' })).kind).toBe('invalid');
    expect((await failureOf(429, { error: 'slow down' })).kind).toBe('busy');
    expect((await failureOf(408, { error: 'timeout' })).kind).toBe('uncertain');
    expect((await failureOf(504, undefined)).kind).toBe('uncertain');
    expect((await failureOf(502, undefined)).kind).toBe('uncertain');
    expect((await failureOf(503, { error: 'down' })).kind).toBe('uncertain');
  });
  test('4xx outcomes are definitive', async () => {
    for (const [status, code] of [[409, 'plan_changed'], [403, 'permission_denied'], [400, 'invalid_request'], [409, 'surprise']] as const) {
      expect((await failureOf(status, { error: 'x', code })).kind).not.toBe('uncertain');
    }
  });
  test('an unknown code keeps the raw message and the code', async () => {
    expect(await failureOf(409, { error: 'a new reason', code: 'brand_new' })).toEqual({ kind: 'server', code: 'brand_new', message: 'a new reason' });
    expect(await failureOf(422, { error: 'odd' })).toEqual({ kind: 'server', code: null, message: 'odd' });
  });
  test('inherited object keys are not codes', async () => {
    for (const code of ['constructor', 'toString', '__proto__']) expect(await failureOf(409, { error: 'x', code })).toMatchObject({ kind: 'server', code });
  });
  test('no HTTP status means the outcome is unknown', () => {
    expect(planEditorFailure(Object.assign(new Error('timed out'), { status: 0 })).kind).toBe('uncertain');
    expect(planEditorFailure(new TypeError('Failed to fetch')).kind).toBe('uncertain');
    expect(planEditorFailure(new DOMException('aborted', 'AbortError')).kind).toBe('uncertain');
    expect(planEditorFailure('weird').kind).toBe('uncertain');
  });
  test('recovery texts tell the user what to do', async () => {
    expect((await failureOf(401, { error: 'x', code: 'unauthenticated' })).message).toMatch(/reload.*sign in/i);
    expect((await failureOf(504, undefined)).message).toMatch(/may have been saved/i);
  });
});
