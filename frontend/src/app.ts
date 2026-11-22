/**
 * CUDly - Application initialization and event setup
 */

import * as api from './api';
import type { User } from './api';
import * as state from './state';
import { showLoginModal, showAdminSetupModal, showResetPasswordModal, updateUserUI } from './auth';
import { loadDashboard, setupDashboardHandlers } from './dashboard';
import { setupRecommendationsHandlers, getPurchaseModalRecommendations, getPurchaseModalSession, clearPurchaseModalRecommendations, getFanOutBuckets, getExecuteMode, clearExecuteMode, getAccountName } from './recommendations';
import { switchTab, setActiveNav, applyTabFromPath, initRouter, switchSettingsSubTab, canonicalTabPath } from './navigation';
import { restorePlanDraft, savePlan, setupPlanHandlers, closePlanModal, openNewPlanModal, closePurchaseModal } from './plans';
import { saveGlobalSettings, setupSettingsHandlers, resetSettings } from './settings';
import { setupUserHandlers } from './users';
import { initApiKeys } from './apikeys';
import { loadHistory, setupHistoryHandlers } from './history';
import { initSavingsHistory } from './modules/savings-history';
import { setupRIExchangeHandlers, saveAutomationSettings } from './riexchange';
import { showToast } from './toast';
import { formatPaymentAdjustmentNotice } from './commitmentOptions';
import { confirmDialog } from './confirmDialog';
import { buildCommitmentSummary, commitmentLine, commitmentTotals, executeButtonLabel, executeConfirmLabel, withIntro } from './commitmentSummary';
import { handlePurchaseDeeplink } from './purchases-deeplink';
import { handleRIExchangeDeeplink } from './riexchange-deeplink';
import { handleArcheraDeeplink, openArcheraOfferModal } from './archera';
import { wireTablistKeyboard } from './lib/tablist';
import { setOpenAccountsHandler } from './first-run-empty-state';
import { setupMobileNav } from './nav/mobile-nav';
import { initPurchasingNav } from './purchasing-nav';
import { renderInitError } from './init-error';
import { initSubtabScroll } from './nav/subtab-scroll';
import { describeDuplicateSubmit, setExecutePurchaseSubmitting } from './appflow/shared';
import { handleFanOutExecute } from './appflow/fanout';

/** Strict Capacity % parse: a whole number 1-100, never coerced. */
export function parseCapacityPercent(raw: string): number | { error: string } {
  const text = raw.trim();
  const n = Number(text);
  if (text === '' || !Number.isInteger(n) || n < 1 || n > 100) {
    return { error: 'Capacity % must be a whole number from 1 to 100.' };
  }
  return n;
}

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

/**
 * Setup event listeners
 */
export function setupEventListeners(): void {
  // Tab switching. The sidebar nav items are anchor tags (issue #462)
  // so the browser's "Open Link in New Tab" affordance works. For
  // un-modified left clicks we preventDefault and route via the SPA;
  // middle-clicks, Ctrl/Cmd/Shift-clicks fall through so the browser
  // can handle "open in new tab/window" natively.
  document.querySelectorAll<HTMLElement>('.tab-btn').forEach(btn => {
    btn.addEventListener('click', (e) => {
      const mouseEvent = e as MouseEvent;
      // Bail out on modifier-key or middle-click so the browser can
      // do its default new-tab/new-window behaviour for anchor elements.
      if (
        mouseEvent.button !== 0 ||
        mouseEvent.metaKey ||
        mouseEvent.ctrlKey ||
        mouseEvent.shiftKey ||
        mouseEvent.altKey
      ) {
        return;
      }
      const tab = btn.dataset['tab'];
      if (tab) {
        e.preventDefault();
        switchTab(tab, { focusContent: true });
      }
    });
  });

  // In-page sub-tab lists (Inventory & Coverage, Admin): arrow/Home/End
  // focus movement and a roving tabindex, the keyboard model their
  // role="tablist" promises (issue #730).
  document.querySelectorAll<HTMLElement>('.inventory-subnav[role="tablist"], .settings-tabs[role="tablist"]')
    .forEach(wireTablistKeyboard);
  // Narrow screens: edge fade on bars that scroll, selected tab kept in view.
  initSubtabScroll();

  // Settings sub-tab switching
  document.querySelectorAll<HTMLButtonElement>('.sub-tab-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const subTab = btn.dataset['settingsTab'];
      if (subTab) switchSettingsSubTab(subTab);
    });
  });

  // Forms
  const planForm = document.getElementById('plan-form');
  if (planForm) {
    planForm.addEventListener('submit', (e) => void savePlan(e));
  }

  const settingsForm = document.getElementById('global-settings-form');
  if (settingsForm) {
    settingsForm.addEventListener('submit', (e) => void saveGlobalSettings(e));
  }

  // Setup settings event handlers (provider toggles, schedule visibility, etc.)
  setupSettingsHandlers();

  // Setup dashboard event handlers (provider filter)
  setupDashboardHandlers();

  // Setup recommendations event handlers (provider filter, service filter, etc.)
  setupRecommendationsHandlers();

  // First-run empty state (issue #756) links to Admin > Accounts. Injected
  // here so that module does not import navigation.ts (import cycle). Same
  // two-step as the RI Exchange settings shortcut: switchSettingsSubTab owns
  // the sub-tab DOM, title and history push.
  setOpenAccountsHandler(() => {
    switchTab('admin', { push: false, skipDefaultLoad: true });
    switchSettingsSubTab('accounts');
  });

  // Setup plan event handlers (provider-aware service dropdown)
  setupPlanHandlers();

  // Setup user management event handlers
  setupUserHandlers();

  // Setup API keys management
  initApiKeys();

  // Setup RI Exchange event handlers
  setupRIExchangeHandlers();

  // Setup savings history charts
  initSavingsHistory();

  // Wire provider/account topbar filter chips to Purchase History +
  // Approval Queue (issue #701). initSavingsHistory above subscribes
  // the chart; setupHistoryHandlers subscribes the two list consumers
  // so all three reload together when a chip changes.
  setupHistoryHandlers();

  // Setup feedback link
  setupFeedbackLink();

  // Mobile navigation drawer (hamburger button below 768px)
  setupMobileNav();

  // Setup all button event listeners (replacing onclick handlers)
  setupButtonHandlers();

  // Ramp schedule toggle
  document.querySelectorAll<HTMLInputElement>('input[name="ramp-schedule"]').forEach(radio => {
    radio.addEventListener('change', () => {
      const customConfig = document.getElementById('custom-ramp-config');
      if (customConfig) {
        customConfig.classList.toggle('hidden', radio.value !== 'custom');
      }
    });
  });
}

/**
 * Setup all button event listeners (Security improvement: replaces inline onclick handlers)
 */
function setupButtonHandlers(): void {
  // Recommendations buttons — there is no visible Refresh affordance.
  // The previous filter-bar Refresh button was replaced by the freshness
  // indicator's Refresh, which was itself removed (#284 follow-up):
  // triggerAutoRefreshIfStale in recommendations.ts now fires on every
  // load if the cache is older than 24h, surfacing collection errors
  // via toast. Users who need a manual refresh can reload the page;
  // operators should expose a more-explicit affordance if support
  // demand surfaces it.
  //
  // Bundle B note (column-filter UX overhaul): #create-plan-btn was relocated
  // from the old top filter bar into the sticky bottom action box. The
  // button is now created and wired by recommendations.ts:mountBottomActionBox,
  // which calls openCreatePlanModal directly. The wiring used to live here.

  // Plans buttons
  const newPlanBtn = document.getElementById('new-plan-btn');
  if (newPlanBtn) {
    newPlanBtn.addEventListener('click', () => openNewPlanModal());
  }

  const closePlanBtn = document.getElementById('close-plan-modal-btn');
  if (closePlanBtn) {
    closePlanBtn.addEventListener('click', () => closePlanModal());
  }

  // Purchase modal buttons
  const closePurchaseBtn = document.getElementById('close-purchase-modal-btn');
  if (closePurchaseBtn) {
    // closePurchaseModal() routes through modal.ts closeModal(), which runs
    // the onClose that openPurchaseModal / openFanOutModal registered; that
    // callback discards the single-row and fan-out purchase state on every
    // close path, Escape included (issue #331), so no manual clear here.
    closePurchaseBtn.addEventListener('click', () => closePurchaseModal());
  }

  const executePurchaseBtn = document.getElementById('execute-purchase-btn');
  if (executePurchaseBtn) {
    executePurchaseBtn.addEventListener('click', () => void handleExecutePurchase());
  }

  // Settings buttons
  const resetSettingsBtn = document.getElementById('reset-settings-btn');
  if (resetSettingsBtn) {
    resetSettingsBtn.addEventListener('click', () => resetSettings());
  }

  // Purchasing reset button
  const resetPurchasingBtn = document.getElementById('reset-purchasing-btn');
  if (resetPurchasingBtn) {
    resetPurchasingBtn.addEventListener('click', () => resetSettings());
  }

  // Purchasing save button — mirrors the General tab's Save Settings by
  // dispatching submit on the shared #global-settings-form (which reads
  // both General-tab and Purchasing-tab fields from across the DOM) and
  // *also* saves the RI Exchange Automation settings when that form is
  // loaded. Keeping both saves behind a single button avoids the prior
  // duplicate "Save Settings" in the panel: the scrolling one inside the
  // RI Exchange form was removed and its persistence rolled into this
  // sticky bar. If the RI Exchange form hasn't rendered yet (panel never
  // visited), the guard inside saveAutomationSettings no-ops cleanly.
  // Admin -> Purchasing policies section links (issue #190).
  initPurchasingNav();

  const savePurchasingBtn = document.getElementById('save-purchasing-btn');
  if (savePurchasingBtn) {
    savePurchasingBtn.addEventListener('click', () => {
      const form = document.getElementById('global-settings-form') as HTMLFormElement | null;
      if (form) form.dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
      if (document.getElementById('ri-exchange-settings-form')) {
        void saveAutomationSettings();
      }
    });
  }

  // History button
  const loadHistoryBtn = document.getElementById('load-history-btn');
  if (loadHistoryBtn) {
    loadHistoryBtn.addEventListener('click', () => void loadHistory());
  }

}

/**
 * Handle execute purchase button click. Routes to the single-bucket
 * path when getPurchaseModalRecommendations has content, or to the
 * multi-bucket fan-out path when the fan-out modal set buckets.
 */
export async function handleExecutePurchase(): Promise<void> {
  const fanOutBuckets = getFanOutBuckets();
  if (fanOutBuckets && fanOutBuckets.length > 0) {
    await handleFanOutExecute(fanOutBuckets);
    return;
  }
  // fanOutBuckets !== null means fan-out mode is active but every bucket
  // was skipped (unpriced/unavailable) -- distinct from null (fan-out was
  // never opened). Refuse rather than silently falling through to
  // getPurchaseModalRecommendations()'s single-row state, which could be
  // stale leftovers from a different selection (issue #331). In practice
  // the Execute button is already disabled in this state; this is
  // defense-in-depth, not a workaround for a reachable click.
  if (fanOutBuckets !== null) {
    showToast({ message: 'No recommendations selected for purchase.', kind: 'warning' });
    return;
  }

  const localRecs = getPurchaseModalRecommendations();
  const modalSession = getPurchaseModalSession();
  if (localRecs.length === 0) {
    showToast({ message: 'No recommendations selected for purchase.', kind: 'warning' });
    return;
  }

  // Refuse before the confirm dialog and the POST when any rec has no payment
  // option. Defaulting here would silently pick the most cash-intensive
  // schedule on a money path (issue #431).
  const recsWithPayment = localRecs.filter(
    (r): r is typeof r & { payment: string } => !!r.payment?.trim(),
  );
  const missingCount = localRecs.length - recsWithPayment.length;
  if (missingCount > 0) {
    showToast({
      message: `Cannot submit: ${missingCount} of ${localRecs.length} selected recommendation${localRecs.length === 1 ? '' : 's'} ${missingCount === 1 ? 'has' : 'have'} no payment option. Choose a payment option for each row and try again.`,
      kind: 'error',
    });
    return;
  }

  // Read the sticky toolbar Capacity % so the execution record carries the
  // user's intent. Parsed strictly and before the confirm dialog: 0, blank or
  // fractional input used to coerce to 100 or truncate, submitting something
  // other than what was typed. Absent toolbar (test harness) means 100.
  const capacityInput = document.getElementById('bulk-purchase-capacity') as HTMLInputElement | null;
  const capacity = capacityInput ? parseCapacityPercent(capacityInput.value) : 100;
  if (typeof capacity !== 'number') {
    showToast({ message: capacity.error, kind: 'error' });
    return;
  }
  const capacityPercent = capacity;

  // Read the execute mode set by the modal toggle (issue #289).
  // "direct" means the session has execute-any/execute-own and chose to
  // bypass approval; "" is the default approval-required path.
  const executeMode = getExecuteMode();
  const isDirect = executeMode === 'direct';

  // Disable the button BEFORE awaiting the confirm dialog and the network
  // call so a double-click or rapid re-click can't fire a second POST and
  // mint a duplicate pending execution (#644). The button is re-enabled on
  // cancel below and in the finally block once the request settles.
  const executeBtn = document.getElementById('execute-purchase-btn') as HTMLButtonElement | null;
  setExecutePurchaseSubmitting(executeBtn, true, 'Sending...');

  // Confirmation dialog varies by mode:
  //   - Approval path: low-friction, non-destructive.
  //   - Direct-execute path: red destructive dialog with cost callout and
  //     immediate-charge callout; no cancellation promise (issue #251).
  const summaryLines = recsWithPayment.map((r) => commitmentLine(
    r,
    r.cloud_account_id ? { account: getAccountName(r.cloud_account_id) } : {},
  ));
  const totals = commitmentTotals(summaryLines);
  const summary = buildCommitmentSummary(
    summaryLines,
    isDirect ? {} : { upfrontLabel: 'Upfront on approval' },
  );
  const ok = isDirect
    ? await confirmDialog({
        title: `Execute ${localRecs.length} purchase${localRecs.length === 1 ? '' : 's'} now?`,
        body: withIntro('This charges the amount shown under "Charged today" immediately and bypasses the approval step. The monthly amount, if any, is billed for the whole term.', summary),
        confirmLabel: executeConfirmLabel(true, totals.upfront),
        destructive: true,
      })
    : await confirmDialog({
        title: `Send ${localRecs.length} purchase${localRecs.length === 1 ? '' : 's'} for approval?`,
        body: withIntro('This emails an approval request to the configured approver. Nothing is charged until the approver clicks the link in that email.', summary),
        confirmLabel: executeConfirmLabel(false, totals.upfront),
        destructive: false,
      });

  if (!ok) {
    setExecutePurchaseSubmitting(executeBtn, false, executeButtonLabel(getExecuteMode() === 'direct'));
    return;
  }

  // Build the POST body recs by spreading the server-provided rec so that
  // all fields (including `details`, `engine`, `cloud_account_id`, and any
  // future additions) flow through unchanged. Only `payment`, `selected`,
  // and `purchased` are overridden: `payment` uses the user-edited value
  // (issue #111), and `selected`/`purchased` are forced to the canonical
  // purchase-intent values. A missing `payment` was refused above, so it is
  // never defaulted here. Passing `details` ensures
  // Windows EC2, dedicated-tenancy, AZ-scoped RIs, and non-default-engine
  // RDS/Cache recs reach the backend with the correct ServiceDetails payload
  // instead of falling back to Linux/regional/default (issue #597).
  const apiRecs: api.Recommendation[] = recsWithPayment.map((r) => ({
    ...r,
    monthly_cost: r.monthly_cost ?? null,
    payment: r.payment,
    selected: true,
    purchased: false,
  }));

  try {
    const result = await api.executePurchase(apiRecs, capacityPercent, executeMode || undefined);
    closePurchaseModal();
    clearPurchaseModalRecommendations();
    clearExecuteMode();

    if (result.duplicate) {
      showToast({
        message: describeDuplicateSubmit(result),
        kind: 'info',
        timeout: 15_000,
      });
    } else if (isDirect) {
      // Direct-execute: purchase is already committed; inform the user.
      showToast({
        message: `Purchase executed immediately (id ${result.execution_id.slice(0, 8)}). Check Purchase History for the result.`,
        kind: 'success',
        timeout: 15_000,
      });
    } else if (result.email_sent === false) {
      // The backend now surfaces email-send status so the toast can be honest
      // about what the user should do next. When email_sent is undefined we
      // fall back to the old "check your email" message for backward compat
      // with any pre-deploy caller that hasn't picked up the new field yet.
      const reason = result.email_reason || 'reason unavailable';
      showToast({
        message: `Purchase queued as pending (id ${result.execution_id.slice(0, 8)}) but the approval email did not send: ${reason}. Approve or cancel it from the Purchase History tab.`,
        kind: 'warning',
        timeout: null,
      });
    } else {
      // Name the approver in the success toast so the user can confirm WHO
      // received the request (CR pass on PR #294 / issue #288). The backend
      // surfaces `approval_recipient` from `resolveApprovalRecipients`; the
      // field may be absent on older deploys or when only the global notify
      // mailbox is configured — fall back to the generic line in that case.
      const recipient = result.approval_recipient;
      showToast({
        message: recipient
          ? `Approval request sent to ${recipient}.`
          : 'Purchase submitted - check your email to approve.',
        kind: 'success',
        timeout: 10_000,
      });
    }
    // Disclose any payment-option coercion the backend applied (#1503). This
    // is a SEPARATE, non-expiring warning toast rather than an addition to the
    // success copy above: the billing schedule changing is the one thing the
    // user did not ask for, and on Azure it cannot be changed after purchase,
    // so it must not scroll away inside a success message.
    const paymentNotice = formatPaymentAdjustmentNotice(result.payment_adjustments);
    if (paymentNotice) {
      showToast({ message: paymentNotice, kind: 'warning', timeout: null });
    }

    // Offer Archera Insurance immediately after the user approves the
    // pre-purchase confirmation and the approval-submission call succeeds
    // (issue #499 follow-up). Firing here, rather than after the async
    // email-link execution completes, surfaces the optional coverage at
    // the moment the user has committed their intent. Gated on the
    // executePurchase success path so a failed submission shows no offer.
    openArcheraOfferModal();
    await loadDashboard();
  } catch (error) {
    const err = error as Error;
    const verb = isDirect ? 'execute' : 'send for approval';
    showToast({ message: `Failed to ${verb} purchase: ${err.message}`, kind: 'error' });
    // A recorded execution, a 409 after the claim or any 5xx may follow a real
    // purchase: close the modal so one more click can't start another. Only
    // act on the modal session that sent this request.
    if (attemptMayHaveRun(err) && getPurchaseModalSession() === modalSession) {
      closePurchaseModal();
      clearPurchaseModalRecommendations();
      clearExecuteMode();
    }
  } finally {
    setExecutePurchaseSubmitting(executeBtn, false, executeButtonLabel(getExecuteMode() === 'direct'));
  }
}

// attemptMayHaveRun is false only for transport failures and 4xx refusals
// other than 409, which the server answers before any execution.
function attemptMayHaveRun(err: unknown): boolean {
  const e = err as { status?: number; details?: { execution_id?: unknown } };
  const status = e.status ?? 0;
  return Boolean(e.details?.execution_id) || status >= 500 || status === 409;
}

/**
 * Setup feedback mailto link with template
 */
function setupFeedbackLink(): void {
  const feedbackLink = document.getElementById('feedback-link') as HTMLAnchorElement;
  if (!feedbackLink) return;

  const feedbackEmail = 'contact@leanercloud.com';
  const subject = 'CUDly Feedback';

  const refreshFeedbackLink = (): void => {
    const body = `Hi CUDly Team,

I'd like to share some feedback about CUDly:

## Feedback Type

[ ] Bug Report
[ ] Feature Request
[ ] General Feedback
[ ] Question

## Description

Please describe your feedback in detail:

## Steps to Reproduce (for bugs)

1.
2.
3.

## Expected vs Actual Behavior (for bugs)

Expected:
Actual:

## Screenshots

Please attach any relevant screenshots to this email.

## Environment

- Browser: ${navigator.userAgent}
- URL: ${window.location.href}
- Date: ${new Date().toISOString()}

---
Thank you for helping us improve CUDly!
`;

    const mailtoUrl = `mailto:${feedbackEmail}?subject=${encodeURIComponent(subject)}&body=${encodeURIComponent(body)}`;
    feedbackLink.href = mailtoUrl;
  };

  refreshFeedbackLink();
  feedbackLink.onclick = refreshFeedbackLink;
  feedbackLink.onfocus = refreshFeedbackLink;
  feedbackLink.oncontextmenu = refreshFeedbackLink;
}
