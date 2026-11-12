// An unsaved plan form survives a session expiry (#1735): kept in sessionStorage while edited, reopened on the next init.
import { openNewPlanModal, closePlanModal, setupPlanHandlers, restorePlanDraft, _resetRampHandlersForTest } from '../plans';
import * as api from '../api';
import * as state from '../state';
import { showToast } from '../toast';

jest.mock('../api', () => ({ getPlan: jest.fn(), listPlanAccounts: jest.fn().mockResolvedValue([]) }));

jest.mock('../state', () => ({
  getCurrentUser: jest.fn(),
  subscribeProvider: jest.fn().mockReturnValue(() => {}), subscribeAccount: jest.fn().mockReturnValue(() => {}),
}));

jest.mock('../toast', () => ({ showToast: jest.fn() }));

function buildPlanModalDOM(): void {
  _resetRampHandlersForTest(); // listeners bind to the DOM built below
  const radios = ['immediate', 'weekly-25pct', 'monthly-10pct', 'custom']
    .map((v, i) => `<input type="radio" name="ramp-schedule" value="${v}"${i ? '' : ' checked'}>`).join('');
  document.body.innerHTML = `<div id="plan-modal"><h3 id="plan-modal-title"></h3><form id="plan-form">
    <input type="hidden" id="plan-id"><input type="text" id="plan-name"><input type="hidden" id="plan-account-ids">
    <div id="plan-accounts-selected"></div><textarea id="plan-description"></textarea>
    <select id="plan-provider"><option value="aws">AWS</option><option value="azure">Azure</option></select>
    <select id="plan-service"><optgroup label="AWS" data-provider="aws"><option value="ec2">EC2</option></optgroup></select>
    <select id="plan-term"><option value="1">1</option></select><select id="plan-payment"><option value="no-upfront">No</option></select>
    <input type="number" id="plan-coverage" value="80"><input type="checkbox" id="plan-auto-purchase">
    <input type="number" id="plan-notify-days" value="3"><input type="checkbox" id="plan-enabled" checked>
    ${radios}
    <div id="custom-ramp-config" class="hidden"><input type="number" id="ramp-step-percent" value="20"><input type="number" id="ramp-interval-days" value="7"></div></form></div>`;
}

const PLAN = {
  id: 'plan-1', name: 'Saved name', enabled: true, auto_purchase: false,
  services: { 'aws:ec2': { provider: 'aws', service: 'ec2', enabled: true, term: 1, payment: 'no-upfront', coverage: 80 } },
  ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0 },
};
let stillOnPlans = true;
const KEY = 'cudly:plan-form-draft';
const seed = (over: Record<string, unknown>) => sessionStorage.setItem(KEY, JSON.stringify({
  userId: 'u-admin', savedAt: Date.now(), planId: '', ramp: 'immediate', accounts: [], fields: { 'plan-name': 'Kept' }, ...over,
}));
const USER = { id: 'u-admin', email: 'a@example.com', groups: [] as string[] };

const vals = (...ids: string[]): string[] => ids.map(id => (document.getElementById(id) as HTMLInputElement).value);
const el = <T extends HTMLElement>(id: string): T => document.getElementById(id) as T;
function type(id: string, value: string): void {
  const input = el<HTMLInputElement>(id);
  input.value = value;
  input.dispatchEvent(new Event('input', { bubbles: true }));
}
const settleDebounce = (): Promise<void> => jest.advanceTimersByTimeAsync(400);
function backSessionStorage(): Record<'getItem' | 'setItem' | 'removeItem', jest.Mock> {
  const store = new Map<string, string>();
  const m = sessionStorage as unknown as Record<'getItem' | 'setItem' | 'removeItem', jest.Mock>;
  m.getItem.mockImplementation((k: string) => store.get(k) ?? null);
  m.setItem.mockImplementation((k: string, v: string) => { store.set(k, v); });
  m.removeItem.mockImplementation((k: string) => { store.delete(k); });
  return m;
}
function reloadPageDom(): void {
  buildPlanModalDOM();
  setupPlanHandlers();
}

describe('plan draft survives session expiry (#1735)', () => {
  beforeEach(() => {
    jest.useFakeTimers();
    jest.clearAllMocks();
    (api.listPlanAccounts as jest.Mock).mockResolvedValue([]);
    (showToast as jest.Mock).mockReset();
    backSessionStorage();
    (state.getCurrentUser as jest.Mock).mockReturnValue(USER);
    buildPlanModalDOM();
    setupPlanHandlers();
    openNewPlanModal();
  });
  afterEach(() => {
    closePlanModal();
    jest.useRealTimers();
  });

  it('saves the draft after typing, without any credential', async () => {
    (state.getCurrentUser as jest.Mock).mockReturnValueOnce(null); // signed out: nothing is kept
    type('plan-name', 'Nobody');
    await settleDebounce();
    expect(sessionStorage.getItem(KEY)).toBeNull();
    type('plan-name', 'Mid-edit plan');
    type('plan-coverage', '65');
    expect(sessionStorage.getItem(KEY)).toBeNull();
    await settleDebounce();
    const raw = sessionStorage.getItem(KEY) as string;
    expect(JSON.parse(raw)).toMatchObject({ userId: 'u-admin', planId: '', fields: { 'plan-name': 'Mid-edit plan', 'plan-coverage': '65' } });
    expect(raw).not.toMatch(/token|password|secret/i);
  });

  it('restores a new-plan draft after the post-login reload, including ramp and accounts', async () => {
    seed({
      ramp: 'weekly-25pct', accounts: [{ id: 'a1', name: 'Prod', external_id: '111' }],
      fields: { 'plan-name': 'Mid-edit plan', 'plan-coverage': '65', 'plan-notify-days': '9' },
    });
    reloadPageDom();
    await expect(restorePlanDraft()).resolves.toBe(true);
    expect(vals('plan-name', 'plan-coverage', 'plan-notify-days', 'plan-account-ids')).toEqual(['Mid-edit plan', '65', '9', 'a1']);
    expect(document.querySelector<HTMLInputElement>('input[name="ramp-schedule"]:checked')?.value).toBe('weekly-25pct');
    expect(document.querySelector('.account-chip-name')?.textContent).toBe('Prod (111)');
  });

  it('restores an edit draft over the freshly loaded plan', async () => {
    (api.getPlan as jest.Mock).mockResolvedValue(PLAN);
    seed({ planId: 'plan-1', fields: { 'plan-name': 'Typed over saved', 'plan-coverage': '55' } });
    reloadPageDom();
    await expect(restorePlanDraft()).resolves.toBe(true);
    expect(vals('plan-id', 'plan-name', 'plan-coverage')).toEqual(['plan-1', 'Typed over saved', '55']);
  });

  it('clears the draft when the modal closes and writes none after a pending edit', async () => {
    type('plan-name', 'Done');
    await settleDebounce();
    expect(sessionStorage.getItem(KEY)).not.toBeNull();
    type('plan-name', 'Done again');
    closePlanModal();
    await settleDebounce();
    expect(sessionStorage.getItem(KEY)).toBeNull();
  });

  it('ignores tampered field ids and malformed account entries without throwing', async () => {
    seed({
      fields: { 'plan-name': 'Kept', '"]x[': 'bad', 'plan-coverage': { evil: 1 } },
      accounts: [null, 7, { id: 'a1' }, { id: 'a2', name: 'Ok', external_id: '222' }],
    });
    reloadPageDom();
    await expect(restorePlanDraft()).resolves.toBe(true);
    expect(vals('plan-name', 'plan-coverage', 'plan-account-ids')).toEqual(['Kept', '80', 'a2']);
  });

  it('never throws and drops a draft it cannot apply', async () => {
    seed({});
    (showToast as jest.Mock).mockImplementationOnce(() => { throw new Error('toast failed'); });
    reloadPageDom();
    await expect(restorePlanDraft()).resolves.toBe(false);
    expect(sessionStorage.getItem(KEY)).toBeNull();
  });

  it.each([['switches tab', () => { stillOnPlans = false; }], ['types', () => type('plan-name', 'Typing')], ['opens another plan', () => openNewPlanModal()]])(
    'does not overwrite when the user %s while the restore waits', async (_name, act) => {
      stillOnPlans = true;
      (api.listPlanAccounts as jest.Mock).mockReturnValue(new Promise(() => {}));
      (api.getPlan as jest.Mock).mockResolvedValue(PLAN);
      seed({ planId: 'plan-1' });
      reloadPageDom();
      const restored = restorePlanDraft(() => stillOnPlans);
      await jest.advanceTimersByTimeAsync(10);
      act();
      await jest.advanceTimersByTimeAsync(5_001);
      await expect(restored).resolves.toBe(false);
      expect(el<HTMLInputElement>('plan-name').value).not.toBe('Kept');
      expect(sessionStorage.getItem(KEY)).toBeNull();
    });

  it.each([[404, false], [403, false], [503, true], [undefined, true]])(
    'after a failed plan load with status %s the draft is kept: %s', async (status, kept) => {
      seed({ planId: 'plan-1' });
      (api.getPlan as jest.Mock).mockRejectedValue(Object.assign(new Error('load failed'), { status }));
      reloadPageDom();
      await expect(restorePlanDraft()).resolves.toBe(false);
      expect(sessionStorage.getItem(KEY) !== null).toBe(kept);
    });

  it.each([['belongs to another user', { userId: 'u-other' }], ['is older than 24 hours', { savedAt: Date.now() - 24 * 3_600_000 - 1 }]])(
    'drops a draft that %s', async (_why, over) => {
      seed(over);
      reloadPageDom();
      await expect(restorePlanDraft()).resolves.toBe(false);
        expect(sessionStorage.getItem(KEY)).toBeNull();
    });

  it('keeps working when storage throws', async () => {
    const blocked = () => { throw new Error('blocked'); };
    const m = backSessionStorage();
    (['getItem', 'setItem', 'removeItem'] as const).forEach(k => m[k].mockImplementation(blocked));
    type('plan-name', 'Blocked');
    await settleDebounce();
    await expect(restorePlanDraft()).resolves.toBe(false);
    expect(() => closePlanModal()).not.toThrow();
  });
});
