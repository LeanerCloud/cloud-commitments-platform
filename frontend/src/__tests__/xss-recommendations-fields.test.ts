// Mock the api module
jest.mock('../api', () => ({
  getRecommendations: jest.fn(),
  refreshRecommendations: jest.fn(),
  listAccounts: jest.fn().mockResolvedValue([]),
  listAccountsMinimal: jest.fn().mockResolvedValue([]),
  getConfig: jest.fn().mockResolvedValue({ global: {} }),
  listAccountServiceOverrides: jest.fn().mockResolvedValue([]),
}));

jest.mock('../api/recommendations', () => ({
  getRecommendationDetail: jest.fn().mockResolvedValue({
    id: 'rec-default',
    usage_history: [],
    confidence_bucket: 'low',
    provenance_note: '',
  }),
  getRecommendationsFreshness: jest.fn().mockResolvedValue({
    last_collected_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
    last_collection_error: null,
  }),
  refreshRecommendations: jest.fn().mockResolvedValue({}),
}));

const mockShowToast = jest.fn<{ dismiss: () => void }, [unknown]>(() => ({ dismiss: jest.fn() }));
jest.mock('../toast', () => ({
  showToast: (opts: unknown) => mockShowToast(opts),
}));

jest.mock('../state', () => ({
  getCurrentProvider: jest.fn().mockReturnValue('all'),
  setCurrentProvider: jest.fn(),
  getCurrentAccountIDs: jest.fn().mockReturnValue([]),
  setCurrentAccountIDs: jest.fn(),
  getRecommendations: jest.fn().mockReturnValue([]),
  getRecommendationByID: jest.fn().mockReturnValue(undefined),
  setRecommendations: jest.fn(),
  getSelectedRecommendationIDs: jest.fn().mockReturnValue(new Set()),
  clearSelectedRecommendations: jest.fn(),
  addSelectedRecommendation: jest.fn(),
  removeSelectedRecommendation: jest.fn(),
  getRecommendationsSort: jest.fn().mockReturnValue({ column: 'savings', direction: 'desc' }),
  setRecommendationsSort: jest.fn(),
  getRecommendationsColumnFilters: jest.fn().mockReturnValue({}),
  setRecommendationsColumnFilter: jest.fn(),
  clearAllRecommendationsColumnFilters: jest.fn(),
  getVisibleRecommendations: jest.fn().mockReturnValue([]),
  setVisibleRecommendations: jest.fn(),
  getCostPeriod: jest.fn().mockReturnValue('monthly'),
  setCostPeriod: jest.fn(),
  getHiddenColumns: jest.fn().mockReturnValue(new Set()),
  setHiddenColumns: jest.fn(),
  getCurrentUser: jest.fn().mockReturnValue({ id: 'u-admin', email: 'admin@example.com', groups: ['00000000-0000-5000-8000-000000000001'] }),
  subscribeProvider: jest.fn(),
  subscribeAccount: jest.fn(),
}));

// Use the real escapeHtml so the DOM-based escaping is exercised, not a
// stub. formatCurrency/formatTerm are stubbed for readable assertions,
// matching recommendations.test.ts's utils mock shape.
jest.mock('../utils', () => {
  const actual = jest.requireActual<typeof import('../utils')>('../utils');
  return {
    ...actual,
    formatCurrency: jest.fn((val: number) => `$${val || 0}`),
    formatTerm: jest.fn((years: number | null | undefined) =>
      years == null ? '' : `${years} Year${years === 1 ? '' : 's'}`),
    populateAccountFilter: jest.fn(() => Promise.resolve()),
    CURRENCY_DEFAULT_DIGITS: 0,
  };
});

import { loadRecommendations } from '../recommendations';
import * as api from '../api';

const SCRIPT_PAYLOAD = '<script>alert(1)</script>';
const IMG_PAYLOAD = '"><img src=x onerror="alert(1)">';

type StringField = 'resource_type' | 'region' | 'engine' | 'service' | 'payment' | 'provider';
const FIELDS: StringField[] = ['resource_type', 'region', 'engine', 'service', 'payment', 'provider'];

function makeRec(field: StringField, value: string) {
  return {
    id: 'rec-1',
    provider: 'aws',
    service: 'ec2',
    resource_type: 't3.medium',
    region: 'us-east-1',
    count: 1,
    term: 1,
    payment: 'all-upfront',
    savings: 100,
    upfront_cost: 500,
    [field]: value,
  };
}

describe('XSS regression: every string-bearing rec field renders as text (#1957)', () => {
  beforeEach(() => {
    document.body.innerHTML = `
      <div id="opportunities-tab" class="tab-content active">
        <div id="recommendations-summary"></div>
        <div id="recommendations-list"></div>
      </div>
    `;
    jest.clearAllMocks();
    jest.useFakeTimers();
  });

  afterEach(() => {
    jest.useRealTimers();
  });

  describe.each(FIELDS)('%s', (field) => {
    test.each([SCRIPT_PAYLOAD, IMG_PAYLOAD])('payload %s creates no element', async (payload) => {
      (api.getRecommendations as jest.Mock).mockResolvedValue({
        summary: {},
        recommendations: [makeRec(field, payload)],
        regions: ['us-east-1'],
      });

      await loadRecommendations();

      const list = document.getElementById('recommendations-list')!;
      expect(list.querySelectorAll('script, img').length).toBe(0);
      expect(document.querySelectorAll('script').length).toBe(0);
    });
  });
});
