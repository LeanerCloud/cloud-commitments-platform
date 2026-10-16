import { mountPlanEditorEditPanel } from '../plan-editor-edit-panel';
import type { PlanEditorResponse } from '../api/plan-editor';
import { response } from './fixtures/plan-editor';

let host: HTMLElement, dispose: () => void;
const field = <T extends HTMLElement>(name: string): T => host.querySelector<T>(`[data-edit="${name}"]`)!;
const input = (name: string, value: string): void => {
  const element = field<HTMLInputElement | HTMLSelectElement>(name); element.value = value;
  element.dispatchEvent(new Event('input', { bubbles: true })); element.dispatchEvent(new Event('change', { bubbles: true }));
};
const toggle = (checked: boolean): void => {
  field<HTMLInputElement>('approved').checked = checked; field('approved').dispatchEvent(new Event('change', { bubbles: true }));
};
beforeEach(() => {
  jest.useFakeTimers({ now: new Date('2026-10-15T12:00:00Z') });
  document.body.innerHTML = '<div class="modal"><div class="plan-editor-layout"><aside id="editor"></aside><form><input value="untouched"></form></div></div>';
  host = document.getElementById('editor')!;
});
afterEach(() => { dispose?.(); jest.useRealTimers(); });

test('shows every logical slot/history and truthful per-service coverage without changing stored data', () => {
  const original = response(), before = JSON.stringify(original);
  dispose = mountPlanEditorEditPanel(host, original).dispose;
  expect(field('slots').children).toHaveLength(5);
  expect(field('slots').children[3]!.textContent).toContain('no current root');
  expect(host.querySelector('[data-preview-step="0"]')?.getAttribute('aria-label')).toContain('target 8%');
  expect(host.querySelector('[data-preview-step="3"]')).toBeNull();
  expect(field('history').textContent).toContain('attempt 2');
  expect(field('settings').textContent).toContain('unspecified / none');
  expect(field<HTMLSelectElement>('service').options).toHaveLength(2);
  input('service', 'disabledOther');
  expect(host.querySelector('[data-preview-step="1"]')?.getAttribute('aria-label')).toContain('target 30%');
  expect(field('target').textContent).toContain('Stored ramp share × service coverage');
  expect(JSON.stringify(original)).toBe(before);
  expect(document.querySelector('form input')?.getAttribute('value')).toBe('untouched');
  expect(field<HTMLInputElement>('date').form).toBeNull();
  expect(host.querySelectorAll('[data-preview-step][role="button"]')).toHaveLength(4);
  expect(host.querySelector('[data-preview-step="0"]')?.hasAttribute('data-fixed')).toBe(true);
  expect(host.querySelector('[data-preview-step="1"]')?.hasAttribute('data-fixed')).toBe(false);
});

test('keeps completed and approved steps fixed until explicit eligible consent, then retains dirty consent warnings', () => {
  dispose = mountPlanEditorEditPanel(host, response()).dispose;
  expect(field<HTMLInputElement>('approved').checked).toBe(false);
  expect(field<HTMLInputElement>('date').disabled).toBe(true);
  input('step', '2');
  expect(field<HTMLInputElement>('date').disabled).toBe(true);
  toggle(true);
  expect(field<HTMLInputElement>('date').disabled).toBe(false);
  input('date', '2026-11-10');
  expect(field('changes').textContent).toContain('2026-11-09 → 2026-11-10; reapproval required');
  toggle(false);
  expect(field<HTMLInputElement>('date').disabled).toBe(true);
  expect(field('status').textContent).toContain('Re-enable approved edits');
  expect(field('changes').textContent).toContain('2026-11-10');
  field<HTMLButtonElement>('reset').click();
  expect(field('changes').children).toHaveLength(0);
  expect(field<HTMLInputElement>('approved').checked).toBe(false);
  expect(field<HTMLInputElement>('date').value).toBe('2026-11-09');
});

test('requires explicit missing dates before block redistribution and preserves paired correction and focus nodes', () => {
  dispose = mountPlanEditorEditPanel(host, response()).dispose;
  toggle(true); input('step', '3');
  expect(field<HTMLInputElement>('date').value).toBe('');
  expect(field<HTMLInputElement>('date').disabled).toBe(false);
  expect(field<HTMLInputElement>('start').disabled).toBe(true);
  input('date', '2026-11-12');
  expect(field<HTMLInputElement>('start').disabled).toBe(false);
  const start = field<HTMLInputElement>('start'), end = field<HTMLInputElement>('end');
  start.focus(); input('start', '2026-11-14');
  expect(field('error').textContent).not.toBe('');
  expect(field('start')).toBe(start); expect(field('end')).toBe(end); expect(document.activeElement).toBe(start);
  expect(start.value).toBe('2026-11-14');
  input('end', '2026-11-16');
  expect(field('error').textContent).not.toBe('');
  input('start', '2026-11-07'); input('end', '2026-11-13');
  expect(field('error').textContent).toBe('');
  expect(field('changes').textContent).toContain('Step 4: no date → 2026-11-13');
  expect(field('slots').textContent).toContain('Step 1: 2026-10-01');
});

test('refuses unknown fixed neighbors and UTC rollover while preserving the last committed proposal', () => {
  const original = response();
  const modified: PlanEditorResponse = { ...original, graph: { ...original.graph!, steps: original.graph!.steps.map((slot, index) =>
    index === 4 ? { ...slot, scheduled_date: null, capability: 'fixed', editable: false } : slot) } };
  dispose = mountPlanEditorEditPanel(host, modified).dispose;
  input('step', '3'); input('date', '2026-11-12');
  expect(field('error').textContent).toContain('fixed date is unavailable');
  expect(field('changes').children).toHaveLength(0);
  field<HTMLButtonElement>('reset').click(); input('step', '1');
  jest.setSystemTime(new Date('2026-11-07T00:00:00Z'));
  input('date', '2026-11-07');
  expect(field('error').textContent).toContain('future UTC date');
  expect(field('changes').children).toHaveLength(0);
});

test('renders an empty calendar and complete missing-slot list without manufacturing dates', () => {
  const original = response();
  const missing: PlanEditorResponse = { ...original, plan: { ...original.plan, ramp_schedule: { ...original.plan.ramp_schedule, current_step: 0 } }, graph: { ...original.graph!, current_step: 0, steps: original.graph!.steps.map(slot => ({
    ...slot, logical_completed: false, scheduled_date: null, execution_id: null, root_state: 'missing',
    capability: 'needs_creation', editable: false, requires_approved_edits: false,
  })) } };
  dispose = mountPlanEditorEditPanel(host, missing).dispose;
  expect(host.querySelector('svg')).toBeNull();
  expect(host.textContent).toContain('No dated points');
  expect(field('slots').children).toHaveLength(5);
  expect(field<HTMLInputElement>('date').value).toBe('');
});

test('preserves ordinary-plan metadata and cleans up without fabricating a graph or carrying consent', () => {
  const original = response();
  dispose = mountPlanEditorEditPanel(host, { ...original, plan: { ...original.plan, ramp_schedule: { ...original.plan.ramp_schedule, current_step: 0, total_steps: 0 } }, graph: null, graph_unavailable_reason: 'unsupported_schedule' }).dispose;
  expect(field('controls').hidden).toBe(true);
  expect(host.textContent).toContain('unsupported_schedule');
  expect(field('settings').textContent).toContain('azure/compute');
  dispose();
  expect(host.hidden).toBe(true); expect(host.children).toHaveLength(0);
  dispose = mountPlanEditorEditPanel(host, response()).dispose;
  expect(field<HTMLInputElement>('approved').checked).toBe(false);
});

test('labels quantity only with returned snapshot provenance and never infers currency', () => {
  const original = response();
  const mount = (source: 'stored_snapshot' | 'unavailable'): (() => void) => mountPlanEditorEditPanel(host, {
    ...original, graph: { ...original.graph!, steps: original.graph!.steps.map((slot, index) => index === 1
      ? { ...slot, display: { ...slot.display!, quantity: 4, quantity_unit: 'instances', quantity_source: source } } : slot) },
  }).dispose;
  dispose = mount('unavailable'); input('step', '1');
  expect(field('selected').textContent).toContain('Quantity unavailable');
  expect(field('selected').textContent).not.toContain('4 instances');
  dispose(); dispose = mount('stored_snapshot'); input('step', '1');
  expect(field('selected').textContent).toContain('Stored snapshot quantity: 4 instances; not a fulfilled total');
  expect(field('selected').textContent).toContain('Amount unavailable');
});

test('keeps ambiguous null-status roots unavailable and fixed while retaining competing history', () => {
  const original = response(), retained = original.history[0]!;
  const ambiguous: PlanEditorResponse = { ...original,
    history: [{ ...retained, execution_id: 'competing-a', step_number: 2, status: 'pending' },
      { ...retained, execution_id: 'competing-b', step_number: 2, status: 'pending' }],
    graph: { ...original.graph!, steps: original.graph!.steps.map((slot, index) => index === 1 ? {
      ...slot, root_state: 'ambiguous', execution_id: null, scheduled_date: null, status: null,
      capability: 'fixed', editable: false, lock_reason: 'ambiguous_root',
    } : slot) },
  };
  dispose = mountPlanEditorEditPanel(host, ambiguous).dispose;
  expect(field('slots').children[1]!.textContent).toContain('status unavailable');
  expect(field('slots').children[1]!.textContent).not.toContain('no retained root');
  expect(field('history').children).toHaveLength(2);
  input('step', '1'); toggle(true);
  expect(field<HTMLInputElement>('date').disabled).toBe(true);
  expect(field<HTMLInputElement>('date').value).toBe('');
});

test('keeps a legacy 99-percent ramp distinct from selected service coverage', () => {
  const original = response();
  dispose = mountPlanEditorEditPanel(host, { ...original, plan: { ...original.plan, ramp_schedule: { ...original.plan.ramp_schedule, type: 'custom', percent_per_step: 33, total_steps: 3 } }, graph: { ...original.graph!, total_steps: 3,
    steps: original.graph!.steps.slice(0, 3).map((slot, index) => ({ ...slot, cumulative_target_percent: (index + 1) * 33 })) } }).dispose;
  expect(field('slots').textContent).toContain('ramp share 99%');
  expect(host.querySelector('[data-preview-step="2"]')?.getAttribute('aria-label')).toContain('target 39.6%');
  input('service', 'disabledOther');
  expect(host.querySelector('[data-preview-step="2"]')?.getAttribute('aria-label')).toContain('target 74.25%');
  expect(field('slots').textContent).toContain('ramp share 99%');
});

test('explains locked and executed steps with sentences instead of raw codes', () => {
  dispose = mountPlanEditorEditPanel(host, response()).dispose;
  const sentence = 'This step already ran and cannot be changed.';
  expect(field('slots').children[0]!.textContent).toContain(sentence);
  expect(field('selected').textContent).toContain(sentence);
  expect(field('slots').children[0]!.textContent).not.toContain('completed prefix');
  expect(field('slots').children[4]!.textContent).toContain('You do not have permission to change this.');
});

test('separates the slot status from its lock sentence with ". " and falls back without a reason code', () => {
  const original = response();
  dispose = mountPlanEditorEditPanel(host, { ...original, graph: { ...original.graph!, steps: original.graph!.steps.map((slot, index) =>
    index === 3 ? { ...slot, capability: 'fixed', editable: false, lock_reason: null } : slot) } }).dispose;
  const slots = field('slots').children;
  expect(slots[0]!.textContent).toBe('Step 1: 2026-10-01; ramp share 20%; pending. This step already ran and cannot be changed.');
  expect(slots[3]!.textContent).toBe('Step 4: date unavailable; ramp share 80%; no current root. fixed.');
  expect(slots[1]!.textContent).toBe('Step 2: 2026-11-06; ramp share 40%; pending. Local proposal enabled.');
  input('step', '3');
  expect(field('selected').textContent).toBe('This step is fixed. Quantity unavailable. Amount unavailable.');
  input('step', '0');
  expect(field('selected').textContent).toMatch(/^This step already ran and cannot be changed\. Quantity unavailable\./);
});
