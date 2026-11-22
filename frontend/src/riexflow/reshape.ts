import * as api from '../api';
import { escapeHtml } from '../utils';
import type { ReshapeRecommendationsResponse, ExcludedRI } from '../api';
import { showSkeletonRows, teardownSkeleton } from '../lib/skeleton';
import { riexState } from './riexState';
import { inlineReason, renderTableCallout } from './shared';
import { renderRecommendations } from './reshapeTable';

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
