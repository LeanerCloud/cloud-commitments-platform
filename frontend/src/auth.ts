/**
 * Authentication module for CUDly
 */

import { isAdmin as permissionsIsAdmin } from './permissions';

/**
 * Check if current user is admin. Re-exported from `permissions.ts`
 * so callers that already import from `auth` keep compiling while the
 * canonical definition lives alongside `canAccess` (issue #365).
 */
export function isAdmin(): boolean {
  return permissionsIsAdmin();
}

export { describePasswordValidationError } from './authflow/password';
export { emailInitials, logout, updateUserUI } from './authflow/profile';
export { MFA_PENDING_TTL_MS, showLoginModal } from './authflow/login';
export { showResetPasswordModal } from './authflow/reset';
export { showAdminSetupModal } from './authflow/adminSetup';
