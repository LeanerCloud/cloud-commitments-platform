import * as fs from 'fs';
import * as path from 'path';
import { fillEditForm } from '../lib/plan-editor-form';
import type { PlanEditorFieldRule } from '../api/plan-editor';
import { response } from './fixtures/plan-editor';

const html = fs.readFileSync(path.join(__dirname, '..', 'index.html'), 'utf8');
const el = <T extends HTMLElement>(id: string): T => document.getElementById(id) as T;
const lock = (reason: string): PlanEditorFieldRule => ({ editable: false, reason, requires_approved_edits: false, requires_proposed_value_revalidation: false });
const open = (percent: number, days: number, type = 'linear', stepCount?: PlanEditorFieldRule, extra: Record<string, unknown> = {}) => {
  const base = response();
  const controls = stepCount ? { ...base.field_policy.controls, step_count: stepCount } : base.field_policy.controls;
  return fillEditForm(el<HTMLFormElement>('plan-form'), {
    ...base, plan: { ...base.plan, ramp_schedule: { ...base.plan.ramp_schedule, type, percent_per_step: percent, step_interval_days: days, ...extra } },
    field_policy: { ...base.field_policy, controls },
  }, { selectService: () => undefined, setAccounts: () => undefined, onAccountsChange: () => () => undefined });
};
const checked = (): string => document.querySelector<HTMLInputElement>('input[name="ramp-schedule"]:checked')!.value;
const rampControls = (): HTMLInputElement[] => Array.from(document.querySelectorAll<HTMLInputElement>('input[name="ramp-schedule"],#ramp-step-percent,#ramp-interval-days'));

beforeEach(() => { document.body.innerHTML = (/<body[^>]*>([\s\S]*?)<\/body>/i.exec(html) ?? [])[1] ?? ''; });

test.each([[100, 0, 'immediate'], [25, 7, 'weekly-25pct'], [10, 30, 'monthly-10pct']])('stored %i%% every %i days selects %s', (percent, days, radio) => {
  open(percent, days, 'custom');
  expect(checked()).toBe(radio);
  expect(el('custom-ramp-config').classList.contains('hidden')).toBe(true);
  expect(rampControls().every(item => item.disabled)).toBe(true);
});

test('a stored pair that matches no preset selects Custom with its values, whatever the type string says', () => {
  open(20, 3, 'weekly');
  expect(checked()).toBe('custom');
  expect(el('custom-ramp-config').classList.contains('hidden')).toBe(false);
  expect(el<HTMLInputElement>('ramp-step-percent').value).toBe('20');
  expect(el<HTMLInputElement>('ramp-interval-days').value).toBe('3');
  expect(rampControls().every(item => item.disabled)).toBe(true);
});

test('the policy lock shows the step_count sentence; otherwise the not-available sentence', () => {
  const handle = open(25, 7, 'linear', lock('progress_or_baseline'));
  const reason = el('plan-ramp-schedule-reason');
  expect(reason.textContent).toBe('Ramp-up schedule: The plan has progress or a baseline, so this cannot be changed.');
  expect(document.querySelector('.ramp-options')!.getAttribute('aria-describedby')).toBe(reason.id);
  handle.dispose();
  open(25, 7);
  expect(el('plan-ramp-schedule-reason').textContent).toBe('Ramp-up schedule: Changing the ramp of an existing plan is not available yet');
});

test('dispose restores the Create ramp defaults and removes the reason', () => {
  const handle = open(20, 3, 'linear', lock('progress_or_baseline'));
  handle.dispose();
  expect(checked()).toBe('immediate');
  expect(el<HTMLInputElement>('ramp-step-percent').value).toBe('20');
  expect(el<HTMLInputElement>('ramp-interval-days').value).toBe('7');
  expect(el('custom-ramp-config').classList.contains('hidden')).toBe(true);
  expect(rampControls().some(item => item.disabled)).toBe(false);
  expect(document.getElementById('plan-ramp-schedule-reason')).toBeNull();
  expect(document.getElementById('plan-ramp-schedule')).toBeNull();
  expect(document.querySelectorAll('[aria-describedby="plan-ramp-schedule-reason"]')).toHaveLength(0);
});

test.each([[3, '33.33'], [31, '3.23']])('an equal-count ramp of %i steps shows Custom with a rounded percent and the step count', (steps, shown) => {
  open(100 / steps, 7, 'linear', undefined, { allocation_mode: 'equal_count', total_steps: steps });
  expect(checked()).toBe('custom');
  expect(el<HTMLInputElement>('ramp-step-percent').value).toBe(shown);
  expect(el('plan-ramp-schedule-reason').textContent).toContain(`Equal-count ramp: ${steps} steps.`);
});

test('a one-step equal-count ramp uses the singular', () => {
  open(100, 7, 'linear', undefined, { allocation_mode: 'equal_count', total_steps: 1 });
  expect(el('plan-ramp-schedule-reason').textContent).toContain('Equal-count ramp: 1 step.');
});

test('a stored immediate type selects Immediate whatever the percent and interval are', () => {
  open(50, 5, 'immediate');
  expect(checked()).toBe('immediate');
});

test('the radio group is named and carries the reason, and dispose removes it', () => {
  const handle = open(25, 7);
  const group = document.querySelector('.ramp-options')!;
  expect(group.getAttribute('role')).toBe('radiogroup');
  expect(document.getElementById(group.getAttribute('aria-labelledby')!)!.textContent).toBe('Ramp-up Schedule');
  expect(group.getAttribute('aria-describedby')).toBe('plan-ramp-schedule-reason');
  expect(el('ramp-step-percent').getAttribute('aria-describedby')).not.toBe('plan-ramp-schedule-reason');
  handle.dispose();
  expect(group.hasAttribute('aria-describedby')).toBe(false);
  expect(group.getAttribute('role')).toBeNull();
  expect(group.hasAttribute('aria-labelledby')).toBe(false);
});

test('the equal-count suffix follows a full stop in both reason branches, without doubling it', () => {
  open(25, 7, 'linear', undefined, { allocation_mode: 'equal_count', total_steps: 4 });
  expect(el('plan-ramp-schedule-reason').textContent).toBe('Ramp-up schedule: Changing the ramp of an existing plan is not available yet. Equal-count ramp: 4 steps.');
  document.body.innerHTML = (/<body[^>]*>([\s\S]*?)<\/body>/i.exec(html) ?? [])[1] ?? '';
  open(25, 7, 'linear', lock('progress_or_baseline'), { allocation_mode: 'equal_count', total_steps: 4 });
  expect(el('plan-ramp-schedule-reason').textContent).toBe('Ramp-up schedule: The plan has progress or a baseline, so this cannot be changed. Equal-count ramp: 4 steps.');
});

test('an equal-count plan whose values match a preset still shows Custom', () => {
  open(25, 7, 'linear', undefined, { allocation_mode: 'equal_count', total_steps: 4 });
  expect(checked()).toBe('custom');
  expect(el('custom-ramp-config').classList.contains('hidden')).toBe(false);
  expect(el('plan-ramp-schedule-reason').textContent).toContain('Equal-count ramp: 4 steps.');
});
