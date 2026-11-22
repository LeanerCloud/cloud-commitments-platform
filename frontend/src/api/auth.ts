/**
 * Authentication API functions
 */

import { apiRequest, getAuthHeaders, setAuthenticated, setCsrfToken, clearAuth, broadcastLogout, addContentHashHeader, base64Encode, getApiBase } from './client';
import type {
  ApiError,
  LoginResponse,
  User,
  PermissionEntry,
  UserPermissionsResponse,
  PublicInfo,
  DeploymentInfo,
  MFALoginErrorCode,
} from './types';
import { fetchOrNetworkError, readJson, responseError, responseErrorWithBody } from './errors';
import { clearPlanDraft } from '../lib/plan-form-draft';

export { setupMFA, enableMFA, disableMFA, regenerateMFARecoveryCodes } from './auth-mfa';

/**
 * Login error raised when the server returns one of the MFA-related
 * machine codes (`mfa_required` / `invalid_mfa_code`). Carrying the
 * code as a typed property lets the caller branch without
 * substring-matching the human message. See issue #497.
 */
export class MFALoginError extends Error {
  readonly code: MFALoginErrorCode;
  /** Short-lived continuation token the server issues with mfa_required; lets the code step skip the password. */
  readonly mfaToken: string | undefined;
  constructor(code: MFALoginErrorCode, mfaToken?: string) {
    super(code);
    this.name = 'MFALoginError';
    this.code = code;
    this.mfaToken = mfaToken;
    // Restore prototype chain for instanceof checks across the
    // ts-jest compile boundary (Error subclasses lose this without
    // an explicit setPrototypeOf).
    Object.setPrototypeOf(this, MFALoginError.prototype);
  }
}

function isMFALoginErrorCode(s: unknown): s is MFALoginErrorCode {
  return s === 'mfa_required' || s === 'invalid_mfa_code';
}

/**
 * Login with email and password, optionally including a 6-digit TOTP
 * code or recovery code when the server is requiring MFA. The caller
 * is expected to detect `MFALoginError` and re-submit with `mfaCode`
 * set rather than treating MFA as a hard failure (issue #497).
 *
 * On success: the server has set the httpOnly session cookie; the client
 * keeps only the CSRF token (memory). The body's `token` is for CLI/MCP
 * callers and is ignored here. On failure:
 * throws `MFALoginError` for the two MFA sentinels, plain `Error`
 * for everything else.
 */
export async function login(email: string, password: string, mfaCode?: string): Promise<LoginResponse> {
  // Base64 encode password to match backend expectation
  const payload: Record<string, string> = { email, password: base64Encode(password) };
  if (mfaCode) {
    payload.mfa_code = mfaCode;
  }
  return postLogin(payload);
}

/**
 * Second login leg using the `mfa_token` from the `mfa_required` answer, so
 * the browser no longer keeps the password while the code prompt is open.
 */
export async function loginWithMFAToken(email: string, mfaToken: string, mfaCode: string): Promise<LoginResponse> {
  return postLogin({ email, mfa_token: mfaToken, mfa_code: mfaCode });
}

function acceptSession(csrf: string): void {
  setAuthenticated(true);
  setCsrfToken(csrf);
}

const COOKIE_REJECTED_MESSAGE =
  'Signed in, but the browser did not keep the session cookie. Serve CUDly over HTTPS and make sure cookies are enabled.';
// Must match the backend's 403 body in internal/api/handler.go.
const CSRF_FAILED_MESSAGE = 'CSRF validation failed';
const LOGOUT_FAILED_MESSAGE = 'Sign out failed, please try again.';

/**
 * Right after a login the cookie must come back on /auth/me. A browser that
 * drops it (plain HTTP, no Secure cookies) would otherwise reload into a
 * silent login loop, so say why instead.
 */
async function verifySessionCookie(csrf: string | undefined): Promise<void> {
  setAuthenticated(true);
  if (csrf) setCsrfToken(csrf);
  try {
    await getCurrentUser();
  } catch (err) {
    clearAuth();
    if ((err as ApiError).status === 401) throw new Error(COOKIE_REJECTED_MESSAGE);
    throw err;
  }
}

async function postLogin(payload: Record<string, string>): Promise<LoginResponse> {
  const API_BASE = getApiBase();
  const body = JSON.stringify(payload);
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };

  // Add content hash for CloudFront OAC
  await addContentHashHeader(headers, body);

  const response = await fetchOrNetworkError(`${API_BASE}/auth/login`, {
    method: 'POST',
    headers,
    credentials: 'same-origin',
    body
  });

  if (!response.ok) {
    const { error: err, body: errBody } = await responseErrorWithBody(response, 'Login failed');
    if (isMFALoginErrorCode(err.message)) {
      const token = errBody['mfa_token'];
      throw new MFALoginError(err.message, typeof token === 'string' && token !== '' ? token : undefined);
    }
    throw err;
  }

  const data = await readJson<LoginResponse>(response);
  await verifySessionCookie(data.csrf_token);
  return data;
}

async function postLogout(): Promise<Response> {
  const headers = getAuthHeaders();
  // Add empty content hash for POST without body
  await addContentHashHeader(headers, '');
  return fetch(`${getApiBase()}/auth/logout`, {
    method: 'POST',
    headers,
    credentials: 'same-origin'
  });
}

async function isCsrfRejection(response: Response): Promise<boolean> {
  if (response.status !== 403) return false;
  try {
    const body = await response.json() as { error?: unknown };
    return body.error === CSRF_FAILED_MESSAGE;
  } catch {
    return false;
  }
}

/**
 * Logout the current user. A CSRF 403 means another tab signed in again and
 * this tab's in-memory token went stale: /auth/me re-derives it and the
 * request is retried once (the server rejects before acting, so it is safe).
 */
export async function logout(): Promise<void> {
  let response: Response;
  try {
    response = await postLogout();
    if (await isCsrfRejection(response)) {
      try {
        await getCurrentUser();
      } catch (err) {
        if ((err as ApiError).status === 401) { finishLogout(); return; }
        throw err;
      }
      response = await postLogout();
    }
  } catch (e) {
    console.warn('Server logout failed:', e);
    throw new Error(LOGOUT_FAILED_MESSAGE);
  }
  // The server clears the httpOnly cookie only on success, so any other
  // answer leaves the session alive; 401 means it was already gone.
  if (!response.ok && response.status !== 401) {
    console.warn('Server logout failed with HTTP', response.status);
    throw new Error(LOGOUT_FAILED_MESSAGE);
  }
  finishLogout();
}

function finishLogout(): void {
  clearAuth();
  clearPlanDraft();
  broadcastLogout();
}

/**
 * Get current user info
 */
export async function getCurrentUser(): Promise<User> {
  const { csrf_token: csrf, ...user } = await apiRequest<User & { csrf_token?: string }>('/auth/me');
  // A reloaded page holds no CSRF token; /auth/me re-derives it from the cookie.
  if (!csrf) throw new Error('The server returned no CSRF token for this session.');
  acceptSession(csrf);
  return user;
}

/**
 * Fetch the effective permission set for the current user from
 * GET /api/auth/me/permissions (issue #917). Returns the full
 * UserPermissionsResponse including is_admin.
 *
 * Called on login/bootstrap and the result is merged onto the
 * current-user state so canAccess() can consult it.
 *
 * Runtime-validates the response shape (same pattern as setupMFA /
 * getResetTokenStatus) so a malicious or misconfigured server can't
 * inject a non-array `permissions` value that would later crash
 * canAccess()'s `for (const p of user.effectivePermissions)` loop. On
 * any shape mismatch this throws so the caller (app.ts) falls back to
 * the safe group-membership gating instead of rendering with broken
 * permission state.
 */
export async function getUserPermissions(): Promise<UserPermissionsResponse> {
  const data = await apiRequest<unknown>('/auth/me/permissions');
  if (data === null || typeof data !== 'object') {
    throw new Error(`getUserPermissions response was not an object: ${JSON.stringify(data)}`);
  }
  const { permissions, is_admin } = data as { permissions?: unknown; is_admin?: unknown };
  if (!Array.isArray(permissions)) {
    throw new Error(`getUserPermissions response.permissions is not an array: ${JSON.stringify(permissions)}`);
  }
  const validated: PermissionEntry[] = permissions.map((entry, i) => {
    if (entry === null || typeof entry !== 'object') {
      throw new Error(`getUserPermissions response.permissions[${i}] is not an object: ${JSON.stringify(entry)}`);
    }
    const { action, resource } = entry as { action?: unknown; resource?: unknown };
    if (typeof action !== 'string') {
      throw new Error(`getUserPermissions response.permissions[${i}].action is not a string: ${JSON.stringify(action)}`);
    }
    if (typeof resource !== 'string') {
      throw new Error(`getUserPermissions response.permissions[${i}].resource is not a string: ${JSON.stringify(resource)}`);
    }
    return { action, resource };
  });
  if (typeof is_admin !== 'boolean') {
    throw new Error(`getUserPermissions response.is_admin is not a boolean: ${JSON.stringify(is_admin)}`);
  }
  return { permissions: validated, is_admin };
}

/**
 * Request password reset
 */
export async function requestPasswordReset(email: string): Promise<void> {
  const API_BASE = getApiBase();
  const body = JSON.stringify({ email });
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  await addContentHashHeader(headers, body);

  const response = await fetchOrNetworkError(`${API_BASE}/auth/forgot-password`, {
    method: 'POST',
    headers,
    credentials: 'same-origin',
    body
  });
  if (!response.ok) {
    throw await responseError(response, 'Failed to send password reset email');
  }
}

/**
 * Reset password with token
 */
export async function resetPassword(token: string, newPassword: string): Promise<void> {
  const API_BASE = getApiBase();
  const body = JSON.stringify({ token, new_password: base64Encode(newPassword) });
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  await addContentHashHeader(headers, body);

  const response = await fetchOrNetworkError(`${API_BASE}/auth/reset-password`, {
    method: 'POST',
    headers,
    credentials: 'same-origin',
    body
  });

  if (!response.ok) {
    // Backend's NewClientError emits {error: "<msg>"} (see internal/api/
    // handler.go writeError). We previously only read `message`, so the
    // specific reason (e.g. "this is your current password, choose a
    // different one") never reached the toast and users saw the opaque
    // fallback. Read `error` first; keep `message` as a defensive
    // fallback for any other shape. See issue #459.
    throw await responseError(response, 'Failed to reset password');
  }
}

/**
 * ResetTokenStatus describes the runtime state of a reset token. The
 * frontend calls getResetTokenStatus() before rendering the reset-
 * password form so it can show an "expired" or "already used" view
 * instead of a form that can never submit (issues #460, #461).
 *
 * state: "valid" | "expired" | "used"  (the server collapses "never
 *        existed" into "used" since the row is wiped on consumption)
 * flow:  "reset" | "invite"             (drives "Reset" vs "Set" copy)
 */
export interface ResetTokenStatus {
  state: 'valid' | 'expired' | 'used';
  flow: 'reset' | 'invite';
}

// Whitelists for runtime validation of the token-status response. Kept
// in sync with the ResetTokenStatus union above — if the union grows,
// these arrays must grow with it (the satisfies clause makes that a
// type-error rather than a silent drift).
const VALID_RESET_TOKEN_STATES = ['valid', 'expired', 'used'] as const satisfies readonly ResetTokenStatus['state'][];
const VALID_RESET_TOKEN_FLOWS  = ['reset', 'invite']           as const satisfies readonly ResetTokenStatus['flow'][];
const VALID_RESET_TOKEN_STATE_SET: ReadonlySet<ResetTokenStatus['state']> = new Set(VALID_RESET_TOKEN_STATES);
const VALID_RESET_TOKEN_FLOW_SET:  ReadonlySet<ResetTokenStatus['flow']>  = new Set(VALID_RESET_TOKEN_FLOWS);

/**
 * Probe a reset token's state before rendering the form. On any
 * network or non-OK response, throws so the caller can fall back to
 * rendering the form (a safer default than hiding the form on a
 * transient failure).
 *
 * The server response is validated at runtime: a malicious or
 * misconfigured server cannot inject arbitrary state/flow values
 * that downstream code (modal-routing, copy selection) is not
 * prepared to handle. Any deviation throws a descriptive error.
 */
export async function getResetTokenStatus(token: string): Promise<ResetTokenStatus> {
  const API_BASE = getApiBase();
  const url = `${API_BASE}/auth/reset-password/status?token=${encodeURIComponent(token)}`;
  const response = await fetch(url, { method: 'GET' });
  if (!response.ok) {
    throw new Error(`reset-password status check failed: ${response.status}`);
  }
  const data = await response.json() as unknown;
  if (data === null || typeof data !== 'object') {
    throw new Error(`reset-password status response was not an object: ${JSON.stringify(data)}`);
  }
  const { state, flow } = data as { state?: unknown; flow?: unknown };
  if (typeof state !== 'string' || !VALID_RESET_TOKEN_STATE_SET.has(state as ResetTokenStatus['state'])) {
    throw new Error(`reset-password status response has invalid state: ${JSON.stringify(state)}`);
  }
  if (typeof flow !== 'string' || !VALID_RESET_TOKEN_FLOW_SET.has(flow as ResetTokenStatus['flow'])) {
    throw new Error(`reset-password status response has invalid flow: ${JSON.stringify(flow)}`);
  }
  return { state: state as ResetTokenStatus['state'], flow: flow as ResetTokenStatus['flow'] };
}

/**
 * Check if admin exists
 */
export async function checkAdminExists(key: string): Promise<boolean> {
  const API_BASE = getApiBase();
  const response = await fetch(`${API_BASE}/auth/check-admin`, {
    headers: { 'X-API-Key': key }
  });
  if (response.ok) {
    const data = await response.json() as { admin_exists: boolean };
    return data.admin_exists;
  }
  return false;
}

/**
 * Setup admin account
 */
export async function setupAdmin(key: string, email: string, password: string): Promise<LoginResponse> {
  const API_BASE = getApiBase();
  // Base64 encode password to match backend expectation
  const body = JSON.stringify({ email, password: base64Encode(password) });
  const headers: Record<string, string> = { 'X-API-Key': key, 'Content-Type': 'application/json' };
  await addContentHashHeader(headers, body);

  const response = await fetchOrNetworkError(`${API_BASE}/auth/setup-admin`, {
    method: 'POST',
    headers,
    credentials: 'same-origin',
    body
  });

  // A proxy 502 used to reach the user as "Unexpected token '<'" here
  // because the error body was parsed with no catch (issue #748).
  if (!response.ok) {
    throw await responseError(response, 'Failed to create admin');
  }

  const data = await readJson<LoginResponse>(response);
  await verifySessionCookie(data.csrf_token);
  return data;
}

/**
 * Change password
 */
export async function changePassword(currentPassword: string, newPassword: string): Promise<void> {
  // Base64 encode passwords to match backend expectation
  return apiRequest<void>('/auth/change-password', {
    method: 'POST',
    body: JSON.stringify({
      current_password: base64Encode(currentPassword),
      new_password: base64Encode(newPassword)
    })
  });
}

/**
 * Get public info (no auth required)
 */
export async function getPublicInfo(): Promise<PublicInfo> {
  const API_BASE = getApiBase();
  const response = await fetch(`${API_BASE}/info`);
  if (!response.ok) {
    // Throw rather than fabricating `admin_exists: false`: a 502/503 must
    // not route every visitor to the first-run admin-setup screen.
    throw httpError('GET /api/info', response.status);
  }
  return response.json() as Promise<PublicInfo>;
}

/**
 * Get deployment info (AuthUser required). Returns sensitive identifiers
 * (API key secret URL, deployment AWS account ID) that must not be exposed
 * to unauthenticated callers (#633).
 */
export async function getDeploymentInfo(): Promise<DeploymentInfo> {
  const API_BASE = getApiBase();
  const response = await fetch(`${API_BASE}/info/deployment`, {
    headers: getAuthHeaders(),
    credentials: 'same-origin',
  });
  if (!response.ok) {
    throw httpError('GET /api/info/deployment', response.status);
  }
  return response.json() as Promise<DeploymentInfo>;
}

function httpError(what: string, status: number): ApiError {
  const error: ApiError = new Error(`${what} failed with HTTP ${status}`);
  error.status = status;
  return error;
}
