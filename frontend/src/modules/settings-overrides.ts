import * as api from '../api';
import { confirmDialog } from '../confirmDialog';
import { showToast } from '../toast';
import { openModal, closeModal } from '../modal';
import { showSkeletonText } from '../lib/skeleton';
import { legacySettingsServiceTarget } from '../lib/settings-service-ids';
import { buildLastUpdatedStamp } from '../lib/last-updated';
import type { AccountProvider } from './settings-state';
import { refreshRecommendationsAfterOverrideChange } from './settings-overridesflow/refresh';
import { openOverrideModal, closeOverrideModal } from './settings-overridesflow/modal';
import { buildPaymentOverrideSelect, buildTermOverrideSelect } from './settings-overridesflow/rowSelects';
import { buildCoverageOverrideInput, buildEnabledOverrideCheckbox } from './settings-overridesflow/rowInputs';

export { setGlobalDefaultsForTest, setCachedGlobalDefaults } from './settings-overridesflow/state';
export { openOverrideModal, closeOverrideModal } from './settings-overridesflow/modal';
export { submitOverrideForm } from './settings-overridesflow/submit';

/**
 * Load and render the service overrides panel for an account.
 *
 * Empty state (no overrides yet for this account) auto-opens the create
 * modal \u2014 the previous "No service overrides set." text was a dead-end
 * because there was no UI affordance to create one. See issue #104.
 *
 * Populated state: render the existing table + an "Add override" button at
 * the top so users can add another override for a different service.
 *
 * Issue #109: Azure and GCP accounts now also get the "Add override" button
 * and full inline editing (term, payment, coverage, enabled) on existing rows.
 * Provider-specific payment options are derived via getCommitmentConfig so
 * GCP shows "Monthly only" and Azure shows "Upfront / Monthly".
 */
/**
 * Open the per-account overrides modal (issue #122). Replaces the inline
 * expandable panel that used to render below the accounts table — the
 * panels stacked without per-row attachment, so two open panels gave the
 * user no way to tell which override row belonged to which account.
 *
 * The modal title binds explicitly to the account; the body uses the same
 * `loadOverridesPanel` rendering function as before, so all CRUD wiring
 * (Reset button, inline payment select, Add override button, empty-state
 * auto-open of the create modal) carries forward unchanged.
 */
export function openAccountOverridesModal(account: api.CloudAccount): void {
  const modal = document.getElementById('account-overrides-modal');
  if (!modal) return;
  const titleEl = document.getElementById('account-overrides-modal-title');
  if (titleEl) {
    titleEl.textContent = `Service overrides for ${account.name} (${account.external_id})`;
  }
  const body = document.getElementById('account-overrides-modal-body');
  if (!body) return;
  // openModal moves focus inside, traps Tab and closes on Escape (#731).
  // onClose runs on every close path, Escape included.
  openModal(modal, { onClose: resetAccountOverridesModal });
  void loadOverridesPanel(account.id, body, account.provider);
}

export function closeAccountOverridesModal(): void {
  const modal = document.getElementById('account-overrides-modal');
  if (modal) closeModal(modal);
  // Idempotent; covers a close of a modal that was never opened via openModal.
  resetAccountOverridesModal();
}

function resetAccountOverridesModal(): void {
  // Close the inner create modal too if it's still open. The user can't
  // visually reach the outer's close affordances while the inner is up
  // (inner backdrop covers them), but a programmatic close should leave a
  // clean slate, not an orphaned inner modal whose Save would target a
  // hidden parent.
  closeOverrideModal();
  // Drop the body content so the next open doesn't briefly flash stale
  // data from a previous account before loadOverridesPanel populates it.
  const body = document.getElementById('account-overrides-modal-body');
  if (body) body.replaceChildren();
}

// Issue #613: overrides saved under an old Azure/GCP service id never match a
// recommendation. Say so on the row and name the id to re-create it under.
function appendLegacyOverrideHint(cell: HTMLElement, o: api.AccountServiceOverride): void {
  const target = legacySettingsServiceTarget(o.provider, o.service);
  if (target === null) return;
  const hint = document.createElement('div');
  hint.className = 'help-text override-legacy-hint';
  hint.textContent = `Old service id: never applied. Re-create it as ${o.provider}/${target}, then delete this row.`;
  cell.appendChild(hint);
}

export async function loadOverridesPanel(accountId: string, panel: HTMLElement, provider: AccountProvider): Promise<void> {
  showSkeletonText(panel, 2); // issue #186: was a plain "Loading…" line
  try {
    const overrides = await api.listAccountServiceOverrides(accountId);
    panel.textContent = '';

    // Issue #109: all providers can now create overrides.
    const canCreate = true;

    if (!overrides || overrides.length === 0) {
      const msg = document.createElement('p');
      msg.className = 'help-text';
      msg.textContent = 'No service overrides yet for this account.';
      panel.appendChild(msg);
      if (canCreate) {
        // No overrides yet \u2014 auto-open the modal so the user lands directly
        // on the create flow instead of staring at empty-state text. The
        // button stays in the panel as a fallback if the user cancels and
        // wants to retry without re-expanding the row.
        const addBtn = document.createElement('button');
        addBtn.type = 'button';
        addBtn.className = 'btn btn-small';
        addBtn.textContent = 'Add override';
        addBtn.addEventListener('click', () => {
          openOverrideModal(accountId, provider, overrides ?? [], panel);
        });
        panel.appendChild(addBtn);
        openOverrideModal(accountId, provider, [], panel);
      }
      return;
    }

    if (canCreate) {
      const addBtn = document.createElement('button');
      addBtn.type = 'button';
      addBtn.className = 'btn btn-small';
      addBtn.textContent = 'Add override';
      addBtn.addEventListener('click', () => {
        openOverrideModal(accountId, provider, overrides, panel);
      });
      panel.appendChild(addBtn);
    }

    const table = document.createElement('table');
    table.className = 'overrides-table';
    const thead = table.createTHead();
    const hrow = thead.insertRow();
    // Column order: Service | Term | Payment | Coverage | Enabled | <action>
    // Enabled is second-to-last (immediately before the action cell) so the
    // descriptive columns keep the layout users learned from PR #72/#23.
    // Issue #110.
    ['Service', 'Term', 'Payment', 'Dashboard coverage', 'Enabled', ''].forEach(h => {
      const th = document.createElement('th');
      th.textContent = h;
      hrow.appendChild(th);
    });
    const tbody = table.createTBody();
    overrides.forEach(o => {
      const tr = tbody.insertRow();
      // Reflect the row's enabled state visually from the initial render so
      // disabled overrides are obvious without waiting for a toggle event.
      if (o.enabled === false) {
        tr.classList.add('override-disabled');
      }

      const serviceTd = tr.insertCell();
      serviceTd.textContent = `${o.provider}/${o.service}`;
      // When this override last changed (issue #8), under the service name so
      // the column order users learned (#110) stays put.
      const stamp = buildLastUpdatedStamp(o.updated_at);
      if (stamp) serviceTd.append(stamp);
      // Old Azure/GCP service id (#613): warn below the stamp.
      appendLegacyOverrideHint(serviceTd, o);

      // Term cell: editable <select> for all providers (issue #109 extends
      // the AWS-only #110 inline editing to Azure/GCP).
      const termTd = tr.insertCell();
      termTd.appendChild(buildTermOverrideSelect(accountId, o, panel));

      // Payment cell: editable <select> for all providers. GCP's payment
      // dropdown only shows "Monthly"; Azure shows upfront/monthly. The
      // underlying getValidPaymentOptions already handles per-provider
      // filtering, so the same builder works for all three clouds.
      const paymentTd = tr.insertCell();
      paymentTd.appendChild(buildPaymentOverrideSelect(accountId, o, panel));

      // Coverage cell: editable numeric <input> for all providers (issue #109).
      const coverageTd = tr.insertCell();
      coverageTd.appendChild(buildCoverageOverrideInput(accountId, o, panel));

      // Enabled cell: per-row checkbox toggling account_service_overrides.enabled.
      // Editable for all providers. Issue `#110`, extended to Azure/GCP in `#109`.
      const enabledTd = tr.insertCell();
      enabledTd.appendChild(buildEnabledOverrideCheckbox(accountId, o, panel, tr));

      const actionTd = tr.insertCell();
      const resetBtn = document.createElement('button');
      resetBtn.type = 'button';
      resetBtn.className = 'btn btn-small btn-danger';
      // Button + dialog wording aligned with the actual data semantics
      // (DELETE on account_service_overrides) per #114. The previous
      // "Reset … will be replaced" copy implied the row stuck around
      // with new values; in fact the row goes away entirely and the
      // engine reads the global default as a side effect of its absence.
      resetBtn.textContent = 'Delete';
      resetBtn.addEventListener('click', async () => {
        const ok = await confirmDialog({
          title: 'Delete override?',
          body: `Delete the ${o.provider}/${o.service} override? This account's recommendations will revert to the global default. The override row and any per-service values you set will be removed.`,
          confirmLabel: 'Delete override',
          destructive: true,
        });
        if (!ok) return;
        // Snapshot all fields before deleting so the Undo action can re-PUT
        // them if the user clicks the toast button within the 5-second window.
        const snapshot: api.AccountServiceOverrideRequest = {
          enabled: o.enabled,
          term: o.term,
          payment: o.payment,
          coverage: o.coverage,
          ramp_schedule: o.ramp_schedule,
          include_engines: o.include_engines,
          exclude_engines: o.exclude_engines,
          include_regions: o.include_regions,
          exclude_regions: o.exclude_regions,
          include_types: o.include_types,
          exclude_types: o.exclude_types,
        };
        try {
          await api.deleteAccountServiceOverride(accountId, o.provider, o.service);
        } catch (err) {
          showToast({ message: `Failed to delete override: ${(err as Error).message}`, kind: 'error' });
          return;
        }
        // The server has confirmed the delete. Show the 10-second undo toast
        // IMMEDIATELY, before the panel reload + recommendations refresh below.
        // Previously the toast was awaited behind both of those calls, so a
        // slow recommendations refresh (a full page-worth of API calls) delayed
        // the Undo affordance long enough that users reported seeing no toast
        // at all (issue #1415). The snapshot-restore Undo is valid regardless
        // of the UI refresh, so it must not be gated behind it. Keeping the
        // failed-delete branch above also stops a refresh error from being
        // misreported as "Failed to delete override" when the delete succeeded.
        //
        // The action closure captures `snapshot` and re-PUTs it if the user
        // clicks Undo before the toast expires. Each Delete click replaces any
        // prior toast handle so a rapid double-click cannot stack multiple
        // Undo buttons.
        showToast({
          message: `Override for ${o.provider}/${o.service} deleted.`,
          kind: 'info',
          // 10 s (was 5 s, issue #728) so there is time to read the message
          // and reach Undo; the countdown also pauses on hover/focus.
          timeout: 10_000,
          actions: [{
            label: 'Undo',
            onClick: () => {
              void (async () => {
                try {
                  await api.saveAccountServiceOverride(accountId, o.provider, o.service, snapshot);
                  await loadOverridesPanel(accountId, panel, provider);
                  await refreshRecommendationsAfterOverrideChange();
                } catch (undoErr) {
                  showToast({ message: `Failed to restore override: ${(undoErr as Error).message}`, kind: 'error' });
                }
              })();
            },
          }],
        });
        await loadOverridesPanel(accountId, panel, provider);
        await refreshRecommendationsAfterOverrideChange();
      });
      actionTd.appendChild(resetBtn);
    });
    panel.appendChild(table);
  } catch (err) {
    panel.textContent = `Failed to load overrides: ${(err as Error).message}`;
  }
}
