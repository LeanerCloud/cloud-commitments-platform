/**
 * Unit tests for API module (login and logout)
 */
import { localStorageMock, sessionStorageMock, fetchMock } from './setup';
import { mockSessionStart } from './api-harness';
import {
  isAuthenticated,
  clearAuth,
  getAuthHeaders,
  login,
  setupAdmin
} from '../api';

describe('API Requests', () => {
  beforeEach(() => {
    clearAuth();
    fetchMock.mockReset();
  });

  describe('login', () => {
    test('ignores the body token, keeps only the CSRF token in memory', async () => {
      mockSessionStart({ token: 'new-token', csrf_token: 'csrf-new' }, { status: 200, body: { id: 'u', email: 'a@b.c', groups: [], csrf_token: 'csrf-new' } });

      await login('test@example.com', 'password');

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/auth/login',
        expect.objectContaining({
          method: 'POST',
          // Password is now base64 encoded
          body: JSON.stringify({ email: 'test@example.com', password: btoa('password') })
        })
      );
      expect(localStorageMock.setItem).not.toHaveBeenCalled();
      expect(sessionStorageMock.setItem).not.toHaveBeenCalled();
      expect(isAuthenticated()).toBe(true);
      expect(getAuthHeaders()['X-CSRF-Token']).toBe('csrf-new');
      expect(getAuthHeaders()['X-Authorization']).toBeUndefined();
    });

    test('a login whose cookie /auth/me rejects fails with the HTTPS hint and stays signed out', async () => {
      mockSessionStart({ token: 'new-token', csrf_token: 'csrf-new' }, { status: 401, body: { error: 'unauthorized' } });

      await expect(login('test@example.com', 'password')).rejects.toThrow('Serve CUDly over HTTPS and make sure cookies are enabled.');
      expect(isAuthenticated()).toBe(false);
      expect(getAuthHeaders()['X-CSRF-Token']).toBeUndefined();
    });

    test('a login whose /auth/me fails otherwise surfaces that error', async () => {
      mockSessionStart({ token: 'new-token', csrf_token: 'csrf-new' }, { status: 503, body: { error: 'down' } });

      await expect(login('test@example.com', 'password')).rejects.toMatchObject({ status: 503 });
      expect(isAuthenticated()).toBe(false);
    });

    test('setupAdmin whose cookie /auth/me rejects fails with the HTTPS hint', async () => {
      mockSessionStart({ token: 't' }, { status: 401, body: { error: 'unauthorized' } });

      await expect(setupAdmin('k', 'a@b.c', 'password')).rejects.toThrow('Serve CUDly over HTTPS and make sure cookies are enabled.');
      expect(isAuthenticated()).toBe(false);
    });

    test('includes x-amz-content-sha256 header for CloudFront OAC', async () => {
      mockSessionStart({ token: 'new-token' });

      await login('test@example.com', 'password');

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/auth/login',
        expect.objectContaining({
          headers: expect.objectContaining({
            'x-amz-content-sha256': expect.stringMatching(/^[a-f0-9]{64}$/)
          })
        })
      );
    });

    test('throws error on failure', async () => {
      fetchMock.mockResolvedValue({
        ok: false,
        json: () => Promise.resolve({ error: 'Invalid credentials' })
      });

      await expect(login('test@example.com', 'wrong')).rejects.toThrow('Invalid credentials');
    });
  });
});
