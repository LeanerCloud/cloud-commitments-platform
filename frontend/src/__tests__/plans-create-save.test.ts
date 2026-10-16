/** Create saves through PUT /editor with the real index.html markup: preview, review, one atomic Save. */
import * as fs from 'fs';
import * as path from 'path';
import { savePlan, openNewPlanModal, openCreatePlanModal, closePlanModal, setupPlanHandlers, _resetRampHandlersForTest } from '../plans';

jest.mock('../api', () => ({
  getPlans: jest.fn().mockResolvedValue({ plans: [] }), getPlan: jest.fn(), getPlannedPurchases: jest.fn().mockResolvedValue({ purchases: [] }),
  listPlanAccounts: jest.fn().mockResolvedValue([]), listAccounts: jest.fn().mockResolvedValue([]), listAccountsMinimal: jest.fn().mockResolvedValue([]),
  getAccount: jest.fn().mockResolvedValue(null),
}));
jest.mock('../state', () => ({
  getRecommendations: jest.fn().mockReturnValue([]), getSelectedRecommendationIDs: jest.fn().mockReturnValue(new Set()), getVisibleRecommendations: jest.fn().mockReturnValue([]),
  setVisibleRecommendations: jest.fn(), getCurrentProvider: jest.fn().mockReturnValue(''), setCurrentProvider: jest.fn(), getCurrentAccountIDs: jest.fn().mockReturnValue([]),
  setCurrentAccountIDs: jest.fn(), subscribeProvider: jest.fn().mockReturnValue(() => {}), subscribeAccount: jest.fn().mockReturnValue(() => {}),
  getCurrentUser: jest.fn().mockReturnValue({ id: 'u-admin', email: 'admin@example.com', groups: ['00000000-0000-5000-8000-000000000001', '00000000-0000-5000-8000-000000000007'] }),
  getPlansColumnFilters: jest.fn().mockReturnValue({}), setPlansColumnFilter: jest.fn(), clearAllPlansColumnFilters: jest.fn(),
}));
jest.mock('../history', () => ({ viewPlanHistory: jest.fn() }));
jest.mock('../archera', () => ({ openArcheraOfferModal: jest.fn() }));
jest.mock('../toast', () => ({ showToast: jest.fn(() => ({ dismiss: jest.fn() })) }));
jest.mock('../confirmDialog', () => ({ confirmDialog: jest.fn(() => Promise.resolve(true)) }));
jest.mock('../api/plan-editor', () => ({ ...jest.requireActual('../api/plan-editor'), getPlanEditor: jest.fn(), previewPlanEditor: jest.fn(), savePlanEditor: jest.fn() }));

import * as api from '../api';
import { showToast } from '../toast';
import { previewPlanEditor, savePlanEditor } from '../api/plan-editor';
import { response as editorResponse } from './fixtures/plan-editor';

const preview = previewPlanEditor as jest.Mock, save = savePlanEditor as jest.Mock;
const ACCOUNT = '3a4b5c6d-1e2f-4a3b-8c9d-0e1f2a3b4c5d';
const diff = { plan: [], services: [], accounts: { added: [], removed: [] }, steps: [], totals: { moved: 0, created: 4, canceled: 0, approvals_affected: 0 } };
const el = <T extends HTMLElement>(id: string): T => document.getElementById(id) as T;
const flush = (): Promise<void> => new Promise(resolve => setTimeout(resolve, 0));
const button = (): HTMLButtonElement => document.querySelector<HTMLButtonElement>('#plan-modal button[type="submit"]')!;
const fire = (node: HTMLElement, type = 'change'): void => { node.dispatchEvent(new Event(type, { bubbles: true })); };
const setValue = (id: string, value: string): void => { el<HTMLInputElement>(id).value = value; fire(el(id)); };
const pickAccount = (): void => setValue('plan-account-ids', ACCOUNT);
const ramp = (value: string): void => { const radio = document.querySelector<HTMLInputElement>(`input[name="ramp-schedule"][value="${value}"]`)!; radio.checked = true; fire(radio); };
const bodyOf = (call: unknown[]): Record<string, any> => JSON.parse(call[1] as string); // eslint-disable-line @typescript-eslint/no-explicit-any
const submit = (): boolean => !el('plan-form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
const check = (id: string, on: boolean): void => { el<HTMLInputElement>(id).checked = on; fire(el(id)); };
const review = async (): Promise<void> => { expect(submit()).toBe(true); await flush(); }; // true: the native submit was prevented

beforeEach(() => {
  document.body.innerHTML = (/<body[^>]*>([\s\S]*?)<\/body>/i.exec(fs.readFileSync(path.join(__dirname, '..', 'index.html'), 'utf8')) ?? [])[1]!;
  jest.clearAllMocks();
  preview.mockReset().mockResolvedValue({ reviewed_diff: 'sha256:' + 'd'.repeat(64), required_cancellation_ids: [], required_reapproval_ids: [], diff });
  save.mockReset().mockResolvedValue({ replayed: false, mapping: [], diff, editor: editorResponse() });
  (api.getPlans as jest.Mock).mockResolvedValue({ plans: [] });
  _resetRampHandlersForTest(); setupPlanHandlers(); el('plan-form').addEventListener('submit', event => void savePlan(event)); openNewPlanModal(); pickAccount();
});
afterEach(() => { closePlanModal(); });

describe('Create request', () => {
  it.each([['weekly-25pct', 4, 25], ['monthly-10pct', 10, 10], ['immediate', 1, 100]])('%s previews %i steps at %i percent, cumulative, with no step_count', async (preset, count, percent) => {
    ramp(preset); await review();
    const body = bodyOf(preview.mock.calls[0]!);
    expect(body.plan.ramp_schedule.percent_per_step).toBe(percent);
    expect(body.steps).toHaveLength(count);
    expect(body.expected_revision).toBeNull();
    expect('step_count' in body).toBe(false);
  });

  it('custom 33 percent sends four steps and the dragged date, never recommendations', async () => {
    openCreatePlanModal([{ id: 'r1', provider: 'aws', service: 'ec2', term: 3, payment: 'no-upfront', cloud_account_id: ACCOUNT } as unknown as api.Recommendation]);
    pickAccount(); ramp('custom'); setValue('ramp-step-percent', '33'); setValue('ramp-interval-days', '7');
    const date = document.querySelector<HTMLInputElement>('[data-editor="date"]')!;
    const step = document.querySelector<HTMLSelectElement>('[data-editor="step"]')!;
    step.value = '1'; fire(step); date.value = new Date(Date.parse(`${date.value}T00:00:00Z`) + 86_400_000).toISOString().slice(0, 10); fire(date);
    const moved = date.value;
    await review();
    const body = bodyOf(preview.mock.calls[0]!);
    expect(body.plan.ramp_schedule.percent_per_step).toBe(33);
    expect(body.steps.map((item: { scheduled_date: string }) => item.scheduled_date)[1]).toBe(moved);
    expect(body.steps).toHaveLength(4);
    expect(JSON.stringify(body)).not.toContain('recommendations');
    expect(body.plan.target_accounts).toEqual([ACCOUNT]);
  });

  it('sends what the form holds: non-default service, term, payment, coverage, notify days, name and custom ramp', async () => {
    setValue('plan-provider', 'aws');
    const service = el<HTMLSelectElement>('plan-service'); service.value = 'rds'; fire(service);
    el<HTMLSelectElement>('plan-term').value = '1'; el<HTMLSelectElement>('plan-payment').value = 'partial-upfront';
    setValue('plan-coverage', '55'); setValue('plan-notify-days', '7'); check('plan-enabled', false);
    ramp('custom'); setValue('ramp-step-percent', '20'); setValue('ramp-interval-days', '9'); pickAccount(); setValue('plan-name', 'My RDS ladder');
    await review();
    expect(el('plan-review-reason').textContent).toBe('');
    const body = bodyOf(preview.mock.calls[0]!);
    expect(body.plan).toMatchObject({
      name: 'My RDS ladder', enabled: false, auto_purchase: false, notification_days_before: 7, target_accounts: [ACCOUNT],
      services: { 'aws/rds': { provider: 'aws', service: 'rds', enabled: true, term: 1, payment: 'partial-upfront', coverage: 55, ramp_schedule: 'custom' } },
      ramp_schedule: { type: 'custom', percent_per_step: 20, step_interval_days: 9 },
    });
    expect(Object.keys(body.plan.services)).toEqual(['aws/rds']);
    expect(body.steps).toHaveLength(5);
    const dates = body.steps.map((item: { scheduled_date: string }) => Date.parse(item.scheduled_date));
    expect(dates.slice(1).map((date: number, i: number) => (date - dates[i]) / 86_400_000)).toEqual([9, 9, 9, 9]);
  });

  it('sends a non-AWS provider from the form: azure/compute, 1 year, monthly', async () => {
    setValue('plan-provider', 'azure');
    const service = el<HTMLSelectElement>('plan-service'); service.value = 'compute'; fire(service);
    el<HTMLSelectElement>('plan-term').value = '1'; el<HTMLSelectElement>('plan-payment').value = 'monthly'; fire(el('plan-payment'));
    pickAccount();
    await review();
    expect(el('plan-review-reason').textContent).toBe('');
    expect(bodyOf(preview.mock.calls[0]!).plan.services).toEqual({
      'azure/compute': { provider: 'azure', service: 'compute', enabled: true, term: 1, payment: 'monthly', coverage: 80, ramp_schedule: expect.any(String) },
    });
  });

  it('names a Savings Plans service once, in the described reason, and disables review', async () => {
    const select = el<HTMLSelectElement>('plan-service');
    select.value = 'savings-plans-compute'; fire(select);
    const sentence = 'Savings Plans are not supported in the schedule editor';
    expect(Array.from(document.querySelectorAll('#plan-modal p')).filter(node => node.textContent === sentence).map(node => node.id)).toEqual(['plan-review-reason']);
    expect(button().getAttribute('aria-describedby')).toBe('plan-review-reason');
    expect(el('plan-review-reason').getAttribute('aria-live')).toBe('polite');
    expect(button().disabled).toBe(true);
  });

  it('says why when no service or no name is entered', async () => {
    const select = el<HTMLSelectElement>('plan-service'); select.selectedIndex = -1; fire(select);
    expect(el('plan-review-reason').textContent).toBe('Choose a service.');
    await review(); expect(preview).not.toHaveBeenCalled();
    select.value = 'ec2'; fire(select); setValue('plan-name', '  ');
    expect(el('plan-review-reason').textContent).toBe('Enter a plan name.');
    expect(button().disabled).toBe(true);
  });

  it('blocks review with a reason when no account is chosen', async () => {
    setValue('plan-account-ids', '');
    expect(button().disabled).toBe(true);
    expect(el('plan-review-reason').textContent).toBe('Choose at least one target account.');
    await review();
    expect(preview).not.toHaveBeenCalled();
  });

  it('blocks review while edited dates are stale after a ramp change, until Rebuild', async () => {
    const date = document.querySelector<HTMLInputElement>('[data-editor="date"]')!;
    date.value = new Date(Date.parse(`${date.value}T00:00:00Z`) + 86_400_000).toISOString().slice(0, 10); fire(date);
    ramp('monthly-10pct');
    expect(button().disabled).toBe(true);
    expect(el('plan-review-reason').textContent).toContain('Rebuild proposed dates');
    await review();
    expect(preview).not.toHaveBeenCalled();
  });

  it('rejects a fractional coverage with a reason instead of sending it', async () => {
    setValue('plan-coverage', '2.5');
    expect(button().disabled).toBe(true);
    expect(el('plan-review-reason').textContent).toContain('Target coverage must be a whole number');
  });
});

describe('Create Save', () => {
  it('shows the created step count for a new plan', async () => {
    await review();
    expect(document.querySelector('#plan-review-feedback [role="status"]')!.textContent).toBe('4 steps created; 0 dates moved; 0 steps removed; 0 approvals affected');
  });

  it('saves once through PUT /editor with the reviewed bytes, then closes, reloads the list and toasts', async () => {
    await review();
    expect(button().textContent).toBe('Save changes');
    submit(); submit();
    expect(save).toHaveBeenCalledTimes(1);
    const [id, saved] = save.mock.calls[0]!;
    expect(id).toBe(preview.mock.calls[0]![0]);
    expect(bodyOf([id, saved]).command_id).toBe(bodyOf(preview.mock.calls[0]!).command_id);
    await flush();
    expect(api.getPlans).toHaveBeenCalled();
    expect(showToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'Plan created successfully', kind: 'success' }));
    expect(el('plan-modal').classList.contains('hidden')).toBe(true);
  });

  it('a retry after an uncertain failure resends the same bytes, command id and plan id', async () => {
    save.mockRejectedValueOnce(Object.assign(new Error('bad gateway'), { status: 502 }));
    await review(); submit(); await flush();
    expect(el('plan-review-feedback').textContent).toContain('This plan may already have been created. Check the plans list before saving again.');
    expect(el('plan-modal').classList.contains('hidden')).toBe(false);
    submit(); await flush();
    expect(save).toHaveBeenCalledTimes(2);
    expect(save.mock.calls[1]).toEqual(save.mock.calls[0]);
  });

  it('an edit after an uncertain failure creates the same plan id again with a new command id', async () => {
    save.mockRejectedValueOnce(Object.assign(new Error('bad gateway'), { status: 502 }))
      .mockRejectedValueOnce(Object.assign(new Error('conflict'), { status: 409, details: { code: 'plan_changed' } }));
    await review(); submit(); await flush();
    setValue('plan-name', 'Edited after the failure');
    await review(); submit(); await flush();
    expect(save).toHaveBeenCalledTimes(2);
    expect(save.mock.calls[1]![0]).toBe(save.mock.calls[0]![0]);
    expect(bodyOf(save.mock.calls[1]!).command_id).not.toBe(bodyOf(save.mock.calls[0]!).command_id);
    expect(bodyOf(save.mock.calls[1]!).plan.name).toBe('Edited after the failure');
    expect(el('plan-review-feedback').textContent).toContain('This plan may already have been created. Check the plans list');
  });

  it('a second dialog uses a new plan id and command id', async () => {
    await review(); submit(); await flush();
    closePlanModal(); openNewPlanModal(); pickAccount(); await review();
    const first = [preview.mock.calls[0]![0], bodyOf(preview.mock.calls[0]!).command_id], second = [preview.mock.calls[1]![0], bodyOf(preview.mock.calls[1]!).command_id];
    expect(second[0]).not.toBe(first[0]);
    expect(second[1]).not.toBe(first[1]);
  });

  it('changing the request after a preview keeps the plan id and gets a new command id', async () => {
    await review();
    ramp('monthly-10pct'); await review();
    expect(preview.mock.calls[1]![0]).toBe(preview.mock.calls[0]![0]);
    expect(bodyOf(preview.mock.calls[1]!).command_id).not.toBe(bodyOf(preview.mock.calls[0]!).command_id);
  });

  it('a failed Save keeps the dialog open and never uses a legacy writer', async () => {
    save.mockRejectedValueOnce(Object.assign(new Error('Validation failed'), { status: 400, details: { code: 'editor_invalid_request' } }));
    await review(); submit(); await flush();
    expect(el('plan-modal').classList.contains('hidden')).toBe(false);
    expect(document.querySelector('#plan-review-feedback [role="alert"]')!.textContent).toBe('Validation failed');
    expect((api as unknown as Record<string, unknown>)['createPlan']).toBeUndefined();
    expect((api as unknown as Record<string, unknown>)['updatePlan']).toBeUndefined();
  });
});
