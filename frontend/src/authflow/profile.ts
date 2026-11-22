/**
 * Topbar user UI, profile modal, profile save and logout.
 */

import * as api from '../api';
import * as state from '../state';
import { reloadPage } from '../utils';
import { openModal, closeModal } from '../modal';
import { canAccess, isAdmin as permissionsIsAdmin } from '../permissions';
import { showToast } from '../toast';
import { renderMFASection, setProfileSecretPending } from './mfa';
import { describePasswordValidationError, setupPasswordToggle, updatePasswordRequirements } from './password';

/**
 * Up to two letters for the topbar avatar: the first letters of the first
 * two dot/underscore/dash separated parts of the address's local part
 * ("dana.okafor@..." -> "DO"), else its first two characters.
 */
export function emailInitials(email: string): string {
  const local = email.split('@')[0] ?? '';
  const parts = local.split(/[._\-+]+/).filter(Boolean);
  const letters = parts.length >= 2
    ? `${parts[0]!.charAt(0)}${parts[1]!.charAt(0)}`
    : local.slice(0, 2);
  return letters.toUpperCase();
}

/**
 * Update user UI after login
 */
export function updateUserUI(): void {
  const currentUser = state.getCurrentUser();
  const userEmailEl = document.getElementById('user-email-display');
  const userInfoEl = document.getElementById('user-info');
  const logoutBtn = document.getElementById('logout-btn');

  const roleEl = document.getElementById('user-role-display');

  if (currentUser) {
    // Update the user email display with click-to-edit functionality
    if (userEmailEl) {
      userEmailEl.textContent = currentUser.email;
      // Drawn as an avatar by CSS (::before), so it stays out of the text.
      userEmailEl.dataset['initials'] = emailInitials(currentUser.email);
      userEmailEl.removeAttribute('aria-label');
      userEmailEl.hidden = false;
      userEmailEl.title = 'Click to edit your profile';
      userEmailEl.classList.add('cursor-pointer');
      // Replace element to avoid duplicate listeners on repeated calls
      const freshEmailEl = userEmailEl.cloneNode(true) as HTMLElement;
      userEmailEl.parentNode?.replaceChild(freshEmailEl, userEmailEl);
      freshEmailEl.addEventListener('click', () => void openProfileModal());
    }
    // Show admin badge when the user is a member of the Administrators
    // group. PR #912 removed user.role from the API response; use the
    // group-membership-based isAdmin() predicate instead.
    if (roleEl) {
      if (permissionsIsAdmin()) {
        roleEl.textContent = 'admin';
        roleEl.classList.remove('hidden');
      } else {
        roleEl.textContent = '';
        roleEl.classList.add('hidden');
      }
    }
    // Show the user info section
    if (userInfoEl) {
      userInfoEl.classList.remove('hidden');
    }

    const adminOnly = permissionsIsAdmin();
    document.querySelectorAll<HTMLElement>('.admin-only').forEach(el => {
      el.classList.toggle('visible', adminOnly);
    });

    // Gate nav entries and page containers that require view:purchases.
    // Mirrors the admin-only pattern: the CSS class hides by default;
    // .visible makes the element visible. Direct URL navigation into a
    // gated page is handled in navigation.ts switchTab().
    const canViewPurchases = canAccess('view', 'purchases');
    document.querySelectorAll<HTMLElement>('.requires-purchases').forEach(el => {
      el.classList.toggle('visible', canViewPurchases);
    });
  } else {
    // Hide user info when not logged in
    if (userInfoEl) {
      userInfoEl.classList.add('hidden');
    }
    if (roleEl) {
      roleEl.classList.add('hidden');
    }
    if (userEmailEl) {
      userEmailEl.hidden = true;
    }
  }

  // Setup logout handler
  if (logoutBtn) {
    logoutBtn.addEventListener('click', () => void logout());
  }
}


/**
 * Logout handler
 */
export async function logout(): Promise<void> {
  try {
    await api.logout();
  } catch (error) {
    // The session is still alive server-side: stay signed in and say so.
    showToast({ message: (error as Error).message, kind: 'error' });
    return;
  }
  state.setCurrentUser(null);
  reloadPage();
}
/**
 * Open profile edit modal
 */
async function openProfileModal(): Promise<void> {
  const currentUser = state.getCurrentUser();
  if (!currentUser) return;

  // Create modal if it doesn't exist
  let modal = document.getElementById('profile-modal');
  if (!modal) {
    modal = document.createElement('div');
    modal.id = 'profile-modal';
    modal.className = 'modal hidden';
    modal.innerHTML = `
      <div class="modal-content">
        <h2>Edit Profile</h2>
        <form id="profile-form">
          <label>
            Email
            <input type="email" id="profile-email" autocomplete="email" autocapitalize="none" autocorrect="off" required>
          </label>
          <label>
            Current Password (required to save changes)
            <div class="password-input-wrapper">
              <input type="password" id="profile-current-password" placeholder="Enter current password" autocomplete="current-password">
              <button type="button" class="toggle-password" data-target="profile-current-password" aria-label="Show password">
                <svg class="eye-icon" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path>
                  <circle cx="12" cy="12" r="3"></circle>
                </svg>
              </button>
            </div>
          </label>
          <label>
            New Password (leave blank to keep current)
            <div class="password-input-wrapper">
              <input type="password" id="profile-new-password" placeholder="Enter new password" autocomplete="new-password">
              <button type="button" class="toggle-password" data-target="profile-new-password" aria-label="Show password">
                <svg class="eye-icon" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path>
                  <circle cx="12" cy="12" r="3"></circle>
                </svg>
              </button>
            </div>
          </label>

          <div id="profile-password-requirements" class="password-requirements">
            <div class="requirement" id="profile-req-length">
              <span class="req-icon">&#9675;</span>
              <span class="req-text">At least 12 characters</span>
            </div>
            <div class="requirement" id="profile-req-uppercase">
              <span class="req-icon">&#9675;</span>
              <span class="req-text">One uppercase letter (A-Z)</span>
            </div>
            <div class="requirement" id="profile-req-lowercase">
              <span class="req-icon">&#9675;</span>
              <span class="req-text">One lowercase letter (a-z)</span>
            </div>
            <div class="requirement" id="profile-req-number">
              <span class="req-icon">&#9675;</span>
              <span class="req-text">One number (0-9)</span>
            </div>
            <div class="requirement" id="profile-req-special">
              <span class="req-icon">&#9675;</span>
              <span class="req-text">One special character (!@#$%^&*)</span>
            </div>
          </div>

          <label>
            Confirm New Password
            <div class="password-input-wrapper">
              <input type="password" id="profile-confirm-password" placeholder="Confirm new password" autocomplete="new-password">
              <button type="button" class="toggle-password" data-target="profile-confirm-password" aria-label="Show password">
                <svg class="eye-icon" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path>
                  <circle cx="12" cy="12" r="3"></circle>
                </svg>
              </button>
            </div>
          </label>
          <div id="profile-password-error" class="error-message hidden"></div>
          <div class="modal-buttons">
            <button type="button" id="profile-cancel">Cancel</button>
            <button type="submit" class="primary">Save Changes</button>
          </div>
        </form>
        <hr class="profile-section-divider">
        <div id="profile-mfa-section">
          <!-- Populated by renderMFASection() on open -->
        </div>
      </div>
    `;
    document.body.appendChild(modal);

    // Add event listeners
    document.getElementById('profile-cancel')?.addEventListener('click', closeProfileModal);
    document.getElementById('profile-form')?.addEventListener('submit', (e) => void saveProfile(e));

    // Live password-strength indicator. Mirrors the reset / admin-setup
    // flows so the user sees criterion check-marks as they type instead
    // of only on submit. Uses a flow-specific prefix so the IDs never
    // collide with other modals that might be mounted on the same page.
    const newPasswordInput = document.getElementById('profile-new-password') as HTMLInputElement | null;
    if (newPasswordInput) {
      newPasswordInput.addEventListener('input', () => {
        updatePasswordRequirements(newPasswordInput.value, 'profile-req-');
      });
    }

    // Setup password toggle
    setupPasswordToggle(modal);
  }

  // Populate with current values
  (document.getElementById('profile-email') as HTMLInputElement).value = currentUser.email;
  (document.getElementById('profile-current-password') as HTMLInputElement).value = '';
  (document.getElementById('profile-new-password') as HTMLInputElement).value = '';
  (document.getElementById('profile-confirm-password') as HTMLInputElement).value = '';

  // (Re-)render the MFA section so its state reflects whatever the
  // user's mfa_enabled flag is right now (could have flipped since
  // the modal was last opened). See renderMFASection below.
  setProfileSecretPending(false);
  renderMFASection();

  // Reset live indicators and any stale error so the previous open
  // doesn't bleed into this one (modal is created once and reused).
  updatePasswordRequirements('', 'profile-req-');
  const errorDiv = document.getElementById('profile-password-error');
  if (errorDiv) {
    errorDiv.textContent = '';
    errorDiv.classList.add('hidden');
  }

  // Show modal
  openModal(modal);
}

/**
 * Close profile modal
 */
function closeProfileModal(): void {
  const modal = document.getElementById('profile-modal');
  if (modal) closeModal(modal);
}

/**
 * Save profile changes
 */
async function saveProfile(e: Event): Promise<void> {
  e.preventDefault();
  if (document.getElementById('profile-modal')?.dataset.requireAck === 'true') return;

  const email = (document.getElementById('profile-email') as HTMLInputElement).value;
  const currentPassword = (document.getElementById('profile-current-password') as HTMLInputElement).value;
  const newPassword = (document.getElementById('profile-new-password') as HTMLInputElement).value;
  const confirmPassword = (document.getElementById('profile-confirm-password') as HTMLInputElement).value;

  // Clear any stale password-validation error from a prior submit so we
  // start with a clean slate on every attempt.
  const passwordErrorDiv = document.getElementById('profile-password-error');
  if (passwordErrorDiv) {
    passwordErrorDiv.textContent = '';
    passwordErrorDiv.classList.add('hidden');
  }

  if (!currentPassword) {
    // Use showToast instead of blocking alert() (finding 11-L3).
    showToast({ message: 'Please enter your current password to save changes', kind: 'error' });
    return;
  }

  if (newPassword) {
    // Password-validation failures render inline (matches reset and
    // admin-setup flows) so the criterion text appears next to the
    // requirements indicator the user is already looking at.
    const requirementError = describePasswordValidationError(newPassword);
    if (requirementError) {
      if (passwordErrorDiv) {
        passwordErrorDiv.textContent = requirementError;
        passwordErrorDiv.classList.remove('hidden');
      }
      return;
    }
    if (newPassword !== confirmPassword) {
      if (passwordErrorDiv) {
        passwordErrorDiv.textContent = 'New passwords do not match';
        passwordErrorDiv.classList.remove('hidden');
      }
      return;
    }
  }

  try {
    // Call API to update profile
    await api.apiRequest('/auth/profile', {
      method: 'PUT',
      body: JSON.stringify({
        email,
        current_password: api.base64Encode(currentPassword),
        new_password: newPassword ? api.base64Encode(newPassword) : undefined
      })
    });

    // Update local state
    const currentUser = state.getCurrentUser();
    if (currentUser) {
      state.setCurrentUser({ ...currentUser, email });
      const userEmailEl = document.getElementById('user-email-display');
      if (userEmailEl) {
        userEmailEl.textContent = email;
        userEmailEl.dataset['initials'] = emailInitials(email);
      }
    }

    closeProfileModal();
    // Use showToast instead of blocking alert() (finding 11-L3).
    showToast({ message: 'Profile updated successfully', kind: 'success', timeout: 5_000 });
  } catch (error) {
    console.error('Failed to update profile:', error);
    const err = error as Error;
    showToast({ message: `Failed to update profile: ${err.message}`, kind: 'error' });
  }
}
