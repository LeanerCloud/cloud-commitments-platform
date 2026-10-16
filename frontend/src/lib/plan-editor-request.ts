import type { PlanEditorPlanPatch, PlanEditorRequest, PlanEditorResponse, PlanEditorServicePatch, PlanEditorStepInput } from '../api/plan-editor';
import type { EditFormPatch } from './plan-editor-form';
import { canDraftStep, type EditorDraft } from './plan-editor-draft';
import { resolvePlanService } from './plan-service-ids';

export interface EditRequestInput {
  readonly response: PlanEditorResponse;
  readonly formPatch: EditFormPatch;
  readonly draft: EditorDraft | null;
  readonly commandId: string;
}
export type EditRequestResult = PlanEditorRequest | { readonly unavailable: string };

const PLAN_KEYS = ['name', 'enabled', 'auto_purchase', 'notification_days_before'] as const;
const SERVICE_KEYS = ['provider', 'service', 'term', 'payment', 'coverage'] as const;

function planPatch(input: EditRequestInput): PlanEditorPlanPatch {
  const { response, formPatch } = input, services = response.plan.services ?? {};
  const plan: Record<string, unknown> = {};
  for (const key of PLAN_KEYS) if (key in formPatch.plan) plan[key] = formPatch.plan[key];
  const patched: Record<string, PlanEditorServicePatch> = {};
  for (const [storedKey, fields] of Object.entries(formPatch.services)) {
    const stored = services[storedKey], entry: Record<string, string | number> = {};
    for (const key of SERVICE_KEYS) {
      const value = fields[key];
      if (value === undefined) continue;
      // a legacy slug and its canonical form are one identity, and the server canonicalizes it on Save
      const same = stored && (key === 'provider' ? value === stored.provider
        : key === 'service' ? value === stored.service || value === resolvePlanService(stored.provider, stored.service).canonical : false);
      if (!same) entry[key] = value;
    }
    if (Object.keys(entry).length) patched[storedKey] = entry;
  }
  if (Object.keys(patched).length) plan['services'] = patched;
  const ids = formPatch.target_accounts;
  if (ids) {
    const want = new Set(ids), have = new Set(response.account_ids);
    if (want.size !== have.size || ids.some(id => !have.has(id))) plan['target_accounts'] = [...ids];
  }
  return plan;
}

function stepInputs(response: PlanEditorResponse, draft: EditorDraft | null): PlanEditorStepInput[] | string {
  const { graph, plan } = response;
  if (graph === null) return plan.ramp_schedule.total_steps === 0 ? [] : response.graph_unavailable_reason ?? 'The schedule of this plan is unavailable.';
  const remaining = graph.steps.filter(slot => slot.step_number > graph.current_step).sort((a, b) => a.step_number - b.step_number);
  if (remaining.length === 0) return [];
  if (draft === null) return 'The schedule of this plan is unavailable.';
  const steps: PlanEditorStepInput[] = [];
  for (const slot of remaining) {
    const date = draft.dates[slot.step_number - 1] ?? null;
    if (date === null && canDraftStep(draft, slot.step_number - 1)) return `Choose a date for step ${slot.step_number}.`;
    steps.push({ step_number: slot.step_number, scheduled_date: date });
  }
  return steps;
}

export function buildEditRequest(input: EditRequestInput): EditRequestResult {
  const steps = stepInputs(input.response, input.draft);
  if (typeof steps === 'string') return { unavailable: steps };
  return {
    command_id: input.commandId, expected_revision: input.response.revision, plan: planPatch(input), steps,
    allow_approved_edits: input.draft?.approvedEdits ?? false, acknowledged_cancellation_ids: [], acknowledged_reapproval_ids: [],
  };
}

export const withReview = (request: PlanEditorRequest, reviewedDiff: string, cancellationIds: readonly string[], reapprovalIds: readonly string[]): PlanEditorRequest =>
  ({ ...request, reviewed_diff: reviewedDiff, acknowledged_cancellation_ids: [...cancellationIds].sort(), acknowledged_reapproval_ids: [...reapprovalIds].sort() });

// The one JSON.stringify whose string preview, Save and every retry send.
export const serializeRequest = (request: PlanEditorRequest): string => JSON.stringify(request);

export interface CreateRequestInput {
  readonly name: string;
  readonly enabled: boolean;
  readonly autoPurchase: boolean;
  readonly notifyDays: number;
  readonly accountIds: readonly string[];
  readonly service: { readonly provider: string; readonly service: string; readonly term: number; readonly payment: string; readonly coverage: number };
  readonly ramp: { readonly preset: string; readonly intervalDays: number; readonly percentPerStep: number };
  readonly dates: readonly string[];
  readonly commandId: string;
}
// Same rule as the server (API root ACK amendment d): the tolerance keeps p = 100/N at N steps.
export const cumulativeStepCount = (percent: number): number => Math.ceil(100 / percent - 1e-9);

// [ramp type, fixed percent per step or null for custom]; the preset fixes its percent (PresetRampSchedules, internal/config/types.go).
const RAMP_PRESETS: ReadonlyMap<string, readonly [string, number | null]> = new Map([
  ['immediate', ['immediate', 100]], ['weekly-25pct', ['weekly', 25]], ['monthly-10pct', ['monthly', 10]], ['custom', ['custom', null]]]);
const MAX_TOTAL_STEPS = 100, MAX_STEP_INTERVAL_DAYS = 365; // internal/config/constants.go:39,42

// Cumulative-percent mode only: top-level step_count is never sent with percent_per_step (the server answers 400).
// User-reachable problems return {unavailable}; an unknown preset is a programmer error (the radio values are a closed set) and throws.
export function buildCreateRequest(input: CreateRequestInput): PlanEditorRequest | { readonly unavailable: string } {
  const { service, ramp, dates } = input, key = `${service.provider}/${service.service}`;
  const preset = RAMP_PRESETS.get(ramp.preset);
  if (preset === undefined) throw new Error(`Unknown ramp schedule ${ramp.preset}.`);
  const [type, fixedPercent] = preset, percent = ramp.percentPerStep;
  if (input.accountIds.length === 0) return { unavailable: 'Choose at least one target account.' };
  if (resolvePlanService(service.provider, service.service).state !== 'canonical') return { unavailable: `${key} is not supported for a new plan.` };
  if (!(percent > 0 && percent <= 100) || fixedPercent !== null && percent !== fixedPercent) return { unavailable: 'Step percent must be above 0 and at most 100, and fixed by the chosen preset.' };
  if (!Number.isInteger(ramp.intervalDays) || ramp.intervalDays < 0 || ramp.intervalDays > MAX_STEP_INTERVAL_DAYS) {
    return { unavailable: `Interval must be a whole number of days from 0 to ${MAX_STEP_INTERVAL_DAYS}.` };
  }
  const count = cumulativeStepCount(percent);
  if (count > MAX_TOTAL_STEPS) return { unavailable: `This ramp needs ${count} steps; the limit is ${MAX_TOTAL_STEPS}.` };
  if (dates.length !== count) return { unavailable: `This ramp needs ${count} dates, got ${dates.length}.` };
  return {
    command_id: input.commandId, expected_revision: null,
    plan: {
      name: input.name, enabled: input.enabled, auto_purchase: input.autoPurchase, notification_days_before: input.notifyDays,
      target_accounts: [...input.accountIds],
      services: { [key]: { provider: service.provider, service: service.service, enabled: true, term: service.term, payment: service.payment,
        coverage: service.coverage, ramp_schedule: ramp.preset } },
      ramp_schedule: { type, percent_per_step: percent, step_interval_days: ramp.intervalDays, start_date: dates[0]! },
    },
    steps: dates.map((date, i) => ({ step_number: i + 1, scheduled_date: date })),
    allow_approved_edits: false, acknowledged_cancellation_ids: [], acknowledged_reapproval_ids: [],
  };
}
