/**
 * Shared helper for the api*.test.ts suites.
 */
import { fetchMock } from './setup';

// A successful login or setup is followed by a /auth/me check of the session cookie.
export function mockSessionStart(body: unknown, me: { status: number; body: unknown } = { status: 200, body: { id: 'u', email: 'a@b.c', groups: [], csrf_token: 'csrf-me' } }): void {
  fetchMock.mockImplementation((url: string) => Promise.resolve(url.endsWith('/auth/me')
    ? { ok: me.status < 400, status: me.status, json: () => Promise.resolve(me.body) }
    : { ok: true, status: 200, json: () => Promise.resolve(body) }));
}
