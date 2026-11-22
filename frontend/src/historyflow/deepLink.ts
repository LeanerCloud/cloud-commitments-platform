import { showToast } from '../toast';

// readDeepLinkExecutionID returns the value of the ?execution=<id>
// query parameter inside the current location hash, or '' when
// absent. The app uses hash routing ("#history"), so the "query" piece
// sits inside `window.location.hash` — `window.location.search` is
// empty. Parse it manually:
//   #history?execution=abc123 → 'abc123'
// Exported for unit-test coverage.
export function readDeepLinkExecutionID(): string {
  const hash = window.location.hash || '';
  const q = hash.split('?')[1];
  if (!q) return '';
  return new URLSearchParams(q).get('execution') || '';
}

// applyExecutionDeepLink scrolls the history table to the row matching
// the ?execution=<id> hash query, if any, and flashes a highlight
// class on it. Called after each loadHistory render so links from
// the Recommendations badge AND the scheduled-purchase email's
// Review & Edit button land on the right row. Returns true when a
// match was found + highlighted.
//
// Falsy paths:
//   - No execID in URL: return false silently (the common case — no
//     deeplink was requested).
//   - execID present but no matching row in the rendered list (e.g.
//     the user's date filter excludes the execution, or the row hasn't
//     been ingested yet): surface a non-blocking toast so the user
//     understands why the page didn't jump anywhere, and clear the
//     hash so a follow-up loadHistory() doesn't re-toast the same
//     miss on every re-render.
//
// Exported for unit-test coverage.
export function applyExecutionDeepLink(): boolean {
  const execID = readDeepLinkExecutionID();
  if (!execID) return false;
  // A pending purchase has a row in both the approval queue and the list. The
  // queue row carries the approve/cancel buttons (the list row only links to
  // it), so email and badge deep links land there; any other status is only in
  // the list. Explicit order, so it no longer depends on section order in the page.
  const selector = `tr[data-execution-id="${CSS.escape(execID)}"]`;
  const row = document.querySelector<HTMLTableRowElement>(`#purchases-approval-queue ${selector}`)
    ?? document.querySelector<HTMLTableRowElement>(`#history-list ${selector}`)
    ?? document.querySelector<HTMLTableRowElement>(selector);
  if (!row) {
    // Short-prefix the ID so the toast is readable but the user can
    // still cross-reference against the email if needed.
    const shortID = execID.length > 8 ? `${execID.slice(0, 8)}…` : execID;
    showToast({
      message: `Execution ${shortID} isn't in the current view — clear filters or widen the date range to find it.`,
      kind: 'info',
      timeout: 8_000,
    });
    // Drop the ?execution= from the hash so the next loadHistory()
    // (e.g. user changes a filter) doesn't fire this toast again.
    if (window.location.hash) {
      const baseHash = window.location.hash.split('?')[0] ?? '';
      window.history.replaceState({}, '', window.location.pathname + window.location.search + baseHash);
    }
    return false;
  }
  row.classList.add('history-row-highlight');
  row.scrollIntoView({ behavior: 'smooth', block: 'center' });
  // Fade the highlight after a few seconds so the row goes back to
  // normal styling — otherwise it stays yellow forever and a future
  // user click on a different row looks inconsistent.
  window.setTimeout(() => row.classList.remove('history-row-highlight'), 4000);
  return true;
}
