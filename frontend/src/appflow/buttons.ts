import { closePlanModal, openNewPlanModal, closePurchaseModal } from '../plans';
import { resetSettings } from '../settings';
import { loadHistory } from '../history';
import { saveAutomationSettings } from '../riexchange';
import { initPurchasingNav } from '../purchasing-nav';
import { handleExecutePurchase } from './purchase';

/**
 * Setup all button event listeners (Security improvement: replaces inline onclick handlers)
 */
export function setupButtonHandlers(): void {
  // Recommendations buttons — there is no visible Refresh affordance.
  // The previous filter-bar Refresh button was replaced by the freshness
  // indicator's Refresh, which was itself removed (#284 follow-up):
  // triggerAutoRefreshIfStale in recsflow/refresh.ts now fires on every
  // load if the cache is older than 24h, surfacing collection errors
  // via toast. Users who need a manual refresh can reload the page;
  // operators should expose a more-explicit affordance if support
  // demand surfaces it.
  //
  // Bundle B note (column-filter UX overhaul): #create-plan-btn was relocated
  // from the old top filter bar into the sticky bottom action box. The
  // button is now created and wired by recsflow/bottomBox.ts:mountBottomActionBox,
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
