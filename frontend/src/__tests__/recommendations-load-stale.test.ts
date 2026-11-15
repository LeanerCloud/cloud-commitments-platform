/**
 * #1957: loadRecommendations must ignore a response (or failure) that was
 * superseded by a newer call, so an older slower load cannot overwrite the
 * newer data, wipe the user's selection, or paint a stale error callout.
 */
import { loadRecommendations, getAccountName } from '../recommendations';
import type { LocalRecommendation } from '../types';

jest.mock('../api', () => ({
  getRecommendations: jest.fn(),
  refreshRecommendations: jest.fn(),
  listAccounts: jest.fn().mockResolvedValue([]),
  listAccountsMinimal: jest.fn().mockResolvedValue([]),
  getConfig: jest.fn(),
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
  // ADMINISTRATORS_GROUP_ID literal used here (not imported) because jest.mock
  // factories are hoisted before imports; jest.requireActual also fails here
  // because permissions.ts has a top-level import of ./state which is the very
  // module being mocked (circular init). permissions.test.ts pins the value.
  getCurrentUser: jest.fn().mockReturnValue({ id: 'u-admin', email: 'admin@example.com', groups: ['00000000-0000-5000-8000-000000000001'] }),
}));

jest.mock('../utils', () => ({
  NO_VALUE: '\u2014',
  formatCurrency: jest.fn((val) => `$${val || 0}`),
  formatTerm: jest.fn((years) => years == null ? '' : `${years} Year${years === 1 ? '' : 's'}`),
  escapeHtml: jest.fn((str) => str || ''),
  populateAccountFilter: jest.fn(() => Promise.resolve()),
}));

import * as api from '../api';
import * as state from '../state';

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (v: T) => void;
  reject: (e: Error) => void;
}
const deferred = <T,>(): Deferred<T> => {
  let resolve!: (v: T) => void;
  let reject!: (e: Error) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
};
const flush = async (): Promise<void> => { for (let i = 0; i < 10; i++) await Promise.resolve(); };

const makeRec = (id: string): LocalRecommendation => ({
  id, provider: 'aws', service: 'ec2', resource_type: 'm5.large', region: 'us-east-1',
  count: 1, term: 3, savings: 100, upfront_cost: 1000,
} as unknown as LocalRecommendation);
const resp = (id: string) => ({ summary: {}, recommendations: [makeRec(id)], regions: [] });

const listEl = (): HTMLElement => document.getElementById('recommendations-list') as HTMLElement;
const toolbarEl = (): HTMLElement => document.getElementById('recommendations-toolbar') as HTMLElement;
const hasSkeleton = (): boolean => listEl().querySelector('.skeleton, .skeleton-group, .skeleton-row, .skeleton-table') !== null;

describe('loadRecommendations stale response guard (#1957)', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    document.body.replaceChildren();
    const tab = document.createElement('div');
    tab.id = 'opportunities-tab';
    const summary = document.createElement('div');
    summary.id = 'recommendations-summary';
    const list = document.createElement('div');
    list.id = 'recommendations-list';
    const toolbar = document.createElement('div');
    toolbar.id = 'recommendations-toolbar';
    tab.append(summary, toolbar, list);
    document.body.appendChild(tab);
    (api.getConfig as jest.Mock).mockResolvedValue({ global: {} });
    jest.spyOn(console, 'error').mockImplementation(() => {});
  });

  it('older response resolving last does not overwrite data or clear the selection', async () => {
    const a = deferred<unknown>();
    const b = deferred<unknown>();
    (api.getRecommendations as jest.Mock).mockReturnValueOnce(a.promise).mockReturnValueOnce(b.promise);
    // Distinct account names per load: the stale one must not overwrite the cache.
    (api.listAccountsMinimal as jest.Mock)
      .mockResolvedValueOnce([{ id: 'acct-1', name: 'stale-name' }])
      .mockResolvedValueOnce([{ id: 'acct-1', name: 'fresh-name' }]);

    const first = loadRecommendations();
    const second = loadRecommendations();
    b.resolve(resp('new'));
    await second;
    expect(state.setRecommendations).toHaveBeenCalledTimes(1);
    expect((state.setRecommendations as jest.Mock).mock.calls[0]![0].map((r: LocalRecommendation) => r.id)).toEqual(['new']);
    expect(state.clearSelectedRecommendations).toHaveBeenCalledTimes(1);
    // renderLookbackToolbar is a follow-on of a completed load: clear it so a
    // stale load re-rendering it would be visible.
    expect(toolbarEl().childElementCount).toBeGreaterThan(0);
    toolbarEl().replaceChildren();
    const htmlAfterNew = listEl().innerHTML;

    a.resolve(resp('old'));
    await first;
    await flush();

    expect(state.setRecommendations).toHaveBeenCalledTimes(1);
    expect(state.clearSelectedRecommendations).toHaveBeenCalledTimes(1);
    // follow-on calls of a stale load are skipped too
    expect(toolbarEl().childElementCount).toBe(0);
    expect(getAccountName('acct-1')).toBe('fresh-name');
    expect(listEl().innerHTML).toBe(htmlAfterNew);
  });

  it('stale rejection paints no error callout and leaves the newer load alone', async () => {
    const a = deferred<unknown>();
    const b = deferred<unknown>();
    (api.getRecommendations as jest.Mock).mockReturnValueOnce(a.promise).mockReturnValueOnce(b.promise);

    const first = loadRecommendations();
    const second = loadRecommendations();
    a.reject(new Error('boom-old'));
    await first;
    await flush();

    expect(listEl().textContent).not.toContain('boom-old');
    expect(console.error).not.toHaveBeenCalled();
    expect(hasSkeleton()).toBe(true); // newer load still pending, skeleton kept

    b.resolve(resp('new'));
    await second;
    expect(hasSkeleton()).toBe(false);
    expect(listEl().textContent).not.toContain('Failed to load');
    expect(state.setRecommendations).toHaveBeenCalledTimes(1);
  });

  it('the latest call still reports its own failure', async () => {
    (api.getRecommendations as jest.Mock).mockRejectedValueOnce(new Error('boom-latest'));
    await loadRecommendations();
    expect(listEl().textContent).toContain('boom-latest');
    expect(hasSkeleton()).toBe(false);
  });

  it('a lone call renders normally', async () => {
    (api.getRecommendations as jest.Mock).mockResolvedValueOnce(resp('only'));
    await loadRecommendations();
    expect(state.setRecommendations).toHaveBeenCalledTimes(1);
    expect(state.clearSelectedRecommendations).toHaveBeenCalledTimes(1);
    expect(toolbarEl().childElementCount).toBeGreaterThan(0);
  });
});
