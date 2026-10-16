import { apiRequest } from './client';
import type { ApiError } from './types';

export type PlanEditorApprovalState = 'not_approved' | 'approved_future' | 'approved_due' | 'legacy_uncertain' | 'not_applicable' | 'unknown';
export interface PlanEditorDisplay {
  readonly resource_type: string;
  readonly region: string;
  readonly quantity: number | null;
  readonly quantity_unit: 'instances' | 'reservation_units' | 'vcpu' | null;
  readonly full_term_commitment: number | null;
  readonly commitment_currency: string | null;
  readonly quantity_source: 'stored_snapshot' | 'unavailable';
  readonly commitment_source: 'stored_snapshot' | 'unavailable';
}
export interface PlanEditorSlot {
  readonly step_number: number;
  readonly logical_completed: boolean;
  readonly logical_completion_source: 'plan_progress';
  readonly execution_id: string | null;
  readonly scheduled_date: string | null;
  readonly status: string | null;
  readonly cumulative_target_percent: number;
  readonly root_state: 'missing' | 'unique' | 'ambiguous';
  readonly approval_state: PlanEditorApprovalState;
  readonly capability: 'fixed' | 'replace_pending' | 'approved_toggle_required' | 'needs_creation' | 'denied';
  readonly lock_reason: string | null;
  readonly editable: boolean;
  readonly requires_approved_edits: boolean;
  readonly display: PlanEditorDisplay | null;
}
export interface PlanEditorExecution {
  readonly execution_id: string;
  readonly status: string;
  readonly step_number: number;
  readonly retry_attempt_n: number;
  readonly scheduled_date: string | null;
  readonly retry_execution_id: string | null;
  readonly approval_state: PlanEditorApprovalState;
  readonly display: PlanEditorDisplay;
}
export interface PlanEditorService {
  readonly provider: string;
  readonly service: string;
  readonly enabled: boolean;
  readonly term: number;
  readonly payment: string;
  readonly coverage: number;
  readonly ramp_schedule: string;
  readonly include_engines: readonly string[] | null;
  readonly exclude_engines: readonly string[] | null;
  readonly include_regions: readonly string[] | null;
  readonly exclude_regions: readonly string[] | null;
  readonly include_types: readonly string[] | null;
  readonly exclude_types: readonly string[] | null;
  readonly min_count: number;
}
export interface PlanEditorStoredPlan {
  readonly id: string;
  readonly name: string;
  readonly enabled: boolean;
  readonly auto_purchase: boolean;
  readonly notification_days_before: number;
  readonly services: Readonly<Record<string, PlanEditorService>> | null;
  readonly ramp_schedule: {
    readonly type: string;
    readonly percent_per_step: number;
    readonly step_interval_days: number;
    // absent means cumulative
    readonly allocation_mode?: 'equal_count';
    readonly current_step: number;
    readonly total_steps: number;
    readonly start_date: string;
  };
  readonly created_at: string;
  readonly updated_at: string;
  readonly next_execution_date: string | null;
  readonly last_execution_date: string | null;
  readonly last_notification_sent: string | null;
  readonly max_step_commitment: number | null;
}
export interface PlanEditorFieldRule {
  readonly editable: boolean;
  readonly reason: string | null;
  readonly requires_approved_edits: boolean;
  readonly requires_proposed_value_revalidation: boolean;
}
export interface PlanEditorFieldPolicy {
  readonly controls: Readonly<Record<string, PlanEditorFieldRule>>;
  readonly services: Readonly<Record<string, Readonly<Record<string, PlanEditorFieldRule>>>>;
  readonly can_change_shape: boolean;
  readonly shape_lock_reason: string | null;
  readonly can_arm_or_raise_cap: boolean;
}
export interface PlanEditorResponse {
  readonly plan: PlanEditorStoredPlan;
  readonly account_ids: readonly string[];
  readonly accounts: readonly { readonly id: string; readonly name: string; readonly provider: string }[];
  readonly revision: string;
  readonly history: readonly PlanEditorExecution[];
  readonly graph: {
    readonly current_step: number;
    readonly total_steps: number;
    readonly structural_shape_change_allowed: boolean;
    readonly shape_lock_reason: string | null;
    readonly steps: readonly PlanEditorSlot[];
  } | null;
  readonly graph_unavailable_reason: string | null;
  readonly ledger_witness: {
    readonly ledger_state: string;
    readonly has_trusted_ramp_ledger: boolean;
    readonly baseline_present: boolean;
  };
  readonly field_policy: PlanEditorFieldPolicy;
}

export async function getPlanEditor(planId: string): Promise<PlanEditorResponse> {
  const response = await apiRequest<PlanEditorResponse | null>(editorPath(planId));
  if (response === null) throw new Error('Plan editor response is unavailable.');
  return response;
}

export interface PlanEditorServicePatch {
  readonly provider?: string;
  readonly service?: string;
  readonly enabled?: boolean;
  readonly term?: number;
  readonly payment?: string;
  readonly coverage?: number;
  readonly ramp_schedule?: string;
  readonly include_engines?: readonly string[] | null;
  readonly exclude_engines?: readonly string[] | null;
  readonly include_regions?: readonly string[] | null;
  readonly exclude_regions?: readonly string[] | null;
  readonly include_types?: readonly string[] | null;
  readonly exclude_types?: readonly string[] | null;
  readonly min_count?: number;
}
export interface PlanEditorPlanPatch {
  readonly name?: string;
  readonly enabled?: boolean;
  readonly auto_purchase?: boolean;
  readonly notification_days_before?: number;
  readonly max_step_commitment?: number | null;
  readonly target_accounts?: readonly string[];
  readonly services?: Readonly<Record<string, PlanEditorServicePatch>>;
  readonly ramp_schedule?: {
    readonly type?: string;
    readonly percent_per_step?: number;
    readonly step_interval_days?: number;
    readonly start_date?: string;
  };
}
export interface PlanEditorStepInput {
  readonly step_number: number;
  readonly scheduled_date: string | null;
}
export interface PlanEditorRequest {
  readonly command_id: string;
  readonly expected_revision: string | null;
  readonly plan: PlanEditorPlanPatch;
  readonly step_count?: number;
  readonly steps: readonly PlanEditorStepInput[];
  readonly allow_approved_edits: boolean;
  readonly reviewed_diff?: string;
  readonly acknowledged_cancellation_ids: readonly string[];
  readonly acknowledged_reapproval_ids: readonly string[];
}

export type PlanEditorDiffValue = string | number | boolean | readonly string[] | null;
export interface PlanEditorDiffChange {
  readonly field: string;
  readonly before: PlanEditorDiffValue;
  readonly after: PlanEditorDiffValue;
}
export interface PlanEditorStepChange {
  readonly step_number: number;
  readonly before_date: string | null;
  readonly after_date: string | null;
  readonly change: 'unchanged' | 'moved' | 'created' | 'replaced' | 'canceled';
  readonly old_execution_id: string | null;
  readonly approval_effect: 'none' | 'fresh_veto_window' | 'reapproval_required';
}
export interface PlanEditorTotals {
  readonly moved: number;
  readonly created: number;
  readonly canceled: number;
  readonly approvals_affected: number;
}
export interface PlanEditorDiff {
  readonly plan: readonly PlanEditorDiffChange[];
  readonly services: readonly { readonly key: string; readonly new_key: string | null; readonly fields: readonly PlanEditorDiffChange[] }[];
  readonly accounts: { readonly added: readonly string[]; readonly removed: readonly string[] };
  readonly steps: readonly PlanEditorStepChange[];
  readonly totals: PlanEditorTotals;
}
export interface PlanEditorPreviewResponse {
  readonly reviewed_diff: string;
  readonly required_cancellation_ids: readonly string[];
  readonly required_reapproval_ids: readonly string[];
  readonly diff: PlanEditorDiff;
}
export interface PlanEditorSaveResponse {
  readonly replayed: boolean;
  readonly mapping: readonly {
    readonly step_number: number;
    readonly old_execution_id: string | null;
    readonly new_execution_id: string | null;
    readonly scheduled_date: string | null;
  }[];
  readonly diff: PlanEditorDiff;
  readonly editor: PlanEditorResponse;
}

export type PlanEditorErrorCode = 'invalid_request' | 'unauthenticated' | 'permission_denied' | 'not_found' | 'plan_changed'
  | 'editor_field_locked' | 'editor_sequence_invalid' | 'editor_review_required' | 'editor_plan_unsupported' | 'editor_state_unsupported'
  | 'editor_attribution_unresolved' | 'editor_history_too_large' | 'internal_error' | 'editor_busy' | 'editor_unavailable';

const editorPath = (planId: string): string => `/plans/${encodeURIComponent(planId)}/editor`;

async function sendPlanEditor<T>(path: string, method: 'POST' | 'PUT', bodyJson: string, signal?: AbortSignal): Promise<T> {
  const response = await apiRequest<T | null>(path, signal ? { method, body: bodyJson, signal } : { method, body: bodyJson });
  if (response === null) throw Object.assign(new Error('The server returned no result.'), { status: 0 });
  return response;
}

export const previewPlanEditor = (planId: string, bodyJson: string, signal?: AbortSignal): Promise<PlanEditorPreviewResponse> =>
  sendPlanEditor(`${editorPath(planId)}/preview`, 'POST', bodyJson, signal);

export const savePlanEditor = (planId: string, bodyJson: string): Promise<PlanEditorSaveResponse> =>
  sendPlanEditor(editorPath(planId), 'PUT', bodyJson);

export type PlanEditorFailureKind = 'stale' | 'locked' | 'sequence' | 'review' | 'unsupported' | 'too-large' | 'busy' | 'forbidden'
  | 'unauthenticated' | 'not-found' | 'invalid' | 'unavailable' | 'uncertain' | 'server';
export interface PlanEditorFailure { readonly kind: PlanEditorFailureKind; readonly code: string | null; readonly message: string }

const conflictKinds: Readonly<Record<string, PlanEditorFailureKind>> = {
  plan_changed: 'stale', editor_field_locked: 'locked', editor_sequence_invalid: 'sequence', editor_review_required: 'review',
  editor_plan_unsupported: 'unsupported', editor_state_unsupported: 'stale', editor_attribution_unresolved: 'stale',
};
const uncertainText = 'The server did not confirm the result, so the change may have been saved. Reload the plan to check before saving again.';
const signInText = 'Your session has ended. Reload the page and sign in again; the draft is not kept.';

export function planEditorFailure(error: unknown): PlanEditorFailure {
  const failure = error as ApiError, status = typeof failure?.status === 'number' ? failure.status : 0;
  const rawCode = failure?.details?.['code'], code = typeof rawCode === 'string' ? rawCode : null;
  const message = failure instanceof Error ? failure.message : String(error);
  const result = (kind: PlanEditorFailureKind, text = message): PlanEditorFailure => ({ kind, code, message: text });
  if (status === 0 || status === 408) return result('uncertain', uncertainText);
  if (status === 401) return result('unauthenticated', signInText);
  if (status === 429 || code === 'editor_busy') return result('busy');
  if (status === 503 && code === 'editor_unavailable') return result('unavailable');
  if (status >= 500) return result('uncertain', uncertainText);
  if (status === 400 || status === 415) return result('invalid');
  if (status === 403) return result('forbidden');
  if (status === 404) return result('not-found');
  if (status === 413) return result('too-large');
  return result(status === 409 && code !== null && Object.prototype.hasOwnProperty.call(conflictKinds, code) ? conflictKinds[code]! : 'server');
}
