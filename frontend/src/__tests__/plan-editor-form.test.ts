import * as fs from 'fs';
import * as path from 'path';
import { fillEditForm, EditFormHooks } from '../lib/plan-editor-form';
import type { PlanEditorFieldRule } from '../api/plan-editor';
import { response } from './fixtures/plan-editor';

const html = fs.readFileSync(path.join(__dirname, '..', 'index.html'), 'utf8');
const rule = (editable: boolean, reason: string | null = null, approved = false): PlanEditorFieldRule =>
  ({ editable, reason, requires_approved_edits: approved, requires_proposed_value_revalidation: false });
const el = <T extends HTMLElement>(id: string): T => document.getElementById(id) as T;
const withControls = (controls: Record<string, PlanEditorFieldRule>) => {
  const base = response();
  return { ...base, field_policy: { ...base.field_policy, controls } };
};
const open = (controls: Record<string, PlanEditorFieldRule>, hooks?: Partial<EditFormHooks>) => {
  const spies = { selectService: jest.fn(), setAccounts: jest.fn() };
  const used: EditFormHooks = { ...spies, onAccountsChange: () => () => undefined, ...hooks };
  return { undo: fillEditForm(el<HTMLFormElement>('plan-form'), withControls(controls), used).dispose, hooks: spies };
};
const allEditable = { name: rule(true), enabled: rule(true), auto_purchase: rule(true), notification_days_before: rule(true) };

beforeEach(() => { document.body.innerHTML = (/<body[^>]*>([\s\S]*?)<\/body>/i.exec(html) ?? [])[1] ?? ''; });

test('fills the plan fields and keeps editable controls enabled', () => {
  open(allEditable);
  expect(el<HTMLInputElement>('plan-name').value).toBe('Original');
  expect(el<HTMLInputElement>('plan-enabled').checked).toBe(false);
  expect(el<HTMLInputElement>('plan-auto-purchase').checked).toBe(false);
  expect(el<HTMLInputElement>('plan-notify-days').value).toBe('5');
  for (const id of ['plan-name', 'plan-enabled', 'plan-auto-purchase', 'plan-notify-days']) {
    expect(el<HTMLInputElement>(id).disabled).toBe(false);
    expect(document.getElementById(`${id}-reason`)).toBeNull();
  }
});

test.each([
  ['plan-name', 'name'], ['plan-enabled', 'enabled'], ['plan-auto-purchase', 'auto_purchase'], ['plan-notify-days', 'notification_days_before'],
])('locks %s with the sentence for its policy reason', (id, key) => {
  open({ ...allEditable, [key]: rule(false, 'permission_denied') });
  const control = el<HTMLInputElement>(id), reason = el(`${id}-reason`);
  expect(control.disabled).toBe(true);
  expect(reason.textContent).toBe('You do not have permission to change this.');
  expect(control.getAttribute('aria-describedby')).toBe(reason.id);
  expect(['plan-name', 'plan-enabled', 'plan-auto-purchase', 'plan-notify-days'].filter(other => document.getElementById(`${other}-reason`))).toEqual([id]);
});

test('uses the approved-edits wording when no reason code is given, and never injects markup', () => {
  open({ ...allEditable, name: rule(false, null, true), enabled: rule(false, '<img src=x onerror=alert(1)>') });
  expect(el('plan-name-reason').textContent).toBe('This is approved. Turn on approved edits to change it.');
  expect(el('plan-enabled-reason').textContent).toBe('Locked (<img src=x onerror=alert(1)>)');
  expect(el('plan-enabled-reason').querySelector('img')).toBeNull();
});

test('a missing policy rule locks the control instead of leaving it open', () => {
  open({ name: rule(true) });
  expect(el<HTMLInputElement>('plan-notify-days').disabled).toBe(true);
  expect(el('plan-notify-days-reason').textContent).toBe('This field cannot be edited here.');
});

test('shows the stored service, term, coverage and accounts disabled', () => {
  const { hooks } = open(allEditable);
  expect(hooks.selectService).toHaveBeenCalledWith('aws', 'ec2', 3, 'all-upfront', undefined);
  expect(el<HTMLInputElement>('plan-coverage').value).toBe('40');
  expect(hooks.setAccounts).toHaveBeenCalledWith([{ id: 'current-account', name: 'Current' }]);
  for (const id of ['plan-provider', 'plan-service', 'plan-term', 'plan-payment', 'plan-coverage', 'plan-description', 'plan-account-search', 'ramp-step-percent', 'ramp-interval-days']) {
    expect((el(id) as HTMLInputElement).disabled).toBe(true);
  }
  document.querySelectorAll<HTMLInputElement>('input[name="ramp-schedule"]').forEach(radio => expect(radio.disabled).toBe(true));
});

test('chip remove buttons are disabled once the hook has rendered them', () => {
  open(allEditable, { setAccounts: () => { el('plan-accounts-selected').innerHTML = '<span class="account-chip">A<button type="button">x</button></span>'; } });
  expect(el('plan-accounts-selected').querySelector('button')!.disabled).toBe(true);
});

test('shows the cap disabled with a reason and the stored value', () => {
  const base = response();
  fillEditForm(el<HTMLFormElement>('plan-form'), { ...base, plan: { ...base.plan, max_step_commitment: 1500 } }, { selectService: jest.fn(), setAccounts: jest.fn(), onAccountsChange: () => () => undefined });
  const cap = el<HTMLInputElement>('plan-max-step-commitment');
  expect(cap.value).toBe('1500'); expect(cap.disabled).toBe(true);
  expect(el('plan-max-step-commitment-reason').textContent).toBe('Per-step cap editing is not available yet');
  expect(cap.getAttribute('aria-describedby')).toBe('plan-max-step-commitment-reason');
  open(allEditable);
  expect(cap.value).toBe('No cap');
});

test('undo re-enables the controls and removes the reasons', () => {
  const { undo } = open({ ...allEditable, name: rule(false, 'permission_denied') });
  undo();
  expect(el<HTMLInputElement>('plan-name').disabled).toBe(false);
  expect(el('plan-name').getAttribute('aria-describedby')).toBeNull();
  expect(document.getElementById('plan-name-reason')).toBeNull();
  expect(el<HTMLInputElement>('plan-term').disabled).toBe(false);
});

test('a plan without stored services does not call the service hook', () => {
  const base = response();
  const hooks = { selectService: jest.fn(), setAccounts: jest.fn(), onAccountsChange: () => () => undefined };
  fillEditForm(el<HTMLFormElement>('plan-form'), { ...base, plan: { ...base.plan, services: null } }, hooks);
  expect(hooks.selectService).not.toHaveBeenCalled();
});

test('throws, leaving nothing disabled, when a required control is missing', () => {
  el('plan-notify-days').remove();
  expect(() => open(allEditable)).toThrow('#plan-notify-days');
  expect(el<HTMLInputElement>('plan-name').disabled).toBe(false);
});
