/**
 * HTTP client and authentication state for CUDly API
 */

import type { ApiError, RequestOptions } from './types';
import { reloadPage } from '../utils';
import {
  BAD_RESPONSE_MESSAGE,
  NETWORK_ERROR_MESSAGE,
  TIMEOUT_ERROR_MESSAGE,
  httpStatusMessage,
  isGenericServerMessage,
  isNetworkFailure,
  makeApiError,
} from './errors';

const API_BASE = '/api';

// State for authentication
// SECURITY: Tokens are stored in localStorage scoped to the dashboard origin
// (issue #462). localStorage was chosen over sessionStorage so a second tab
// opened on the same origin inherits the active session; admins need to
// compare Settings vs. Opportunities side by side without re-authenticating.
// Tradeoff: the XSS exposure window widens from "tab close" to "explicit
// logout". Mitigations:
//   - cross-tab logout via the `storage` event (signing out in one tab
//     clears state and reloads the others within ~1s).
//   - same-origin only; no third-party iframes have access.
//   - HttpOnly session cookies remain the cleaner long-term option (see
//     issue #462 discussion); that is a larger refactor and is tracked
//     separately.
let authToken = '';
let apiKey = '';
let csrfToken = '';

// Keys we mirror to/from localStorage. Listed once so the storage-event
// listener and the migration block stay in sync.
const STORAGE_KEYS = ['authToken', 'apiKey', 'csrfToken'] as const;

// Cross-tab sign-out: logout() in one tab tells the others to drop state.
const LOGOUT_CHANNEL = 'cudly-auth';
// Non-secret marker written when BroadcastChannel is unavailable; the
// `storage` event it fires is the only signal, its value is never read.
const LOGOUT_SIGNAL_KEY = 'cudly:logout-signal';

let authenticated = false;
let logoutListenerInstalled = false;
let logoutChannel: BroadcastChannel | undefined;

/**
 * Calculate SHA256 hash of a string using Web Crypto API
 * Required for CloudFront OAC with Lambda Function URL for POST/PUT/PATCH requests
 */
async function sha256(message: string): Promise<string> {
  const msgBuffer = new TextEncoder().encode(message);
  const hashBuffer = await crypto.subtle.digest('SHA-256', msgBuffer);
  const hashArray = Array.from(new Uint8Array(hashBuffer));
  return hashArray.map(b => b.toString(16).padStart(2, '0')).join('');
}

/**
 * Add x-amz-content-sha256 header for requests with body (required for CloudFront OAC)
 */
export async function addContentHashHeader(headers: Record<string, string>, body: string): Promise<void> {
  // crypto.subtle is only available in secure contexts (HTTPS).
  // Skip on plain HTTP (e.g. ALB without TLS) — the header is only required for CloudFront OAC.
  if (!crypto?.subtle) return;
  headers['x-amz-content-sha256'] = await sha256(body);
}

/**
 * Initialize authentication from localStorage.
 *
 * Issue #462: tokens were previously kept in sessionStorage so a fresh
 * tab on the same origin would force a re-login. We moved them to
 * localStorage to support multi-tab workflows. See the SECURITY comment
 * at the top of the module for the tradeoff and mitigations.
 *
 * Migration: any token still living in sessionStorage from the previous
 * release is copied into localStorage and then removed, so users stay
 * signed in across the upgrade.
 */
export function initAuth(): void {
  // Migrate sessionStorage → localStorage for users upgrading from the
  // pre-#462 build. Removes the sessionStorage entry afterwards.
  for (const key of STORAGE_KEYS) {
    const fromSession = sessionStorage.getItem(key);
    if (fromSession && !localStorage.getItem(key)) {
      localStorage.setItem(key, fromSession);
    }
    if (fromSession) {
      sessionStorage.removeItem(key);
    }
  }

  authToken = localStorage.getItem('authToken') || '';
  apiKey = localStorage.getItem('apiKey') || '';
  csrfToken = localStorage.getItem('csrfToken') || '';

  installLogoutListener();
}

/**
 * Sync sign-out across tabs (issues #462, #34).
 */
function handleRemoteLogout(): void {
  // A signed-out tab has nothing to drop and must not reload in a loop.
  if (!authenticated && !authToken && !apiKey && !csrfToken) return;
  clearAuth();
  reloadPage();
}

function installLogoutListener(): void {
  if (logoutListenerInstalled) return;
  if (typeof BroadcastChannel === 'function') {
    logoutChannel = new BroadcastChannel(LOGOUT_CHANNEL);
    logoutChannel.onmessage = (e: MessageEvent) => {
      if (e.data === 'logout') handleRemoteLogout();
    };
  } else if (typeof window !== 'undefined' && window.addEventListener) {
    window.addEventListener('storage', (e: StorageEvent) => {
      if (e.key === LOGOUT_SIGNAL_KEY) handleRemoteLogout();
    });
  }
  logoutListenerInstalled = true;
}

/** Tell every other tab on this origin that the session ended. */
export function broadcastLogout(): void {
  if (logoutChannel) {
    logoutChannel.postMessage('logout');
    return;
  }
  if (typeof BroadcastChannel === 'function') {
    const channel = new BroadcastChannel(LOGOUT_CHANNEL);
    channel.postMessage('logout');
    channel.close();
    return;
  }
  try {
    localStorage.setItem(LOGOUT_SIGNAL_KEY, String(Date.now()));
  } catch {
    // No cross-tab channel at all: other tabs find out on their next 401.
  }
}

/**
 * Set authentication token
 */
export function setAuthToken(token: string): void {
  authToken = token;
  if (token) {
    localStorage.setItem('authToken', token);
  } else {
    localStorage.removeItem('authToken');
  }
}

/**
 * Set CSRF token
 */
export function setCsrfToken(token: string): void {
  csrfToken = token;
  if (token) {
    localStorage.setItem('csrfToken', token);
  } else {
    localStorage.removeItem('csrfToken');
  }
}

/**
 * Set API key
 */
export function setApiKey(key: string): void {
  apiKey = key;
  if (key) {
    localStorage.setItem('apiKey', key);
  } else {
    localStorage.removeItem('apiKey');
  }
}

/**
 * Check if user is authenticated
 */
export function isAuthenticated(): boolean {
  return authenticated || !!(authToken || apiKey);
}

/**
 * Record that the server accepted the session cookie.
 */
export function setAuthenticated(value: boolean): void {
  authenticated = value;
}

/**
 * Clear all authentication state in this tab.
 */
export function clearAuth(): void {
  authenticated = false;
  authToken = '';
  apiKey = '';
  csrfToken = '';
  for (const key of STORAGE_KEYS) {
    localStorage.removeItem(key);
    // Also clear any leftover sessionStorage entries from older builds.
    sessionStorage.removeItem(key);
  }
}

/**
 * Get auth headers for API requests
 * Note: Uses X-Authorization instead of Authorization because CloudFront OAC
 * signs requests with SigV4, which overwrites the Authorization header.
 */
export function getAuthHeaders(): Record<string, string> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  if (authToken) {
    headers['X-Authorization'] = `Bearer ${authToken}`;
  } else if (apiKey) {
    headers['X-API-Key'] = apiKey;
  }
  // Include CSRF token for state-changing requests (added by caller as needed)
  if (csrfToken) {
    headers['X-CSRF-Token'] = csrfToken;
  }
  return headers;
}

/**
 * Base64 encode a string (for password encoding).
 *
 * `btoa` only accepts code points up to U+00FF and throws a raw
 * InvalidCharacterError otherwise, so a passphrase with an emoji, a CJK
 * character or a curly quote could never be submitted. Strings that fit in
 * Latin-1 keep the exact bytes `btoa` has always sent, so existing passwords
 * still hash the same on the server; anything wider is sent as UTF-8, which
 * is how Go's `string(decoded)` reads it.
 */
export function base64Encode(str: string): string {
  // eslint-disable-next-line no-control-regex
  if (/^[\u0000-\u00ff]*$/.test(str)) {
    return btoa(str);
  }
  let binary = '';
  for (const byte of new TextEncoder().encode(str)) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary);
}

/**
 * Default request timeout in milliseconds. Generous enough for legit
 * long-running endpoints (AWS Cost Explorer / RI utilization can take
 * 20–40s during cold starts), tight enough that a hung backend
 * surfaces as an error in the UI instead of spinning forever (issue
 * #20). Callers can override via `options.timeoutMs` or by passing
 * their own `signal`.
 */
const DEFAULT_API_TIMEOUT_MS = 90_000;

/**
 * Make an authenticated API request
 */
export async function apiRequest<T>(endpoint: string, options: RequestOptions = {}): Promise<T> {
  const url = `${API_BASE}${endpoint}`;
  const headers = { ...getAuthHeaders(), ...options.headers };

  // Add content hash for CloudFront OAC (required for all POST/PUT/PATCH/DELETE requests)
  const method = options.method?.toUpperCase();
  if (method === 'POST' || method === 'PUT' || method === 'PATCH' || method === 'DELETE') {
    const body = typeof options.body === 'string' ? options.body : '';
    await addContentHashHeader(headers, body);
  }

  // Layer a timeout-driven AbortController on top of any caller-
  // provided signal so both paths can cancel the request. If the
  // caller passes their own signal we still add a timeout (unless
  // they opt out by passing timeoutMs: 0) — the UI doesn't want an
  // indefinite hang regardless of who owns cancellation.
  const timeoutMs = options.timeoutMs ?? DEFAULT_API_TIMEOUT_MS;
  const timeoutController = new AbortController();
  const timeoutHandle = timeoutMs > 0
    ? setTimeout(() => timeoutController.abort(), timeoutMs)
    : undefined;
  const signal = options.signal
    ? anySignal([options.signal, timeoutController.signal])
    : timeoutController.signal;

  let response: Response;
  try {
    response = await fetch(url, {
      ...options,
      headers,
      credentials: 'same-origin',
      signal,
    });
  } catch (err) {
    // Transport failures carry status 0 and a sentence the user can act
    // on; the raw text stays on `detail` for support (issue #748). A
    // caller's own abort is rethrown untouched so it can tell it apart.
    if (timeoutController.signal.aborted && !options.signal?.aborted) {
      throw makeApiError(TIMEOUT_ERROR_MESSAGE, 0, `Request to ${endpoint} timed out after ${timeoutMs}ms`);
    }
    if (isNetworkFailure(err)) {
      throw makeApiError(NETWORK_ERROR_MESSAGE, 0, `Request to ${endpoint} failed: ${(err as Error).message}`);
    }
    throw err;
  } finally {
    if (timeoutHandle !== undefined) clearTimeout(timeoutHandle);
  }

  if (!response.ok) {
    // Without a server message, say what the status means instead of
    // showing a bare "HTTP 502" (issue #748). A server message wins.
    const error: ApiError = makeApiError(
      httpStatusMessage(response.status), response.status, `HTTP ${response.status} from ${endpoint}`,
    );
    try {
      const data = await response.json() as Record<string, unknown>;
      const message = typeof data['error'] === 'string' ? data['error'] : '';
      if (message && !isGenericServerMessage(message)) error.message = message;
      // Strip the `error` key and surface the rest as structured
      // details so callers can branch on ops_hint / retry_attempt_n /
      // threshold / retry_execution_id without substring-matching the
      // human message (see PurchaseHistory Retry button — issue #47).
      const details: Record<string, unknown> = {};
      for (const k of Object.keys(data)) {
        if (k === 'error') continue;
        details[k] = data[k];
      }
      if (Object.keys(details).length > 0) {
        error.details = details;
      }
    } catch {
      // Ignore JSON parse errors
    }
    throw error;
  }

  // A 204 or an explicitly empty body has nothing to parse. Any other
  // body that fails to parse (truncated, proxy error page) must reject:
  // resolving null would read as "no data" instead of a failure.
  if (response.status === 204 || response.headers?.get('content-length') === '0') {
    return null as T;
  }
  try {
    return await response.json() as T;
  } catch (parseErr) {
    // A proxy or CDN error page served with a 2xx would otherwise reach
    // the user as "Unexpected token '<' ..." (issue #748).
    throw makeApiError(BAD_RESPONSE_MESSAGE, response.status,
      `Unparseable response from ${endpoint}: ${(parseErr as Error).message}`);
  }
}

/**
 * Combine multiple AbortSignals into one. AbortSignal.any is available
 * in modern browsers (Chrome 116+, Firefox 124+, Safari 17.4+); fall
 * back to a manual listener for older targets.
 */
function anySignal(signals: AbortSignal[]): AbortSignal {
  const AnyFn = (AbortSignal as unknown as {
    any?: (s: AbortSignal[]) => AbortSignal;
  }).any;
  if (typeof AnyFn === 'function') {
    return AnyFn.call(AbortSignal, signals);
  }
  const controller = new AbortController();
  for (const s of signals) {
    if (s.aborted) {
      controller.abort(s.reason);
      return controller.signal;
    }
    s.addEventListener('abort', () => controller.abort(s.reason), { once: true });
  }
  return controller.signal;
}

/**
 * Get the API base URL
 */
export function getApiBase(): string {
  return API_BASE;
}
