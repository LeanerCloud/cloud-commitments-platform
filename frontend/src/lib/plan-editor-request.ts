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
