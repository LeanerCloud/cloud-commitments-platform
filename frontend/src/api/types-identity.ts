// User types

/**
 * PermissionEntry matches the JSON shape returned by GET /api/auth/me/permissions.
 * Both action and resource are string to stay forward-compatible with new
 * constants added to internal/auth/types.go without a frontend change.
 */
export interface PermissionEntry {
  action: string;
  resource: string;
}

/**
 * UserPermissionsResponse is the shape of GET /api/auth/me/permissions.
 */
export interface UserPermissionsResponse {
  permissions: PermissionEntry[];
  is_admin: boolean;
}

export interface User {
  id: string;
  email: string;
  // groups holds the UUIDs of the groups this user belongs to.
  // Authorization is now derived entirely from group membership
  // (PR #912 drops the role column). Admin status = member of
  // Administrators group (00000000-0000-5000-8000-000000000001).
  groups: string[];
  // Whether two-factor authentication is enabled on this user
  // account. Optional for backward compatibility -- older login
  // responses may not include it. The profile/MFA section in
  // auth.ts treats `mfa_enabled === true` (strict) as "enabled".
  mfa_enabled?: boolean;
  // Effective permissions fetched from GET /api/auth/me/permissions
  // on login/bootstrap (issue #917). Undefined until the fetch
  // completes; canAccess() falls back to group-membership-only
  // gating (admins pass, others blocked) while this is loading.
  effectivePermissions?: PermissionEntry[];
}

export interface LoginResponse {
  // Kept for CLI/MCP clients; the browser session rides the httpOnly cookie.
  token: string;
  csrf_token?: string;
  user?: User;
}

// MFA enrollment / lifecycle response shapes (issue #497). All four
// endpoints live under /api/auth/mfa/ and require an authenticated
// session.

/**
 * Response from POST /api/auth/mfa/setup. The secret is also the
 * payload inside the otpauth:// provisioning URI; we expose both
 * separately so the UI can show the QR code AND a manual-entry
 * fallback (some authenticator apps don't have a camera).
 */
export interface MFASetupResponse {
  secret: string;
  provisioning_uri: string;
}

/**
 * Response from POST /api/auth/mfa/enable and POST
 * /api/auth/mfa/regenerate-recovery-codes. Plaintext codes are
 * returned exactly once; the backend stores only bcrypt hashes.
 */
export interface MFARecoveryCodesResponse {
  recovery_codes: string[];
}

/**
 * Discriminator returned by the login endpoint when MFA is required
 * or when the supplied MFA code didn't match. The backend encodes
 * these as the `error` field on the 401 response body so the
 * frontend can branch on a machine-readable code rather than
 * substring-matching a human message. See issue #497.
 */
export type MFALoginErrorCode = 'mfa_required' | 'invalid_mfa_code';

// User Management Types
export interface APIUser {
  id: string;
  email: string;
  // role is intentionally absent: authorization is now purely
  // group-membership based (PR #912). The groups array is the
  // single source of truth for what a user can do.
  groups: string[];
  mfa_enabled: boolean;
  created_at?: string;
  updated_at?: string;
  last_login?: string;
}

export interface CreateUserRequest {
  email: string;
  password: string;
  // groups is required (>= 1) by the backend (PR #912). Sending an
  // empty array is rejected with 400.
  groups: string[];
}

// CreateUserResponse extends APIUser with optional invite-delivery
// status fields that POST /api/users returns when the request created
// an invited (passwordless) user. invite_email_sent is undefined for
// password-up-front creates, true when the invite email was handed to
// the configured sender, and false when the user row exists but the
// recipient has not been told how to activate it (admin should re-mail
// the setup link via Forgot Password).
export interface CreateUserResponse extends APIUser {
  invite_email_sent?: boolean;
  invite_email_error?: string;
}

export interface UpdateUserRequest {
  email?: string;
  // role is removed; update group membership to change authorization.
  groups?: string[];
}

// Group Management Types
export interface APIGroup {
  id: string;
  name: string;
  description: string;
  permissions: Permission[];
  allowed_accounts?: string[];
  // system_managed groups are seeded by migrations; they cannot be
  // renamed or deleted via the UI (only membership can change).
  system_managed?: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface Permission {
  action: string;
  resource: string;
  constraints?: {
    accounts?: string[];
    providers?: string[];
    services?: string[];
    regions?: string[];
    max_amount?: number;
  };
}

export interface CreateGroupRequest {
  name: string;
  description?: string;
  permissions: Permission[];
  allowed_accounts?: string[];
}

export interface UpdateGroupRequest {
  name?: string;
  description?: string;
  permissions?: Permission[];
  allowed_accounts?: string[];
}
