import type { PlanEditorDiffChange, PlanEditorDiffValue, PlanEditorPreviewResponse, PlanEditorStepChange } from './api/plan-editor';

// What Save must send alongside the reviewed diff: exactly the server's required sets, never inferred locally.
export interface ReviewAcknowledgments {
  readonly reviewedDiff: string;
  readonly cancellationIds: readonly string[];
  readonly reapprovalIds: readonly string[];
}
export interface ReviewRegion {
  readonly element: HTMLElement;
  readonly heading: HTMLElement;
  // Non-null only once every required acknowledgment is checked (a zero-impact preview is ready at once).
  ready(): ReviewAcknowledgments | null;
}

export const plural = (count: number, one: string, many: string): string => `${count} ${count === 1 ? one : many}`;
const text = (tag: string, content: string, className?: string): HTMLElement => {
  const node = document.createElement(tag);
  node.textContent = content;
  if (className) node.className = className;
  return node;
};
const show = (value: PlanEditorDiffValue): string => value === null ? 'none' : Array.isArray(value) ? (value.length ? value.join(', ') : 'none') : String(value);
const fieldRow = (change: PlanEditorDiffChange, prefix = ''): HTMLElement => text('li', `${prefix}${change.field}: ${show(change.before)} -> ${show(change.after)}`);
const effects: Readonly<Record<PlanEditorStepChange['approval_effect'], string>> = { none: '', fresh_veto_window: ', new veto window', reapproval_required: ', needs approval again' };
const stepLabel = (step: PlanEditorStepChange): string => `step ${step.step_number} (${step.before_date ?? 'no date'} -> ${step.after_date ?? 'no date'})`;

function list(title: string, rows: readonly HTMLElement[]): HTMLElement {
  const section = document.createElement('div');
  const ul = document.createElement('ul');
  ul.append(...rows);
  section.append(text('h4', title), ul);
  return section;
}

export function createReviewRegion(preview: PlanEditorPreviewResponse): ReviewRegion {
  const { diff } = preview, element = document.createElement('section');
  element.id = 'plan-review'; element.className = 'plan-review';
  const heading = text('h3', 'Review changes');
  heading.tabIndex = -1; heading.id = 'plan-review-heading';
  element.setAttribute('aria-labelledby', heading.id);
  element.append(heading);

  const fields = [...diff.plan.map(change => fieldRow(change)),
    ...diff.services.flatMap(service => service.fields.length || service.new_key !== null
      ? [...(service.new_key !== null ? [text('li', `Service ${service.key} becomes ${service.new_key}`)] : []), ...service.fields.map(change => fieldRow(change, `${service.new_key ?? service.key} `))]
      : [])];
  if (fields.length) element.append(list('Plan and service changes', fields));
  const accounts = [...diff.accounts.added.map(id => text('li', `Account added: ${id}`)), ...diff.accounts.removed.map(id => text('li', `Account removed: ${id}`))];
  if (accounts.length) element.append(list('Account changes', accounts));
  const steps = diff.steps.filter(step => step.change !== 'unchanged' || step.approval_effect !== 'none');
  element.append(steps.length ? list('Step changes', steps.map(step => text('li', `Step ${step.step_number}: ${step.change}, ${step.before_date ?? 'no date'} -> ${step.after_date ?? 'no date'}${effects[step.approval_effect]}`)))
    : text('p', 'No step changes.'));

  const byExecution = new Map(diff.steps.flatMap(step => step.old_execution_id === null ? [] : [[step.old_execution_id, step] as const]));
  const boxes: HTMLInputElement[] = [];
  const group = (ids: readonly string[], legend: string, sentence: string): void => {
    if (ids.length === 0) return;
    const set = document.createElement('fieldset');
    set.append(text('legend', legend));
    for (const id of ids) {
      const step = byExecution.get(id), label = document.createElement('label'), box = document.createElement('input');
      box.type = 'checkbox'; box.addEventListener('change', () => { sync(); });
      boxes.push(box);
      label.append(box, document.createTextNode(` I understand ${step ? stepLabel(step) : 'a purchase without a listed step'} ${sentence}`));
      set.append(label);
    }
    element.append(set);
  };
  group(preview.required_cancellation_ids, `${plural(preview.required_cancellation_ids.length, 'purchase', 'purchases')} will be canceled`, 'will be canceled.');
  group(preview.required_reapproval_ids, `${preview.required_reapproval_ids.length} approved ${preview.required_reapproval_ids.length === 1 ? 'purchase needs' : 'purchases need'} approval again`, 'will need approval again.');

  const confirm = text('button', 'Confirm and save') as HTMLButtonElement;
  confirm.type = 'button'; confirm.id = 'plan-review-confirm';
  const note = text('p', '', 'help-text');
  note.setAttribute('role', 'status');
  element.append(confirm, note);
  const done = (): boolean => boxes.every(box => box.checked);
  function sync(): void {
    confirm.disabled = !done();
    const left = boxes.filter(box => !box.checked).length;
    note.textContent = left ? `Acknowledge ${plural(left, 'more item', 'more items')} to continue.` : 'Ready to save.';
  }
  sync();
  return {
    element, heading,
    ready: () => done() ? { reviewedDiff: preview.reviewed_diff, cancellationIds: [...preview.required_cancellation_ids], reapprovalIds: [...preview.required_reapproval_ids] } : null,
  };
}
