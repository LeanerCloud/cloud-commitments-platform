/**
 * RI Exchange module for CUDly
 * Manages convertible RI listing, reshape recommendations, and exchange operations
 */

import * as api from './api';
import * as state from './state';
import { formatDate, escapeHtml } from './utils';
import { switchTab, switchSettingsSubTab } from './navigation';
import { openColumnFilterPopover, rebindColumnFilterPopover, sortedDistinctValues } from './lib/column-filter-popover';
import type { ConvertibleRI, ExchangeableAzureRI, RIUtilization, ReshapeRecommendation, OfferingOption, ReshapeRecommendationsResponse, ExcludedRI, Provider } from './api';
import { closeModal } from './modal';
import { showSkeletonRows, teardownSkeleton } from './lib/skeleton';
import { canAccess } from './permissions';
import { iconSvg } from './icons';
import { attachAzureExchangeToolbar, buildAzureSelectCell, buildAzureSelectHeader, canPreviewAzureExchange } from './riexchange-azure';
import { formatIsoTermShort, formatPaymentOption } from './lib/table-format';
import { riexState } from './riexflow/riexState';
import { providerLabel, renderEmptyState, inlineReason, renderTableCallout, formatOfferingCost, riAccountLabel } from './riexflow/shared';
import { loadExchangeHistory } from './riexflow/history';
import { openExchangeModal } from './riexflow/exchangeModal';

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

function hasKnownUtilization(riID: string): boolean {
  const pct = riexState.currentUtilization.get(riID)?.utilization_percent;
  return typeof pct === 'number' && Number.isFinite(pct);
}

function renderRIsTable(container: HTMLElement, accountID?: string): void {
  if (!riexState.currentRIs || riexState.currentRIs.length === 0) {
    // issue #871: name the active scope so the user knows the result is
    // filtered (by the account chip) rather than globally empty.
    const msg = accountID
      ? `No active convertible Reserved Instances for AWS account ${accountID}.`
      : 'No active convertible Reserved Instances found for AWS.';
    renderEmptyState(container, msg);
    return;
  }

  // Issue #365: RI exchanges mutate cloud-provider RI state and are
  // admin-only by default on the backend. Hide the per-row Exchange
  // button for non-admin sessions so readonly users don't get a 403
  // on click. Defense in depth, backend still enforces.
  const canExchange = canAccess('admin', '*');

  // Apply per-column filters (issue #1414). Mirrors the pattern in
  // renderRecommendations; broken numeric expressions are skipped so
  // the popover can surface the inline error without forcing a clear.
  const filters = state.getActiveRiColumnFilters();
  const visibleRIs = applyActiveRiColumnFilters(riexState.currentRIs, riexState.currentUtilization, filters);

  const filterBtn = (column: state.ActiveRiColumnId): string => {
    const active = filters[column] ? ' active' : '';
    const lbl = activeRiLabelFor(column);
    const label = filters[column] ? `Filter ${lbl} — currently active` : `Filter ${lbl}`;
    return `<button type="button" class="column-filter-btn${active}" data-column="${column}" aria-haspopup="dialog" aria-expanded="false" aria-label="${escapeHtml(label)}" title="${escapeHtml(label)}">${iconSvg('filter')}</button>`;
  };

  const accountLabels = [...new Set(riexState.currentRIs.map(r => riAccountLabel(r.account_id)))];
  container.innerHTML = `
    <p class="setting-hint" id="ri-exchange-account-label">Account: ${escapeHtml(accountLabels.join(', '))}</p>
    <table class="table-compact${canExchange ? ' table-sticky-end' : ''}">
      <thead>
        <tr>
          <th>RI ID</th>
          <th>Instance Type${filterBtn('instance_type')}</th>
          <th>AZ${filterBtn('availability_zone')}</th>
          <th class="num">Count${filterBtn('instance_count')}</th>
          <th>Offering${filterBtn('offering_type')}</th>
          <th>Expiry</th>
          <th>Utilization${filterBtn('utilization_pct')}</th>
          ${canExchange ? '<th>Actions</th>' : ''}
        </tr>
      </thead>
      <tbody>
        ${visibleRIs.length === 0 ? `<tr><td colspan="${canExchange ? 8 : 7}" class="empty">No rows match the active column filters. Clear a filter to see them.</td></tr>` : ''}
        ${visibleRIs.map(ri => {
          const utilPct = hasKnownUtilization(ri.reserved_instance_id)
            ? riexState.currentUtilization.get(ri.reserved_instance_id)!.utilization_percent
            : null;
          const utilClass = utilPct === null ? '' : utilPct >= 95 ? 'util-green' : utilPct >= 70 ? 'util-yellow' : 'util-red';
          const utilText = utilPct !== null
            ? `${utilPct.toFixed(1)}%`
            : riexState.utilizationStatus === 'loading'
              ? '<span class="loading-inline">...</span>'
              : `<span class="text-muted" title="${riexState.utilizationStatus === 'failed' ? 'Utilization could not be loaded' : 'No utilization data was returned for this RI'}">No data</span>`;
          return `
          <tr>
            <td class="monospace">${escapeHtml(ri.reserved_instance_id)}</td>
            <td>${escapeHtml(ri.instance_type)}</td>
            <td>${escapeHtml(ri.availability_zone)}</td>
            <td class="num">${ri.instance_count}</td>
            <td>${escapeHtml(formatPaymentOption(ri.offering_type))}</td>
            <td>${formatDate(ri.end)}</td>
            <td class="${utilClass}">${utilText}</td>
            ${canExchange ? `<td><button class="btn-small" data-action="quote-ri" data-ri-id="${escapeHtml(ri.reserved_instance_id)}" data-count="${ri.instance_count}" aria-label="Exchange ${escapeHtml(ri.reserved_instance_id)}">Exchange</button></td>` : ''}
          </tr>`;
        }).join('')}
      </tbody>
    </table>
  `;

  // Wire per-column filter buttons (same pattern as renderRecommendations).
  // e.stopPropagation prevents the surrounding <th> from also handling
  // the click so future sort handlers won't conflict.
  container.querySelectorAll<HTMLButtonElement>('.column-filter-btn').forEach((btn) => {
    const column = btn.dataset['column'] as state.ActiveRiColumnId | undefined;
    if (!column) return;
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      activeRiOpenPopoverFor(column, btn);
    });
  });
  // Keep an open filter popover attached to the re-rendered trigger.
  rebindColumnFilterPopover(ACTIVE_RI_POPOVER_SCOPE);

  // Attach "Exchange" handlers for individual RIs
  container.querySelectorAll<HTMLButtonElement>('[data-action="quote-ri"]').forEach(btn => {
    btn.addEventListener('click', () => {
      const count = parseInt(btn.dataset['count'] || '1', 10);
      openExchangeModal(btn.dataset['riId'] || '', isNaN(count) ? 1 : count);
    });
  });
}

/**
 * Render the Azure exchangeable-reservations table (issue #871). Built
 * entirely via DOM construction / textContent — never innerHTML — so any
 * Azure-derived field (SKU, display name, region) cannot inject markup.
 *
 * Users who can preview an exchange get a selection column and an
 * "Exchange selected" action that opens the Azure exchange modal
 * (riexchange-azure.ts, issue #81).
 */
function renderAzureRIsTable(
  container: HTMLElement,
  reservations: ExchangeableAzureRI[],
  accountID?: string,
): void {
  if (!reservations || reservations.length === 0) {
    const msg = accountID
      ? `No exchangeable reservations for Azure subscription ${accountID}.`
      : 'No exchangeable reservations found for Azure.';
    renderEmptyState(container, msg);
    return;
  }

  container.textContent = '';
  const table = document.createElement('table');
  const canExchange = canPreviewAzureExchange();
  table.className = canExchange ? 'table-compact table-sticky-start' : 'table-compact';

  const thead = document.createElement('thead');
  const headerRow = document.createElement('tr');
  const labels = ['Reservation', 'SKU', 'Quantity', 'Region', 'Term', 'Expiry'];
  if (canExchange) headerRow.appendChild(buildAzureSelectHeader());
  for (const label of labels) {
    const th = document.createElement('th');
    th.textContent = label;
    if (label === 'Quantity') th.className = 'num';
    headerRow.appendChild(th);
  }
  thead.appendChild(headerRow);
  table.appendChild(thead);

  const tbody = document.createElement('tbody');
  reservations.forEach((r, i) => {
    const tr = buildAzureRIRow(r);
    if (canExchange) tr.prepend(buildAzureSelectCell(r, i));
    tbody.appendChild(tr);
  });
  table.appendChild(tbody);
  container.appendChild(table);
  if (canExchange) attachAzureExchangeToolbar(container, table, reservations, () => { void loadRIExchange(); });
}

function buildAzureRIRow(r: ExchangeableAzureRI): HTMLTableRowElement {
  const tr = document.createElement('tr');

  appendAzureCell(tr, r.display_name || r.reservation_id);
  appendAzureCell(tr, r.sku);
  appendAzureCell(tr, String(r.quantity), true);
  appendAzureCell(tr, r.region || '—');
  appendAzureCell(tr, formatIsoTermShort(r.term) || '—');
  appendAzureCell(tr, r.expiry_date ? formatDate(r.expiry_date) : '—');

  return tr;
}

// numeric right-aligns the cell with tabular digits (td.num, issue #739).
function appendAzureCell(tr: HTMLTableRowElement, text: string, numeric = false): void {
  const td = document.createElement('td');
  td.textContent = text;
  if (numeric) td.className = 'num';
  tr.appendChild(td);
}

// ──────────────────────────────────────────────
// Reshape recommendations
// ──────────────────────────────────────────────

// Resolves true when this call rendered a fresh reshape list (empty or not),
// false when it was superseded, failed, or had nowhere to render.
export async function loadReshapeRecommendations(generation: number = riexState.scopeGeneration): Promise<boolean> {
  const container = document.getElementById('ri-exchange-recommendations-list');
  if (!container) return false;

  // Issue #344 T3: skeleton rows for the reshape-recommendations table.
  // 3 rows × 8 cols matches the rendered table shape — see
  // renderRecommendations: Source RI / Current / Suggested /
  // Alternatives / Utilization / Normalized Units / Reason / Actions.
  showSkeletonRows(container, 3, 8);
  riexState.currentRecsLoaded = false;

  try {
    const resp: ReshapeRecommendationsResponse = await api.getReshapeRecommendations();
    if (generation !== riexState.scopeGeneration) return false;
    riexState.currentRecommendations = resp.recommendations ?? [];
    riexState.currentRecsLoaded = true;
    renderRecommendations(container);
    renderReshapeStalenessBanner(resp.recs_staleness, resp.recs_collected_at);
    renderExcludedRIsNotice(resp.excluded_ris);
    return true;
  } catch (error) {
    if (generation !== riexState.scopeGeneration) return false;
    teardownSkeleton(container);
    renderExcludedRIsNotice(undefined);
    const err = error as Error;
    container.innerHTML = `<p class="error">Failed to load recommendations: ${escapeHtml(err.message)}</p>`;
    return false;
  }
}

/**
 * Render (or clear) the notice listing RIs the backend left out of the
 * reshape analysis. Sits in a sibling slot before the recommendations list;
 * ids are inserted as text, never HTML.
 */
export function renderExcludedRIsNotice(excluded: ExcludedRI[] | undefined): void {
  const NOTICE_ID = 'ri-exchange-recommendations-excluded';
  let notice = document.getElementById(NOTICE_ID);
  if (!notice) {
    const listEl = document.getElementById('ri-exchange-recommendations-list');
    if (!listEl || !listEl.parentElement) return;
    notice = document.createElement('div');
    notice.id = NOTICE_ID;
    listEl.parentElement.insertBefore(notice, listEl);
  }
  if (!excluded || excluded.length === 0) {
    notice.textContent = '';
    notice.className = '';
    notice.removeAttribute('role');
    return;
  }
  const byReason = new Map<string, string[]>();
  for (const e of excluded) {
    const reason = e.reason || 'AWS returned no term length';
    const ids = byReason.get(reason);
    if (ids) ids.push(e.ri_id);
    else byReason.set(reason, [e.ri_id]);
  }
  // textContent keeps server-provided reasons and ids inert. The server
  // writes reasons as sentences ("Expires in 35 days; ... the term."), so
  // the trailing full stop and a leading capital are dropped to read
  // inside "... left out because <reason>: <ids>" (it used to end ".:").
  const lines = [...byReason]
    .map(([reason, ids]) => `${ids.length} convertible RI${ids.length === 1 ? ' was' : 's were'} left out because ${inlineReason(reason)}: ${ids.join(', ')}`);
  renderTableCallout(notice, 'warning', lines);
}

/**
 * Render (or clear) the staleness banner above the reshape-recommendations
 * table. The banner slot is a sibling element with id
 * "ri-exchange-recommendations-freshness"; it is created here if absent.
 *
 * staleness: "" or undefined clears any existing banner (fresh data).
 * "soft"  : soft-warning copy ("data is more than 12 h old").
 * "hard"  : hard-warning copy ("data is more than 24 h old").
 */
export function renderReshapeStalenessBanner(
  staleness: string | undefined,
  collectedAt: string | null | undefined,
): void {
  const BANNER_ID = 'ri-exchange-recommendations-freshness';
  // Locate or create the banner slot. It lives immediately before
  // ri-exchange-recommendations-list in the DOM.
  let banner = document.getElementById(BANNER_ID);
  if (!banner) {
    const listEl = document.getElementById('ri-exchange-recommendations-list');
    if (!listEl || !listEl.parentElement) return;
    banner = document.createElement('div');
    banner.id = BANNER_ID;
    listEl.parentElement.insertBefore(banner, listEl);
  }

  if (!staleness) {
    banner.textContent = '';
    banner.className = '';
    banner.removeAttribute('role');
    return;
  }

  // Build the age label from collectedAt when available.
  let ageLabel = '';
  if (collectedAt) {
    const ms = Date.now() - new Date(collectedAt).getTime();
    const hours = Math.floor(ms / (1000 * 60 * 60));
    const mins = Math.floor((ms % (1000 * 60 * 60)) / (1000 * 60));
    if (hours > 48) {
      const days = Math.floor(hours / 24);
      ageLabel = ` (last collected ${days} days ago)`;
    } else {
      ageLabel = hours > 0 ? ` (last collected ${hours}h${mins > 0 ? ` ${mins}m` : ''} ago)` : ` (last collected ${mins}m ago)`;
    }
  }

  const isSoft = staleness === 'soft';
  const copy = isSoft
    ? `Cross-family alternatives are based on Cost Explorer recommendations that are more than 12h old${ageLabel}. Some prices may be stale.`
    : `Cross-family alternatives are based on Cost Explorer recommendations older than 24h${ageLabel}. Prices may be significantly out of date.`;
  renderTableCallout(banner, isSoft ? 'warning' : 'error', [copy]);
}

// ──────────────────────────────────────────────
// Column filters (issue #166 follow-up to merged #570)
//
// Wires inline column filters to the reshape-recommendations table via the
// shared lib/column-filters helpers. Categorical columns (Source RI, Current,
// Suggested, Reason) get a checkbox-list popover; numeric columns (Count,
// Utilization, Normalized Used/Purchased) get a free-text expression popover.
// Display-rounded numeric values match what the cell renders so a typed
// value like "95.0" matches the visible utilization figure.
// ──────────────────────────────────────────────

interface RiExchangeColumnDef {
  key: state.RiExchangeColumnId;
  label: string;
  kind: 'numeric' | 'categorical';
}

const RIEX_COLUMN_DEFS: readonly RiExchangeColumnDef[] = [
  { key: 'source_ri_id',          label: 'Source RI',          kind: 'categorical' },
  { key: 'source_count',          label: 'Source count',       kind: 'numeric'     },
  { key: 'source_instance_type',  label: 'Source instance',    kind: 'categorical' },
  { key: 'target_count',          label: 'Target count',       kind: 'numeric'     },
  { key: 'target_instance_type',  label: 'Target instance',    kind: 'categorical' },
  { key: 'utilization_percent',   label: 'Utilization %',      kind: 'numeric'     },
  { key: 'normalized_used',       label: 'Normalized used',    kind: 'numeric'     },
  { key: 'normalized_purchased',  label: 'Normalized purchased', kind: 'numeric'   },
  { key: 'reason',                label: 'Reason',             kind: 'categorical' },
];

const RIEX_NUMERIC_COLUMNS: ReadonlySet<state.RiExchangeColumnId> = new Set(
  RIEX_COLUMN_DEFS.filter((c) => c.kind === 'numeric').map((c) => c.key),
);

function riexCategoricalCellValue(
  r: import('./api').ReshapeRecommendation,
  col: state.RiExchangeColumnId,
): string {
  switch (col) {
    case 'source_ri_id':         return r.source_ri_id ?? '';
    case 'source_instance_type': return r.source_instance_type ?? '';
    case 'target_instance_type': return r.target_instance_type ?? '';
    case 'reason':               return r.reason ?? '';
    // Numeric columns never reach this branch — return '' so the type matches.
    case 'source_count':
    case 'target_count':
    case 'utilization_percent':
    case 'normalized_used':
    case 'normalized_purchased': return '';
  }
}

function riexNumericCellValue(
  r: import('./api').ReshapeRecommendation,
  col: state.RiExchangeColumnId,
): number {
  switch (col) {
    case 'source_count':         return r.source_count ?? 0;
    case 'target_count':         return r.target_count ?? 0;
    case 'utilization_percent':  return r.utilization_percent ?? Number.NaN;
    case 'normalized_used':      return r.normalized_used ?? Number.NaN;
    case 'normalized_purchased': return r.normalized_purchased ?? Number.NaN;
    // Categorical columns never reach this branch — NaN fails every predicate.
    case 'source_ri_id':
    case 'source_instance_type':
    case 'target_instance_type':
    case 'reason':               return Number.NaN;
  }
}

// Decimal places the cell renders with — mirrors the toFixed() calls below
// in the row markup so a user typing the displayed value matches the
// rounded cell value. Counts render as integers; utilization and
// normalized units render with one decimal place.
function riexDisplayPrecision(col: state.RiExchangeColumnId): number {
  switch (col) {
    case 'source_count':
    case 'target_count':
      return 0;
    case 'utilization_percent':
    case 'normalized_used':
    case 'normalized_purchased':
      return 1;
    case 'source_ri_id':
    case 'source_instance_type':
    case 'target_instance_type':
    case 'reason':
      return 0;
  }
}

function riexRoundForDisplay(n: number, precision: number): number {
  if (!Number.isFinite(n)) return n;
  return Number(n.toFixed(precision));
}

export function applyRiExchangeColumnFilters(
  recs: readonly import('./api').ReshapeRecommendation[],
  filters: state.RiExchangeColumnFilters,
): import('./api').ReshapeRecommendation[] {
  return applyColumnFiltersLib<
    import('./api').ReshapeRecommendation,
    state.RiExchangeColumnId
  >(recs, filters, {
    categorical: riexCategoricalCellValue,
    numeric: (r, col) => riexRoundForDisplay(riexNumericCellValue(r, col), riexDisplayPrecision(col)),
  });
}

// ── Active Convertible RIs column filters (issue #1414) ──────────────────

interface ActiveRiColumnDef {
  key: state.ActiveRiColumnId;
  label: string;
  kind: 'numeric' | 'categorical';
}

const ACTIVE_RI_COLUMN_DEFS: readonly ActiveRiColumnDef[] = [
  { key: 'instance_type',     label: 'Instance Type', kind: 'categorical' },
  { key: 'availability_zone', label: 'AZ',            kind: 'categorical' },
  { key: 'offering_type',     label: 'Offering',      kind: 'categorical' },
  { key: 'instance_count',    label: 'Count',         kind: 'numeric'     },
  { key: 'utilization_pct',   label: 'Utilization %', kind: 'numeric'     },
];

const ACTIVE_RI_NUMERIC_COLUMNS: ReadonlySet<state.ActiveRiColumnId> = new Set(
  ACTIVE_RI_COLUMN_DEFS.filter((c) => c.kind === 'numeric').map((c) => c.key),
);

function activeRiLabelFor(col: state.ActiveRiColumnId): string {
  return ACTIVE_RI_COLUMN_DEFS.find((c) => c.key === col)?.label ?? col;
}

function activeRiCategoricalCellValue(ri: ConvertibleRI, col: state.ActiveRiColumnId): string {
  switch (col) {
    case 'instance_type':     return ri.instance_type ?? '';
    case 'availability_zone': return ri.availability_zone ?? '';
    case 'offering_type':     return ri.offering_type ?? '';
    case 'instance_count':
    case 'utilization_pct':   return '';
  }
}

function activeRiNumericCellValue(
  ri: ConvertibleRI,
  util: Map<string, RIUtilization>,
  col: state.ActiveRiColumnId,
): number {
  switch (col) {
    case 'instance_count':  return ri.instance_count ?? 0;
    case 'utilization_pct': {
      const u = util.get(ri.reserved_instance_id);
      return u !== undefined ? u.utilization_percent : Number.NaN;
    }
    case 'instance_type':
    case 'availability_zone':
    case 'offering_type':   return Number.NaN;
  }
}

function activeRiDisplayPrecision(col: state.ActiveRiColumnId): number {
  switch (col) {
    case 'instance_count':  return 0;
    case 'utilization_pct': return 1;
    case 'instance_type':
    case 'availability_zone':
    case 'offering_type':   return 0;
  }
}

/**
 * Pure filter function for the Active Convertible RIs table. Exported so
 * unit tests can exercise the filter pipeline without DOM setup. Mirrors
 * applyRiExchangeColumnFilters.
 */
export function applyActiveRiColumnFilters(
  ris: readonly ConvertibleRI[],
  util: Map<string, RIUtilization>,
  filters: state.ActiveRiColumnFilters,
): ConvertibleRI[] {
  return applyColumnFiltersLib<ConvertibleRI, state.ActiveRiColumnId>(ris, filters, {
    categorical: activeRiCategoricalCellValue,
    numeric: (ri, col) => {
      const raw = activeRiNumericCellValue(ri, util, col);
      if (!Number.isFinite(raw)) return raw;
      return Number(raw.toFixed(activeRiDisplayPrecision(col)));
    },
  });
}

// ── Active RI popover ─────────────────────────────────────────────────────
// Lifecycle lives in lib/column-filter-popover.ts (issue #18).

const ACTIVE_RI_POPOVER_SCOPE = 'active-ri';

function rerenderRIs(): void {
  const container = document.getElementById('ri-exchange-instances-list');
  if (container) renderRIsTable(container, riexState.currentRIAccountID);
}

function activeRiTrigger(column: state.ActiveRiColumnId): HTMLButtonElement | null {
  return document.querySelector<HTMLButtonElement>(
    `#ri-exchange-instances-list .column-filter-btn[data-column="${column}"]`,
  );
}

function activeRiOpenPopoverFor(column: state.ActiveRiColumnId, anchor: HTMLElement): void {
  openColumnFilterPopover({
    scope: ACTIVE_RI_POPOVER_SCOPE,
    column,
    label: activeRiLabelFor(column),
    kind: ACTIVE_RI_NUMERIC_COLUMNS.has(column) ? 'numeric' : 'categorical',
    placeholder: 'e.g. >50, 10..20, 4',
    distinctValues: sortedDistinctValues(riexState.currentRIs, (ri) => activeRiCategoricalCellValue(ri, column)),
    getFilter: () => state.getActiveRiColumnFilters()[column],
    onCommit: (filter) => {
      state.setActiveRiColumnFilter(column, filter);
      rerenderRIs();
    },
    findTrigger: () => activeRiTrigger(column),
  }, anchor);
}

// ── Reshape recommendations popover ──────────────────────────────────────

const RIEX_POPOVER_SCOPE = 'riex';

function riexLabelFor(col: state.RiExchangeColumnId): string {
  return RIEX_COLUMN_DEFS.find((c) => c.key === col)?.label ?? col;
}

function riexTrigger(column: state.RiExchangeColumnId): HTMLButtonElement | null {
  return document.querySelector<HTMLButtonElement>(
    `#ri-exchange-recommendations-list .column-filter-btn[data-column="${column}"]`,
  );
}

function riexOpenPopoverFor(column: state.RiExchangeColumnId, anchor: HTMLElement): void {
  openColumnFilterPopover({
    scope: RIEX_POPOVER_SCOPE,
    column,
    label: riexLabelFor(column),
    kind: RIEX_NUMERIC_COLUMNS.has(column) ? 'numeric' : 'categorical',
    placeholder: 'e.g. >50, 70..95, 1',
    distinctValues: sortedDistinctValues(riexState.currentRecommendations, (r) => riexCategoricalCellValue(r, column)),
    getFilter: () => state.getRiExchangeColumnFilters()[column],
    onCommit: (filter) => {
      state.setRiExchangeColumnFilter(column, filter);
      rerenderReshape();
    },
    findTrigger: () => riexTrigger(column),
  }, anchor);
}

function rerenderReshape(): void {
  const container = document.getElementById('ri-exchange-recommendations-list');
  if (container) renderRecommendations(container);
}

// Empty-state copy for a loaded, non-empty RI list. Only RIs with known
// utilization can be said to meet the threshold (issue #1687).
function reshapeFleetCopy(): string {
  const total = riexState.currentRIs.length;
  const plural = total === 1 ? '' : 's';
  if (riexState.utilizationStatus === 'loading') return 'No reshape recommendations right now.';
  const unknown = riexState.currentRIs.filter(ri => !hasKnownUtilization(ri.reserved_instance_id)).length;
  if (unknown === 0) {
    return `All ${total} convertible RI${plural} meet your utilization threshold. No reshape needed.`;
  }
  return `No reshape recommendations right now. Utilization data is unavailable for ${unknown} of ${total} convertible RI${plural}, so ${unknown === 1 ? 'it was' : 'they were'} not checked against your utilization threshold.`;
}

function renderRecommendations(container: HTMLElement): void {
  if (!riexState.currentRecommendations || riexState.currentRecommendations.length === 0) {
    // The "well-utilized" copy is only truthful when the RI fleet actually
    // contains convertibles to be utilized. Prior to this commit the same
    // message fired regardless of fleet presence, so a brand-new tenant saw
    // a claim about utilization of a non-existent fleet.
    // Until this scope's RI list is in (or when it failed to load), say
    // nothing about the fleet rather than quote a stale count (#1135).
    const copy = !riexState.currentRIsLoaded
      ? 'No reshape recommendations right now.'
      : riexState.currentRIs.length === 0
        ? 'Reshape recommendations appear once your accounts have active convertible Reserved Instances — none are registered yet.'
        : reshapeFleetCopy();
    container.innerHTML = `<p class="empty">${escapeHtml(copy)}</p>`;
    return;
  }

  // Issue #365: same admin-only gate as the convertible-RI table.
  const canExchange = canAccess('admin', '*');

  // Apply per-column filters before rendering. Each filter is ANDed with
  // the others; broken numeric expressions are skipped so the popover can
  // surface the error without forcing the user to clear the field first.
  const filters = state.getRiExchangeColumnFilters();
  const visible = applyRiExchangeColumnFilters(riexState.currentRecommendations, filters);

  const filterBtn = (column: state.RiExchangeColumnId): string => {
    const active = filters[column] ? ' active' : '';
    const lbl = riexLabelFor(column);
    const label = filters[column] ? `Filter ${lbl} — currently active` : `Filter ${lbl}`;
    return `<button type="button" class="column-filter-btn${active}" data-column="${column}" aria-haspopup="dialog" aria-expanded="false" aria-label="${escapeHtml(label)}" title="${escapeHtml(label)}">${iconSvg('filter')}</button>`;
  };
  // Columns that pack two values ("4x m5.large", "26.6 / 64.0") have one
  // filter per value. Two bare funnels side by side gave no hint which was
  // which, so each one is named under the header label. The visible word is
  // part of the button's accessible name ("Filter Source count").
  const namedFilterBtn = (column: state.RiExchangeColumnId, name: string): string =>
    filterBtn(column).replace('</button>', `<span class="column-filter-btn-name">${name}</span></button>`);
  const filterGroup = (...btns: string[]): string => `<span class="th-filter-group">${btns.join('')}</span>`;

  // Render an indexable list so per-row Exchange buttons can find the
  // matching original recommendation (idx into riexState.currentRecommendations).
  // Index the full list once instead of an indexOf scan per visible row,
  // which made the row build quadratic (#319). The first index wins, as
  // with indexOf.
  const indexByRec = new Map<ReshapeRecommendation, number>();
  riexState.currentRecommendations.forEach((rec, i) => {
    if (!indexByRec.has(rec)) indexByRec.set(rec, i);
  });
  const visibleWithIdx = visible.map((rec) => ({
    rec,
    idx: indexByRec.get(rec) ?? -1,
  }));

  container.innerHTML = `
    <table class="table-compact${canExchange ? ' table-sticky-end' : ''}">
      <thead>
        <tr>
          <th>Source RI${filterBtn('source_ri_id')}</th>
          <th>Current${filterGroup(namedFilterBtn('source_count', 'count'), namedFilterBtn('source_instance_type', 'instance'))}</th>
          <th>Suggested${filterGroup(namedFilterBtn('target_count', 'count'), namedFilterBtn('target_instance_type', 'instance'))}</th>
          <th>Alternatives</th>
          <th>Utilization${filterBtn('utilization_percent')}</th>
          <th>Normalized Units${filterGroup(namedFilterBtn('normalized_used', 'used'), namedFilterBtn('normalized_purchased', 'purchased'))}</th>
          <th>Reason${filterBtn('reason')}</th>
          ${canExchange ? '<th>Actions</th>' : ''}
        </tr>
      </thead>
      <tbody>
        ${visibleWithIdx.length === 0 ? `<tr><td colspan="${canExchange ? 8 : 7}" class="empty">No rows match the active column filters. Clear a filter to see them.</td></tr>` : ''}
        ${visibleWithIdx.map(({ rec, idx }) => {
          // The API can omit utilization_percent (the filter extractor
          // already maps it to NaN). Unknown renders as a neutral em-dash:
          // calling toFixed on it threw and left the panel on its skeleton,
          // and undefined >= 70 is false, which painted unknown as red.
          const utilPct = typeof rec.utilization_percent === 'number' && Number.isFinite(rec.utilization_percent)
            ? rec.utilization_percent
            : null;
          const utilClass = utilPct === null ? '' : utilPct >= 95 ? 'util-green' : utilPct >= 70 ? 'util-yellow' : 'util-red';
          const altCell = renderAlternativesCell(rec.alternative_targets);
          return `
          <tr>
            <td class="monospace"><span class="cell-truncate cell-truncate--xs" title="${escapeHtml(rec.source_ri_id)}">${escapeHtml(rec.source_ri_id)}</span></td>
            <td>${rec.source_count}x ${escapeHtml(rec.source_instance_type)}</td>
            <td>${renderSuggestedCell(rec)}</td>
            <td class="cell-wrap">${altCell}</td>
            <td class="${utilClass}">${utilPct === null ? '—' : `${utilPct.toFixed(1)}%`}</td>
            <td>${rec.normalized_used.toFixed(1)} / ${rec.normalized_purchased.toFixed(1)}</td>
            <td class="cell-wrap">${escapeHtml(rec.reason)}</td>
            ${canExchange ? `<td>${renderReshapeExchangeButton(rec, idx)}</td>` : ''}
          </tr>`;
        }).join('')}
      </tbody>
    </table>
  `;

  // Wire per-column filter buttons.  e.stopPropagation prevents the
  // surrounding <th> from also handling the click (no sort handler today,
  // but matches the pattern in recommendations.ts so future <th>-level
  // handlers won't conflict).
  container.querySelectorAll<HTMLButtonElement>('.column-filter-btn').forEach((btn) => {
    const column = btn.dataset['column'] as state.RiExchangeColumnId | undefined;
    if (!column) return;
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      riexOpenPopoverFor(column, btn);
    });
  });
  // Keep an open filter popover attached to the re-rendered trigger.
  rebindColumnFilterPopover(RIEX_POPOVER_SCOPE);

  // Attach "Exchange" handlers. Same as renderRIsTable: for non-admin
  // sessions the selector matches zero elements and this loop is a no-op.
  container.querySelectorAll<HTMLButtonElement>('[data-action="fill-quote"]').forEach(btn => {
    btn.addEventListener('click', () => {
      const idx = parseInt(btn.dataset['index'] || '0', 10);
      const rec = riexState.currentRecommendations[idx];
      if (rec) fillQuoteFromRecommendation(rec);
    });
  });
}

// renderAlternativesCell formats the cross-family alternative targets
// as a comma-separated list of "<instance_type> $X.XX/mo" chips inside
// a single table cell. Each instance_type is escapeHtml'd; cost values
// go through the shared formatCurrency helper (digits: 2) and the
// currency prefix is escaped too, so there's no HTML injection vector.
function renderAlternativesCell(alternatives: OfferingOption[] | undefined): string {
  if (!alternatives || alternatives.length === 0) {
    return '—';
  }
  return alternatives
    .map((alt) => `<span class="cost-chip">${escapeHtml(alt.instance_type)} ${escapeHtml(formatOfferingCost(alt))}/mo</span>`)
    .join(' ');
}

// hasReshapeTarget reports whether a reshape recommendation names a primary
// exchange target. The analyzer emits target_instance_type "" and
// target_count 0 when it has none, for example for a completely unused RI
// or one whose target would exceed the units the account owns (#1410).
function hasReshapeTarget(rec: ReshapeRecommendation): boolean {
  return Boolean(rec.target_instance_type) && rec.target_count > 0;
}

// canExchangeReshape reports whether the row's Exchange button has anything
// to offer: a primary target, or cross-family alternatives the modal's picker
// can preselect from.
function canExchangeReshape(rec: ReshapeRecommendation): boolean {
  return hasReshapeTarget(rec) || (rec.alternative_targets?.length ?? 0) > 0;
}

function renderSuggestedCell(rec: ReshapeRecommendation): string {
  if (!hasReshapeTarget(rec)) {
    return '<span class="text-muted">No exchange target</span>';
  }
  return `${rec.target_count}x ${escapeHtml(rec.target_instance_type)}`;
}

// renderReshapeExchangeButton renders the row's Exchange action. A row with
// nothing to exchange into gets a disabled button with no data-action, so no
// click handler is attached and the reason is still discoverable on hover.
function renderReshapeExchangeButton(rec: ReshapeRecommendation, idx: number): string {
  if (!canExchangeReshape(rec)) {
    const why = 'No exchange target available for this RI';
    return `<button type="button" class="btn-small" disabled title="${why}" aria-label="Exchange: ${why}">Exchange</button>`;
  }
  return `<button type="button" class="btn-small" data-action="fill-quote" data-index="${idx}">Exchange</button>`;
}

function fillQuoteFromRecommendation(rec: ReshapeRecommendation): void {
  // With no primary target the modal opens on an empty target row for the
  // alternatives; a count of 0 would only fail validation.
  const count = hasReshapeTarget(rec) ? rec.target_count : 1;
  openExchangeModal(rec.source_ri_id, count, rec.target_instance_type, rec.alternative_targets);
}

export function fillQuoteFromRI(riId: string, count: number): void {
  openExchangeModal(riId, count);
}

export { currencyPrefix } from './riexflow/shared';
export { loadAutomationSettings, saveAutomationSettings } from './riexflow/automation';
export { openExchangeModal } from './riexflow/exchangeModal';
