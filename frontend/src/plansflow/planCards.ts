import * as api from '../api';
import * as state from '../state';
import { formatDate, formatTerm, getStatusBadge, escapeHtml, escapeHtmlAttr, NO_VALUE, providerBadgeHtml } from '../utils';
import { showToast } from '../toast';
import { iconSvg } from '../icons';
import { confirmDialog } from '../confirmDialog';
import type { PlansResponse, LocalPlan } from '../types';
import { viewPlanHistory } from '../history';
import { showSkeletonTiles, teardownSkeleton } from '../lib/skeleton';
import { renderErrorCallout } from '../lib/error-callout';
import { canAccess } from '../permissions';
import { BackendPlan, planMatchesProvider, extractPlanInfo, isPlanOverdue, PRESET_RAMPS, rampRadioValue } from './planModel';
import { healthBadgeHtml } from './planHealth';
import { loadPlannedPurchases } from './plannedLoad';
import { editPlan } from './planEdit';
import { openAddPurchasesModal } from './addPurchases';

// Request generations for loadPlans / loadPlannedPurchases (#1957). A filter
// change starts a new load while an older one may still be in flight; only the
// latest call may render, clear the skeleton or show an error callout.
let plansLoadGeneration = 0;

/**
 * Load plans and planned purchases
 */
export async function loadPlans(): Promise<void> {
  const generation = ++plansLoadGeneration;
  // Issue #344 T3: skeleton tiles for the plans list. Synchronous
  // render before fetch so the page doesn't sit blank during the
  // round-trip. The planned-purchases skeleton lives in
  // loadPlannedPurchases so direct callers of that fetch (not via
  // loadPlans) get the same loading affordance.
  const plansList = document.getElementById('plans-list');
  if (plansList) showSkeletonTiles(plansList, 3);

  // Issue #365: hide the top-level "New Plan" button for sessions that
  // can't create plans. Readonly users hit a 403 on click otherwise.
  // The button itself stays in the DOM (HTML keeps the static markup)
  // so admin/user sessions get the same layout they always did.
  const newPlanBtn = document.getElementById('new-plan-btn');
  if (newPlanBtn) newPlanBtn.hidden = !canAccess('create', 'plans');

  try {
    // Account filter: pass account_ids to the backend so it JOINs
    // plan_accounts and returns only plans that reference one of the
    // selected accounts. Empty array means "all plans" — the backend
    // omits the JOIN entirely in that case. Mirrors the pattern used by
    // getRecommendations (see recommendations.ts, issue #705).
    const accountIDs = state.getCurrentAccountIDs();
    const data = await api.getPlans(
      accountIDs.length > 0 ? { account_ids: accountIDs } : {}
    ) as unknown as PlansResponse;
    let plans = data.plans || [];

    // Client-side provider filter. Backend `config.PurchasePlan` has no
    // top-level `provider` field — the plan's provider is derived from
    // its first service entry (see extractPlanInfo below). Filtering on
    // `p.provider` directly silently returned zero rows for every
    // non-empty filter value.
    // Filter source is the global topbar (state.ts), shared across
    // sections. The topbar's "All Providers" chip writes '' to state,
    // which is falsy so the filter is naturally skipped.
    const providerFilter = state.getCurrentProvider();
    if (providerFilter) {
      plans = plans.filter(p => planMatchesProvider(p as unknown as BackendPlan, providerFilter));
    }

    if (generation !== plansLoadGeneration) return;
    renderPlans(plans);
  } catch (error) {
    if (generation !== plansLoadGeneration) return;
    console.error('Failed to load plans:', error);
    const list = document.getElementById('plans-list');
    if (list) {
      teardownSkeleton(list);
      const err = error as Error;
      renderErrorCallout(list, {
        message: `Failed to load plans: ${err.message}`,
        onRetry: () => void loadPlans(),
      });
    }
  }

  // Load planned purchases
  await loadPlannedPurchases();
}

// Format ramp schedule from backend struct. A preset name is shown only when
// type, percent AND interval match the preset, the same rule Edit uses
// (rampRadioValue), so the list never says "Weekly 20%" for a plan Edit opens
// as Custom 20% / 7 days, or hides a 14-day interval behind "Weekly 25%" (#1185).
function formatBackendRampSchedule(ramp: BackendPlan['ramp_schedule'] | undefined): string {
  if (!ramp) return 'Immediate';
  if (!['immediate', 'weekly', 'monthly', 'custom'].includes(ramp.type)) return ramp.type || 'Unknown';
  const value = rampRadioValue(ramp);
  if (value === 'immediate') return 'Immediate';
  const preset = PRESET_RAMPS.find(p => p.value === value);
  if (preset) return preset.label;
  const days = ramp.step_interval_days;
  return days ? `Custom ${ramp.percent_per_step}% every ${days} day${days === 1 ? '' : 's'}` : `Custom ${ramp.percent_per_step}%`;
}

async function loadPlanAccountNames(planId: string, cardEl: Element): Promise<void> {
  try {
    const accounts = await api.listPlanAccounts(planId);
    if (accounts.length === 0) return;
    const detailsEl = cardEl.querySelector('.plan-details');
    if (!detailsEl) return;
    const names = accounts.map(a => escapeHtml(a.name)).join(', ');
    const div = document.createElement('div');
    div.className = 'plan-detail';
    div.innerHTML = `<span class="plan-detail-label">Accounts</span><span class="plan-detail-value">${names}</span>`;
    detailsEl.appendChild(div);
  } catch {
    // Non-critical --- just do not show account names
  }
}

// renderPlanCard generates the HTML for a single plan card.
// When the plan is unassigned (no plan_accounts rows) account-scoped
// actions that require an account (Add Purchases, Edit) are suppressed
// — only History and Delete remain — and a read-only "Unassigned" badge
// is shown so operators can identify and re-scope the plan.
/** "n/m steps", or NO_VALUE when the step count is missing or not positive (#136). */
function formatRampProgress(ramp: { current_step?: number; total_steps?: number } | undefined): string {
  const total = ramp?.total_steps;
  if (typeof total !== 'number' || !Number.isFinite(total) || total <= 0) return NO_VALUE;
  return `${ramp?.current_step || 0}/${total} steps`;
}

function renderPlanCard(plan: BackendPlan, canManagePlan: boolean, canDeletePlan: boolean, canAddPurchases: boolean): string {
  const info = extractPlanInfo(plan);
  const status = getStatusBadge(plan.enabled, plan.auto_purchase);
  const rampSchedule = plan.ramp_schedule;
  const overdue = isPlanOverdue(plan);
  // Hide the stale next_execution_date for disabled plans — keeping it
  // visible implies the plan will still run on that date, which it won't.
  const showNextDate = Boolean(plan.next_execution_date) && plan.enabled;
  const overdueBadge = overdue && plan.enabled
    ? '<span class="badge badge-danger" title="Next purchase date is in the past">Overdue</span>'
    : '';
  // Read-only mode: unassigned plans cannot be purchased against until an
  // operator re-assigns them to at least one account.
  const isUnassigned = Boolean(plan.unassigned);

  return `
    <div class="plan-card">
      <div class="plan-header">
        <h3>${escapeHtml(plan.name)}</h3>
        <div class="plan-status">
          <span class="badge badge-${status.class}">${status.label}</span>
          ${healthBadgeHtml(plan)}
          ${overdueBadge}
          ${canManagePlan && !isUnassigned ? `
          <label class="plan-toggle">
            <span class="plan-toggle-text">Enabled</span>
            <span class="toggle-label">
              <input type="checkbox" data-action="toggle-plan" data-id="${escapeHtmlAttr(plan.id)}" aria-label="Enabled: ${escapeHtmlAttr(plan.name)}" ${plan.enabled ? 'checked' : ''}>
              <span class="slider"></span>
            </span>
          </label>
          ` : ''}
        </div>
      </div>
      <div class="plan-body">
        <div class="plan-details">
          <div class="plan-detail">
            <span class="plan-detail-label">Provider</span>
            <span class="plan-detail-value">${info.providers.length > 1 ? info.providers.map((p) => providerBadgeHtml(p)).join(' ') : providerBadgeHtml(info.provider)}</span>
          </div>
          <div class="plan-detail">
            <span class="plan-detail-label">Service</span>
            <span class="plan-detail-value">${escapeHtml(info.serviceDisplay)}</span>
          </div>
          <div class="plan-detail">
            <span class="plan-detail-label">Term</span>
            <span class="plan-detail-value">${info.terms.length > 1 ? info.terms.map((t) => formatTerm(t)).join(', ') : info.term !== null ? formatTerm(info.term) : NO_VALUE}</span>
          </div>
          <div class="plan-detail">
            <span class="plan-detail-label">Coverage</span>
            <span class="plan-detail-value">${info.coverages.length > 1 ? info.coverages.map((c) => `${c}%`).join(', ') : info.coverage !== null ? `${info.coverage}%` : NO_VALUE}</span>
          </div>
          ${info.boundCount > 0 ? `
          <div class="plan-detail">
            <span class="plan-detail-label">Recommendations</span>
            <span class="plan-detail-value">${info.boundCount}</span>
          </div>
          ` : ''}
          <div class="plan-detail">
            <span class="plan-detail-label">Ramp Schedule</span>
            <span class="plan-detail-value">${formatBackendRampSchedule(rampSchedule)}</span>
          </div>
          <div class="plan-detail">
            <span class="plan-detail-label">Progress</span>
            <span class="plan-detail-value">${formatRampProgress(rampSchedule)}</span>
          </div>
          ${showNextDate ? `
          <div class="plan-detail">
            <span class="plan-detail-label">Next Purchase</span>
            <span class="plan-detail-value">${formatDate(plan.next_execution_date || '')}</span>
          </div>
          ` : ''}
        </div>
        <div class="plan-actions">
          ${canAddPurchases && !isUnassigned ? `<button data-action="add-purchases" data-id="${escapeHtmlAttr(plan.id)}" data-name="${escapeHtmlAttr(plan.name)}" data-notification-days="${escapeHtmlAttr(String(plan.notification_days_before))}" class="primary">Add Purchases</button>` : ''}
          ${canManagePlan && !isUnassigned ? `<button data-action="edit-plan" data-id="${escapeHtmlAttr(plan.id)}" class="plan-action-ghost">Edit</button>` : ''}
          <button data-action="view-history" data-id="${escapeHtmlAttr(plan.id)}" class="plan-action-ghost">History</button>
          ${canDeletePlan ? `<button data-action="delete-plan" data-id="${escapeHtmlAttr(plan.id)}" class="plan-action-delete">${iconSvg('trash-2')}Delete</button>` : ''}
        </div>
      </div>
    </div>
  `;
}

function renderPlans(plans: LocalPlan[]): void {
  const container = document.getElementById('plans-list');
  if (!container) return;

  if (!plans || plans.length === 0) {
    container.innerHTML = '<p class="empty">No purchase plans configured. Create one to automate your commitment purchases.</p>';
    return;
  }

  // Issue #365: cache permission checks once per render rather than
  // per-card so a 100-plan list doesn't bounce through the helper
  // 600 times. Action buttons hidden for sessions that lack the verb.
  const canManagePlan = canAccess('update', 'plans');
  const canDeletePlan = canAccess('delete', 'plans');
  // Issue #1406 / #1418: "Add Purchases" requires BOTH plan-management AND
  // purchase-write permission. Plan Authors hold update:plans but not
  // update:purchases; Standard Users hold both. Gate the button on the
  // conjunction so Plan Authors cannot schedule purchases.
  const canAddPurchases = canManagePlan && canAccess('update', 'purchases');

  // Issue #973: split plans into assigned (have plan_accounts rows) and
  // unassigned (legacy plans with zero plan_accounts rows, flagged by the
  // backend). Unassigned plans are rendered under a separate read-only
  // section so operators can discover and re-scope them.
  const assignedPlans = plans.filter(p => !(p as unknown as BackendPlan).unassigned);
  const unassignedPlans = plans.filter(p => (p as unknown as BackendPlan).unassigned);

  const assignedHtml = assignedPlans.map(rawPlan =>
    renderPlanCard(rawPlan as unknown as BackendPlan, canManagePlan, canDeletePlan, canAddPurchases)
  ).join('');

  let unassignedHtml = '';
  if (unassignedPlans.length > 0) {
    const cards = unassignedPlans.map(rawPlan =>
      renderPlanCard(rawPlan as unknown as BackendPlan, canManagePlan, canDeletePlan, canAddPurchases)
    ).join('');
    unassignedHtml = `
      <div class="plans-section-header unassigned-plans-header">
        <h4>Unassigned</h4>
        <span class="plans-section-description">These legacy plans have no associated accounts and cannot be purchased against. Assign accounts or delete them.</span>
      </div>
      ${cards}
    `;
  }

  container.innerHTML = assignedHtml + unassignedHtml;

  // Asynchronously populate account names per assigned plan card.
  // Unassigned plans intentionally skip this: they have no plan_accounts
  // rows so the API call would return an empty list.
  container.querySelectorAll<HTMLElement>('.plan-card').forEach((card) => {
    const planId = card.querySelector<HTMLElement>('[data-id]')?.dataset['id'];
    // Find the matching plan to check unassigned status.
    const allPlans = [...assignedPlans, ...unassignedPlans];
    const matchedPlan = allPlans.find(p => (p as unknown as BackendPlan).id === planId) as unknown as BackendPlan | undefined;
    if (planId && matchedPlan && !matchedPlan.unassigned) {
      void loadPlanAccountNames(planId, card);
    }
  });

  // Add event listeners
  container.querySelectorAll<HTMLInputElement>('[data-action="toggle-plan"]').forEach(toggle => {
    toggle.addEventListener('change', () => void togglePlan(toggle.dataset['id'] || '', toggle.checked));
  });
  container.querySelectorAll<HTMLButtonElement>('[data-action="add-purchases"]').forEach(btn => {
    btn.addEventListener('click', () => void openAddPurchasesModal(btn.dataset['id'] || '', btn.dataset['name'] || '', Number(btn.dataset['notificationDays'])));
  });
  container.querySelectorAll<HTMLButtonElement>('[data-action="edit-plan"]').forEach(btn => {
    btn.addEventListener('click', () => void editPlan(btn.dataset['id'] || ''));
  });
  container.querySelectorAll<HTMLButtonElement>('[data-action="view-history"]').forEach(btn => {
    btn.addEventListener('click', () => void viewPlanHistory(btn.dataset['id'] || ''));
  });
  container.querySelectorAll<HTMLButtonElement>('[data-action="delete-plan"]').forEach(btn => {
    btn.addEventListener('click', () => void deletePlanAction(btn.dataset['id'] || ''));
  });
}

async function togglePlan(planId: string, enabled: boolean): Promise<void> {
  try {
    await api.patchPlan(planId, { enabled } as Partial<api.CreatePlanRequest>);
    await loadPlans();
  } catch (error) {
    console.error('Failed to toggle plan:', error);
    showToast({ message: 'Failed to update plan', kind: 'error' });
    await loadPlans();
  }
}

async function deletePlanAction(planId: string): Promise<void> {
  const ok = await confirmDialog({
    title: 'Delete this plan?',
    body: 'This removes the plan and cancels all its scheduled purchases. This action cannot be undone.',
    confirmLabel: 'Delete plan',
    destructive: true,
  });
  if (!ok) return;

  try {
    await api.deletePlan(planId);
    await loadPlans();
    showToast({ message: 'Plan deleted', kind: 'success', timeout: 5_000 });
  } catch (error) {
    console.error('Failed to delete plan:', error);
    showToast({ message: 'Failed to delete plan', kind: 'error' });
  }
}
