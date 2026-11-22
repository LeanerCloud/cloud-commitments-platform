import * as api from '../api';
import * as state from '../state';
import type { User } from '../api';
import { showLoginModal, showAdminSetupModal, showResetPasswordModal, updateUserUI } from '../auth';
import { switchTab, setActiveNav, applyTabFromPath, initRouter, canonicalTabPath } from '../navigation';
import { restorePlanDraft } from '../plans';
import { handlePurchaseDeeplink } from '../purchases-deeplink';
import { handleRIExchangeDeeplink } from '../riexchange-deeplink';
import { handleArcheraDeeplink } from '../archera';
import { renderInitError } from '../init-error';
import { setupEventListeners } from './events';

function describePublicInfoFailure(err: unknown): string {
  const status = (err as { status?: unknown } | null)?.status;
  const detail = typeof status === 'number' && status > 0 ? ` (HTTP ${status})` : '';
  return `Could not reach the CUDly API${detail}. Sign-in may fail until it is available again.`;
}

/**
 * Initialize app
 */
export async function init(): Promise<void> {
  try {
    await initApp();
  } finally {
    // index.html hides the shell until auth state is known; clear it on every
    // exit path (signed in, signed out, init error) so it is never left blank.
    document.body.classList.remove('auth-pending');
  }
}

async function initApp(): Promise<void> {
  api.initAuth();

  // Check if this is a password reset link. Must be scoped to the
  // /reset-password path specifically — other deep-links (e.g. the
  // purchase approve/cancel flow at /purchases/{action}/:id?token=…)
  // also carry a `token` query param and would otherwise hijack into
  // the reset-password modal. The backend emits exactly
  // `${dashboardURL}/reset-password?token=…` in
  // internal/auth/service_password.go:276, so pinning the check to
  // that path matches the issuer contract.
  const urlParams = new URLSearchParams(window.location.search);
  const resetToken = urlParams.get('token');
  if (resetToken && window.location.pathname.replace(/\/+$/, '') === '/reset-password') {
    await showResetPasswordModal(resetToken);
    return;
  }

  // The server-rendered markup marks Home active; correct it from the URL
  // now instead of after the async fetches below.
  setActiveNav(applyTabFromPath());

  let restored: User | null;
  try {
    restored = await api.getCurrentUser();
  } catch (error) {
    const err = error as { status?: number; message?: string };
    // Deliberate: an unreachable API shows the init error page here, not the
    // login form with a notice; only a 401 means "signed out".
    if (err.status !== 401 && !err.message?.includes('Unauthorized')) {
      console.error('Init error:', error);
      renderInitError(error);
      return;
    }
    restored = null;
  }

  if (!restored) {
    let notice: string | undefined;
    try {
      const publicInfo = await api.getPublicInfo();
      if (!publicInfo.admin_exists) {
        // api_key_secret_url is no longer served on the unauthenticated
        // /api/info endpoint (#633). The admin setup modal works without
        // the hint — the deployer can find the secret in the AWS console.
        await showAdminSetupModal(undefined);
        return;
      }
    } catch (err) {
      // getPublicInfo throws on a non-OK response, so an unreachable API
      // lands on the login form with the failure named, rather than on the
      // first-run setup screen asserting that no admin exists.
      console.error('Failed to load /api/info:', err);
      notice = describePublicInfoFailure(err);
    }
    await showLoginModal(notice);
    return;
  }

  try {
    const user = restored;
    state.setCurrentUser(user);

    // Fetch the effective permission set from /api/auth/me/permissions
    // (issue #917) and merge it onto the user state so canAccess() can
    // consult the real group-derived set instead of blocking non-admins.
    // Best-effort: a failure here must not prevent the app from loading.
    try {
      const permsResp = await api.getUserPermissions();
      const updatedUser = state.getCurrentUser();
      if (updatedUser) {
        state.setCurrentUser({ ...updatedUser, effectivePermissions: permsResp.permissions });
      }
    } catch (permErr) {
      console.warn('Failed to fetch effective permissions, using fallback gating:', permErr);
    }

    initRouter();
    // Deep-link check BEFORE tab routing: the path /purchases/{approve,
    // cancel}/:id?token=… isn't a tab — it's a one-shot action landing
    // page from the approval email. handlePurchaseDeeplink runs the
    // confirm+POST flow, replaces the URL with /purchases, then falls
    // through so the user lands on the Purchases tab with their action's
    // outcome rendered as a toast.
    await handlePurchaseDeeplink();
    // Same one-shot pattern for the RI-exchange approval email's
    // /inventory/ri-exchange/{approve,reject}/:id?token=… links (#579): confirm,
    // POST, then land on /inventory/ri-exchange.
    await handleRIExchangeDeeplink();
    // Archera education deep-links (/archera-insurance, /archera-insurance/
    // how-it-works) open the overlay panel on top of the dashboard. Normal
    // tab routing still runs underneath so the app is fully functional.
    handleArcheraDeeplink();
    const target = applyTabFromPath();
    // switchTab below re-reads the sub-tab from the URL, so this rewrite must
    // keep the segment of a deep-linked /inventory/<subtab> or /admin/<subtab>.
    window.history.replaceState(
      { tab: target, id: 0 },
      '',
      canonicalTabPath(target) + window.location.search + window.location.hash,
    );
    switchTab(target, { push: false });
    setupEventListeners();
    updateUserUI();
    // A plan form left open when the session expired comes back after sign-in (#1735).
    // Not awaited: it never throws and must not delay or fail init.
    void restorePlanDraft(() => applyTabFromPath() === 'plans');
  } catch (error) {
    console.error('Init error:', error);
    const err = error as { status?: number; message?: string };
    if (err.status === 401 || err.message?.includes('Unauthorized')) {
      await showLoginModal();
      return;
    }
    // Anything else would leave the pre-rendered Home markup on screen with
    // no listeners bound, under whatever URL was requested (#206).
    renderInitError(error);
  }
}
