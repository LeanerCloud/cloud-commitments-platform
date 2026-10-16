/** Edit must not leak into the next Create open. Uses the real index.html markup. */
import * as fs from 'fs';
import * as path from 'path';
import { openCreatePlanModal, openNewPlanModal, closePlanModal, loadPlans, setupPlanHandlers, _resetRampHandlersForTest } from '../plans';

jest.mock('../api', () => ({
  getPlans: jest.fn(),
  getPlan: jest.fn(),
  getPlannedPurchases: jest.fn().mockResolvedValue({ purchases: [] }),
  listPlanAccounts: jest.fn().mockResolvedValue([]),
  listAccounts: jest.fn().mockResolvedValue([]),
  listAccountsMinimal: jest.fn().mockResolvedValue([]),
  getAccount: jest.fn().mockResolvedValue(null),
}));

jest.mock('../state', () => ({
  getRecommendations: jest.fn().mockReturnValue([]),
  getSelectedRecommendationIDs: jest.fn().mockReturnValue(new Set()),
  getVisibleRecommendations: jest.fn().mockReturnValue([]),
  setVisibleRecommendations: jest.fn(),
  getCurrentProvider: jest.fn().mockReturnValue(''),
  setCurrentProvider: jest.fn(),
  getCurrentAccountIDs: jest.fn().mockReturnValue([]),
  setCurrentAccountIDs: jest.fn(),
  subscribeProvider: jest.fn().mockReturnValue(() => {}),
  subscribeAccount: jest.fn().mockReturnValue(() => {}),
  // Literal ids (jest.mock is hoisted): administrators + purchasers groups.
  getCurrentUser: jest.fn().mockReturnValue({ id: 'u-admin', email: 'admin@example.com', groups: ['00000000-0000-5000-8000-000000000001', '00000000-0000-5000-8000-000000000007'] }),
  getPlansColumnFilters: jest.fn().mockReturnValue({}),
  setPlansColumnFilter: jest.fn(),
  clearAllPlansColumnFilters: jest.fn(),
}));

jest.mock('../history', () => ({ viewPlanHistory: jest.fn() }));
jest.mock('../archera', () => ({ openArcheraOfferModal: jest.fn() }));
jest.mock('../toast', () => ({ showToast: jest.fn(() => ({ dismiss: jest.fn() })) }));
jest.mock('../confirmDialog', () => ({ confirmDialog: jest.fn(() => Promise.resolve(true)) }));
jest.mock('../api/plan-editor', () => ({ getPlanEditor: jest.fn() }));

import * as api from '../api';
import { getPlanEditor, PlanEditorResponse } from '../api/plan-editor';
import { response as editorResponse, service as editorService } from './fixtures/plan-editor';

const el = <T extends HTMLElement>(id: string): T => document.getElementById(id) as T;
const flush = (): Promise<void> => new Promise(resolve => setTimeout(resolve, 0));
const azureEditor = (): PlanEditorResponse => {
  const base = editorResponse();
  return { ...base, plan: { ...base.plan, services: { vm: { ...editorService(60), provider: 'azure', service: 'compute', term: 1, payment: 'monthly' } } } };
};
const openEdit = async (editor: PlanEditorResponse): Promise<void> => {
  (getPlanEditor as jest.Mock).mockResolvedValue(editor);
  (api.getPlans as jest.Mock).mockResolvedValue({ plans: [{ id: 'plan-id', name: 'Stored', services: {} }] });
  await loadPlans();
  document.querySelector<HTMLButtonElement>('[data-action="edit-plan"]')!.click(); await flush();
};
const createDefaults = (): void => {
  expect(el<HTMLSelectElement>('plan-provider').value).toBe('aws');
  expect(el<HTMLSelectElement>('plan-service').value).toBe('ec2');
  expect(el<HTMLSelectElement>('plan-term').value).toBe('3');
  expect(el<HTMLSelectElement>('plan-payment').value).toBe('no-upfront');
  expect(el<HTMLInputElement>('plan-name').value).toBe('EC2 Full Coverage Purchase');
  expect(el<HTMLSelectElement>('plan-service').selectedOptions[0]!.parentElement!.classList.contains('hidden')).toBe(false);
};

describe('Edit then Create', () => {
  beforeEach(() => {
    document.body.innerHTML = (/<body[^>]*>([\s\S]*?)<\/body>/i.exec(fs.readFileSync(path.join(__dirname, '..', 'index.html'), 'utf8')) ?? [])[1]!;
    jest.clearAllMocks();
    (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
    _resetRampHandlersForTest(); setupPlanHandlers();
  });

  test('Edit shows the stored service through the real hooks and opens Advanced', async () => {
    await openEdit(editorResponse());
    expect(el<HTMLSelectElement>('plan-service').value).toBe('ec2');
    expect(el<HTMLSelectElement>('plan-term').value).toBe('3');
    expect(el<HTMLSelectElement>('plan-payment').value).toBe('all-upfront');
    expect(el('plan-accounts-selected').textContent).toBe('Current×');
    expect(el<HTMLDetailsElement>('plan-advanced-settings').open).toBe(true);
  });

  test('a stored value without an option is shown on a disabled option, never blank', async () => {
    const base = editorResponse();
    await openEdit({ ...base, plan: { ...base.plan, services: { first: { ...editorService(40), payment: 'bespoke', term: 7 } } } });
    const payment = el<HTMLSelectElement>('plan-payment'), term = el<HTMLSelectElement>('plan-term');
    expect(payment.value).toBe('bespoke'); expect(payment.selectedOptions[0]!.disabled).toBe(true);
    expect(term.value).toBe('7'); expect(term.selectedOptions[0]!.textContent).toBe('7 Years');
  });

  test('an unmatched stored service does not stay in the Create service list', async () => {
    const base = editorResponse();
    await openEdit({ ...base, plan: { ...base.plan, services: { first: { ...editorService(40), service: 'bespokesvc' } } } });
    expect(el<HTMLSelectElement>('plan-service').value).toBe('bespokesvc');
    closePlanModal();
    openNewPlanModal();
    expect(Array.from(el<HTMLSelectElement>('plan-service').options).map(option => option.value)).not.toContain('bespokesvc');
    createDefaults();
  });

  test('Edit-only DOM from the settings form does not leak into New Plan', async () => {
    const base = editorResponse(), open = { editable: true, reason: null, requires_approved_edits: false, requires_proposed_value_revalidation: false };
    await openEdit({ ...base, field_policy: { ...base.field_policy, controls: { ...base.field_policy.controls, target_accounts: open },
      services: { first: Object.fromEntries(['provider', 'service', 'term', 'payment', 'coverage'].map(field => [field, open])) } } });
    const sp = Array.from(el<HTMLSelectElement>('plan-service').options).find(option => option.value === 'savings-plans-compute')!;
    expect(sp.disabled).toBe(true);
    expect(document.getElementById('plan-account-search-reason')).toBeNull();
    closePlanModal();
    openNewPlanModal();
    expect(sp.disabled).toBe(false); expect(sp.textContent).toBe('Compute SP');
    expect(document.querySelectorAll('#plan-form p[id$="-reason"]:not(#plan-max-step-commitment-reason)')).toHaveLength(0);
    expect(document.querySelectorAll('#plan-form [aria-describedby$="-reason"]:not(#plan-max-step-commitment)')).toHaveLength(0);
    expect(el<HTMLInputElement>('plan-coverage').disabled).toBe(false);
  });

  test('a locked Edit leaves reasons that New Plan removes', async () => {
    await openEdit(editorResponse());
    expect(el('plan-term-reason')).not.toBeNull();
    closePlanModal();
    openNewPlanModal();
    expect(document.querySelectorAll('#plan-form p[id$="-reason"]:not(#plan-max-step-commitment-reason)')).toHaveLength(0);
    expect(el<HTMLSelectElement>('plan-term').disabled).toBe(false);
  });

  test('an Edit ramp does not leak into New Plan', async () => {
    await openEdit(editorResponse());
    expect((document.querySelector('input[name="ramp-schedule"]:checked') as HTMLInputElement).value).toBe('custom');
    closePlanModal();
    openNewPlanModal();
    expect((document.querySelector('input[name="ramp-schedule"]:checked') as HTMLInputElement).value).toBe('immediate');
    expect(Array.from(document.querySelectorAll<HTMLInputElement>('input[name="ramp-schedule"],#ramp-step-percent,#ramp-interval-days')).some(item => item.disabled)).toBe(false);
    expect(el('custom-ramp-config').classList.contains('hidden')).toBe(true);
    expect(el<HTMLInputElement>('ramp-interval-days').value).toBe('7');
    expect(document.getElementById('plan-ramp-schedule-reason')).toBeNull();
  });

  test('New Plan after an Azure Edit opens with Create defaults', async () => {
    await openEdit(azureEditor());
    expect(el<HTMLSelectElement>('plan-provider').value).toBe('azure');
    closePlanModal();
    openNewPlanModal();
    createDefaults();
  });

  test('Plan from N selected after an Azure Edit keeps only its own prefill', async () => {
    await openEdit(azureEditor());
    closePlanModal();
    openCreatePlanModal([{ id: 'r1', provider: 'aws', service: 'rds', term: 1, payment: 'no-upfront' } as unknown as api.Recommendation]);
    expect(el<HTMLSelectElement>('plan-provider').value).toBe('aws');
    expect(el<HTMLSelectElement>('plan-service').value).toBe('rds');
    expect(el<HTMLSelectElement>('plan-term').value).toBe('1');
    expect(el<HTMLInputElement>('plan-name').value).toBe('RDS Full Coverage Purchase');
    closePlanModal();
    openCreatePlanModal();
    createDefaults();
  });
});
