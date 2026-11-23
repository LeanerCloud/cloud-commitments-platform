/**
 * Unit tests for API module (account and password reset)
 */
import { localStorageMock, fetchMock } from './setup';
import { mockSessionStart } from './api-harness';
import {
  isAuthenticated,
  clearAuth,
  getAuthHeaders,
  login,
  getCurrentUser,
  requestPasswordReset,
  getResetTokenStatus,
  checkAdminExists,
  setupAdmin
} from '../api';
import { base64Encode } from '../api/client';

describe('API Requests', () => {
  beforeEach(() => {
    clearAuth();
    fetchMock.mockReset();
  });

  describe('getCurrentUser', () => {
    test('fetches current user', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ email: 'test@example.com', groups: ['00000000-0000-5000-8000-000000000001'], csrf_token: 'csrf-1' })
      });

      const user = await getCurrentUser();
      expect(user.email).toBe('test@example.com');
      expect(fetchMock).toHaveBeenCalledWith('/api/auth/me', expect.anything());
    });

    test('restores the session and CSRF token from /auth/me without leaking it onto the user', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ id: 'u1', email: 'a@b.c', groups: [], csrf_token: 'csrf-restored' })
      });

      const user = await getCurrentUser();

      expect(isAuthenticated()).toBe(true);
      expect(getAuthHeaders()['X-CSRF-Token']).toBe('csrf-restored');
      expect(user).not.toHaveProperty('csrf_token');
      expect(localStorageMock.setItem).not.toHaveBeenCalled();
    });

    test('a 200 without csrf_token fails loud and leaves the page signed out', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ id: 'u1', email: 'a@b.c', groups: [] })
      });

      await expect(getCurrentUser()).rejects.toThrow('no CSRF token');
      expect(isAuthenticated()).toBe(false);
      expect(getAuthHeaders()['X-CSRF-Token']).toBeUndefined();
    });

    test('a 401 rejects and leaves the page signed out', async () => {
      fetchMock.mockResolvedValue({
        ok: false,
        status: 401,
        json: () => Promise.resolve({ error: 'unauthorized' })
      });

      await expect(getCurrentUser()).rejects.toMatchObject({ status: 401 });
      expect(isAuthenticated()).toBe(false);
    });
  });

  describe('requestPasswordReset', () => {
    test('sends reset request', async () => {
      fetchMock.mockResolvedValue({ ok: true });

      await requestPasswordReset('test@example.com');

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/auth/forgot-password',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ email: 'test@example.com' })
        })
      );
    });
  });

  // CR pass-1: the token-status response is consumed by branching UI
  // (modal-routing, copy selection) that only handles the closed unions
  // 'valid'|'expired'|'used' and 'reset'|'invite'. A malicious or
  // misconfigured server returning e.g. {state:'pwned'} would otherwise
  // be silently accepted via the unchecked `as` cast and cause downstream
  // logic to fall through to the form (or worse). Validate at the boundary.
  describe('getResetTokenStatus (runtime validation)', () => {
    test('parses a valid response', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ state: 'valid', flow: 'reset' })
      });

      await expect(getResetTokenStatus('tok')).resolves.toEqual({ state: 'valid', flow: 'reset' });
    });

    test('throws when state is not in the closed union', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ state: 'pwned', flow: 'reset' })
      });

      await expect(getResetTokenStatus('tok')).rejects.toThrow(/invalid state.*pwned/);
    });

    test('throws when flow is not in the closed union', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ state: 'valid', flow: 'phish' })
      });

      await expect(getResetTokenStatus('tok')).rejects.toThrow(/invalid flow.*phish/);
    });

    test('throws when state is not a string (e.g. a number)', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ state: 42, flow: 'reset' })
      });

      await expect(getResetTokenStatus('tok')).rejects.toThrow(/invalid state/);
    });

    test('throws when the body is not an object', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve('not-an-object')
      });

      await expect(getResetTokenStatus('tok')).rejects.toThrow(/was not an object/);
    });
  });

  describe('checkAdminExists', () => {
    test('returns true when admin exists', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ admin_exists: true })
      });

      const result = await checkAdminExists('api-key');
      expect(result).toBe(true);
    });

    test('returns false when no admin', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ admin_exists: false })
      });

      const result = await checkAdminExists('api-key');
      expect(result).toBe(false);
    });

    test('returns false on error', async () => {
      fetchMock.mockResolvedValue({ ok: false });

      const result = await checkAdminExists('api-key');
      expect(result).toBe(false);
    });
  });

  describe('setupAdmin', () => {
    test('creates admin and keeps only the CSRF token in memory', async () => {
      mockSessionStart({ token: 'admin-token', csrf_token: 'csrf-admin' }, { status: 200, body: { id: 'u', email: 'a@b.c', groups: [], csrf_token: 'csrf-admin' } });

      await setupAdmin('api-key', 'admin@example.com', 'password');

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/auth/setup-admin',
        expect.objectContaining({
          method: 'POST',
          headers: expect.objectContaining({ 'X-API-Key': 'api-key' }),
          // Password is now base64 encoded
          body: JSON.stringify({ email: 'admin@example.com', password: btoa('password') })
        })
      );
      expect(localStorageMock.setItem).not.toHaveBeenCalled();
      expect(isAuthenticated()).toBe(true);
      expect(getAuthHeaders()['X-CSRF-Token']).toBe('csrf-admin');
    });

    // Issue #748: a proxy 502 with an HTML body used to surface as
    // "Unexpected token '<'" because the error body was parsed uncaught.
    test('a 502 HTML error page fails with friendly text, not a JSON parse error', async () => {
      fetchMock.mockResolvedValue({ ok: false, status: 502, json: () => Promise.reject(new SyntaxError("Unexpected token '<'")) });
      await expect(setupAdmin('api-key', 'admin@example.com', 'password'))
        .rejects.toMatchObject({ message: 'CUDly is temporarily unavailable. Try again in a moment.', status: 502 });
    });

    test('a 400 keeps the server reason', async () => {
      fetchMock.mockResolvedValue({ ok: false, status: 400, json: () => Promise.resolve({ error: 'admin already exists' }) });
      await expect(setupAdmin('api-key', 'admin@example.com', 'password')).rejects.toThrow('admin already exists');
    });

    test('a network failure says the server cannot be reached', async () => {
      fetchMock.mockRejectedValue(new TypeError('Failed to fetch'));
      await expect(setupAdmin('api-key', 'admin@example.com', 'password'))
        .rejects.toThrow("Can't reach CUDly. Check your connection and try again.");
    });

    test('a JSON error body still surfaces the server message', async () => {
      fetchMock.mockResolvedValue({
        ok: false,
        status: 400,
        json: () => Promise.resolve({ error: 'password too weak' }),
      });

      await expect(setupAdmin('api-key', 'admin@example.com', 'password')).rejects.toThrow('password too weak');
    });
  });

  describe('base64Encode', () => {
    test('Latin-1 passwords keep the exact bytes btoa has always sent', () => {
      expect(base64Encode('password')).toBe(btoa('password'));
      expect(base64Encode('caf\u00e9')).toBe(btoa('caf\u00e9'));
    });

    test('non-Latin-1 passwords are sent as UTF-8 instead of throwing', () => {
      const pw = 'it\u2019s a \u5bc6\u7801 \u{1F511}';
      const encoded = base64Encode(pw);
      expect(Buffer.from(encoded, 'base64').toString('utf8')).toBe(pw);
    });

    test('login with a non-Latin-1 password reaches the network', async () => {
      mockSessionStart({ token: 't' });
      await login('user@example.com', 'p\u00e4ss\u2014w\u00f6rd \u{1F600}');
      const body = JSON.parse(fetchMock.mock.calls[0][1].body as string) as { password: string };
      expect(Buffer.from(body.password, 'base64').toString('utf8')).toBe('p\u00e4ss\u2014w\u00f6rd \u{1F600}');
    });
  });
});
