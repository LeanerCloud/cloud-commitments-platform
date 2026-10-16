/** Edit must not leak into the next Create open. Uses the real index.html markup. */
import * as fs from 'fs';
import * as path from 'path';
import { loadPlans, setupPlanHandlers, _resetRampHandlersForTest } from '../plans';

jest.mock('../api', () => ({
  getPlans: jest.fn(),
  getPlan: jest.fn(),
  getPlannedPurchases: jest.fn().mockResolvedValue({ purchases: [] }),
  listPlanAccounts: jest.fn().mockResolvedValue([]),
  listAccounts: jest.fn().mockResolvedValue([]),
  listAccountsMinimal: jest.fn().mockResolvedValue([]),
  getAccount: jest.fn().mockResolvedValue(null),
  deletePlan: jest.fn(),
  resumePlannedPurchase: jest.fn(),
  createPlannedPurchases: jest.fn(),
  pausePlannedPurchase: jest.fn(),
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
jest.mock('../api/plan-editor', () => ({ ...jest.requireActual('../api/plan-editor'), getPlanEditor: jest.fn() }));

import * as api from '../api';
import { showToast } from '../toast';

const flush = (): Promise<void> => new Promise(resolve => setTimeout(resolve, 0));
const refusal = (status: number, code: string, message: string): Error => Object.assign(new Error(message), { status, details: { code } });
const plan = (managed: boolean) => ({ id: 'plan-1', name: 'Managed', enabled: true, auto_purchase: false, notification_days_before: 3,
  services: {}, ramp_schedule: { type: 'weekly', percent_per_step: 25, step_interval_days: 7, current_step: 1, total_steps: 4, ...(managed ? { dates_managed: true } : {}) } });
const open = async (managed: boolean): Promise<void> => {
  (api.getPlans as jest.Mock).mockResolvedValue({ plans: [plan(managed)] });
  await loadPlans(); await flush();
};
const add = (): HTMLButtonElement => document.querySelector<HTMLButtonElement>('[data-action="add-purchases"]')!;

beforeEach(() => {
  document.body.innerHTML = (/<body[^>]*>([\s\S]*?)<\/body>/i.exec(fs.readFileSync(path.join(__dirname, '..', 'index.html'), 'utf8')) ?? [])[1]!;
  jest.clearAllMocks();
  (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [] });
  _resetRampHandlersForTest(); setupPlanHandlers();
});

test('a managed plan shows Add Purchases disabled with a visible reason', async () => {
  await open(true);
  expect(add().disabled).toBe(true);
  const note = document.getElementById(add().getAttribute('aria-describedby')!)!;
  expect(note.textContent).toBe("This plan's schedule is managed by the editor. Use Edit to change dates.");
  add().click();
  expect(document.getElementById('add-purchases-modal')).toBeNull();
});

test('an unmanaged plan keeps Add Purchases enabled with no note', async () => {
  await open(false);
  expect(add().disabled).toBe(false);
  expect(add().hasAttribute('aria-describedby')).toBe(false);
  expect(document.querySelector('[id^="plan-managed-"]')).toBeNull();
});

test('dates_managed false is not managed', async () => {
  const p = plan(false);
  (api.getPlans as jest.Mock).mockResolvedValue({ plans: [{ ...p, ramp_schedule: { ...p.ramp_schedule, dates_managed: false } }] });
  await loadPlans(); await flush();
  expect(add().disabled).toBe(false);
});

test('deleting a managed plan with live purchases shows the specific sentence', async () => {
  await open(true);
  (api.deletePlan as jest.Mock).mockRejectedValue(refusal(409, 'managed_plan_has_live_executions', 'raw server text'));
  document.querySelector<HTMLButtonElement>('[data-action="delete-plan"]')!.click(); await flush(); await flush();
  expect(showToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'This plan still has scheduled or unsettled purchases. Cancel or revoke them, then delete the plan.' }));
});

test('any other delete failure, including a 409 with another code, keeps the generic text', async () => {
  await open(false);
  (api.deletePlan as jest.Mock).mockRejectedValue(refusal(409, 'plan_changed', 'x'));
  document.querySelector<HTMLButtonElement>('[data-action="delete-plan"]')!.click(); await flush(); await flush();
  expect(showToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'Failed to delete plan' }));
});

test('a code on a non-409 status is not mapped', async () => {
  await open(false);
  (api.deletePlan as jest.Mock).mockRejectedValue(refusal(500, 'managed_plan_has_live_executions', 'x'));
  document.querySelector<HTMLButtonElement>('[data-action="delete-plan"]')!.click(); await flush(); await flush();
  expect(showToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'Failed to delete plan' }));
});

test('resume refused for a managed plan shows the editor sentence', async () => {
  (api.getPlannedPurchases as jest.Mock).mockResolvedValue({ purchases: [{ id: 'p1', plan_id: 'plan-1', plan_name: 'Managed', scheduled_date: '2026-11-01', provider: 'aws', service: 'ec2',
    resource_type: 'm5', region: 'us-east-1', count: 1, term: 1, payment: 'all-upfront', upfront_cost: 1, estimated_savings: 1, status: 'paused', step_number: 1, total_steps: 4 }] });
  await open(true);
  (api.resumePlannedPurchase as jest.Mock).mockRejectedValue(refusal(409, 'editor_reconciliation_required', 'raw'));
  document.querySelector<HTMLButtonElement>('[data-action="resume"]')!.click(); await flush(); await flush();
  expect(showToast).toHaveBeenCalledWith(expect.objectContaining({ message: 'This plan is managed by the schedule editor. Use Edit to change its dates and settings.' }));
});

test('a refused Add Purchases submit shows the editor sentence in the modal', async () => {
  await open(false);
  add().click(); await flush();
  (api.createPlannedPurchases as jest.Mock).mockRejectedValue(refusal(409, 'editor_reconciliation_required', 'raw'));
  document.getElementById('add-purchases-form')!.dispatchEvent(new Event('submit')); await flush(); await flush();
  expect(document.getElementById('add-purchases-error')!.textContent).toBe('This plan is managed by the schedule editor. Use Edit to change its dates and settings.');
});
