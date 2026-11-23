/**
 * API Keys module tests
 */

// The harness registers the module mocks, so it must be imported first.
import { mockConfirmDialog } from './apikeys-harness';
import {
  loadApiKeys,
  renderApiKeysList,
  closeCreateKeyModal,
  createApiKey
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

  describe('loadApiKeys', () => {
    test('loads and renders API keys on success', async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Test Key 1',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z',
          last_used_at: '2024-01-16T15:30:00Z'
        },
        {
          id: 'key-2',
          name: 'Test Key 2',
          key_prefix: 'xyz789',
          is_active: false,
          created_at: '2024-01-10T08:00:00Z',
          expires_at: '2024-02-10T08:00:00Z'
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });

      await loadApiKeys();

      expect(api.getApiKeys).toHaveBeenCalled();
      const container = document.getElementById('apikeys-list');
      expect(container?.innerHTML).toContain('Test Key 1');
      expect(container?.innerHTML).toContain('Test Key 2');
    });

    test('shows error on API failure', async () => {
      // Remove the create-apikey-error element so showError falls back to alert
      document.getElementById('create-apikey-error')?.remove();

      const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
      (api.getApiKeys as jest.Mock).mockRejectedValue(new Error('API Error'));

      await loadApiKeys();

      expect(consoleError).toHaveBeenCalledWith('Failed to load API keys:', expect.any(Error));
      // Q4: fallback when #create-apikey-error is absent now surfaces
      // via toast rather than blocking alert().
      expect(document.querySelector('.toast-error')?.querySelector('.toast-message')?.textContent).toBe('Failed to load API keys');
      consoleError.mockRestore();
    });

    // Regression: GitHub issue #9. Backend returns `{api_keys: [...]}`
    // (auth.APIListAPIKeysResponse with `json:"api_keys"`); the previous
    // frontend read `response.keys`, leaving currentApiKeys = undefined
    // and crashing renderApiKeysList with TypeError. Lock in the
    // contract so a future field rename has to update tests too.
    test('issue #9: parses api_keys field from backend response', async () => {
      const mockKeys = [{
        id: 'key-1', name: 'Test', key_prefix: 'abc',
        is_active: true, created_at: '2024-01-15T10:00:00Z',
      }];
      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });

      const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
      await loadApiKeys();

      expect(document.getElementById('apikeys-list')?.innerHTML).toContain('Test');
      expect(consoleError).not.toHaveBeenCalled();
      consoleError.mockRestore();
    });

    test('issue #9: tolerates legacy bare-array response without crashing', async () => {
      // Defence-in-depth — if a proxy or alternate handler returns the
      // bare array (no envelope), we should still render the rows
      // rather than crashing on currentApiKeys.length.
      const mockKeys = [{
        id: 'key-2', name: 'Bare', key_prefix: 'def',
        is_active: true, created_at: '2024-01-15T10:00:00Z',
      }];
      (api.getApiKeys as jest.Mock).mockResolvedValue(mockKeys);

      const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
      await expect(loadApiKeys()).resolves.toBeUndefined();
      expect(document.getElementById('apikeys-list')?.innerHTML).toContain('Bare');
      expect(consoleError).not.toHaveBeenCalled();
      consoleError.mockRestore();
    });

    test('unexpected response shape is an error, not "no keys" (#136 A11-018)', async () => {
      // Neither `api_keys` nor a bare array: still must not crash (issue #9),
      // but an operator reviewing credentials must not be told none exist.
      for (const shape of [{ unexpected: 'shape' }, null]) {
        (api.getApiKeys as jest.Mock).mockResolvedValue(shape);
        const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
        await expect(loadApiKeys()).resolves.toBeUndefined();
        const text = document.getElementById('apikeys-list')?.textContent ?? '';
        expect(text).toContain('Failed to load API keys');
        expect(text).not.toContain('No API keys yet');
        consoleError.mockRestore();
      }
    });
  });

  describe('renderApiKeysList', () => {
    test('shows empty message when no keys', () => {
      // First load empty keys
      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: [] });

      // Call renderApiKeysList (it uses internal state, so we need to load first)
      loadApiKeys().then(() => {
        const container = document.getElementById('apikeys-list');
        expect(container?.innerHTML).toContain('No API keys yet');
      });
    });

    test('handles missing container gracefully', () => {
      document.body.innerHTML = '';

      // Should not throw
      expect(() => renderApiKeysList()).not.toThrow();
    });

    test('renders active keys with revoke button', async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Active Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z'
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });

      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      expect(container?.innerHTML).toContain('revoke-key-btn');
      expect(container?.innerHTML).toContain('Active');
    });

    test('renders revoked keys without revoke button', async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Revoked Key',
          key_prefix: 'abc123',
          is_active: false,
          created_at: '2024-01-15T10:00:00Z'
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });

      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      expect(container?.innerHTML).toContain('Revoked');
      // Revoked keys should not have revoke button, but should have delete
      expect(container?.innerHTML).toContain('delete-key-btn');
    });

    test('renders expired keys with warning badge', async () => {
      const pastDate = new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString();
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Expired Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z',
          expires_at: pastDate
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });

      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      expect(container?.innerHTML).toContain('Expired');
      expect(container?.innerHTML).toContain('badge-warning');
    });

    test('renders never used and never expires labels', async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'New Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z'
          // No last_used_at or expires_at
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });

      await loadApiKeys();

      const container = document.getElementById('apikeys-list');
      expect(container?.innerHTML).toContain('Never');
    });

    test('adds event listeners to revoke and delete buttons', async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Test Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z'
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });
      (api.revokeApiKey as jest.Mock).mockResolvedValue({});

      await loadApiKeys();

      const revokeBtn = document.querySelector('.revoke-key-btn') as HTMLButtonElement;
      expect(revokeBtn).not.toBeNull();

      // Click the revoke button
      revokeBtn.click();

      await new Promise(resolve => setTimeout(resolve, 0));

      expect(api.revokeApiKey).toHaveBeenCalledWith('key-1');
    });

    // Regression for ARCH-07 (issue #1195): the previous local escapeHtml
    // copy (div.textContent -> div.innerHTML round-trip) did not escape
    // quote characters, so a key id containing a double quote interpolated
    // into data-key-id="..." broke out of the attribute and injected a new
    // attribute (here onmouseover) into the button markup.
    test('issue #1195: double quote in key id cannot inject attributes', async () => {
      const hostileId = 'key-1" onmouseover="alert(1)';
      const mockKeys = [
        {
          id: hostileId,
          name: 'Hostile Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z'
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });

      await loadApiKeys();

      const deleteBtn = document.querySelector('.delete-key-btn');
      expect(deleteBtn).not.toBeNull();
      expect(deleteBtn?.getAttribute('onmouseover')).toBeNull();
      expect(deleteBtn?.getAttribute('data-key-id')).toBe(hostileId);
    });

    test('adds event listener to delete buttons', async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Test Key',
          key_prefix: 'abc123',
          is_active: true,
          created_at: '2024-01-15T10:00:00Z'
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });
      (api.deleteApiKey as jest.Mock).mockResolvedValue({});

      await loadApiKeys();

      const deleteBtn = document.querySelector('.delete-key-btn') as HTMLButtonElement;
      expect(deleteBtn).not.toBeNull();

      // Click the delete button
      deleteBtn.click();

      await new Promise(resolve => setTimeout(resolve, 0));

      expect(api.deleteApiKey).toHaveBeenCalledWith('key-1');
    });
  });

  describe('closeCreateKeyModal', () => {
    test('hides the modal', () => {
      const modal = document.getElementById('create-apikey-modal');
      modal?.classList.remove('hidden');

      closeCreateKeyModal();

      expect(modal?.classList.contains('hidden')).toBe(true);
    });

    test('handles missing modal gracefully', () => {
      document.body.innerHTML = '';

      // Should not throw
      expect(() => closeCreateKeyModal()).not.toThrow();
    });
  });

  describe('createApiKey', () => {
    const expiresAt = new Date('2025-12-31T00:00:00Z');
    // permissions is required (issue #61): the backend rejects an empty
    // array with 400, so every real caller (handleCreateApiKey) always has
    // a non-empty selection by the time it calls this wrapper.
    const permissions = [{ action: 'view', resource: 'recommendations' }];

    test('creates API key with name, password, expiration and permissions', async () => {
      const mockResponse = {
        api_key: 'full-api-key-value',
        key_id: 'key-1',
        key: { id: 'key-1', name: 'Test Key', key_prefix: 'abc' }
      };

      (api.createApiKey as jest.Mock).mockResolvedValue(mockResponse);

      const result = await createApiKey('Test Key', 'my-password', expiresAt, permissions);

      expect(api.createApiKey).toHaveBeenCalledWith({
        name: 'Test Key',
        password: Buffer.from('my-password', 'binary').toString('base64'),
        expires_at: expiresAt.toISOString(),
        permissions,
      });
      expect(result.api_key).toBe('full-api-key-value');
    });

    test('sends every permission passed in', async () => {
      const mockResponse = {
        api_key: 'full-api-key-value',
        key_id: 'key-1',
        key: { id: 'key-1', name: 'Test Key', key_prefix: 'abc' }
      };

      (api.createApiKey as jest.Mock).mockResolvedValue(mockResponse);

      const multiplePermissions = [
        { action: 'view', resource: 'recommendations' },
        { action: 'create', resource: 'plans' },
      ];
      await createApiKey('Test Key', 'my-password', expiresAt, multiplePermissions);

      expect(api.createApiKey).toHaveBeenCalledWith({
        name: 'Test Key',
        password: Buffer.from('my-password', 'binary').toString('base64'),
        expires_at: expiresAt.toISOString(),
        permissions: multiplePermissions
      });
    });

    test('throws error on API failure', async () => {
      const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
      (api.createApiKey as jest.Mock).mockRejectedValue(new Error('Create failed'));

      await expect(createApiKey('Test Key', 'my-password', expiresAt, permissions)).rejects.toThrow('Create failed');
      expect(consoleError).toHaveBeenCalled();
      consoleError.mockRestore();
    });
  });
});
