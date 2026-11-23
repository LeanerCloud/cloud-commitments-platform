/**
 * Unit tests for API module (account and password reset)
 */
import { localStorageMock, fetchMock } from './setup';
import {
  isAuthenticated,
  clearAuth,
  getAuthHeaders,
  getCurrentUser,
  requestPasswordReset,
  getResetTokenStatus
} from '../api';

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
});
