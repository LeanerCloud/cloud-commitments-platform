import type { PlanEditorResponse, PlanEditorService, PlanEditorSlot } from '../../api/plan-editor';

export const service = (coverage: number): PlanEditorService => ({
  provider: 'aws', service: 'ec2', enabled: false, term: 3, payment: 'All Upfront', coverage,
  ramp_schedule: 'legacy', include_engines: null, exclude_engines: [], include_regions: ['region-a'],
  exclude_regions: null, include_types: [], exclude_types: ['type-b'], min_count: 2,
});
export const display = { resource_type: 'mixed', region: 'multiple', quantity: null, quantity_unit: null,
  full_term_commitment: null, commitment_currency: null, quantity_source: 'unavailable', commitment_source: 'unavailable' } as const;
const slot = (step_number: number, overrides: Partial<PlanEditorSlot> = {}): PlanEditorSlot => ({
  step_number, logical_completed: false, logical_completion_source: 'plan_progress', execution_id: `root-${step_number}`,
  scheduled_date: `2026-11-${String(step_number * 3).padStart(2, '0')}`, status: 'pending',
  cumulative_target_percent: step_number * 20, root_state: 'unique', approval_state: 'not_approved',
  capability: 'replace_pending', lock_reason: null, editable: true, requires_approved_edits: false, display, ...overrides,
});
export const response = (): PlanEditorResponse => ({
  plan: { id: 'plan-id', name: 'Original', enabled: false, auto_purchase: false, notification_days_before: 5,
    services: { first: service(40), disabledOther: { ...service(75), provider: 'azure', service: 'compute' } },
    ramp_schedule: { type: 'linear', percent_per_step: 20, step_interval_days: 3, current_step: 1, total_steps: 5, start_date: '2026-10-01T12:34:56Z' },
    created_at: '2026-09-01T01:00:00Z', updated_at: '2026-10-01T02:00:00Z',
    next_execution_date: null, last_execution_date: '2026-10-01T00:00:00Z', last_notification_sent: null, max_step_commitment: null },
  account_ids: ['current-account'], accounts: [{ id: 'current-account', name: 'Current', provider: 'aws' },
    { id: 'retained-account', name: 'Retained', provider: 'azure' }], revision: 'opaque-revision',
  history: [{ execution_id: 'retained-child', status: 'failed', step_number: 8, retry_attempt_n: 2,
    scheduled_date: null, retry_execution_id: null, approval_state: 'unknown', display }],
  graph: { current_step: 1, total_steps: 5, structural_shape_change_allowed: false, shape_lock_reason: 'completed_prefix', steps: [
    slot(1, { logical_completed: true, logical_completion_source: 'plan_progress', scheduled_date: '2026-10-01', capability: 'fixed', editable: false, lock_reason: 'completed_prefix' }),
    slot(2), slot(3, { status: 'scheduled', approval_state: 'approved_future', capability: 'approved_toggle_required', editable: false, requires_approved_edits: true, lock_reason: 'approved_edits_disabled' }),
    slot(4, { status: null, execution_id: null, scheduled_date: null, root_state: 'missing', capability: 'needs_creation', editable: false, display: null }),
    slot(5, { capability: 'denied', editable: false, lock_reason: 'permission_denied' }),
  ] }, graph_unavailable_reason: null,
  ledger_witness: { ledger_state: 'trusted', has_trusted_ramp_ledger: true, baseline_present: true },
  field_policy: { controls: { name: { editable: true, reason: null, requires_approved_edits: false, requires_proposed_value_revalidation: false } },
    services: { disabledOther: { coverage: { editable: false, reason: 'affected_root_fixed', requires_approved_edits: false, requires_proposed_value_revalidation: false } } },
    can_change_shape: false, shape_lock_reason: 'completed_prefix', can_arm_or_raise_cap: false },
});
