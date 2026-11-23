/**
 * API Keys module tests: showKeyCreatedModal, revokeApiKey, deleteApiKey and
 * initApiKeys. Split from apikeys.test.ts to stay under the 500-line ceiling;
 * the module mocks live in the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockConfirmDialog } from './apikeys-harness';
import {
  loadApiKeys,
  showKeyCreatedModal,
  revokeApiKey,
  deleteApiKey
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

  describe('showKeyCreatedModal', () => {
    test('creates and shows modal with API key', () => {
      showKeyCreatedModal('test-api-key-12345');

      const modal = document.getElementById('apikey-created-modal');
      expect(modal).not.toBeNull();
      expect(modal?.classList.contains('hidden')).toBe(false);
      expect(modal?.innerHTML).toContain('test-api-key-12345');
    });

    test('exposes dialog semantics labelled by its heading', () => {
      showKeyCreatedModal('test-key');

      const modal = document.getElementById('apikey-created-modal');
      expect(modal?.getAttribute('role')).toBe('dialog');
      expect(modal?.getAttribute('aria-modal')).toBe('true');
      const labelId = modal?.getAttribute('aria-labelledby') ?? '';
      expect(labelId).not.toBe('');
      expect(document.getElementById(labelId)?.textContent).toBe('API Key Created Successfully');
    });

    test('shows warning about one-time display', () => {
      showKeyCreatedModal('test-key');

      const modal = document.getElementById('apikey-created-modal');
      expect(modal?.innerHTML).toContain('only time');
    });

    test('removes existing modal before creating new one', () => {
      // Create first modal
      showKeyCreatedModal('first-key');

      // Create second modal
      showKeyCreatedModal('second-key');

      // Should only be one modal
      const modals = document.querySelectorAll('#apikey-created-modal');
      expect(modals.length).toBe(1);
      expect(modals[0]?.innerHTML).toContain('second-key');
    });

    test('copy button copies key to clipboard', async () => {
      const writeTextMock = jest.fn().mockResolvedValue(undefined);
      Object.defineProperty(navigator, 'clipboard', {
        value: { writeText: writeTextMock },
        writable: true,
        configurable: true
      });

      showKeyCreatedModal('test-api-key');

      const copyBtn = document.getElementById('copy-apikey-btn');
      copyBtn?.click();

      await new Promise(resolve => setTimeout(resolve, 0));

      expect(writeTextMock).toHaveBeenCalledWith('test-api-key');
    });

    test('copy button shows feedback on success', async () => {
      jest.useFakeTimers();

      const writeTextMock = jest.fn().mockResolvedValue(undefined);
      Object.defineProperty(navigator, 'clipboard', {
        value: { writeText: writeTextMock },
        writable: true,
        configurable: true
      });

      showKeyCreatedModal('test-api-key');

      const copyBtn = document.getElementById('copy-apikey-btn') as HTMLButtonElement;
      copyBtn?.click();

      await Promise.resolve();

      expect(copyBtn.textContent).toBe('Copied!');
      expect(copyBtn.classList.contains('copied')).toBe(true);

      jest.advanceTimersByTime(2000);

      expect(copyBtn.textContent).toBe('Copy');
      expect(copyBtn.classList.contains('copied')).toBe(false);

      jest.useRealTimers();
    });

    test('copy failure shows an inline error and selects the key, not a native alert (#705)', async () => {
      const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
      const writeTextMock = jest.fn().mockRejectedValue(new Error('Clipboard error'));
      Object.defineProperty(navigator, 'clipboard', {
        value: { writeText: writeTextMock },
        writable: true,
        configurable: true
      });

      showKeyCreatedModal('test-api-key');

      const copyError = document.getElementById('apikey-copy-error') as HTMLElement;
      expect(copyError.getAttribute('role')).toBe('alert');
      expect(copyError.classList.contains('hidden')).toBe(true);

      document.getElementById('copy-apikey-btn')?.click();

      await new Promise(resolve => setTimeout(resolve, 0));

      expect(window.alert).not.toHaveBeenCalled();
      expect(copyError.classList.contains('hidden')).toBe(false);
      expect(copyError.textContent).toContain('The key is selected');
      expect(window.getSelection()?.toString()).toBe('test-api-key');

      // A later successful copy clears the error again.
      writeTextMock.mockResolvedValueOnce(undefined);
      document.getElementById('copy-apikey-btn')?.click();
      await new Promise(resolve => setTimeout(resolve, 0));
      expect(copyError.classList.contains('hidden')).toBe(true);
      consoleError.mockRestore();
    });

    test('close button removes modal', () => {
      showKeyCreatedModal('test-key');

      const closeBtn = document.getElementById('close-apikey-created-btn');
      closeBtn?.click();

      const modal = document.getElementById('apikey-created-modal');
      expect(modal).toBeNull();
    });
  });

  describe('revokeApiKey', () => {
    beforeEach(async () => {
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
      await loadApiKeys();
    });

    test('does nothing if key not found', async () => {
      await revokeApiKey('non-existent-key');

      expect(api.revokeApiKey).not.toHaveBeenCalled();
    });

    test('does nothing if user cancels confirmation', async () => {
      mockConfirmDialog.mockResolvedValueOnce(false);

      await revokeApiKey('key-1');

      expect(api.revokeApiKey).not.toHaveBeenCalled();
    });

    test('revokes key and reloads list', async () => {
      (api.revokeApiKey as jest.Mock).mockResolvedValue({});

      await revokeApiKey('key-1');

      expect(api.revokeApiKey).toHaveBeenCalledWith('key-1');
      expect(api.getApiKeys).toHaveBeenCalledTimes(2); // Initial load + after revoke
    });

    test('toasts a failure while the hidden create modal still has its error element (issue #717)', async () => {
      const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
      (api.revokeApiKey as jest.Mock).mockRejectedValue(new Error('Revoke failed'));

      await revokeApiKey('key-1');

      expect(document.querySelector('.toast-error')?.querySelector('.toast-message')?.textContent).toBe('Failed to revoke API key: Revoke failed');
      expect(document.getElementById('create-apikey-error')?.classList.contains('hidden')).toBe(true);
      consoleError.mockRestore();
    });

    test('shows error on API failure', async () => {
      // Remove the create-apikey-error element so showError falls back to alert
      document.getElementById('create-apikey-error')?.remove();

      const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
      (api.revokeApiKey as jest.Mock).mockRejectedValue(new Error('Revoke failed'));

      await revokeApiKey('key-1');

      expect(consoleError).toHaveBeenCalledWith('Failed to revoke API key:', expect.any(Error));
      expect(document.querySelector('.toast-error')?.querySelector('.toast-message')?.textContent).toBe('Failed to revoke API key: Revoke failed');
      consoleError.mockRestore();
    });
  });

  describe('deleteApiKey', () => {
    beforeEach(async () => {
      const mockKeys = [
        {
          id: 'key-1',
          name: 'Test Key',
          key_prefix: 'abc123',
          is_active: false,
          created_at: '2024-01-15T10:00:00Z'
        }
      ];

      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: mockKeys });
      await loadApiKeys();
    });

    test('does nothing if key not found', async () => {
      await deleteApiKey('non-existent-key');

      expect(api.deleteApiKey).not.toHaveBeenCalled();
    });

    test('does nothing if user cancels confirmation', async () => {
      mockConfirmDialog.mockResolvedValueOnce(false);

      await deleteApiKey('key-1');

      expect(api.deleteApiKey).not.toHaveBeenCalled();
    });

    test('deletes key and reloads list', async () => {
      (api.deleteApiKey as jest.Mock).mockResolvedValue({});

      await deleteApiKey('key-1');

      expect(api.deleteApiKey).toHaveBeenCalledWith('key-1');
      expect(api.getApiKeys).toHaveBeenCalledTimes(2); // Initial load + after delete
    });

    test('shows error on API failure', async () => {
      // Remove the create-apikey-error element so showError falls back to alert
      document.getElementById('create-apikey-error')?.remove();

      const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
      (api.deleteApiKey as jest.Mock).mockRejectedValue(new Error('Delete failed'));

      await deleteApiKey('key-1');

      expect(consoleError).toHaveBeenCalledWith('Failed to delete API key:', expect.any(Error));
      expect(document.querySelector('.toast-error')?.querySelector('.toast-message')?.textContent).toBe('Failed to delete API key: Delete failed');
      consoleError.mockRestore();
    });
  });
});
