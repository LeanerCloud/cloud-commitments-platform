import { buildEditRequest, serializeRequest, withReview, EditRequestInput } from '../lib/plan-editor-request';
import { newUuid } from '../lib/uuid';
import { adaptPlanEditor } from '../plan-editor-adapter';
import { setApprovedDraftEdits, setDraftDate } from '../lib/plan-editor-draft';
import type { EditorDraft } from '../lib/plan-editor-draft';
import type { PlanEditorRequest, PlanEditorResponse } from '../api/plan-editor';
import { response, service } from './fixtures/plan-editor';

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const ID = '3f2b8c1e-5d4a-4e6f-8a9b-0c1d2e3f4a5b';
const ready = (r: PlanEditorResponse): EditorDraft => {
  let draft = adaptPlanEditor(r).graphDraft!;
  draft = setDraftDate(draft, 1, '2026-11-07');
  return setDraftDate(draft, 3, '2026-11-12');
};
const build = (over: Partial<EditRequestInput> & { r?: PlanEditorResponse } = {}): PlanEditorRequest => {
  const r = over.r ?? response();
  const out = buildEditRequest({ response: r, formPatch: { plan: {}, services: {} }, draft: 'draft' in over ? over.draft! : ready(r), commandId: ID, ...over });
  if ('unavailable' in out) throw new Error(out.unavailable);
  return out;
};
const unavailable = (input: Partial<EditRequestInput>): string => {
  const out = buildEditRequest({ response: response(), formPatch: { plan: {}, services: {} }, draft: null, commandId: ID, ...input });
  if (!('unavailable' in out)) throw new Error('expected unavailable');
  return out.unavailable;
};
const withServices = (services: PlanEditorResponse['plan']['services']): PlanEditorResponse => {
  const r = response(); return { ...r, plan: { ...r.plan, services } };
};

describe('buildEditRequest', () => {
  it('sends only the changed members and the complete remaining sequence', () => {
    const request = build({ formPatch: { plan: { name: 'Renamed' }, services: {} } });
    expect(request).toEqual({
      command_id: ID, expected_revision: 'opaque-revision', plan: { name: 'Renamed' },
      steps: [{ step_number: 2, scheduled_date: '2026-11-07' }, { step_number: 3, scheduled_date: '2026-11-09' },
        { step_number: 4, scheduled_date: '2026-11-12' }, { step_number: 5, scheduled_date: '2026-11-15' }],
      allow_approved_edits: false, acknowledged_cancellation_ids: [], acknowledged_reapproval_ids: [],
    });
    expect(Object.keys(request).sort()).toEqual(['acknowledged_cancellation_ids', 'acknowledged_reapproval_ids', 'allow_approved_edits',
      'command_id', 'expected_revision', 'plan', 'steps']);
  });
  it('maps every plan field and nothing else', () => {
    const patch = { name: 'N', enabled: true, auto_purchase: true, notification_days_before: 9, max_step_commitment: 5, description: 'x' };
    expect(build({ formPatch: { plan: patch, services: {} } }).plan).toEqual({ name: 'N', enabled: true, auto_purchase: true, notification_days_before: 9 });
  });
  it('maps every service field under the stored key and drops unknown ones', () => {
    const patch = { first: { provider: 'azure', service: 'cache', term: 1, payment: 'no-upfront', coverage: 55, min_count: 3 } };
    expect(build({ formPatch: { plan: {}, services: patch } }).plan).toEqual({
      services: { first: { provider: 'azure', service: 'cache', term: 1, payment: 'no-upfront', coverage: 55 } } });
  });
  it('never sends provider or service for a mapped legacy service that was not changed', () => {
    const r = withServices({ vm: { ...service(80), provider: 'azure', service: 'vm' } });
    const unchanged = { vm: { provider: 'azure', service: 'compute', term: 1 } };
    expect(build({ r, formPatch: { plan: {}, services: unchanged } }).plan).toEqual({ services: { vm: { term: 1 } } });
    expect(build({ r, formPatch: { plan: {}, services: { vm: { service: 'cache' } } } }).plan).toEqual({ services: { vm: { service: 'cache' } } });
  });
  it('sends nothing for provider and service that restate a non-legacy stored identity', () => {
    const patch = { first: { provider: 'aws', service: 'ec2', term: 1 } };
    expect(build({ formPatch: { plan: {}, services: patch } }).plan).toEqual({ services: { first: { term: 1 } } });
    const r = withServices({ odd: { ...service(80), provider: 'azure', service: 'bogus' } });
    expect(build({ r, formPatch: { plan: {}, services: { odd: { provider: 'azure', service: 'bogus', term: 1 } } } }).plan).toEqual({ services: { odd: { term: 1 } } });
  });
  it('emits steps ascending whatever the order of graph.steps', () => {
    const r = response(), shuffled = { ...r, graph: { ...r.graph!, steps: [...r.graph!.steps].reverse() } };
    expect(build({ r: shuffled, draft: ready(r) }).steps.map(s => s.step_number)).toEqual([2, 3, 4, 5]);
  });
  it('omits an empty service patch and an equal account set, sends a changed one', () => {
    const base = response(), r = { ...base, account_ids: ['a', 'b'] };
    const plan = (ids: string[]) => build({ r, formPatch: { plan: {}, services: { first: {} }, target_accounts: ids } }).plan;
    expect(plan(['b', 'a'])).toEqual({});
    expect(plan(['a', 'c'])).toEqual({ target_accounts: ['a', 'c'] });
    expect(plan(['a'])).toEqual({ target_accounts: ['a'] });
    expect(plan(['a', 'a'])).toEqual({ target_accounts: ['a', 'a'] });
    expect(plan(['a', 'b', 'b'])).toEqual({});
  });
  it('sends the draft approval flag and never the shape or read-only keys', () => {
    const r = response(), draft = setApprovedDraftEdits(ready(r), true);
    const request = build({ r, draft });
    expect(request.allow_approved_edits).toBe(true);
    for (const key of ['step_count', 'reviewed_diff', 'recommendations', 'allocation_mode', 'id']) expect(key in request).toBe(false);
    expect('ramp_schedule' in request.plan).toBe(false);
  });
  it('sends null only for a fixed slot whose date is unavailable', () => {
    const base = response(), steps = base.graph!.steps.map(s => s.step_number === 5 ? { ...s, scheduled_date: null }
      : s.step_number === 4 ? { ...s, capability: 'denied' as const, execution_id: 'root-4', root_state: 'unique' as const, scheduled_date: '2026-11-12' } : s);
    const r = { ...base, graph: { ...base.graph!, steps } };
    const draft = setDraftDate(adaptPlanEditor(r).graphDraft!, 1, '2026-11-07');
    expect(build({ r, draft }).steps[3]).toEqual({ step_number: 5, scheduled_date: null });
  });
  it('is unavailable for a draftable slot without a date, a missing draft or a graph-less plan with steps', () => {
    const r = response();
    expect(unavailable({ response: r, draft: adaptPlanEditor(r).graphDraft })).toMatch(/step 4/i);
    expect(unavailable({ response: r, draft: null })).toMatch(/schedule/i);
    expect(unavailable({ response: { ...r, graph: null, graph_unavailable_reason: 'projection_unsupported' } })).toBe('projection_unsupported');
    expect(unavailable({ response: { ...r, graph: null, graph_unavailable_reason: null } })).toBe('The schedule of this plan is unavailable.');
  });
  it('sends an empty sequence for a graph-less zero-step plan and when nothing remains', () => {
    const r = response(), zero = { ...r, graph: null, plan: { ...r.plan, ramp_schedule: { ...r.plan.ramp_schedule, total_steps: 0, current_step: 0 } } };
    const request = build({ r: zero, draft: null, formPatch: { plan: { enabled: true }, services: {} } });
    expect(request.steps).toEqual([]);
    expect(request.allow_approved_edits).toBe(false);
    expect(request.plan).toEqual({ enabled: true });
    const done = { ...r, graph: { ...r.graph!, current_step: 5 } };
    expect(build({ r: done, draft: null }).steps).toEqual([]);
  });
});

describe('withReview and serializeRequest', () => {
  it('returns the Save body with sorted copies and keeps the preview body untouched', () => {
    const preview = build(), cancel = ['b', 'a'], reapprove = ['d', 'c'];
    const save = withReview(preview, 'sha256:' + 'a'.repeat(64), cancel, reapprove);
    expect(save).toEqual({ ...preview, reviewed_diff: 'sha256:' + 'a'.repeat(64), acknowledged_cancellation_ids: ['a', 'b'], acknowledged_reapproval_ids: ['c', 'd'] });
    expect(cancel).toEqual(['b', 'a']);
    expect('reviewed_diff' in preview).toBe(false);
  });
  it('serializes once to a string that round-trips byte for byte', () => {
    const request = withReview(build(), 'sha256:' + 'b'.repeat(64), [], []), text = serializeRequest(request);
    expect(JSON.parse(text)).toEqual(request);
    expect(serializeRequest(request)).toBe(text);
  });
});

describe('newUuid', () => {
  const original = Object.getOwnPropertyDescriptor(globalThis, 'crypto');
  const install = (value: unknown): void => { Object.defineProperty(globalThis, 'crypto', { value, configurable: true }); };
  afterEach(() => { if (original) Object.defineProperty(globalThis, 'crypto', original); });
  it('uses crypto.randomUUID when present, lowercased', () => {
    install({ randomUUID: () => ID.toUpperCase() });
    expect(newUuid()).toBe(ID);
  });
  it('falls back to a canonical v4 from getRandomValues', () => {
    install({ getRandomValues: (bytes: Uint8Array) => { bytes.fill(0xff); return bytes; } });
    expect(newUuid()).toBe('ffffffff-ffff-4fff-bfff-ffffffffffff');
    install({ getRandomValues: (bytes: Uint8Array) => { bytes.fill(0); return bytes; } });
    expect(newUuid()).toBe('00000000-0000-4000-8000-000000000000');
    const real = require('crypto').webcrypto;
    install({ getRandomValues: real.getRandomValues.bind(real) });
    const ids = new Set(Array.from({ length: 20 }, newUuid));
    expect(ids.size).toBe(20);
    ids.forEach(id => expect(id).toMatch(UUID));
  });
  it('fails loudly without any secure random source', () => {
    install({});
    expect(() => newUuid()).toThrow(/Secure random/);
  });
});
