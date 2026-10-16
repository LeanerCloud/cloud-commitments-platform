import * as fs from 'fs';
import * as path from 'path';
import { fillEditForm, mountCreateGuards } from '../lib/plan-editor-form';
import { mountPlanEditorEditPanel } from '../plan-editor-edit-panel';
import { mountPlanReview, saveBlockers, unsupportedServiceBlocker } from '../plan-editor-save';
import { previewPlanEditor } from '../api/plan-editor';
import type { PlanEditorResponse } from '../api/plan-editor';
import { response as baseResponse, service } from './fixtures/plan-editor';

jest.mock('../api/plan-editor', () => ({ ...jest.requireActual('../api/plan-editor'), previewPlanEditor: jest.fn() }));
const html = fs.readFileSync(path.join(__dirname, '..', 'index.html'), 'utf8');
const el = <T extends HTMLElement>(id: string): T => document.getElementById(id) as T;
const withServices = (services: Record<string, ReturnType<typeof service>>, total = 5): PlanEditorResponse => {
  const base = baseResponse();
  return { ...base, plan: { ...base.plan, services, ramp_schedule: { ...base.plan.ramp_schedule, total_steps: total } } };
};
const svc = (provider: string, name: string, enabled = true) => ({ ...service(40), provider, service: name, enabled });
const noPatch = { plan: {}, services: {} };

describe('saveBlockers', () => {
  it('names an enabled unsupported service', () => {
    expect(saveBlockers(withServices({ k: svc('azure', 'bogus') }), noPatch)).toBe('This plan uses a service the editor does not support (azure/bogus). It cannot be saved here.');
  });
  it('ignores a disabled unsupported entry and a legacy slug', () => {
    expect(saveBlockers(withServices({ k: svc('azure', 'bogus', false), v: svc('azure', 'vm') }), noPatch)).toBeNull();
  });
  it.each(['savings-plans', 'savingsplans', 'savingsplans-compute', 'savings-plans-compute'])('treats %s as Savings Plans, not as an unsupported service', slug => {
    const r = withServices({ k: svc('aws', slug) });
    expect(unsupportedServiceBlocker(r)).toBeNull();
    expect(saveBlockers(r, noPatch)).toMatch(/^Savings Plans cannot be laddered/);
  });
  it('ignores a disabled Savings Plans service on a multi-step plan', () => {
    expect(saveBlockers(withServices({ k: svc('aws', 'savings-plans-compute', false) }), noPatch)).toBeNull();
  });
  it('lets a single-step Savings Plans plan through', () => {
    expect(saveBlockers(withServices({ k: svc('aws', 'savings-plans-compute') }, 1), noPatch)).toBeNull();
  });
  it('blocks an empty target set, from the patch or from storage', () => {
    const r = withServices({ k: svc('aws', 'ec2') });
    expect(saveBlockers(r, { ...noPatch, target_accounts: [] })).toBe('Choose at least one target account.');
    expect(saveBlockers({ ...r, account_ids: [] }, noPatch)).toBe('Choose at least one target account.');
    expect(saveBlockers({ ...r, account_ids: [] }, { ...noPatch, target_accounts: ['a'] })).toBeNull();
  });
  it('blocks arming auto-purchase without a cap', () => {
    const r = withServices({ k: svc('aws', 'ec2') });
    expect(saveBlockers(r, { plan: { auto_purchase: true }, services: {} })).toMatch(/per-step cap/);
    expect(saveBlockers({ ...r, plan: { ...r.plan, max_step_commitment: 5 } }, { plan: { auto_purchase: true }, services: {} })).toBeNull();
  });
});

describe('Edit of a plan with an enabled unsupported service', () => {
  const mount = (r: PlanEditorResponse) => {
    document.body.innerHTML = (/<body[^>]*>([\s\S]*?)<\/body>/i.exec(html) ?? [])[1] ?? '';
    const form = el<HTMLFormElement>('plan-form'), host = el('plan-editor-panel');
    const fill = fillEditForm(form, r, { selectService: jest.fn(), setAccounts: jest.fn(), onAccountsChange: () => () => undefined });
    const edit = mountPlanEditorEditPanel(host, r);
    const button = Array.from(form.elements).find((item): item is HTMLButtonElement => item instanceof HTMLButtonElement && item.type === 'submit')!;
    const review = mountPlanReview({ response: r, host, button, edit, form: fill.model, generation: () => 1 });
    return { button, edit, review, fill };
  };
  const clickStep = (n: number): void => { el('plan-editor-panel').querySelector(`[data-preview-step="${n}"]`)!.dispatchEvent(new MouseEvent('click', { bubbles: true })); };
  beforeEach(() => (previewPlanEditor as jest.Mock).mockReset());

  it('is read-only, shows the reason in the footer feedback, and sends nothing', () => {
    const { button, edit, review } = mount(withServices({ k: svc('azure', 'bogus') }));
    expect(button.disabled).toBe(true);
    expect(el('plan-review-reason').textContent).toContain('azure/bogus');
    expect(el('plan-review-feedback').contains(el('plan-review-reason'))).toBe(true);
    expect(el('plan-readonly-reason').textContent).toContain('azure/bogus');
    expect(el<HTMLInputElement>('plan-name').disabled).toBe(true);
    for (const name of ['date', 'step', 'start', 'reset']) expect(el('plan-editor-panel').querySelector<HTMLInputElement>(`[data-edit="${name}"]`)!.disabled).toBe(true);
    expect(el('plan-editor-panel').querySelector('[data-edit="readonly"]')!.textContent).toContain('azure/bogus');
    const before = edit.draft()!.selected;
    clickStep(2);
    expect(edit.draft()!.selected).toBe(before);
    review.review();
    expect(previewPlanEditor).not.toHaveBeenCalled();
  });
  it('keeps the form locked after the stored-service select switches', () => {
    const r = withServices({ k: svc('azure', 'bogus'), j: svc('aws', 'ec2') }), open = { editable: true, reason: null, requires_approved_edits: false, requires_proposed_value_revalidation: false };
    const { fill } = mount({ ...r, field_policy: { ...r.field_policy, services: { j: Object.fromEntries(['provider', 'service', 'term', 'payment', 'coverage'].map(field => [field, open])) } } });
    const select = el('plan-editor-panel').querySelector<HTMLSelectElement>('[data-edit="service"]')!;
    select.value = 'j'; select.dispatchEvent(new Event('change', { bubbles: true }));
    expect(el<HTMLInputElement>('plan-name').disabled).toBe(true);
    expect(el<HTMLSelectElement>('plan-service').disabled).toBe(true);
    fill.dispose();
    expect(document.getElementById('plan-readonly-reason')).toBeNull();
    expect(el<HTMLInputElement>('plan-name').disabled).toBe(false);
  });
  it('stays interactive for a supported plan', () => {
    const { edit, button } = mount(withServices({ k: svc('aws', 'ec2') }));
    expect(document.getElementById('plan-readonly-reason')).toBeNull();
    const before = edit.draft()!.selected;
    clickStep(2);
    expect(edit.draft()!.selected).not.toBe(before);
    expect(button.disabled).toBe(true);
    expect(el('plan-review-reason').textContent).not.toMatch(/does not support|Savings Plans/);
  });
  it('renders a stored SP alias as Savings Plans, with the SP reason', () => {
    mount(withServices({ k: svc('aws', 'savingsplans') }, 1));
    expect(el('plan-service-reason').textContent).toContain('Savings Plans are not supported yet.');
    expect(el('plan-review-reason').textContent).not.toMatch(/does not support/);
  });
  it('disables the auto-purchase toggle with its reason when there is no cap or no permission', () => {
    mount(withServices({ k: svc('aws', 'ec2') }));
    expect(el<HTMLInputElement>('plan-auto-purchase').disabled).toBe(true);
    expect(el('plan-auto-purchase-reason').textContent).toBe('Automatic purchase needs a per-step cap, which cannot be set yet');
    const r = withServices({ k: svc('aws', 'ec2') });
    mount({ ...r, plan: { ...r.plan, max_step_commitment: 10 } });
    expect(el('plan-auto-purchase-reason').textContent).toBe('You do not have permission to enable automatic purchase');
    mount({ ...r, plan: { ...r.plan, max_step_commitment: 10 }, field_policy: { ...r.field_policy, can_arm_or_raise_cap: true } });
    expect(el<HTMLInputElement>('plan-auto-purchase').disabled).toBe(false);
    mount({ ...r, plan: { ...r.plan, auto_purchase: true }, field_policy: { ...r.field_policy, can_arm_or_raise_cap: false } });
    expect(el<HTMLInputElement>('plan-auto-purchase').disabled).toBe(false);
  });
  it('blocks Save with a visible reason when the target set is emptied', () => {
    const r = withServices({ k: svc('aws', 'ec2') });
    const { button } = mount({ ...r, account_ids: [] });
    expect(button.disabled).toBe(true);
    expect(el('plan-review-reason').textContent).toBe('Choose at least one target account.');
  });
});

describe('Create guards', () => {
  const create = () => {
    document.body.innerHTML = (/<body[^>]*>([\s\S]*?)<\/body>/i.exec(html) ?? [])[1] ?? '';
    const onChange = jest.fn(), guards = mountCreateGuards(el<HTMLFormElement>('plan-form'), onChange);
    return { guards, onChange, select: el<HTMLSelectElement>('plan-service') };
  };
  it('shows every Savings Plans option disabled with the note, still in the list', () => {
    const { select } = create();
    const sp = Array.from(select.options).filter(option => option.value.startsWith('savings-plans-'));
    expect(sp).toHaveLength(4);
    sp.forEach(option => { expect(option.disabled).toBe(true); expect(option.textContent).toContain('Savings Plans are not supported yet'); });
    expect(select.querySelector<HTMLOptionElement>('option[value="ec2"]')!.disabled).toBe(false);
  });
  it('disables auto-purchase with the cap reason', () => {
    create();
    expect(el<HTMLInputElement>('plan-auto-purchase').disabled).toBe(true);
    expect(el<HTMLInputElement>('plan-auto-purchase').checked).toBe(false);
    expect(el('plan-auto-purchase-reason').textContent).toBe('Automatic purchase needs a per-step cap, which cannot be set yet');
  });
  it('blocks a programmatically selected SP service with a visible reason, and clears it', () => {
    const { guards, select, onChange } = create();
    expect(guards.sync()).toBe('');
    select.value = 'savings-plans-compute'; select.dispatchEvent(new Event('change'));
    expect(onChange).toHaveBeenCalled();
    expect(guards.sync()).toBe('Savings Plans are not supported in the schedule editor');
    expect(el('plan-create-reason').textContent).toBe('Savings Plans are not supported in the schedule editor');
    select.value = 'ec2';
    expect(guards.sync()).toBe('');
    guards.dispose();
    expect(document.getElementById('plan-create-reason')).toBeNull();
    expect(el<HTMLInputElement>('plan-auto-purchase').disabled).toBe(false);
  });
});
