import { apiRequest } from './client';

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
  const response = await apiRequest<PlanEditorResponse | null>(`/plans/${encodeURIComponent(planId)}/editor`);
  if (response === null) throw new Error('Plan editor response is unavailable.');
  return response;
}
