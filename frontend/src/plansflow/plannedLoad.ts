import * as api from '../api';
import * as state from '../state';
import { iconSvg } from '../icons';
import type { PlannedPurchase } from '../api';
import { showSkeletonRows, teardownSkeleton } from '../lib/skeleton';
import { renderErrorCallout } from '../lib/error-callout';
import { rebindColumnFilterPopover } from '../lib/column-filter-popover';
import { plansState } from './plansState';
import { applyPlansColumnFilters, PLANS_POPOVER_SCOPE, openPlansColumnPopover } from './plannedColumns';
import { renderPlannedPurchaseRow } from './plannedRows';
import { handlePlannedPurchaseAction } from './plannedActions';

let plannedLoadGeneration = 0;

/**
 * Load planned purchases
 */
export async function loadPlannedPurchases(): Promise<void> {
  const container = document.getElementById('planned-purchases-list');
  if (!container) return;
  const generation = ++plannedLoadGeneration;

  // Issue #344 T3 (CR follow-up on PR #346): skeleton lives here, not
  // in loadPlans, so direct callers (e.g. follow-up refresh paths after
  // a single purchase action) also get the loading affordance. 5 rows
  // × 11 cols matches the rendered table — see renderPlannedPurchases.
  showSkeletonRows(container, 5, 11);

  try {
    const data = await api.getPlannedPurchases();
    if (generation !== plannedLoadGeneration) return;
    renderPlannedPurchases(data.purchases || []);
  } catch (error) {
    if (generation !== plannedLoadGeneration) return;
    console.error('Failed to load planned purchases:', error);
    teardownSkeleton(container);
    const err = error as Error;
    renderErrorCallout(container, {
      message: `Failed to load planned purchases: ${err.message}`,
      onRetry: () => void loadPlannedPurchases(),
    });
  }
}

/**
 * Render planned purchases list
 */
function renderPlannedPurchases(purchases: PlannedPurchase[]): void {
  plansState.lastLoadedPurchases = [...purchases];
  renderPlannedPurchasesInternal();
}

// renderPlannedPurchasesInternal is the actual render — separate from
// renderPlannedPurchases() so popover commits can re-render the table
// against the cached unfiltered set without re-fetching from the API.
export function renderPlannedPurchasesInternal(): void {
  const container = document.getElementById('planned-purchases-list');
  if (!container) return;

  const purchases = plansState.lastLoadedPurchases;
  if (!purchases || purchases.length === 0) {
    container.innerHTML = '<p class="empty">No planned purchases. Create a purchase plan to schedule automatic purchases.</p>';
    return;
  }

  const filters = state.getPlansColumnFilters();
  const filtered = applyPlansColumnFilters(purchases, filters);

  const filterBtn = (column: state.PlansColumnId, lbl: string): string => {
    const active = filters[column] ? ' active' : '';
    const label = filters[column] ? `Filter ${lbl} \u2014 currently active` : `Filter ${lbl}`;
    return `<button type="button" class="column-filter-btn${active}" data-column="${column}" aria-haspopup="dialog" aria-expanded="false" aria-label="${label}" title="${label}">${iconSvg('filter')}</button>`;
  };

  const tbody = filtered.length === 0
    ? `<tr><td colspan="12" class="empty">No rows match these filters.</td></tr>`
    : filtered.map(purchase => renderPlannedPurchaseRow(purchase)).join('');

  container.innerHTML = `
    <table class="planned-purchases-table table-compact table-sticky-end">
      <thead>
        <tr>
          <th><span>Plan</span>${filterBtn('plan_name', 'Plan')}</th>
          <th><span>Scheduled Date</span>${filterBtn('scheduled_date', 'Scheduled Date')}</th>
          <th><span>Provider</span>${filterBtn('provider', 'Provider')}</th>
          <th><span>Service</span>${filterBtn('service', 'Service')}</th>
          <th><span>Resource Type</span>${filterBtn('resource_type', 'Resource Type')}</th>
          <th class="num"><span>Count</span>${filterBtn('count', 'Count')}</th>
          <th><span>Term</span>${filterBtn('term', 'Term')}</th>
          <th><span>Payment</span>${filterBtn('payment', 'Payment')}</th>
          <th class="num"><span>Upfront</span>${filterBtn('upfront_cost', 'Upfront')}</th>
          <th class="num"><span>Est. Savings</span>${filterBtn('estimated_savings', 'Est. Savings')}</th>
          <th><span>Status</span>${filterBtn('status', 'Status')}</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        ${tbody}
      </tbody>
    </table>
  `;

  // Add event listeners for row action buttons
  container.querySelectorAll<HTMLButtonElement>('[data-action]').forEach(btn => {
    btn.addEventListener('click', () => void handlePlannedPurchaseAction(
      btn.dataset['action'] || '',
      btn.dataset['id'] || '',
      btn.dataset['planId'] || ''
    ));
  });

  // Per-column filter trigger buttons. e.stopPropagation prevents any future
  // surrounding-th handlers (sort etc.) from firing on the same click.
  container.querySelectorAll<HTMLButtonElement>('.column-filter-btn').forEach((btn) => {
    const column = btn.dataset['column'] as state.PlansColumnId | undefined;
    if (!column) return;
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      openPlansColumnPopover(column, btn);
    });
  });

  // Re-anchor any open popover to the freshly-rendered trigger so the
  // popover survives table re-renders triggered by other state changes.
  rebindColumnFilterPopover(PLANS_POPOVER_SCOPE);
}
