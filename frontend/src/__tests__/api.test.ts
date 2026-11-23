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
  apiRequest
} from '../api';

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

});
