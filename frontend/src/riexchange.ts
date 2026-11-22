/**
 * RI Exchange module for CUDly
 * Manages convertible RI listing, reshape recommendations, and exchange operations
 */

import * as api from './api';
import * as state from './state';
import { escapeHtml } from './utils';
import { switchTab, switchSettingsSubTab } from './navigation';
import type { Provider } from './api';
import { closeModal } from './modal';
import { showSkeletonRows, teardownSkeleton } from './lib/skeleton';
import { riexState } from './riexflow/riexState';
import { providerLabel, renderEmptyState } from './riexflow/shared';
import { loadExchangeHistory } from './riexflow/history';
import { renderRIsTable, renderAzureRIsTable } from './riexflow/riTable';
import { rerenderReshape } from './riexflow/reshapeTable';
import { loadReshapeRecommendations, renderExcludedRIsNotice, renderReshapeStalenessBanner } from './riexflow/reshape';

// resolveScope reads the Main Header global Provider/Account chips so every
// RI Exchange load is scoped consistently with the other Inventory sub-tabs
// (issue #871). The account chip holds a CloudAccount UUID, but the RI
// endpoints expect the provider account number / subscription GUID
// (external_id), so the single selected account is translated here.
async function resolveScope(): Promise<{ provider: Provider | ''; accountID?: string }> {
  const provider = state.getCurrentProvider();
  const accountIDs = state.getCurrentAccountIDs();
  if (accountIDs.length !== 1) return { provider };
  const accounts = await api.listAccountsMinimal();
  const account = accounts.find(a => a.id === accountIDs[0]);
  if (!account) throw new Error(`Selected account ${accountIDs[0]} was not found`);
  if (!account.external_id) throw new Error(`Selected account ${accountIDs[0]} has no provider account ID`);
  return { provider, accountID: account.external_id };
}

/**
 * Load the RI Exchange tab — called when tab is activated and whenever the
 * Main Header global Provider/Account filter changes (issue #871).
 *
 * RI Exchange is multi-provider:
 *   - AWS:   convertible RIs + reshape recommendations + exchange history.
 *   - Azure: exchangeable VM reservations + exchange history. Reshape
 *            recommendations are a Cost-Explorer/AWS concept, so that
 *            section shows a provider-aware "not applicable" empty state.
 *   - GCP:   no RI-exchange concept; every section shows a "not available
 *            for GCP" empty state and no list endpoint is called.
 */
export async function loadRIExchange(): Promise<void> {
  const generation = ++riexState.scopeGeneration;
  // Supersede any in-flight utilization load from the previous scope, before
  // the account lookup, so a late result can't re-render over the new view.
  riexState.utilizationGeneration++;
  let scope: Awaited<ReturnType<typeof resolveScope>>;
  try {
    scope = await resolveScope();
  } catch (error) {
    if (generation !== riexState.scopeGeneration) return;
    riexState.currentRIs = [];
    riexState.currentRIsLoaded = false;
    riexState.currentUtilization = new Map();
    riexState.utilizationStatus = 'loading';
    riexState.currentRecommendations = [];
    riexState.currentRecsLoaded = false;
    riexState.currentRIAccountID = undefined;
    const message = 'Select a valid account to load this section.';
    for (const id of ['ri-exchange-recommendations-list', 'ri-exchange-history-list']) {
      const el = document.getElementById(id);
      if (el) renderEmptyState(el, message);
    }
    renderReshapeStalenessBanner('', null);
    renderExcludedRIsNotice(undefined);
    const container = document.getElementById('ri-exchange-instances-list');
    if (container) {
      container.innerHTML = `<p class="error">Failed to resolve the selected account: ${escapeHtml((error as Error).message)}</p>`;
    }
    return;
  }
  if (generation !== riexState.scopeGeneration) return;
  const { provider, accountID } = scope;

  if (provider === 'gcp') {
    renderGCPEmptyStates();
    return;
  }

  if (provider === 'azure') {
    await Promise.all([
      loadExchangeableAzureRIs(accountID, generation),
      loadExchangeHistory(generation),
    ]);
    renderReshapeNotApplicable('azure');
    return;
  }

  // AWS (default / empty provider): full convertible-RI flow.
  const [, recsLoaded] = await Promise.all([
    loadConvertibleRIs(accountID, generation),
    loadReshapeRecommendations(generation),
    loadExchangeHistory(generation),
  ]);
  // The three loads race. When the reshape list came back empty before the
  // RI list, its empty state was written without this scope's RI count, so
  // render it again now that both are in. A failed reshape load keeps its
  // error message (issue #1135).
  if (generation !== riexState.scopeGeneration) return;
  if (recsLoaded && riexState.currentRecommendations.length === 0) rerenderReshape();
}

/**
 * Render the "not available for GCP" empty state across all three RI Exchange
 * sections. GCP has no Reserved-Instance exchange concept, so we never call a
 * list endpoint and never leave stale AWS/Azure rows behind.
 */
function renderGCPEmptyStates(): void {
  riexState.currentRIs = [];
  riexState.currentRIsLoaded = false;
  riexState.currentUtilization = new Map();
  riexState.utilizationStatus = 'loading';
  riexState.currentRecommendations = [];
  riexState.currentRecsLoaded = false;
  riexState.currentRIAccountID = undefined;

  const instances = document.getElementById('ri-exchange-instances-list');
  if (instances) renderEmptyState(instances, "RI Exchange isn't available for GCP.");

  const recs = document.getElementById('ri-exchange-recommendations-list');
  if (recs) renderEmptyState(recs, "RI Exchange isn't available for GCP.");

  const history = document.getElementById('ri-exchange-history-list');
  if (history) renderEmptyState(history, "RI Exchange isn't available for GCP.");

  renderReshapeStalenessBanner('', null);
  renderExcludedRIsNotice(undefined);
}

/**
 * Render a provider-aware "reshape recommendations not applicable" message.
 * Reshape recommendations are derived from AWS Cost Explorer, so they do not
 * apply to Azure (or any non-AWS) provider.
 */
function renderReshapeNotApplicable(provider: string): void {
  riexState.currentRecommendations = [];
  riexState.currentRecsLoaded = false;
  const container = document.getElementById('ri-exchange-recommendations-list');
  if (container) {
    renderEmptyState(container, `Reshape recommendations are not available for ${providerLabel(provider)}.`);
  }
  renderReshapeStalenessBanner('', null);
  renderExcludedRIsNotice(undefined);
}

/**
 * True when the RI Exchange sub-tab is the currently visible panel.
 * The sub-tab panel id is "inventory-ri-exchange" (see index.html).
 * Used by the provider/account change subscriptions below to avoid
 * unnecessary fetches while the user is on a different tab.
 */
function isRIExchangeSubtabActive(): boolean {
  const panel = document.getElementById('inventory-ri-exchange');
  return panel !== null && !panel.classList.contains('hidden');
}

/**
 * Setup RI Exchange event handlers.
 *
 * Wires the refresh button, the settings deep-link, and
 * provider/account state subscriptions so the convertible-RI list
 * and reshape recommendations reload when the operator switches the
 * global account filter (issue #186). An active-subtab guard
 * mirrors the Recommendations tab pattern to avoid redundant fetches
 * while the panel is off-screen.
 */
export function setupRIExchangeHandlers(): void {
  // Refresh button. Quote + execute flow lives in the per-row "Exchange"
  // button handlers, which open the exchange modal (openExchangeModal).
  const refreshBtn = document.getElementById('ri-exchange-refresh-btn');
  if (refreshBtn) {
    refreshBtn.addEventListener('click', () => void loadRIExchange());
  }

  const settingsBtn = document.getElementById('ri-exchange-settings-btn');
  if (settingsBtn) {
    settingsBtn.addEventListener('click', () => {
      // switchSettingsSubTab owns the sub-tab DOM, title and history push, so
      // switchTab only has to reveal the Admin panel.
      switchTab('admin', { push: false, skipDefaultLoad: true });
      switchSettingsSubTab('purchasing');
      const target = document.getElementById('ri-exchange-automation-settings');
      if (target) {
        requestAnimationFrame(() => {
          target.scrollIntoView({ behavior: 'smooth', block: 'start' });
        });
      }
    });
  }

  // issue #186 / #871: reload (scoped to the new provider/account) when the
  // global Main Header filter changes so the RI Exchange tables stay
  // consistent with the rest of the Inventory sub-tabs. Coalesce the two
  // events into a single reload (provider change also fires an account change
  // via the topbar-filters.ts clearing logic).
  //
  // RI Exchange is a mutating workflow, so we ALSO close any in-progress
  // exchange modal the moment the filter changes — the source RI it targets
  // may no longer be visible (e.g. provider switched AWS -> Azure), and acting
  // on a now-hidden RI would be confusing and potentially wrong. We close
  // synchronously on the chip event (not inside the coalescing microtask) so
  // the stale selection is torn down even if the reload is skipped because the
  // sub-tab is off-screen.
  let reloadQueued = false;
  const scheduleReload = (): void => {
    closeExchangeModalIfOpen();
    if (!isRIExchangeSubtabActive() || reloadQueued) return;
    reloadQueued = true;
    queueMicrotask(() => {
      reloadQueued = false;
      if (isRIExchangeSubtabActive()) void loadRIExchange();
    });
  };
  state.subscribeProvider(scheduleReload);
  state.subscribeAccount(scheduleReload);
}

/**
 * Close the RI Exchange quote/execute modal if it is currently open. Called
 * on a global filter change so the user can't act on an exchange selection
 * whose source RI may no longer be in scope (issue #871). No-op when the
 * modal is absent or already hidden.
 */
function closeExchangeModalIfOpen(): void {
  const modal = document.getElementById('ri-exchange-modal');
  if (modal && !modal.classList.contains('hidden')) {
    closeModal(modal);
  }
}

// ──────────────────────────────────────────────
// Convertible RIs table
// ──────────────────────────────────────────────

async function loadConvertibleRIs(accountID?: string, generation: number = riexState.scopeGeneration): Promise<void> {
  const container = document.getElementById('ri-exchange-instances-list');
  if (!container) return;

  // Issue #344 T3: shimmer skeleton replaces the static "Loading…" text.
  // 3 rows × 8 cols matches the convertible-RI table's column shape
  // (RI ID / Instance Type / AZ / Count / Offering / Expiry /
  // Utilization / Actions — see renderRIsTable); renderRIsTable swaps
  // the children on success for a clean handoff.
  showSkeletonRows(container, 3, 8);

  riexState.currentRIAccountID = accountID;
  riexState.currentRIsLoaded = false;

  try {
    // issue #871: scope to the selected account so the page honours the
    // Main Header global filter.
    const [ris] = await Promise.all([api.listConvertibleRIs(accountID), loadRIAccountNames()]);
    if (generation !== riexState.scopeGeneration) return;
    riexState.currentRIs = ris;
    riexState.currentRIsLoaded = true;
    riexState.utilizationStatus = 'loading';
    renderRIsTable(container, accountID);
    // Load utilization asynchronously (Cost Explorer is slow)
    riexState.utilizationGeneration++;
    void loadUtilization(riexState.utilizationGeneration);
  } catch (error) {
    if (generation !== riexState.scopeGeneration) return;
    teardownSkeleton(container);
    const err = error as Error;
    container.innerHTML = `<p class="error">Failed to load convertible RIs: ${escapeHtml(err.message)}</p>`;
  }
}

/**
 * Load and render the Azure exchangeable VM reservations (issue #871).
 * Azure reservations have no utilization/reshape pipeline, so this renders a
 * standalone table into the same Active-list container the AWS path uses.
 * The optional accountID is the selected subscription chip; it scopes the
 * capacity-provider registration check on the backend.
 */
async function loadExchangeableAzureRIs(accountID: string | undefined, generation: number): Promise<void> {
  const container = document.getElementById('ri-exchange-instances-list');
  if (!container) return;

  // Azure table has 6 columns: Reservation / SKU / Quantity / Region /
  // Term / Expiry (see renderAzureRIsTable).
  showSkeletonRows(container, 3, 6);

  // AWS-only module state is irrelevant on the Azure path; reset it so a
  // later provider switch back to AWS starts clean and reshape copy that
  // reads riexState.currentRIs.length isn't skewed by stale AWS rows.
  riexState.currentRIs = [];
  riexState.currentRIsLoaded = false;
  riexState.currentUtilization = new Map();
  riexState.utilizationStatus = 'loading';
  riexState.currentRIAccountID = undefined;

  try {
    const reservations = await api.listExchangeableAzureRIs(accountID);
    if (generation !== riexState.scopeGeneration) return;
    renderAzureRIsTable(container, reservations, accountID);
  } catch (error) {
    if (generation !== riexState.scopeGeneration) return;
    teardownSkeleton(container);
    const err = error as Error;
    container.innerHTML = `<p class="error">Failed to load Azure reservations: ${escapeHtml(err.message)}</p>`;
  }
}

// Best effort: without names the label falls back to the account ID.
async function loadRIAccountNames(): Promise<void> {
  try {
    const accounts = (await api.listAccountsMinimal({ provider: 'aws' })) ?? [];
    riexState.riAccountNames = new Map(accounts.map(a => [a.external_id, a.name]));
  } catch (error) {
    console.warn('RI Exchange: could not load account names:', error);
    riexState.riAccountNames = new Map();
  }
}

async function loadUtilization(generation: number): Promise<void> {
  try {
    const utilization = await api.getRIUtilization();
    // Discard if a newer load was started while we were waiting
    if (generation !== riexState.utilizationGeneration) return;
    riexState.currentUtilization = new Map(utilization.map(u => [u.reserved_instance_id, u]));
    riexState.utilizationStatus = 'loaded';
  } catch (error) {
    if (generation !== riexState.utilizationGeneration) return;
    console.error('Failed to load RI utilization:', error);
    // Values from an earlier load are no longer current (same-scope reload).
    riexState.currentUtilization = new Map();
    riexState.utilizationStatus = 'failed';
  }
  // Re-render table with utilization data, preserving the active account
  // scope so a scoped empty-state message isn't lost (issue #871).
  const container = document.getElementById('ri-exchange-instances-list');
  if (container) renderRIsTable(container, riexState.currentRIAccountID);
  // The reshape empty state depends on which RIs have known utilization.
  if (riexState.currentRecsLoaded && riexState.currentRecommendations.length === 0) rerenderReshape();
}

export { currencyPrefix } from './riexflow/shared';
export { loadAutomationSettings, saveAutomationSettings } from './riexflow/automation';
export { openExchangeModal } from './riexflow/exchangeModal';
export { applyActiveRiColumnFilters } from './riexflow/riTable';
export { applyRiExchangeColumnFilters, fillQuoteFromRI } from './riexflow/reshapeTable';
export { loadReshapeRecommendations, renderExcludedRIsNotice, renderReshapeStalenessBanner } from './riexflow/reshape';
