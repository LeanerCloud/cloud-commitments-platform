/**
 * API Keys module tests: usage stats, date formatting, HTML escaping, error
 * display and secret handling. Split from apikeys.test.ts to stay under the
 * 500-line ceiling; the module mocks live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockConfirmDialog } from './apikeys-harness';
import {
  loadApiKeys
} from '../apikeys';

import * as api from '../api';

describe('API Keys Module', () => {
  beforeEach(() => {
    // Reset DOM
    document.body.innerHTML = `
      <div id="apikeys-usage-summary"></div>
      <div id="apikeys-list"></div>
      <button id="create-apikey-btn"></button>
      <div id="create-apikey-modal" class="hidden">
        <form id="create-apikey-form">
          <input type="text" id="apikey-name" value="">
          <input type="password" id="apikey-password" value="">
          <div id="apikey-permissions-list">
            <label class="apikey-permission-option">
              <input type="checkbox" class="apikey-permission-checkbox" id="apikey-perm-0"
                     data-action="view" data-resource="recommendations" checked>
              view:recommendations
            </label>
          </div>
          <input type="date" id="apikey-expires-at" value="">
          <div id="create-apikey-error" class="hidden"></div>
          <button type="submit" id="create-apikey-submit-btn">Create</button>
        </form>
        <button id="close-create-apikey-modal-btn"></button>
      </div>
    `;

    jest.clearAllMocks();
    window.alert = jest.fn();
    window.confirm = jest.fn().mockReturnValue(true);
    // Reset the confirmDialog mock between tests — clearAllMocks only
    // clears call history, not the mockResolvedValueOnce queue.
    mockConfirmDialog.mockReset();
    mockConfirmDialog.mockImplementation(() => Promise.resolve(true));
    // Default the usage-stats endpoint to an empty response so the
    // parallel-load path in loadApiKeys doesn't reject in tests that
    // only set up `getApiKeys`. Individual tests can override.
    (api.getApiKeysUsageStats as jest.Mock).mockResolvedValue({
      total_active: 0,
      total_requests_window: 0,
      total_requests_lifetime: 0,
      top_keys: [],
    });
    // Default the owner's effective-permissions fetch (issue #61's
    // frontend follow-up) so showCreateKeyModal tests that don't care
    // about the picker's own behavior aren't forced to stub it too.
    (api.getUserPermissions as jest.Mock).mockResolvedValue({
      permissions: [{ action: 'view', resource: 'recommendations' }],
      is_admin: false,
    });
  });

  // Usage stats (issue #340/#344 deferred sub-task) — section-level summary
  // card above the table + per-row request count columns.
  describe('Usage stats', () => {
    test('renders summary tiles when stats load successfully', async () => {
      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: [] });
      (api.getApiKeysUsageStats as jest.Mock).mockResolvedValue({
        total_active: 2,
        total_requests_window: 42,
        total_requests_lifetime: 1234,
        top_keys: [
          { id: 'key-1', name: 'Busy', key_prefix: 'aaaa1111', request_count_window: 30 },
          { id: 'key-2', name: 'Medium', key_prefix: 'bbbb2222', request_count_window: 12 },
        ],
      });

      await loadApiKeys();
      // Allow the parallel stats fetch to resolve.
      await new Promise((r) => setTimeout(r, 0));

      const summary = document.getElementById('apikeys-usage-summary');
      expect(summary?.textContent).toContain('Active keys');
      expect(summary?.textContent).toContain('2');
      expect(summary?.textContent).toContain('Requests (window)');
      expect(summary?.textContent).toContain('42');
      expect(summary?.textContent).toContain('Requests (lifetime)');
      // formatCount abbreviates large numbers in the summary tiles to
      // fit the tile width — table cells keep the exact integer.
      expect(summary?.textContent).toContain('1.2k');
      expect(summary?.textContent).toContain('Most active (window)');
      expect(summary?.textContent).toContain('Busy');
      expect(summary?.textContent).toContain('aaaa1111');
    });

    test('omits top-list when no key has window activity', async () => {
      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: [] });
      (api.getApiKeysUsageStats as jest.Mock).mockResolvedValue({
        total_active: 1,
        total_requests_window: 0,
        total_requests_lifetime: 7,
        top_keys: [],
      });

      await loadApiKeys();
      await new Promise((r) => setTimeout(r, 0));

      const summary = document.getElementById('apikeys-usage-summary');
      expect(summary?.textContent).not.toContain('Most active (window)');
    });

    test('shows inline error when stats endpoint fails, table still renders', async () => {
      (api.getApiKeys as jest.Mock).mockResolvedValue({
        api_keys: [
          { id: 'key-1', name: 'k', key_prefix: 'abc12345', is_active: true, created_at: '2024-01-15T10:00:00Z' },
        ],
      });
      (api.getApiKeysUsageStats as jest.Mock).mockRejectedValue(new Error('boom'));

      await loadApiKeys();
      await new Promise((r) => setTimeout(r, 0));

      const summary = document.getElementById('apikeys-usage-summary');
      expect(summary?.querySelector('.error')?.textContent).toBe('Failed to load usage summary');
      // The table itself still renders despite the summary failure.
      const list = document.getElementById('apikeys-list');
      expect(list?.innerHTML).toContain('abc12345');
    });

    test('renders Requests (window) and Requests (total) columns per row', async () => {
      (api.getApiKeys as jest.Mock).mockResolvedValue({
        api_keys: [
          {
            id: 'key-1',
            name: 'k1',
            key_prefix: 'abc12345',
            is_active: true,
            created_at: '2024-01-15T10:00:00Z',
            request_count_window: 12,
            request_count_total: 1234,
          },
        ],
      });

      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      expect(container?.innerHTML).toContain('Requests (window)');
      expect(container?.innerHTML).toContain('Requests (total)');
      // Counts render with thousands separator for readability.
      expect(container?.innerHTML).toContain('1,234');
      expect(container?.innerHTML).toContain('apikeys-count-cell');
    });

    test('shows no data when counter fields are missing from the response', async () => {
      (api.getApiKeys as jest.Mock).mockResolvedValue({
        api_keys: [
          // No request_count_* fields — simulates a cached older response.
          { id: 'key-1', name: 'k1', key_prefix: 'abc12345', is_active: true, created_at: '2024-01-15T10:00:00Z' },
        ],
      });

      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      const cells = container?.querySelectorAll('.apikeys-count-cell');
      expect(cells?.length).toBe(2);
      expect(cells?.[0]?.textContent).toBe('n/a');
      expect(cells?.[1]?.textContent).toBe('n/a');
    });

    // Regression: a key already in use before migration 000094 has no
    // recoverable lifetime count. The backend sends null for it; rendering
    // "0" would state a request volume nobody measured. A genuinely-unused
    // key still shows 0.
    test('renders an unknown lifetime count as no data, and a real zero as 0', async () => {
      (api.getApiKeys as jest.Mock).mockResolvedValue({
        api_keys: [
          {
            id: 'pre-migration',
            name: 'old',
            key_prefix: 'abc12345',
            is_active: true,
            created_at: '2024-01-15T10:00:00Z',
            request_count_window: 0,
            request_count_total: null,
          },
          {
            id: 'never-used',
            name: 'fresh',
            key_prefix: 'def67890',
            is_active: true,
            created_at: '2024-01-15T10:00:00Z',
            request_count_window: 0,
            request_count_total: 0,
          },
        ],
      });

      await loadApiKeys();

      const cells = document.getElementById('apikeys-list')?.querySelectorAll('.apikeys-count-cell');
      expect(cells?.length).toBe(4);
      // Row 1: window 0 is a real value, lifetime is unknown.
      expect(cells?.[0]?.textContent).toBe('0');
      expect(cells?.[1]?.textContent).toBe('n/a');
      // Row 2: never used, so both are a genuine zero.
      expect(cells?.[2]?.textContent).toBe('0');
      expect(cells?.[3]?.textContent).toBe('0');
    });

    test('list-fetch failure clears summary skeleton and shows error', async () => {
      (api.getApiKeys as jest.Mock).mockRejectedValue(new Error('list boom'));
      (api.getApiKeysUsageStats as jest.Mock).mockResolvedValue({
        total_active: 0,
        total_requests_window: 0,
        total_requests_lifetime: 0,
        top_keys: [],
      });

      await loadApiKeys();
      await new Promise((r) => setTimeout(r, 0));

      const list = document.getElementById('apikeys-list');
      expect(list?.querySelector('.error')?.textContent).toBe('Failed to load API keys');
      // The skeleton-active marker is gone (success/error both clear it).
      expect(list?.dataset['skeletonActive']).toBeUndefined();
    });
  });

  describe('Date formatting', () => {
    test('formats dates correctly in the list', async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Test Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z',
          last_used_at: '2024-01-16T15:30:00Z',
          expires_at: '2025-01-15T10:00:00Z'
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });

      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      // The exact format depends on locale, but it should contain date parts
      expect(container?.innerHTML).toContain('2024');
    });

    // #492: last_used_at null renders "Never", not "0 min ago" or empty
    test('renders Never for null last_used_at (issue #492)', async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Unused Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z'
          // last_used_at intentionally absent
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });
      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      expect(container?.innerHTML).toContain('Never');
    });

    test('one malformed last_used_at degrades to one cell instead of blanking the table (#136)', async () => {
      const mockKeys = [
        { id: 'key-bad', name: 'Bad Timestamp', key_prefix: 'bad111', is_active: true,
          created_at: '2024-01-15T10:00:00Z', last_used_at: 'not-a-date' },
        { id: 'key-ok', name: 'Leaked Key', key_prefix: 'ok2222', is_active: true,
          created_at: '2024-01-15T10:00:00Z', last_used_at: '2024-01-16T15:30:00Z' },
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });
      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      const rows = container?.querySelectorAll('tbody tr');
      expect(rows).toHaveLength(2);
      expect(rows?.[0]?.textContent).toContain('--');
      expect(container?.querySelectorAll('.revoke-key-btn')).toHaveLength(2);
    });

    // #492: populated last_used_at renders relative time with ISO title for hover
    test('renders relative time with ISO title for populated last_used_at (issue #492)', async () => {
      const recentTs = new Date(Date.now() - 2 * 3600 * 1000).toISOString(); // 2 hours ago
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Used Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z',
          last_used_at: recentTs
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });
      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      // Relative label: ends with "h ago" for a 2-hour-old timestamp
      expect(container?.innerHTML).toMatch(/\d+h ago/);
      // Absolute ISO on the title attribute for hover
      const span = container?.querySelector<HTMLElement>('[title]');
      expect(span?.getAttribute('title')).toBe(new Date(recentTs).toISOString());
    });
  });
});
