/**
 * Lightweight column-filter popover shared by the two History tables
 * (Purchase History + Approval Queue) — issue #166 follow-up.
 *
 * Thin adapter over lib/column-filter-popover.ts (issue #18), which owns
 * the popover DOM and lifecycle. This module keeps the History-specific
 * parts: the scope-qualified `.history-column-filter-btn` trigger, its
 * renderer, and the caller-owned filter state and re-render callback.
 * Both tables on the Purchases tab share column ids, so each trigger is
 * identified by (scope, column).
 */

import {
  type ColumnFilterKind,
} from './column-filters';
import { escapeHtmlAttr } from '../utils';
import { iconSvg } from '../icons';
import {
  closeColumnFilterPopover,
  isColumnFilterPopoverOpen,
  openColumnFilterPopover,
  rebindColumnFilterPopover,
} from './column-filter-popover';

export interface PopoverConfig<TColumnId extends string> {
  // Namespace of the owning table (e.g. 'history' / 'queue'). Both tables
  // on the Purchases tab use the same bare column ids, so the open-popover
  // key, the anchor lookup and the generated element ids are all qualified
  // by scope; otherwise one table's popover toggles or refocuses the other
  // table's button (issue #281). Must match the scope passed to
  // renderHistoryFilterButton.
  scope: string;
  column: TColumnId;
  anchor: HTMLElement;
  // Current filter for this column (null = not narrowed).
  currentFilter: ColumnFilterKind | undefined;
  // Header label rendered inside the popover ("Filter <Label>").
  headerLabel: string;
  // 'numeric' → free-text expression input; 'categorical' → checkbox list.
  kind: 'numeric' | 'categorical';
  // Categorical only: distinct raw cell values (post-extract). Order is
  // preserved as the caller provided it (caller controls sort).
  distinctValues?: readonly string[];
  // Categorical only: render-time label resolver (e.g. account_id → name).
  displayLabel?: (value: string) => string;
  // Commit callback: caller writes to state then re-renders the table.
  // Passing `null` clears the filter (no narrowing applied).
  onCommit: (filter: ColumnFilterKind | null) => void;
}

const TRIGGER_SELECTOR = '.history-column-filter-btn';

// Resolve a table's filter button by (scope, column). Document-wide, because
// the button is re-created on every table re-render, but never ambiguous
// across tables thanks to the scope attribute.
function findFilterButton(scope: string, column: string): HTMLElement | null {
  return document.querySelector<HTMLElement>(
    `${TRIGGER_SELECTOR}[data-filter-scope="${CSS.escape(scope)}"][data-column="${CSS.escape(column)}"]`,
  );
}

export function closeOpenHistoryPopover(restoreFocus = false): void {
  closeColumnFilterPopover(restoreFocus);
}

/**
 * Open the popover anchored to `anchor`. Toggles closed if the same
 * column is already open. Closes any previously-open popover (from
 * either History table — only one is open at a time, even across
 * the two tables, to avoid stacking dialogs).
 */
export function openHistoryColumnPopover<TColumnId extends string>(
  config: PopoverConfig<TColumnId>,
): void {
  const column = String(config.column);
  // The caller's commit re-renders its table (innerHTML), which replaces the
  // trigger button; rebind re-anchors the still-open popover to the new one.
  // The state lives with the caller, so track the committed filter here for
  // the resync that rebind performs.
  let current: ColumnFilterKind | null = config.currentFilter ?? null;
  openColumnFilterPopover({
    scope: config.scope,
    column,
    label: config.headerLabel,
    kind: config.kind,
    distinctValues: config.distinctValues,
    displayLabel: config.displayLabel,
    getFilter: () => current,
    onCommit: (filter) => {
      current = filter;
      config.onCommit(filter);
      rebindColumnFilterPopover(config.scope);
    },
    findTrigger: () => findFilterButton(config.scope, column),
    triggerSelector: TRIGGER_SELECTOR,
    followOnScroll: true,
  }, config.anchor);
}

/**
 * Render the filter-icon button placed inside a `<th>` cell. The caller
 * binds the click handler against `.history-column-filter-btn` after the
 * table is innerHTML-rewritten. `scope` names the owning table and must
 * match the `scope` the caller passes to openHistoryColumnPopover.
 */
export function renderHistoryFilterButton<TColumnId extends string>(
  column: TColumnId,
  label: string,
  active: boolean,
  scope: string,
): string {
  const cls = `history-column-filter-btn${active ? ' active' : ''}`;
  const ariaLabel = escapeHtmlAttr(
    active ? `Filter ${label} - currently active` : `Filter ${label}`,
  );
  const safeColumn = escapeHtmlAttr(String(column));
  const safeScope = escapeHtmlAttr(scope);
  // A re-render while this column's popover is open keeps it expanded.
  const expanded = isColumnFilterPopoverOpen(scope, String(column));
  // Use the same filter icon the recommendations popover uses so the
  // two surfaces are visually identical to the user.
  return `<button type="button" class="${cls}" data-filter-scope="${safeScope}" data-column="${safeColumn}" aria-haspopup="dialog" aria-expanded="${expanded}" aria-label="${ariaLabel}" title="${ariaLabel}">${iconSvg('filter')}</button>`;
}
