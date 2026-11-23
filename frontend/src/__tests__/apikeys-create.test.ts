/**
 * API Keys module tests: showCreateKeyModal and handleCreateApiKey. Split from
 * apikeys.test.ts to stay under the 500-line ceiling; the module mocks live in
 * the harness.
 */

// The harness registers the module mocks, so it must be imported first.
import { mockConfirmDialog } from './apikeys-harness';
import {
  showCreateKeyModal,
  handleCreateApiKey
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

  describe('showCreateKeyModal', () => {
    test('shows modal and resets form', () => {
      const modal = document.getElementById('create-apikey-modal');
      const nameInput = document.getElementById('apikey-name') as HTMLInputElement;
      nameInput.value = 'Previous Value';

      showCreateKeyModal();

      expect(modal?.classList.contains('hidden')).toBe(false);
      expect(nameInput.value).toBe('');
    });

    test('hides error element', () => {
      const errorEl = document.getElementById('create-apikey-error');
      errorEl?.classList.remove('hidden');

      showCreateKeyModal();

      expect(errorEl?.classList.contains('hidden')).toBe(true);
    });

    test('prefills expiration date 90 days out', () => {
      const expiresAtInput = document.getElementById('apikey-expires-at') as HTMLInputElement;
      expiresAtInput.value = '';

      showCreateKeyModal();

      // Compare local calendar components, not a UTC round-trip: the field
      // is filled from local date parts (issue #102 CodeRabbit finding), so
      // asserting via toISOString would flake near local midnight in
      // timezones ahead of or behind UTC.
      const expected = new Date();
      expected.setDate(expected.getDate() + 90);
      const expectedValue = [
        expected.getFullYear(),
        String(expected.getMonth() + 1).padStart(2, '0'),
        String(expected.getDate()).padStart(2, '0'),
      ].join('-');
      expect(expiresAtInput.value).toBe(expectedValue);
    });

    test('handles missing modal gracefully', () => {
      document.body.innerHTML = '';

      // Should not throw
      expect(() => showCreateKeyModal()).not.toThrow();
    });

    // Issue #61 frontend follow-up: the create-key modal now fetches the
    // caller's own effective permissions and renders one checkbox per
    // entry, since the backend rejects an empty permissions array.
    describe('permission picker (issue #61)', () => {
      test('fetches and renders the owner\'s effective permissions as unchecked checkboxes', async () => {
        (api.getUserPermissions as jest.Mock).mockResolvedValue({
          permissions: [
            { action: 'view', resource: 'recommendations' },
            { action: 'create', resource: 'plans' },
          ],
          is_admin: false,
        });

        await showCreateKeyModal();

        expect(api.getUserPermissions).toHaveBeenCalled();
        const container = document.getElementById('apikey-permissions-list');
        const checkboxes = container?.querySelectorAll<HTMLInputElement>('.apikey-permission-checkbox');
        expect(checkboxes?.length).toBe(2);
        checkboxes?.forEach(cb => expect(cb.checked).toBe(false));
        expect(container?.textContent).toContain('view:recommendations');
        expect(container?.textContent).toContain('create:plans');
      });

      test('labels the admin wildcard entry in plain language', async () => {
        (api.getUserPermissions as jest.Mock).mockResolvedValue({
          permissions: [{ action: 'admin', resource: '*' }],
          is_admin: true,
        });

        await showCreateKeyModal();

        const container = document.getElementById('apikey-permissions-list');
        expect(container?.textContent).toContain('Admin (all actions, all resources)');
      });

      test('escapes hostile action/resource values instead of injecting markup', async () => {
        const hostile = '"><img src=x onerror=alert(1)>';
        (api.getUserPermissions as jest.Mock).mockResolvedValue({
          permissions: [{ action: hostile, resource: 'plans' }],
          is_admin: false,
        });

        await showCreateKeyModal();

        const container = document.getElementById('apikey-permissions-list');
        // No element was injected -- the quote-breakout attempt stayed
        // inside the data-action attribute's value rather than closing it
        // and opening a real <img> tag.
        expect(container?.querySelector('img')).toBeNull();
        // The checkbox's dataset round-trips the exact hostile string,
        // proving escapeHtml neutralized it as data rather than mangling
        // or truncating it.
        const checkbox = container?.querySelector<HTMLInputElement>('.apikey-permission-checkbox');
        expect(checkbox?.dataset['action']).toBe(hostile);
        // The visible label text is HTML-escaped, not raw markup.
        expect(container?.textContent).toContain(`${hostile}:plans`);
      });

      test('shows a message and no checkboxes when the owner holds no permissions', async () => {
        (api.getUserPermissions as jest.Mock).mockResolvedValue({ permissions: [], is_admin: false });

        await showCreateKeyModal();

        const container = document.getElementById('apikey-permissions-list');
        expect(container?.querySelectorAll('.apikey-permission-checkbox').length).toBe(0);
        expect(container?.textContent).toContain('No permissions available');
      });

      test('shows an inline error when the permissions fetch fails', async () => {
        const consoleError = jest.spyOn(console, 'error').mockImplementation(() => {});
        (api.getUserPermissions as jest.Mock).mockRejectedValue(new Error('boom'));

        await showCreateKeyModal();

        const container = document.getElementById('apikey-permissions-list');
        expect(container?.querySelector('.error')?.textContent).toContain('Failed to load your permissions');
        consoleError.mockRestore();
      });

      test('Create button starts disabled and enables only once a permission is checked', async () => {
        (api.getUserPermissions as jest.Mock).mockResolvedValue({
          permissions: [{ action: 'view', resource: 'recommendations' }],
          is_admin: false,
        });

        await showCreateKeyModal();

        const submitBtn = document.getElementById('create-apikey-submit-btn') as HTMLButtonElement;
        expect(submitBtn.disabled).toBe(true);

        const checkbox = document.querySelector('.apikey-permission-checkbox') as HTMLInputElement;
        checkbox.checked = true;
        checkbox.dispatchEvent(new Event('change'));
        expect(submitBtn.disabled).toBe(false);

        checkbox.checked = false;
        checkbox.dispatchEvent(new Event('change'));
        expect(submitBtn.disabled).toBe(true);
      });
    });
  });

  describe('handleCreateApiKey', () => {
    beforeEach(() => {
      document.getElementById('create-apikey-modal')?.classList.remove('hidden');
    });

    test('prevents default form submission', async () => {
      (api.createApiKey as jest.Mock).mockResolvedValue({
        api_key: 'test-key',
        key_id: 'key-1',
        key: { id: 'key-1', name: 'Test', key_prefix: 'abc' }
      });
      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: [] });

      (document.getElementById('apikey-name') as HTMLInputElement).value = 'Test Key';

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await handleCreateApiKey(event);

      expect(event.preventDefault).toHaveBeenCalled();
    });

    test('shows error when name is empty', async () => {
      const event = { preventDefault: jest.fn() } as unknown as Event;
      await handleCreateApiKey(event);

      const errorEl = document.getElementById('create-apikey-error');
      expect(errorEl?.classList.contains('hidden')).toBe(false);
      expect(errorEl?.textContent).toBe('API key name is required');
    });

    // Regression coverage for issue #102: the form must not let a key be
    // created with no password re-verification.
    test('shows error when password is empty', async () => {
      const futureDate = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000);
      (document.getElementById('apikey-name') as HTMLInputElement).value = 'Test Key';
      (document.getElementById('apikey-expires-at') as HTMLInputElement).value = futureDate.toISOString().split('T')[0] || "";

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await handleCreateApiKey(event);

      const errorEl = document.getElementById('create-apikey-error');
      expect(errorEl?.textContent).toBe('Your password is required to create an API key');
      expect(api.createApiKey).not.toHaveBeenCalled();
    });

    // Regression coverage for issue #102: the form must not let a key be
    // created with no expiration.
    test('shows error when expiration date is empty', async () => {
      (document.getElementById('apikey-name') as HTMLInputElement).value = 'Test Key';
      (document.getElementById('apikey-password') as HTMLInputElement).value = 'my-password';
      (document.getElementById('apikey-expires-at') as HTMLInputElement).value = '';

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await handleCreateApiKey(event);

      const errorEl = document.getElementById('create-apikey-error');
      expect(errorEl?.textContent).toBe('An expiration date is required');
      expect(api.createApiKey).not.toHaveBeenCalled();
    });

    test('shows error when expiration date is in the past', async () => {
      const pastDate = new Date(Date.now() - 24 * 60 * 60 * 1000);

      (document.getElementById('apikey-name') as HTMLInputElement).value = 'Test Key';
      (document.getElementById('apikey-password') as HTMLInputElement).value = 'my-password';
      (document.getElementById('apikey-expires-at') as HTMLInputElement).value = pastDate.toISOString().split('T')[0] || "";

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await handleCreateApiKey(event);

      const errorEl = document.getElementById('create-apikey-error');
      expect(errorEl?.textContent).toBe('Expiration date must be in the future');
    });

    test('creates key and shows success modal', async () => {
      const futureDate = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000);
      const mockResponse = {
        api_key: 'new-api-key-12345',
        key_id: 'key-1',
        key: { id: 'key-1', name: 'Test Key', key_prefix: 'new' }
      };

      (api.createApiKey as jest.Mock).mockResolvedValue(mockResponse);
      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: [] });

      (document.getElementById('apikey-name') as HTMLInputElement).value = 'Test Key';
      (document.getElementById('apikey-password') as HTMLInputElement).value = 'my-password';
      (document.getElementById('apikey-expires-at') as HTMLInputElement).value = futureDate.toISOString().split('T')[0] || "";

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await handleCreateApiKey(event);

      expect(api.createApiKey).toHaveBeenCalledWith(
        expect.objectContaining({
          name: 'Test Key',
          password: expect.any(String),
          expires_at: expect.any(String)
        })
      );

      // Check that the key created modal is shown
      const createdModal = document.getElementById('apikey-created-modal');
      expect(createdModal).not.toBeNull();
      expect(createdModal?.innerHTML).toContain('new-api-key-12345');
    });

    test('shows error on API failure', async () => {
      const futureDate = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000);
      (api.createApiKey as jest.Mock).mockRejectedValue(new Error('Create failed'));

      (document.getElementById('apikey-name') as HTMLInputElement).value = 'Test Key';
      (document.getElementById('apikey-password') as HTMLInputElement).value = 'my-password';
      (document.getElementById('apikey-expires-at') as HTMLInputElement).value = futureDate.toISOString().split('T')[0] || "";

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await handleCreateApiKey(event);

      const errorEl = document.getElementById('create-apikey-error');
      expect(errorEl?.textContent).toContain('Failed to create API key');
    });

    // Issue #61: the backend now 400s on an empty/missing permissions
    // array, so the request body sent from the form must always carry the
    // checked selection.
    test('sends the checked permissions in the request body', async () => {
      const futureDate = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000);
      (api.createApiKey as jest.Mock).mockResolvedValue({
        api_key: 'test-key',
        key_id: 'key-1',
        key: { id: 'key-1', name: 'Test', key_prefix: 'abc' }
      });
      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: [] });

      (document.getElementById('apikey-name') as HTMLInputElement).value = 'Test Key';
      (document.getElementById('apikey-password') as HTMLInputElement).value = 'my-password';
      (document.getElementById('apikey-expires-at') as HTMLInputElement).value = futureDate.toISOString().split('T')[0] || "";
      // The default fixture ships one pre-checked view:recommendations box.

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await handleCreateApiKey(event);

      expect(api.createApiKey).toHaveBeenCalledWith(
        expect.objectContaining({
          name: 'Test Key',
          permissions: [{ action: 'view', resource: 'recommendations' }],
        })
      );
      const [request] = (api.createApiKey as jest.Mock).mock.calls[0] as [{ permissions: unknown[] }];
      expect(request.permissions.length).toBeGreaterThan(0);
    });

    test('rejects submission with an error, and never calls the API, when no permission is checked', async () => {
      const futureDate = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000);
      (document.getElementById('apikey-name') as HTMLInputElement).value = 'Test Key';
      (document.getElementById('apikey-password') as HTMLInputElement).value = 'my-password';
      (document.getElementById('apikey-expires-at') as HTMLInputElement).value = futureDate.toISOString().split('T')[0] || "";
      const checkbox = document.querySelector('.apikey-permission-checkbox') as HTMLInputElement;
      checkbox.checked = false;

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await handleCreateApiKey(event);

      const errorEl = document.getElementById('create-apikey-error');
      expect(errorEl?.textContent).toBe('Select at least one permission');
      expect(api.createApiKey).not.toHaveBeenCalled();
    });

    test('sends every checked permission when more than one is selected', async () => {
      const futureDate = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000);
      document.getElementById('apikey-permissions-list')!.innerHTML = `
        <label><input type="checkbox" class="apikey-permission-checkbox" data-action="view" data-resource="recommendations" checked></label>
        <label><input type="checkbox" class="apikey-permission-checkbox" data-action="create" data-resource="plans" checked></label>
      `;
      (api.createApiKey as jest.Mock).mockResolvedValue({
        api_key: 'test-key',
        key_id: 'key-1',
        key: { id: 'key-1', name: 'Test', key_prefix: 'abc' }
      });
      (api.getApiKeys as jest.Mock).mockResolvedValue({ api_keys: [] });
      (document.getElementById('apikey-name') as HTMLInputElement).value = 'Test Key';
      (document.getElementById('apikey-password') as HTMLInputElement).value = 'my-password';
      (document.getElementById('apikey-expires-at') as HTMLInputElement).value = futureDate.toISOString().split('T')[0] || "";

      const event = { preventDefault: jest.fn() } as unknown as Event;
      await handleCreateApiKey(event);

      expect(api.createApiKey).toHaveBeenCalledWith(
        expect.objectContaining({
          permissions: [
            { action: 'view', resource: 'recommendations' },
            { action: 'create', resource: 'plans' },
          ],
        })
      );
    });
  });
});
