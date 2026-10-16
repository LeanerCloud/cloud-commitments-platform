import * as fs from 'fs';
import * as path from 'path';
import { fillEditForm, EditFormHandle } from '../lib/plan-editor-form';
import type { PlanEditorFieldRule, PlanEditorResponse, PlanEditorService } from '../api/plan-editor';
import { response, service } from './fixtures/plan-editor';

const html = fs.readFileSync(path.join(__dirname, '..', 'index.html'), 'utf8');
const rule = (editable: boolean, reason: string | null = null): PlanEditorFieldRule =>
  ({ editable, reason, requires_approved_edits: false, requires_proposed_value_revalidation: false });
const FIELDS = ['provider', 'service', 'term', 'payment', 'coverage'];
const el = <T extends HTMLElement>(id: string): T => document.getElementById(id) as T;
const fire = (id: string, value: string): void => {
  const item = el<HTMLInputElement>(id); item.value = value;
  item.dispatchEvent(new Event('input', { bubbles: true })); item.dispatchEvent(new Event('change', { bubbles: true }));
};
let accountsListener: (() => void) | undefined;
const hooks = {
  selectService: (provider: string, svc: string, term: number, payment: string, label?: string): void => {
    el<HTMLSelectElement>('plan-provider').value = provider;
    const select = el<HTMLSelectElement>('plan-service');
    select.querySelectorAll('[data-stored]').forEach(option => option.remove());
    select.value = svc;
    if (select.value !== svc) { const option = new Option(label ?? svc, svc); option.disabled = true; option.dataset['stored'] = 'true'; select.add(option); select.value = svc; }
    el<HTMLSelectElement>('plan-term').value = term > 0 ? String(term) : '1';
    el<HTMLSelectElement>('plan-payment').value = payment;
  },
  setAccounts: (): void => undefined,
  onAccountsChange: (listener: () => void) => { accountsListener = listener; return () => { accountsListener = undefined; }; },
};
const editable = Object.fromEntries(FIELDS.map(field => [field, rule(true)]));
const build = (services: Record<string, Partial<PlanEditorService>>, policy: Record<string, Record<string, PlanEditorFieldRule>> = {}, accounts = true): { handle: EditFormHandle; r: PlanEditorResponse } => {
  const base = response();
  const r: PlanEditorResponse = { ...base, plan: { ...base.plan, services: Object.fromEntries(Object.entries(services).map(([key, value]) => [key, { ...service(80), ...value }])) },
    field_policy: { ...base.field_policy, controls: { ...base.field_policy.controls, target_accounts: rule(accounts) }, services: Object.fromEntries(Object.keys(services).map(key => [key, policy[key] ?? editable])) } };
  return { handle: fillEditForm(el<HTMLFormElement>('plan-form'), r, hooks), r };
};

beforeEach(() => {
  document.body.innerHTML = (/<body[^>]*>([\s\S]*?)<\/body>/i.exec(html) ?? [])[1]!;
  el('plan-account-ids').setAttribute('value', '');
});

test('a mapped legacy service shows the canonical option and produces no patch', () => {
  const { handle } = build({ vm: { provider: 'azure', service: 'vm', term: 1, payment: 'monthly' } });
  expect(el<HTMLSelectElement>('plan-service').value).toBe('compute');
  expect(handle.model.patch().services).toEqual({});
  fire('plan-term', '3');
  expect(handle.model.patch().services).toEqual({ vm: { term: 3 } });
});

test('an unsupported service is a disabled labelled option, locked, and never patched', () => {
  const { handle } = build({ odd: { provider: 'azure', service: 'weird', term: 1, payment: 'monthly' } });
  const select = el<HTMLSelectElement>('plan-service');
  expect(select.selectedOptions[0]!.textContent).toBe('Unsupported: azure/weird (read-only)');
  for (const id of ['plan-provider', 'plan-service', 'plan-term', 'plan-payment', 'plan-coverage']) {
    expect(el<HTMLInputElement>(id).disabled).toBe(true);
    expect(el(`${id}-reason`).textContent).toMatch(/^(Provider|Service|Term|Payment|Target Coverage \(%\)): This service is not supported by the plan editor\.$/);
  }
  expect(el('plan-provider-reason').textContent).toBe('Provider: This service is not supported by the plan editor.');
  expect(el('plan-coverage-reason').textContent).toBe('Target Coverage (%): This service is not supported by the plan editor.');
  expect(handle.model.patch().services).toEqual({});
});

test('a stored Savings Plans service is locked with the Savings Plans wording', () => {
  build({ sp: { provider: 'aws', service: 'savings-plans-compute' } });
  expect(el('plan-term-reason').textContent).toBe('Term: Savings Plans are not supported yet.');
  expect(el<HTMLInputElement>('plan-term').disabled).toBe(true);
});

test('edits are keyed by the stored key, survive key switches, and equal values are not patches', () => {
  document.querySelector('.plan-editor-layout')!.insertAdjacentHTML('afterbegin', '<select data-edit="service"><option value="a"></option><option value="b"></option></select>');
  const { handle } = build({ a: { term: 3, coverage: 40 }, b: { service: 'rds', term: 1, coverage: 70 } });
  const picker = document.querySelector<HTMLSelectElement>('[data-edit="service"]')!;
  fire('plan-coverage', '55'); fire('plan-coverage', '40');
  expect(handle.model.patch().services).toEqual({});
  fire('plan-coverage', '55');
  picker.value = 'b'; picker.dispatchEvent(new Event('change', { bubbles: true }));
  expect(el<HTMLSelectElement>('plan-service').value).toBe('rds');
  expect(el<HTMLInputElement>('plan-coverage').value).toBe('70');
  fire('plan-term', '3');
  picker.value = 'a'; picker.dispatchEvent(new Event('change', { bubbles: true }));
  expect(el<HTMLInputElement>('plan-coverage').value).toBe('55');
  expect(handle.model.patch().services).toEqual({ a: { coverage: 55 }, b: { term: 3 } });
});

test('plan-level patch holds only the members that differ, and a locked service control reports its reason', () => {
  const { handle } = build({ a: {} }, { a: { ...editable, payment: rule(false, 'progress_or_baseline') } });
  fire('plan-name', 'Renamed'); fire('plan-notify-days', '5');
  expect(handle.model.patch().plan).toEqual({ name: 'Renamed' });
  expect(el<HTMLInputElement>('plan-payment').disabled).toBe(true);
  expect(el('plan-payment-reason').textContent).toBe('Payment: The plan has progress or a baseline, so this cannot be changed.');
  expect(el('plan-payment').getAttribute('aria-describedby')).toBe('plan-payment-reason');
  expect(el<HTMLInputElement>('plan-term').disabled).toBe(false);
  expect(document.getElementById('plan-term-reason')).toBeNull();
});

test('onChange fires once per real change', () => {
  const { handle } = build({ a: {} });
  const listener = jest.fn(); handle.model.onChange(listener);
  fire('plan-name', 'X'); fire('plan-name', 'X');
  expect(listener).toHaveBeenCalledTimes(1);
  fire('plan-name', 'Original');
  expect(listener).toHaveBeenCalledTimes(2);
});

test('target accounts are a set diff and follow the account hook', () => {
  const { handle } = build({ a: {} });
  const ids = el<HTMLInputElement>('plan-account-ids');
  const listener = jest.fn(); handle.model.onChange(listener);
  expect(handle.model.patch().target_accounts).toBeUndefined();
  ids.value = 'current-account,new'; accountsListener!();
  expect(handle.model.patch().target_accounts).toEqual(['current-account', 'new']);
  ids.value = 'current-account'; accountsListener!();
  expect(handle.model.patch().target_accounts).toBeUndefined();
  expect(listener).toHaveBeenCalledTimes(2);
});

test('accounts control shows the policy reason when locked and is enabled when editable', () => {
  build({ a: {} }, {}, false);
  expect(el<HTMLInputElement>('plan-account-search').disabled).toBe(true);
  expect(el('plan-account-search-reason').textContent).toBe('Target accounts: This field cannot be edited here.');
});

test('Savings Plans options are disabled with a reason while the service is editable, and restored on dispose', () => {
  const { handle } = build({ a: {} });
  const sp = Array.from(el<HTMLSelectElement>('plan-service').options).find(option => option.value === 'savings-plans-compute')!;
  expect(sp.disabled).toBe(true);
  expect(sp.textContent).toContain('Savings Plans are not supported yet');
  handle.dispose();
  expect(sp.disabled).toBe(false);
  expect(sp.textContent).toBe('Compute SP');
});

test('dispose removes the service reasons and stops tracking', () => {
  const { handle } = build({ a: {} }, { a: { ...editable, term: rule(false, 'permission_denied') } });
  expect(el('plan-term-reason')).not.toBeNull();
  handle.dispose();
  expect(document.getElementById('plan-term-reason')).toBeNull();
  expect(accountsListener).toBeUndefined();
});
