/**
 * MFA enrollment / lifecycle API functions (split from ./auth, issue #210)
 */

import { apiRequest, base64Encode } from './client';
import type { MFASetupResponse, MFARecoveryCodesResponse } from './types';

// ----------------------------------------------------------------
// MFA enrollment / lifecycle helpers (issue #497).
//
// Runtime-validates each response so a malicious or misconfigured
// server can't inject unexpected shapes that the UI is not prepared
// to handle (same pattern as getResetTokenStatus above).
// ----------------------------------------------------------------

function isStringArray(v: unknown): v is string[] {
  return Array.isArray(v) && v.every((x) => typeof x === 'string');
}

/**
 * Begin an MFA enrollment. Requires the user's current password as
 * defence-in-depth. Returns the freshly-generated secret + the
 * otpauth:// provisioning URI the UI renders as a QR code.
 */
export async function setupMFA(password: string): Promise<MFASetupResponse> {
  const data = await apiRequest<unknown>('/auth/mfa/setup', {
    method: 'POST',
    body: JSON.stringify({ password: base64Encode(password) }),
  });
  if (data === null || typeof data !== 'object') {
    throw new Error('setupMFA response was not an object');
  }
  const { secret, provisioning_uri } = data as { secret?: unknown; provisioning_uri?: unknown };
  if (typeof secret !== 'string' || secret === '') {
    throw new Error('setupMFA response missing secret');
  }
  if (typeof provisioning_uri !== 'string' || !provisioning_uri.startsWith('otpauth://')) {
    throw new Error('setupMFA response missing valid provisioning_uri');
  }
  return { secret, provisioning_uri };
}

/**
 * Finalize an MFA enrollment by proving the user has the secret
 * loaded in their authenticator. Returns the plaintext recovery
 * codes — the UI must surface them once and not store them.
 */
export async function enableMFA(code: string): Promise<MFARecoveryCodesResponse> {
  const data = await apiRequest<unknown>('/auth/mfa/enable', {
    method: 'POST',
    body: JSON.stringify({ code }),
  });
  if (data === null || typeof data !== 'object') {
    throw new Error('enableMFA response was not an object');
  }
  const { recovery_codes } = data as { recovery_codes?: unknown };
  if (!isStringArray(recovery_codes)) {
    throw new Error('enableMFA response missing recovery_codes');
  }
  return { recovery_codes };
}

/**
 * Turn off MFA. Requires both the current password AND a fresh
 * proof-of-possession (TOTP code or an unused recovery code).
 */
export async function disableMFA(password: string, code: string): Promise<void> {
  await apiRequest<void>('/auth/mfa/disable', {
    method: 'POST',
    body: JSON.stringify({ password: base64Encode(password), code }),
  });
}

/**
 * Replace all stored recovery codes with a fresh batch. Requires a
 * current TOTP code (NOT a recovery code — the backend rejects the
 * recovery-code path here on purpose; see service_mfa.go).
 */
export async function regenerateMFARecoveryCodes(code: string): Promise<MFARecoveryCodesResponse> {
  const data = await apiRequest<unknown>('/auth/mfa/regenerate-recovery-codes', {
    method: 'POST',
    body: JSON.stringify({ code }),
  });
  if (data === null || typeof data !== 'object') {
    throw new Error('regenerateMFARecoveryCodes response was not an object');
  }
  const { recovery_codes } = data as { recovery_codes?: unknown };
  if (!isStringArray(recovery_codes)) {
    throw new Error('regenerateMFARecoveryCodes response missing recovery_codes');
  }
  return { recovery_codes };
}
