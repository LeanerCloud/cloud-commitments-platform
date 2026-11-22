import * as api from '../api';
import { escapeHtml } from '../utils';
import { showSkeletonRows, teardownSkeleton } from '../lib/skeleton';
import { riexState } from './riexState';
import { renderRIsTable, renderAzureRIsTable } from './riTable';
import { rerenderReshape } from './reshapeTable';

// ──────────────────────────────────────────────
// Convertible RIs table
// ──────────────────────────────────────────────

export async function loadConvertibleRIs(accountID?: string, generation: number = riexState.scopeGeneration): Promise<void> {
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
export async function loadExchangeableAzureRIs(accountID: string | undefined, generation: number): Promise<void> {
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
