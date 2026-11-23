/**
 * Shared setup for the plan range validation suites (plans-range-validation*.test.ts):
 * the module mocks and the DOM builders. Import this module FIRST so the mocks
 * register before the suite loads ../plans. This file is not a test (jest only
 * collects *.test.ts).
 */
import type { ToastOptions } from '../toast';

jest.mock('../api', () => ({
  getPlans: jest.fn(),
  getPlannedPurchases: jest.fn(),
  getPlan: jest.fn(),
  createPlan: jest.fn(),
  updatePlan: jest.fn(),
  patchPlan: jest.fn(),
  deletePlan: jest.fn(),
  runPlannedPurchase: jest.fn(),
  pausePlannedPurchase: jest.fn(),
  resumePlannedPurchase: jest.fn(),
  deletePlannedPurchase: jest.fn(),
  createPlannedPurchases: jest.fn(),
  listPlanAccounts: jest.fn().mockResolvedValue([]),
  setPlanAccounts: jest.fn().mockResolvedValue(undefined),
  listAccounts: jest.fn().mockResolvedValue([]),
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
  getCurrentUser: jest.fn().mockReturnValue({ id: 'u-admin', email: 'admin@example.com', groups: ['00000000-0000-5000-8000-000000000001'] }),
  getPlansColumnFilters: jest.fn().mockReturnValue({}),
  setPlansColumnFilter: jest.fn(),
  clearAllPlansColumnFilters: jest.fn(),
}));

jest.mock('../history', () => ({ viewPlanHistory: jest.fn() }));

jest.mock('../commitmentOptions', () => ({
  populateTermSelect: jest.fn(),
  populatePaymentSelect: jest.fn(),
  isValidCombination: jest.fn().mockReturnValue(true),
  normalizePaymentValue: jest.fn((value) => value),
}));

export const mockShowToast = jest.fn((_opts: ToastOptions) => ({ dismiss: jest.fn() }));
jest.mock('../toast', () => ({
  showToast: (opts: ToastOptions) => mockShowToast(opts),
}));

jest.mock('../confirmDialog', () => ({
  confirmDialog: jest.fn(() => Promise.resolve(true)),
}));

jest.mock('../utils', () => ({
  NO_VALUE: '\u2014',
  formatDate: jest.fn((v) => v ?? ''),
  formatTerm: jest.fn((y) => `${y}yr`),
  formatRampSchedule: jest.fn((v) => v ?? ''),
  getStatusBadge: jest.fn(() => ({ class: 'success', label: 'Active' })),
  escapeHtml: jest.fn((s) => s ?? ''),
  escapeHtmlAttr: jest.fn((str: string | null | undefined) => {
    if (str == null) return '';
    return str
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }),
  formatCurrency: jest.fn((v) => `$${v}`),
  populateAccountFilter: jest.fn(() => Promise.resolve()),
}));

// ---------------------------------------------------------------------------
// DOM helpers
// ---------------------------------------------------------------------------

function addInput(
  parent: Element,
  id: string,
  type: string,
  value: string,
  attrs: Record<string, string> = {},
): HTMLInputElement {
  const el = document.createElement('input');
  el.id = id;
  el.type = type;
  el.value = value;
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
  parent.appendChild(el);
  return el;
}

function addSelect(
  parent: Element,
  id: string,
  options: Array<{ value: string; label: string }>,
): HTMLSelectElement {
  const el = document.createElement('select');
  el.id = id;
  for (const { value, label } of options) {
    const opt = document.createElement('option');
    opt.value = value;
    opt.textContent = label;
    el.appendChild(opt);
  }
  parent.appendChild(el);
  return el;
}

export function buildPlanModalDOM(): void {
  // Outer modal container
  const modal = document.createElement('div');
  modal.id = 'plan-modal';

  const title = document.createElement('h3');
  title.id = 'plan-modal-title';
  title.textContent = 'New purchase plan';
  modal.appendChild(title);

  const form = document.createElement('form');
  form.id = 'plan-form';
  modal.appendChild(form);

  addInput(form, 'plan-id', 'hidden', '');
  addInput(form, 'plan-name', 'text', '');

  const desc = document.createElement('textarea');
  desc.id = 'plan-description';
  form.appendChild(desc);

  // Provider select with optgroups for service
  addSelect(form, 'plan-provider', [
    { value: 'aws', label: 'AWS' },
    { value: 'azure', label: 'Azure' },
    { value: 'gcp', label: 'GCP' },
  ]);

  const svcSelect = document.createElement('select');
  svcSelect.id = 'plan-service';
  const awsGroup = document.createElement('optgroup');
  awsGroup.label = 'AWS Services';
  const ec2Opt = document.createElement('option');
  ec2Opt.value = 'ec2';
  ec2Opt.textContent = 'EC2';
  awsGroup.appendChild(ec2Opt);
  svcSelect.appendChild(awsGroup);
  form.appendChild(svcSelect);

  addSelect(form, 'plan-term', [
    { value: '1', label: '1 Year' },
    { value: '3', label: '3 Years' },
  ]);
  addSelect(form, 'plan-payment', [
    { value: 'no-upfront', label: 'No Upfront' },
    { value: 'partial-upfront', label: 'Partial Upfront' },
    { value: 'all-upfront', label: 'All Upfront' },
  ]);

  // The five numeric inputs under test (min/max match HTML spec + issue #702)
  addInput(form, 'plan-coverage', 'number', '80', { min: '0', max: '100' });

  const autoPurchase = document.createElement('input');
  autoPurchase.id = 'plan-auto-purchase';
  autoPurchase.type = 'checkbox';
  form.appendChild(autoPurchase);

  addInput(form, 'plan-notify-days', 'number', '3', { min: '1', max: '30' });

  const enabledCheck = document.createElement('input');
  enabledCheck.id = 'plan-enabled';
  enabledCheck.type = 'checkbox';
  enabledCheck.checked = true;
  form.appendChild(enabledCheck);

  // Ramp radio buttons
  for (const [value, checked] of [['immediate', true], ['weekly-25pct', false], ['monthly-10pct', false], ['custom', false]] as const) {
    const r = document.createElement('input');
    r.type = 'radio';
    r.name = 'ramp-schedule';
    r.value = value;
    if (checked) r.checked = true;
    form.appendChild(r);
  }

  // Custom ramp config section
  const customConfig = document.createElement('div');
  customConfig.id = 'custom-ramp-config';
  customConfig.className = 'hidden';
  addInput(customConfig, 'ramp-step-percent', 'number', '20', { min: '1', max: '100' });
  addInput(customConfig, 'ramp-interval-days', 'number', '7', { min: '1', max: '365' });
  form.appendChild(customConfig);

  // Plans list and planned purchases (required by plans.ts loadPlans path)
  const plansList = document.createElement('div');
  plansList.id = 'plans-list';
  const ppList = document.createElement('div');
  ppList.id = 'planned-purchases-list';

  document.body.replaceChildren(modal, plansList, ppList);
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

export function fire(el: HTMLElement, type: 'input' | 'blur'): void {
  el.dispatchEvent(new Event(type));
}

export function errorEl(inputId: string): HTMLElement | null {
  return document.getElementById(`${inputId}-range-error`);
}
