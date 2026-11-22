/**
 * Topbar user UI, profile modal, profile save and logout.
 */

import * as api from '../api';
import * as state from '../state';
import { reloadPage } from '../utils';
import { canAccess, isAdmin as permissionsIsAdmin } from '../permissions';
import { showToast } from '../toast';
import { openProfileModal } from '../auth';

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
