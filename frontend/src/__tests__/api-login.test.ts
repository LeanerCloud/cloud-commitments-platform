/**
 * Unit tests for API module (login and logout)
 */
import { localStorageMock, sessionStorageMock, fetchMock } from './setup';
import { mockSessionStart } from './api-harness';
import {
  setAuthenticated,
  setCsrfToken,
  isAuthenticated,
  clearAuth,
  getAuthHeaders,
  login,
  logout,
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

  describe('logout', () => {
    test('calls logout endpoint and clears auth', async () => {
      setAuthenticated(true);
      fetchMock.mockResolvedValue({ ok: true });

      await logout();

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/auth/logout',
        expect.objectContaining({ method: 'POST' })
      );
      expect(fetchMock.mock.calls[0][1]).toMatchObject({ credentials: 'same-origin' });
      expect(isAuthenticated()).toBe(false);
      // An intentional sign-out must not resurrect a plan draft (#1735).
      expect(sessionStorage.removeItem).toHaveBeenCalledWith('cudly:plan-form-draft');
    });

    test('tells other tabs to sign out (jsdom has no BroadcastChannel, so the marker key)', async () => {
      setAuthenticated(true);
      fetchMock.mockResolvedValue({ ok: true });

      await logout();

      expect(localStorageMock.setItem).toHaveBeenCalledWith('cudly:logout-signal', expect.any(String));
    });

    test('includes x-amz-content-sha256 header for CloudFront OAC', async () => {
      setAuthenticated(true);
      fetchMock.mockResolvedValue({ ok: true });

      await logout();

      // SHA256 of empty string since logout has no body
      const emptyHash = 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855';
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/auth/logout',
        expect.objectContaining({
          headers: expect.objectContaining({
            'x-amz-content-sha256': emptyHash
          })
        })
      );
    });

    test.each([[500], [403]])('a %i answer keeps the session, does not broadcast and throws', async (status) => {
      setAuthenticated(true);
      setCsrfToken('csrf');
      fetchMock.mockResolvedValue({ ok: false, status });
      const warn = jest.spyOn(console, 'warn').mockImplementation(() => undefined);

      await expect(logout()).rejects.toThrow('Sign out failed');
      warn.mockRestore();

      expect(isAuthenticated()).toBe(true);
      expect(getAuthHeaders()['X-CSRF-Token']).toBe('csrf');
      expect(localStorageMock.setItem).not.toHaveBeenCalledWith('cudly:logout-signal', expect.anything());
      expect(sessionStorage.removeItem).not.toHaveBeenCalledWith('cudly:plan-form-draft');
    });

    describe('stale CSRF token', () => {
      const csrfFail = { ok: false, status: 403, json: () => Promise.resolve({ error: 'CSRF validation failed' }) };
      const meOk = { ok: true, status: 200, json: () => Promise.resolve({ id: 'u', email: 'a@b.c', groups: [], csrf_token: 'fresh' }) };
      const calls = (suffix: string) => fetchMock.mock.calls.filter((c) => String(c[0]).endsWith(suffix));

      beforeEach(() => {
        setAuthenticated(true);
        setCsrfToken('stale');
        fetchMock.mockReset();
      });

      test('refreshes the token via /auth/me and retries logout once', async () => {
        fetchMock.mockResolvedValueOnce(csrfFail).mockResolvedValueOnce(meOk).mockResolvedValueOnce({ ok: true, status: 200 });

        await logout();

        expect(calls('/auth/logout')).toHaveLength(2);
        expect(calls('/auth/logout')[0][1].headers['X-CSRF-Token']).toBe('stale');
        expect(calls('/auth/logout')[1][1].headers['X-CSRF-Token']).toBe('fresh');
        expect(isAuthenticated()).toBe(false);
        expect(localStorageMock.setItem).toHaveBeenCalledWith('cudly:logout-signal', expect.any(String));
      });

      test('a retry that still fails throws and keeps the session, never a third attempt', async () => {
        fetchMock.mockResolvedValueOnce(csrfFail).mockResolvedValueOnce(meOk).mockResolvedValueOnce(csrfFail);
        const warn = jest.spyOn(console, 'warn').mockImplementation(() => undefined);

        await expect(logout()).rejects.toThrow('Sign out failed');
        warn.mockRestore();

        expect(calls('/auth/logout')).toHaveLength(2);
        expect(calls('/auth/me')).toHaveLength(1);
        expect(isAuthenticated()).toBe(true);
        expect(localStorageMock.setItem).not.toHaveBeenCalledWith('cudly:logout-signal', expect.anything());
        expect(sessionStorage.removeItem).not.toHaveBeenCalledWith('cudly:plan-form-draft');
      });

      test('a 401 from /auth/me means already signed out: success without a retry', async () => {
        fetchMock.mockResolvedValueOnce(csrfFail).mockResolvedValueOnce({ ok: false, status: 401, json: () => Promise.resolve({}) });

        await logout();

        expect(calls('/auth/logout')).toHaveLength(1);
        expect(isAuthenticated()).toBe(false);
        expect(localStorageMock.setItem).toHaveBeenCalledWith('cudly:logout-signal', expect.any(String));
      });

      test('another /auth/me failure throws and does not retry', async () => {
        fetchMock.mockResolvedValueOnce(csrfFail).mockResolvedValueOnce({ ok: false, status: 500, json: () => Promise.resolve({}) });
        const warn = jest.spyOn(console, 'warn').mockImplementation(() => undefined);

        await expect(logout()).rejects.toThrow('Sign out failed');
        warn.mockRestore();

        expect(calls('/auth/logout')).toHaveLength(1);
        expect(isAuthenticated()).toBe(true);
      });

      test('a 403 with an unparsable body is not retried', async () => {
        fetchMock.mockResolvedValue({ ok: false, status: 403, json: () => Promise.reject(new SyntaxError('bad json')) });
        const warn = jest.spyOn(console, 'warn').mockImplementation(() => undefined);

        await expect(logout()).rejects.toThrow('Sign out failed');
        warn.mockRestore();

        expect(calls('/auth/logout')).toHaveLength(1);
        expect(calls('/auth/me')).toHaveLength(0);
        expect(isAuthenticated()).toBe(true);
      });

      test.each([[200, true], [401, false]])('only a 403 is retried: a %i carrying the CSRF error body is not', async (status, ok) => {
        fetchMock.mockResolvedValue({ ok, status, json: () => Promise.resolve({ error: 'CSRF validation failed' }) });

        await logout();

        expect(fetchMock).toHaveBeenCalledTimes(1);
        expect(calls('/auth/me')).toHaveLength(0);
        expect(isAuthenticated()).toBe(false);
        expect(localStorageMock.setItem).toHaveBeenCalledWith('cudly:logout-signal', expect.any(String));
      });

      test('a 403 that is not a CSRF failure is not retried', async () => {
        fetchMock.mockResolvedValue({ ok: false, status: 403, json: () => Promise.resolve({ error: 'forbidden' }) });
        const warn = jest.spyOn(console, 'warn').mockImplementation(() => undefined);

        await expect(logout()).rejects.toThrow('Sign out failed');
        warn.mockRestore();

        expect(fetchMock).toHaveBeenCalledTimes(1);
      });
    });

    test('a network error keeps the session and does not broadcast', async () => {
      setAuthenticated(true);
      fetchMock.mockRejectedValue(new Error('Network error'));
      const warn = jest.spyOn(console, 'warn').mockImplementation(() => undefined);

      await expect(logout()).rejects.toThrow('Sign out failed');

      expect(isAuthenticated()).toBe(true);
      expect(localStorageMock.setItem).not.toHaveBeenCalledWith('cudly:logout-signal', expect.anything());
      warn.mockRestore();
    });

    test('a 401 means the session is already gone and signs out', async () => {
      setAuthenticated(true);
      fetchMock.mockResolvedValue({ ok: false, status: 401 });

      await logout();

      expect(isAuthenticated()).toBe(false);
      expect(localStorageMock.setItem).toHaveBeenCalledWith('cudly:logout-signal', expect.any(String));
    });
  });
});
