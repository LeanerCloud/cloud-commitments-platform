import * as api from '../api';
import type { APIKeyInfo } from '../types';
import { loadApiKeysUsageStats } from '../apikeys_usage';
import { escapeHtml, formatDateTime, formatRelativeTime } from '../utils';
import { confirmDialog } from '../confirmDialog';
import { friendlyError } from '../api/errors';
import { showToast } from '../toast';
import { iconSvg } from '../icons';
import { showSkeletonRows, teardownSkeleton } from '../lib/skeleton';

// State for modal management
let currentApiKeys: APIKeyInfo[] = [];

/**
 * Cell content for a request count the backend cannot report, i.e. a key
 * already in use before migration 000094 introduced the counters. Static
 * markup with no interpolated data, matching the "Never" treatment the
 * Last Used and Expires columns already use for absent values.
 */
const NO_COUNT_DATA = '<span class="text-muted" title="Not recorded for this key">n/a</span>';

/**
 * Load and display API keys.
 *
 * Defensive parsing (issue #9): the previous implementation read
 * `response.keys` while the backend returns `{api_keys: [...]}`,
 * leaving `currentApiKeys` set to undefined and crashing the next
 * render with "Cannot read properties of undefined (reading 'length')".
 * Read the documented `api_keys` field, then fall back to a bare array
 * (some other deployments/proxies might unwrap) and finally to `[]` so
 * a contract drift can never crash the page.
 *
 * The usage-stats summary loads in parallel -- a failure in either path
 * only takes down its own region, never both. See loadApiKeysUsageStats
 * for the section header's lifecycle.
 */
export async function loadApiKeys(): Promise<void> {
  const listContainer = document.getElementById('apikeys-list');
  if (listContainer) {
    showSkeletonRows(listContainer, 3, 9);
  }
  // Kick the summary off in parallel -- it has its own container + error
  // path so we don't await it inside the list flow.
  void loadApiKeysUsageStats();

  try {
    const response = await api.getApiKeys();
    const list = (response as { api_keys?: APIKeyInfo[] } | undefined)?.api_keys
      ?? (Array.isArray(response) ? response as APIKeyInfo[] : undefined);
    // Neither known shape (a wrapped envelope, a truncated 200 resolving as
    // null) is an error, not "no keys": an empty state would tell an operator
    // reviewing credentials that none exist.
    if (!Array.isArray(list)) throw new Error('Unexpected API keys response shape');
    currentApiKeys = list;
    renderApiKeysList();
  } catch (error) {
    console.error('Failed to load API keys:', error);
    if (listContainer) teardownSkeleton(listContainer);
    renderApiKeysListError(listContainer, 'Failed to load API keys');
    showError('Failed to load API keys');
  }
}

/**
 * Replace the API keys list region with an inline error message so the
 * shimmer skeleton doesn't sit beside a stale empty table after a
 * failed fetch. Uses textContent (no innerHTML) to stay XSS-safe and
 * matches the patterns used by other modules (e.g. dashboard.ts).
 */
/**
 * ISO timestamp for a title attribute, or '' for an unparseable value:
 * toISOString throws a RangeError on an Invalid Date, and that throw inside
 * the row map blanked the whole keys table, Revoke buttons included (#136).
 */
function isoOrEmpty(value: string): string {
  const d = new Date(value);
  return isNaN(d.getTime()) ? '' : d.toISOString();
}

function renderApiKeysListError(container: HTMLElement | null, message: string): void {
  if (!container) return;
  container.replaceChildren();
  const p = document.createElement('p');
  p.className = 'error';
  p.textContent = message;
  container.appendChild(p);
}

/**
 * Render API keys list
 */
export function renderApiKeysList(): void {
  const container = document.getElementById('apikeys-list');
  if (!container) return;

  if (!Array.isArray(currentApiKeys) || currentApiKeys.length === 0) {
    // DOM construction rather than template literal so the security hook
    // doesn't flag the innerHTML write — and all copy is static anyway.
    container.replaceChildren();
    delete container.dataset['skeletonActive'];
    const wrap = document.createElement('div');
    wrap.className = 'empty apikeys-empty';
    const h = document.createElement('h4');
    h.textContent = 'No API keys yet';
    const p = document.createElement('p');
    p.textContent = 'Create an API key to let automation tools (CI pipelines, scripts, integrations) call CUDly programmatically. Each key can be revoked or rotated at any time.';
    wrap.appendChild(h);
    wrap.appendChild(p);
    container.appendChild(wrap);
    return;
  }

  const table = `
    <table class="data-table table-compact table-sticky-end">
      <thead>
        <tr>
          <th>Name</th>
          <th>Key Prefix</th>
          <th>Status</th>
          <th>Created</th>
          <th>Last Used</th>
          <th class="num">Requests (window)</th>
          <th class="num">Requests (total)</th>
          <th>Expires</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        ${currentApiKeys.map(key => {
          const isExpired = key.expires_at && new Date(key.expires_at) < new Date();
          const statusClass = !key.is_active ? 'badge-danger' : isExpired ? 'badge-warning' : 'badge-success';
          const statusText = !key.is_active ? 'Revoked' : isExpired ? 'Expired' : 'Active';
          const countWindow = formatRequestCount(key.request_count_window);
          const countTotal = formatRequestCount(key.request_count_total);

          return `
            <tr>
              <td><strong>${escapeHtml(key.name)}</strong></td>
              <td><code>${escapeHtml(key.key_prefix)}...</code></td>
              <td><span class="badge ${statusClass}">${statusText}</span></td>
              <td>${formatDateTime(key.created_at)}</td>
              <td>${key.last_used_at ? `<span title="${escapeHtml(isoOrEmpty(key.last_used_at))}">${escapeHtml(formatRelativeTime(key.last_used_at) || '--')}</span>` : '<span class="text-muted">Never</span>'}</td>
              <td class="num apikeys-count-cell">${countWindow}</td>
              <td class="num apikeys-count-cell">${countTotal}</td>
              <td>${key.expires_at ? formatDateTime(key.expires_at) : '<span class="text-muted">Never</span>'}</td>
              <td>
                <div class="row-actions">
                  ${key.is_active && !isExpired ? `<button type="button" class="btn-small btn-warning icon-btn revoke-key-btn" data-key-id="${escapeHtml(key.id)}" aria-label="Revoke API key" title="Revoke API key">${iconSvg('slash')}</button>` : ''}
                  <button type="button" class="btn-small btn-danger icon-btn delete-key-btn" data-key-id="${escapeHtml(key.id)}" aria-label="Delete API key" title="Delete API key">${iconSvg('trash-2')}</button>
                </div>
              </td>
            </tr>
          `;
        }).join('')}
      </tbody>
    </table>
  `;

  // Clear the skeleton marker before the render — the existing
  // innerHTML write (kept intact for diff minimality) replaces all
  // children, and we don't want a stale `data-skeleton-active`
  // attribute lingering on the live table.
  delete container.dataset['skeletonActive'];
  container.innerHTML = table;

  // Add event delegation after rendering
  container.querySelectorAll('.revoke-key-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const keyId = (btn as HTMLElement).dataset.keyId;
      if (keyId) void revokeApiKey(keyId);
    });
  });
  container.querySelectorAll('.delete-key-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const keyId = (btn as HTMLElement).dataset.keyId;
      if (keyId) void deleteApiKey(keyId);
    });
  });
}

/**
 * Revoke an API key
 */
export async function revokeApiKey(keyId: string): Promise<void> {
  const key = currentApiKeys.find(k => k.id === keyId);
  if (!key) return;

  const ok = await confirmDialog({
    title: `Revoke API key "${key.name}"?`,
    body: 'The key will immediately stop working. This action cannot be undone. (You can delete the row afterwards to remove it from the list.)',
    confirmLabel: 'Revoke key',
    destructive: true,
  });
  if (!ok) return;

  try {
    await api.revokeApiKey(keyId);
    await loadApiKeys();
  } catch (error) {
    console.error('Failed to revoke API key:', error);
    showError(`Failed to revoke API key: ${friendlyError(error)}`);
  }
}

/**
 * Delete an API key
 */
export async function deleteApiKey(keyId: string): Promise<void> {
  const key = currentApiKeys.find(k => k.id === keyId);
  if (!key) return;

  const ok = await confirmDialog({
    title: `Delete API key "${key.name}"?`,
    body: 'This permanently removes the key from the list. If the key is still active, it will also stop working.',
    confirmLabel: 'Delete key',
    destructive: true,
  });
  if (!ok) return;

  try {
    await api.deleteApiKey(keyId);
    await loadApiKeys();
  } catch (error) {
    console.error('Failed to delete API key:', error);
    showError(`Failed to delete API key: ${friendlyError(error)}`);
  }
}

/**
 * Show error message.
 *
 * When the Create-API-Key modal is open, validation-style errors belong
 * inline on the form (so the user sees them next to the offending
 * field). Outside that context — load failures, revoke/delete errors —
 * we surface via the shared toast system (Q4) so the message matches
 * the rest of the app rather than using a blocking alert().
 */
export function showError(message: string): void {
  const errorEl = document.getElementById('create-apikey-error');
  const createModalOpen = !document.getElementById('create-apikey-modal')?.classList.contains('hidden');
  if (errorEl && createModalOpen) {
    errorEl.textContent = message;
    errorEl.classList.remove('hidden');
  } else {
    showToast({ message, kind: 'error' });
  }
}

/**
 * Format a per-row request count for display in the table cells.
 * Renders the exact integer (no abbreviation) so a row with "1,234,567"
 * is unambiguous — the section-level summary card in apikeys_usage.ts
 * uses a separate `formatCount` that abbreviates large values to fit
 * the tile width.
 *
 * `null` means the backend does not know the count (a key already in use
 * before migration 000094 added the counters) and renders as NO_COUNT_DATA
 * ("n/a") rather than "0", which would state a request volume nobody
 * measured.
 * `undefined` (an older cached response with no counter fields at all) is
 * treated the same way, as is any non-finite or negative value.
 */
function formatRequestCount(n: number | null | undefined): string {
  if (typeof n !== 'number' || !Number.isFinite(n) || n < 0) return NO_COUNT_DATA;
  return Math.trunc(n).toLocaleString('en-US');
}
