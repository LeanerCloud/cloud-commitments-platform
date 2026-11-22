import { setupDashboardHandlers } from '../dashboard';
import { setupRecommendationsHandlers } from '../recommendations';
import { switchTab, switchSettingsSubTab } from '../navigation';
import { savePlan, setupPlanHandlers } from '../plans';
import { saveGlobalSettings, setupSettingsHandlers } from '../settings';
import { setupUserHandlers } from '../users';
import { initApiKeys } from '../apikeys';
import { setupHistoryHandlers } from '../history';
import { initSavingsHistory } from '../modules/savings-history';
import { setupRIExchangeHandlers } from '../riexchange';
import { wireTablistKeyboard } from '../lib/tablist';
import { setOpenAccountsHandler } from '../first-run-empty-state';
import { setupMobileNav } from '../nav/mobile-nav';
import { initSubtabScroll } from '../nav/subtab-scroll';
import { setupFeedbackLink } from './feedback';
import { setupButtonHandlers } from './buttons';

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
