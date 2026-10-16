/**
 * Plan modal service ids (#608).
 *
 * Uses the REAL frontend/src/index.html markup (not a hand-written copy) so
 * the #plan-service optgroups cannot drift from what the tests assert.
 */
import * as fs from 'fs';
import * as path from 'path';
import { openCreatePlanModal, setupPlanHandlers, _resetRampHandlersForTest } from '../plans';

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

import * as api from '../api';
import { CANONICAL_PLAN_SERVICES } from '../lib/plan-service-ids';

function loadRealIndexBody(): string {
  const html = fs.readFileSync(path.join(__dirname, '..', 'index.html'), 'utf8');
  return new DOMParser().parseFromString(html, 'text/html').body.innerHTML;
}

function azureOptgroup(): HTMLOptGroupElement {
  return document.querySelector('#plan-service > optgroup[data-provider="azure"]') as HTMLOptGroupElement;
}

describe('plan modal service ids (#608)', () => {
  beforeEach(() => {
    document.body.innerHTML = loadRealIndexBody();
    jest.clearAllMocks();
    _resetRampHandlersForTest();
  });

  test('every #plan-service option is a canonical slug for its optgroup provider', () => {
    const groups = document.querySelectorAll<HTMLOptGroupElement>('#plan-service > optgroup');
    expect(groups.length).toBeGreaterThan(0);
    const seenProviders = new Set<string>();
    groups.forEach(group => {
      const provider = group.dataset['provider']!;
      seenProviders.add(provider);
      const values = Array.from(group.querySelectorAll('option')).map(o => o.value);
      expect(values.length).toBeGreaterThan(0);
      for (const v of values) {
        expect(CANONICAL_PLAN_SERVICES[provider as 'aws']).toContain(v);
      }
    });
    expect(Array.from(seenProviders).sort()).toEqual(['aws', 'azure', 'gcp']);
  });

  test.each([
    ['compute', 'Virtual Machines'],
    ['relational-db', 'SQL Database'],
    ['cache', 'Cache for Redis'],
    ['search', 'AI Search'],
  ])('prefill from an Azure %s recommendation selects the Azure option', (service, label) => {
    setupPlanHandlers();
    openCreatePlanModal([
      { id: 'r1', provider: 'azure', service, term: 1, payment: 'upfront' } as unknown as api.Recommendation,
    ]);

    const select = document.getElementById('plan-service') as HTMLSelectElement;
    const chosen = select.selectedOptions[0]!;
    expect((document.getElementById('plan-provider') as HTMLSelectElement).value).toBe('azure');
    expect(chosen.value).toBe(service);
    expect(chosen.parentElement).toBe(azureOptgroup());
    expect(chosen.textContent).toBe(label);
  });
});
