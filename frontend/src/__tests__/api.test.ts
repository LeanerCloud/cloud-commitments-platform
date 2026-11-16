/**
 * Unit tests for API module
 */
import { localStorageMock, sessionStorageMock, fetchMock } from './setup';
import {
  initAuth,
  setAuthenticated,
  setCsrfToken,
  isAuthenticated,
  clearAuth,
  getAuthHeaders,
  apiRequest,
  login,
  logout,
  getCurrentUser,
  requestPasswordReset,
  getResetTokenStatus,
  checkAdminExists,
  setupAdmin,
  getDashboardSummary,
  getUpcomingPurchases,
  getRecommendations,
  refreshRecommendations,
  getPlans,
  getPlan,
  createPlan,
  updatePlan,
  patchPlan,
  deletePlan,
  getHistory,
  getConfig,
  updateConfig,
  executePurchase,
  getPurchaseDetails,
  cancelPurchase,
  getPublicInfo,
  getDeploymentInfo
} from '../api';
import { base64Encode } from '../api/client';
import type { CreatePlanRequest, Config, Recommendation, UpcomingPurchasesResponse } from '../api';

// A successful login or setup is followed by a /auth/me check of the session cookie.
function mockSessionStart(body: unknown, me: { status: number; body: unknown } = { status: 200, body: { id: 'u', email: 'a@b.c', groups: [], csrf_token: 'csrf-me' } }): void {
  fetchMock.mockImplementation((url: string) => Promise.resolve(url.endsWith('/auth/me')
    ? { ok: me.status < 400, status: me.status, json: () => Promise.resolve(me.body) }
    : { ok: true, status: 200, json: () => Promise.resolve(body) }));
}

describe('Authentication', () => {
  beforeEach(() => {
    localStorageMock.getItem.mockReturnValue(null);
    sessionStorageMock.getItem.mockReturnValue(null);
    localStorageMock.setItem.mockClear();
    localStorageMock.removeItem.mockClear();
    sessionStorageMock.removeItem.mockClear();
    clearAuth();
  });

  describe('initAuth', () => {
    test('purges legacy token keys from localStorage and sessionStorage', () => {
      initAuth();
      for (const key of ['authToken', 'apiKey', 'csrfToken']) {
        expect(localStorageMock.removeItem).toHaveBeenCalledWith(key);
        expect(sessionStorageMock.removeItem).toHaveBeenCalledWith(key);
      }
    });

    test('a stored legacy token no longer authenticates and is never copied', () => {
      localStorageMock.getItem.mockImplementation((key: string) => (key === 'authToken' ? 'old' : null));
      initAuth();
      expect(isAuthenticated()).toBe(false);
      expect(getAuthHeaders()['X-Authorization']).toBeUndefined();
      expect(localStorageMock.setItem).not.toHaveBeenCalled();
    });

    test('blocked storage does not break boot', () => {
      localStorageMock.removeItem.mockImplementationOnce(() => { throw new Error('denied'); });
      expect(() => initAuth()).not.toThrow();
    });
  });

  describe('isAuthenticated', () => {
    test('is false until the server accepts the session', () => {
      expect(isAuthenticated()).toBe(false);
      setAuthenticated(true);
      expect(isAuthenticated()).toBe(true);
    });
  });

  describe('clearAuth', () => {
    test('drops the in-memory session and CSRF token', () => {
      setAuthenticated(true);
      setCsrfToken('csrf');
      clearAuth();
      expect(isAuthenticated()).toBe(false);
      expect(getAuthHeaders()['X-CSRF-Token']).toBeUndefined();
    });
  });

  describe('getAuthHeaders', () => {
    test('returns content-type only with no session', () => {
      expect(getAuthHeaders()).toEqual({ 'Content-Type': 'application/json' });
    });

    test('includes the in-memory CSRF token and no credential header', () => {
      setCsrfToken('csrf-1');
      const headers = getAuthHeaders();
      expect(headers['X-CSRF-Token']).toBe('csrf-1');
      expect(headers['X-Authorization']).toBeUndefined();
      expect(headers['X-API-Key']).toBeUndefined();
    });

    test('never writes the CSRF token to web storage', () => {
      setCsrfToken('csrf-1');
      expect(localStorageMock.setItem).not.toHaveBeenCalled();
      expect(sessionStorageMock.setItem).not.toHaveBeenCalled();
    });
  });
});

describe('API Requests', () => {
  beforeEach(() => {
    clearAuth();
    fetchMock.mockReset();
  });

  describe('apiRequest', () => {
    test('makes request with correct URL', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ data: 'test' })
      });

      await apiRequest('/test-endpoint');
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/test-endpoint',
        expect.objectContaining({
          headers: expect.objectContaining({
            'Content-Type': 'application/json'
          })
        })
      );
    });

    test('adds x-amz-content-sha256 header for POST requests with body', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({})
      });

      await apiRequest('/test', {
        method: 'POST',
        body: JSON.stringify({ data: 'test' })
      });

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/test',
        expect.objectContaining({
          headers: expect.objectContaining({
            'x-amz-content-sha256': expect.stringMatching(/^[a-f0-9]{64}$/)
          })
        })
      );
    });

    test('adds x-amz-content-sha256 header for POST requests without body', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({})
      });

      await apiRequest('/test', { method: 'POST' });

      // SHA256 of empty string
      const emptyHash = 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855';
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/test',
        expect.objectContaining({
          headers: expect.objectContaining({
            'x-amz-content-sha256': emptyHash
          })
        })
      );
    });

    test('adds x-amz-content-sha256 header for PUT requests', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({})
      });

      await apiRequest('/test', {
        method: 'PUT',
        body: JSON.stringify({ data: 'update' })
      });

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/test',
        expect.objectContaining({
          headers: expect.objectContaining({
            'x-amz-content-sha256': expect.stringMatching(/^[a-f0-9]{64}$/)
          })
        })
      );
    });

    test('adds x-amz-content-sha256 header for PATCH requests', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({})
      });

      await apiRequest('/test', {
        method: 'PATCH',
        body: JSON.stringify({ enabled: false })
      });

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/test',
        expect.objectContaining({
          headers: expect.objectContaining({
            'x-amz-content-sha256': expect.stringMatching(/^[a-f0-9]{64}$/)
          })
        })
      );
    });

    test('adds x-amz-content-sha256 header for DELETE requests', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({})
      });

      await apiRequest('/test', { method: 'DELETE' });

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/test',
        expect.objectContaining({
          headers: expect.objectContaining({
            'x-amz-content-sha256': expect.stringMatching(/^[a-f0-9]{64}$/)
          })
        })
      );
    });

    test('does not add x-amz-content-sha256 header for GET requests', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({})
      });

      await apiRequest('/test');

      const callArgs = fetchMock.mock.calls[0][1] as { headers: Record<string, string> };
      expect(callArgs.headers['x-amz-content-sha256']).toBeUndefined();
    });

    test('produces consistent hash for same body content', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({})
      });

      const body = JSON.stringify({ email: 'test@example.com', password: 'secret' });

      await apiRequest('/test1', { method: 'POST', body });
      await apiRequest('/test2', { method: 'POST', body });

      const call1 = fetchMock.mock.calls[0][1] as { headers: Record<string, string> };
      const call2 = fetchMock.mock.calls[1][1] as { headers: Record<string, string> };

      expect(call1.headers['x-amz-content-sha256']).toBe(call2.headers['x-amz-content-sha256']);
    });

    test('sends the CSRF header and same-origin credentials, no bearer token', async () => {
      setCsrfToken('test-csrf');
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({})
      });

      await apiRequest('/test');
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/test',
        expect.objectContaining({
          headers: expect.objectContaining({
            'X-CSRF-Token': 'test-csrf'
          }),
          credentials: 'same-origin'
        })
      );
    });

    test('throws error for non-ok response', async () => {
      fetchMock.mockResolvedValue({
        ok: false,
        status: 404,
        json: () => Promise.resolve({ error: 'Not found' })
      });

      await expect(apiRequest('/test')).rejects.toThrow('Not found');
    });

    test('includes status code in error', async () => {
      fetchMock.mockResolvedValue({
        ok: false,
        status: 500,
        json: () => Promise.reject(new Error('parse error'))
      });

      await expect(apiRequest('/test')).rejects.toMatchObject({ status: 500 });
    });

    test('resolves a 204 No Content response to null', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        status: 204,
        json: () => Promise.reject(new SyntaxError('Unexpected end of JSON input')),
      });
      await expect(apiRequest('/test')).resolves.toBeNull();
    });

    test('resolves a content-length 0 response to null', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        status: 200,
        headers: new Headers({ 'content-length': '0' }),
        json: () => Promise.reject(new SyntaxError('Unexpected end of JSON input')),
      });
      await expect(apiRequest('/test')).resolves.toBeNull();
    });

    test('rejects a malformed 2xx body instead of resolving null (#273)', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        status: 200,
        json: () => Promise.reject(new SyntaxError('Unexpected token in JSON at position 0')),
      });
      // Issue #748: rejects with friendly text, not "Unexpected token ...".
      await expect(apiRequest('/test')).rejects.toMatchObject({
        message: 'CUDly sent an unexpected response. Try again in a moment.',
        status: 200,
        detail: expect.stringContaining('Unexpected token in JSON'),
      });
    });

    test('surfaces a timeout error when the backend hangs past timeoutMs (issue #20)', async () => {
      // Simulate a hanging backend: fetch rejects with AbortError when
      // its AbortSignal fires. That's exactly what the browser does in
      // real life when the controller times out.
      fetchMock.mockImplementation((_url: string, init: RequestInit) => new Promise((_resolve, reject) => {
        init.signal?.addEventListener('abort', () => {
          const err = new Error('The operation was aborted.');
          err.name = 'AbortError';
          reject(err);
        });
      }));

      // Issue #748: the user sees a sentence; the raw timing stays on detail.
      await expect(apiRequest('/test', { timeoutMs: 10 })).rejects.toMatchObject({
        message: 'CUDly took too long to respond. Try again in a moment.',
        status: 0,
        detail: 'Request to /test timed out after 10ms',
      });
    });

    describe('friendly transport errors (issue #748)', () => {
      test('a network failure becomes "Can\'t reach CUDly" with status 0', async () => {
        fetchMock.mockRejectedValue(new TypeError('Failed to fetch'));
        await expect(apiRequest('/test')).rejects.toMatchObject({
          message: "Can't reach CUDly. Check your connection and try again.",
          status: 0,
          detail: 'Request to /test failed: Failed to fetch',
        });
      });

      test('a caller abort is rethrown untouched', async () => {
        const controller = new AbortController();
        fetchMock.mockImplementation(() => {
          controller.abort();
          const err = new Error('The operation was aborted.');
          err.name = 'AbortError';
          return Promise.reject(err);
        });
        await expect(apiRequest('/test', { signal: controller.signal })).rejects.toMatchObject({ name: 'AbortError' });
      });

      test.each([
        [502, 'CUDly is temporarily unavailable. Try again in a moment.'],
        [503, 'CUDly is temporarily unavailable. Try again in a moment.'],
        [504, 'CUDly took too long to respond. Try again in a moment.'],
        [500, 'CUDly hit an unexpected error. Try again in a moment.'],
        [403, "You don't have permission to do that."],
        [418, 'The request could not be completed (error 418).'],
      ])('HTTP %i with an HTML body says what it means, not "HTTP %i"', async (status, message) => {
        fetchMock.mockResolvedValue({ ok: false, status, json: () => Promise.reject(new SyntaxError("Unexpected token '<'")) });
        await expect(apiRequest('/test')).rejects.toMatchObject({ message, status, detail: `HTTP ${status} from /test` });
      });

      test('the backend catch-all "Internal server error" gets the friendly 500 text', async () => {
        fetchMock.mockResolvedValue({ ok: false, status: 500, json: () => Promise.resolve({ error: 'Internal server error' }) });
        await expect(apiRequest('/test')).rejects.toThrow('CUDly hit an unexpected error. Try again in a moment.');
      });

      test('a specific server message still wins over the status text', async () => {
        fetchMock.mockResolvedValue({ ok: false, status: 503, json: () => Promise.resolve({ error: 'maintenance until 10:00 UTC' }) });
        await expect(apiRequest('/test')).rejects.toThrow('maintenance until 10:00 UTC');
      });
    });

    test('passes the caller-provided signal through alongside the timeout controller', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({}),
      });
      const controller = new AbortController();
      await apiRequest('/test', { signal: controller.signal });
      const callArgs = fetchMock.mock.calls[0][1] as { signal?: AbortSignal };
      // The client wraps the caller signal in a combined AbortSignal
      // (via AbortSignal.any or a manual listener). We can't assert
      // referential equality, but we can assert a signal was forwarded.
      expect(callArgs.signal).toBeDefined();
    });
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

describe('Dashboard API', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({})
    });
  });

  describe('getDashboardSummary', () => {
    test('fetches summary with provider filter', async () => {
      await getDashboardSummary('aws');
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/dashboard/summary?provider=aws',
        expect.anything()
      );
    });

    test('uses all providers by default (empty provider filter)', async () => {
      await getDashboardSummary();
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/dashboard/summary?provider=',
        expect.anything()
      );
    });
  });

  describe('getUpcomingPurchases', () => {
    test('fetches upcoming purchases', async () => {
      await getUpcomingPurchases();
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/dashboard/upcoming',
        expect.anything()
      );
    });

    test('sends provider and account chips as query params (#713)', async () => {
      await getUpcomingPurchases('aws', ['a1', 'a2']);
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/dashboard/upcoming?provider=aws&account_ids=a1%2Ca2',
        expect.anything()
      );
    });

    // #1189: the endpoint returns { purchases: [...] }, not a bare array. The
    // annotation makes tsc (npm run typecheck) reject a regression to the
    // old UpcomingPurchase[] return type.
    test('resolves the { purchases } envelope the handler returns', async () => {
      const row = {
        execution_id: 'exec-1', plan_id: 'plan-1', plan_name: 'Plan', scheduled_date: '2026-11-01',
        provider: 'aws', service: 'ec2', step_number: 1, total_steps: 2, estimated_savings: 10,
        has_recommendations: true,
      };
      fetchMock.mockResolvedValueOnce({ ok: true, status: 200, headers: new Headers(), json: () => Promise.resolve({ purchases: [row] }) });
      const res: UpcomingPurchasesResponse = await getUpcomingPurchases();
      expect(Array.isArray(res)).toBe(false);
      expect(res.purchases).toEqual([row]);
    });
  });
});

describe('Recommendations API', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({})
    });
  });

  describe('getRecommendations', () => {
    test('fetches with no filters', async () => {
      await getRecommendations();
      expect(fetchMock).toHaveBeenCalledWith('/api/recommendations?limit=1000', expect.anything());
    });

    // Issue #723: the list is read in bounded pages so no single response
    // can pass the 6 MB Lambda cap.
    describe('cursor paging (issue #723)', () => {
      const page = (body: unknown) => ({ ok: true, status: 200, headers: { get: () => null }, json: () => Promise.resolve(body) });
      const urls = (): string[] => fetchMock.mock.calls.map((c) => c[0] as string);

      test('follows next_cursor, sends the filters on every page and merges the rows', async () => {
        fetchMock
          .mockResolvedValueOnce(page({ recommendations: [{ id: 'a' }, { id: 'b' }], summary: { total_count: 3 }, regions: ['us-east-1'], next_cursor: 'c1' }))
          .mockResolvedValueOnce(page({ recommendations: [{ id: 'c' }], summary: { total_count: 3 }, regions: ['us-east-1'] }));
        const result = await getRecommendations({ provider: 'aws', account_ids: ['acct-1'] }) as unknown as Record<string, unknown>;
        expect(urls()).toEqual([
          '/api/recommendations?provider=aws&account_ids=acct-1&limit=1000',
          '/api/recommendations?provider=aws&account_ids=acct-1&limit=1000&cursor=c1',
        ]);
        expect(result).toEqual({ recommendations: [{ id: 'a' }, { id: 'b' }, { id: 'c' }], summary: { total_count: 3 }, regions: ['us-east-1'] });
      });

      test('a single page (or a server that ignores limit) is one request', async () => {
        fetchMock.mockResolvedValueOnce(page({ recommendations: [{ id: 'a' }], summary: {}, regions: [] }));
        const result = await getRecommendations() as unknown as Record<string, unknown>;
        expect(fetchMock).toHaveBeenCalledTimes(1);
        expect(result).toEqual({ recommendations: [{ id: 'a' }], summary: {}, regions: [] });
      });

      test('a repeated cursor fails instead of looping', async () => {
        fetchMock
          .mockResolvedValueOnce(page({ recommendations: [{ id: 'a' }], next_cursor: 'same' }))
          .mockResolvedValueOnce(page({ recommendations: [{ id: 'b' }], next_cursor: 'same' }));
        await expect(getRecommendations()).rejects.toThrow(/cursor/i);
        expect(fetchMock).toHaveBeenCalledTimes(2);
      });

      test('stops with an error after the page cap', async () => {
        let n = 0;
        fetchMock.mockImplementation(() => Promise.resolve(page({ recommendations: [{ id: `r${n}` }], next_cursor: `c${++n}` })));
        await expect(getRecommendations()).rejects.toThrow(/pages/i);
        expect(fetchMock.mock.calls.length).toBeLessThanOrEqual(200);
      });

      test('an empty (204) page after a cursor fails instead of returning a partial list', async () => {
        fetchMock
          .mockResolvedValueOnce(page({ recommendations: [{ id: 'a' }], next_cursor: 'c1' }))
          .mockResolvedValueOnce({ ok: true, status: 204, headers: { get: () => null }, json: () => Promise.reject(new Error('no body')) });
        await expect(getRecommendations()).rejects.toThrow(/empty page/i);
      });

      test('an empty (204) first page still resolves null', async () => {
        fetchMock.mockResolvedValueOnce({ ok: true, status: 204, headers: { get: () => null }, json: () => Promise.reject(new Error('no body')) });
        await expect(getRecommendations()).resolves.toBeNull();
      });
    });

    // Regression test for issue #1089: minSavingsUsd (dollar) and minSavingsPct
    // (percentage) must map to distinct query params -- min_savings_usd and
    // min_savings_pct -- and must NEVER both map to the same legacy `min_savings`
    // key. Conflating them would silently treat a percentage as a dollar amount
    // or vice versa.
    test('minSavingsUsd maps to min_savings_usd (dollar) query param', async () => {
      await getRecommendations({
        provider: 'aws',
        service: 'ec2',
        region: 'us-east-1',
        minSavingsUsd: 100
      });

      const url = fetchMock.mock.calls[0][0] as string;
      expect(url).toContain('provider=aws');
      expect(url).toContain('service=ec2');
      expect(url).toContain('region=us-east-1');
      // Dollar filter uses the distinct usd-suffixed key.
      expect(url).toContain('min_savings_usd=100');
      // Must NOT appear as the bare (ambiguous) key or as the pct key.
      expect(url).not.toContain('min_savings=');
      expect(url).not.toContain('min_savings_pct=');
    });

    test('minSavingsPct maps to min_savings_pct (percentage) query param', async () => {
      await getRecommendations({
        minSavingsPct: 30
      });

      const url = fetchMock.mock.calls[0][0] as string;
      // Percentage filter uses the distinct pct-suffixed key.
      expect(url).toContain('min_savings_pct=30');
      // Must NOT appear as the bare (ambiguous) key or as the usd key.
      expect(url).not.toContain('min_savings=');
      expect(url).not.toContain('min_savings_usd=');
    });

    test('usd and pct filters can be combined and remain distinct', async () => {
      await getRecommendations({
        minSavingsUsd: 50,
        minSavingsPct: 20
      });

      const url = fetchMock.mock.calls[0][0] as string;
      expect(url).toContain('min_savings_usd=50');
      expect(url).toContain('min_savings_pct=20');
      // The same value "50" for usd must not appear on the pct key and vice versa.
      expect(url).not.toContain('min_savings_pct=50');
      expect(url).not.toContain('min_savings_usd=20');
      expect(url).not.toContain('min_savings=');
    });

    // Issue #65: non-finite or non-positive savings floors must never reach
    // the query string. The backend 400s on NaN/Infinity/negatives, which
    // would turn a bad widget value into an opaque load error.
    test.each([
      ['NaN', NaN],
      ['Infinity', Infinity],
      ['-Infinity', -Infinity],
      ['a negative value', -5],
      ['zero', 0],
    ])('drops min_savings_usd and min_savings_pct when the floor is %s', async (_label, value) => {
      await getRecommendations({ provider: 'aws', minSavingsUsd: value, minSavingsPct: value });

      const url = fetchMock.mock.calls[0][0] as string;
      expect(url).toBe('/api/recommendations?provider=aws&limit=1000');
      expect(url).not.toContain('min_savings_usd');
      expect(url).not.toContain('min_savings_pct');
    });

    test('a bad usd floor does not suppress a valid pct floor', async () => {
      await getRecommendations({ minSavingsUsd: Infinity, minSavingsPct: 12.5 });

      const url = fetchMock.mock.calls[0][0] as string;
      expect(url).toBe('/api/recommendations?min_savings_pct=12.5&limit=1000');
    });

    test('applies other filters to query string', async () => {
      await getRecommendations({
        provider: 'aws',
        service: 'ec2',
        region: 'us-east-1',
      });

      const url = fetchMock.mock.calls[0][0] as string;
      expect(url).toContain('provider=aws');
      expect(url).toContain('service=ec2');
      expect(url).toContain('region=us-east-1');
    });
  });

  describe('refreshRecommendations', () => {
    test('sends POST request', async () => {
      await refreshRecommendations();
      expect(fetchMock).toHaveBeenCalledWith(
        '/api/recommendations/refresh',
        expect.objectContaining({ method: 'POST' })
      );
    });
  });
});

describe('Plans API', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({})
    });
  });

  describe('getPlans', () => {
    test('fetches all plans', async () => {
      await getPlans();
      expect(fetchMock).toHaveBeenCalledWith('/api/plans', expect.anything());
    });
  });

  describe('getPlan', () => {
    test('fetches single plan by ID', async () => {
      await getPlan('plan-123');
      expect(fetchMock).toHaveBeenCalledWith('/api/plans/plan-123', expect.anything());
    });
  });

  describe('createPlan', () => {
    test('sends POST with plan data', async () => {
      const plan: CreatePlanRequest = {
        name: 'Test Plan',
        enabled: true,
        auto_purchase: false,
        notification_days_before: 3,
        services: { 'aws:ec2': { provider: 'aws', service: 'ec2', enabled: true, term: 3, payment: 'all-upfront', coverage: 80 } },
        ramp_schedule: { type: 'immediate', percent_per_step: 100, step_interval_days: 0, current_step: 0, total_steps: 1 },
        target_accounts: ['11111111-1111-1111-1111-111111111111'],
      };
      await createPlan(plan);

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/plans',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify(plan)
        })
      );
    });
  });

  describe('updatePlan', () => {
    test('sends PUT with plan data', async () => {
      const plan: CreatePlanRequest = {
        name: 'Updated Plan',
        enabled: true,
        auto_purchase: true,
        notification_days_before: 5,
        services: { 'aws:rds': { provider: 'aws', service: 'rds', enabled: true, term: 1, payment: 'no-upfront', coverage: 70 } },
        ramp_schedule: { type: 'weekly', percent_per_step: 25, step_interval_days: 7, current_step: 0, total_steps: 4 },
        target_accounts: ['22222222-2222-2222-2222-222222222222'],
      };
      await updatePlan('plan-123', plan);

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/plans/plan-123',
        expect.objectContaining({
          method: 'PUT',
          body: JSON.stringify(plan)
        })
      );
    });
  });

  describe('patchPlan', () => {
    test('sends PATCH with partial data', async () => {
      await patchPlan('plan-123', { enabled: false });

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/plans/plan-123',
        expect.objectContaining({
          method: 'PATCH',
          body: JSON.stringify({ enabled: false })
        })
      );
    });
  });

  describe('deletePlan', () => {
    test('sends DELETE request', async () => {
      await deletePlan('plan-123');

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/plans/plan-123',
        expect.objectContaining({ method: 'DELETE' })
      );
    });
  });
});

describe('History API', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({})
    });
  });

  describe('getHistory', () => {
    test('fetches with no filters', async () => {
      await getHistory();
      expect(fetchMock).toHaveBeenCalledWith('/api/history', expect.anything());
    });

    test('applies date filters', async () => {
      await getHistory({ start: '2024-01-01', end: '2024-03-31' });

      const url = fetchMock.mock.calls[0][0] as string;
      expect(url).toContain('start=2024-01-01');
      expect(url).toContain('end=2024-03-31');
    });

    test('applies provider and plan filters', async () => {
      await getHistory({ provider: 'aws', planId: 'plan-123' });

      const url = fetchMock.mock.calls[0][0] as string;
      expect(url).toContain('provider=aws');
      expect(url).toContain('plan_id=plan-123');
    });
  });
});

describe('Config API', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({})
    });
  });

  describe('getConfig', () => {
    test('fetches config', async () => {
      await getConfig();
      expect(fetchMock).toHaveBeenCalledWith('/api/config', expect.anything());
    });
  });

  describe('updateConfig', () => {
    test('sends PUT with config data', async () => {
      const config: Config = {
        enabled_providers: ['aws', 'azure'],
        notification_email: 'test@example.com',
        auto_collect: true,
        collection_schedule: 'daily',
        default_term: 3,
        default_payment: 'all-upfront',
        default_coverage: 80,
        notification_days_before: 3
      };
      await updateConfig(config);

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/config',
        expect.objectContaining({
          method: 'PUT',
          body: JSON.stringify(config)
        })
      );
    });
  });
});

describe('Purchase API', () => {
  beforeEach(() => {
    fetchMock.mockReset();
    fetchMock.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({})
    });
  });

  describe('executePurchase', () => {
    test('sends POST with recommendations', async () => {
      const recs: Recommendation[] = [{
        id: 'rec-1',
        provider: 'aws',
        service: 'ec2',
        region: 'us-east-1',
        resource_type: 'm5.large',
        count: 1,
        term: 3,
        payment: 'all-upfront',
        upfront_cost: 100,
        monthly_cost: 0,
        savings: 30,
        selected: true,
        purchased: false,
      }];
      await executePurchase(recs);

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/purchases/execute',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ recommendations: recs })
        })
      );
    });
  });

  describe('getPurchaseDetails', () => {
    test('fetches purchase by ID', async () => {
      await getPurchaseDetails('exec-123');
      expect(fetchMock).toHaveBeenCalledWith('/api/purchases/exec-123', expect.anything());
    });
  });

  describe('cancelPurchase', () => {
    test('sends POST to cancel', async () => {
      await cancelPurchase('exec-123');

      expect(fetchMock).toHaveBeenCalledWith(
        '/api/purchases/cancel/exec-123',
        expect.objectContaining({ method: 'POST' })
      );
    });
  });
});

describe('Public Info API', () => {
  describe('getPublicInfo', () => {
    test('fetches version and admin_exists without auth (#633: no sensitive fields)', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ version: '1.0.0', admin_exists: true })
      });

      const info = await getPublicInfo();
      expect(info.version).toBe('1.0.0');
      expect(info.admin_exists).toBe(true);
      // Sensitive fields must not be present on the public endpoint response.
      expect((info as unknown as Record<string, unknown>)['api_key_secret_url']).toBeUndefined();
      expect((info as unknown as Record<string, unknown>)['deployment_aws_account_id']).toBeUndefined();
      expect(fetchMock).toHaveBeenCalledWith('/api/info');
    });

    test('throws with the HTTP status on a non-OK response instead of fabricating admin_exists: false', async () => {
      fetchMock.mockResolvedValue({ ok: false, status: 502 });

      await expect(getPublicInfo()).rejects.toMatchObject({ status: 502, message: expect.stringContaining('HTTP 502') });
    });
  });

  describe('getDeploymentInfo', () => {
    test('fetches api_key_secret_url and deployment_aws_account_id with auth', async () => {
      fetchMock.mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({
          api_key_secret_url: 'https://us-east-1.console.aws.amazon.com/secretsmanager/secret?name=arn:...',
          deployment_aws_account_id: '123456789012',
        })
      });

      const info = await getDeploymentInfo();
      expect(info.api_key_secret_url).toContain('secretsmanager');
      expect(info.deployment_aws_account_id).toBe('123456789012');
      expect(fetchMock).toHaveBeenCalledWith('/api/info/deployment', expect.objectContaining({}));
    });

    test('throws with the HTTP status on a non-OK response', async () => {
      fetchMock.mockResolvedValue({ ok: false, status: 401 });

      await expect(getDeploymentInfo()).rejects.toMatchObject({ status: 401 });
    });
  });
});
