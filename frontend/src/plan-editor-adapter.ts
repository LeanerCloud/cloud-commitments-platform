import type { PlanEditorResponse } from './api/plan-editor';
import { createEditorDraft, EditorDraft } from './lib/plan-editor-draft';

export function adaptPlanEditor(response: PlanEditorResponse): {
  readonly response: PlanEditorResponse;
  readonly graphDraft: EditorDraft | null;
} {
  const graph = response.graph;
  const graphDraft = graph === null ? null : createEditorDraft({
    kind: 'edit', planId: response.plan.id, revision: response.revision, totalSteps: graph.total_steps,
    slots: graph.steps.map(slot => ({
      step_number: slot.step_number, scheduled_date: slot.scheduled_date,
      cumulative_target_percent: slot.cumulative_target_percent, execution_id: slot.execution_id,
      logical_completed: slot.logical_completed, root_state: slot.root_state, capability: slot.capability,
      editable: slot.editable, requires_approved_edits: slot.requires_approved_edits,
    })),
  });
  return { response, graphDraft };
}

export function editorServiceTargets(response: PlanEditorResponse, serviceKey: string): readonly number[] | null {
  if (response.graph === null) return null;
  const services = response.plan.services;
  if (services === null || !Object.prototype.hasOwnProperty.call(services, serviceKey)) throw new Error('Choose a stored service.');
  const service = services[serviceKey]!;
  return response.graph.steps.map(slot => slot.cumulative_target_percent * service.coverage / 100);
}
