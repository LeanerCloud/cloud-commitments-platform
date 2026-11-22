import * as api from '../api';
import { escapeHtml, escapeHtmlAttr } from '../utils';
import { confirmDialog } from '../confirmDialog';
import type { RIExchangeConfig } from '../api';
import { showSkeletonText } from '../lib/skeleton';
import { applyReadOnlySettings, isPermissionDeniedError } from '../settings';
import { MODE_LABELS } from './shared';

// ──────────────────────────────────────────────
// Automation Settings
// ──────────────────────────────────────────────

export async function loadAutomationSettings(): Promise<void> {
  const container = document.getElementById('ri-exchange-automation-settings');
  if (!container) return;

  // Hold the section's place with its own titled frame while the config
  // loads (issue #190): a bare "Loading settings..." line between the GCP
  // block and Commitment Laddering read as the whole page half-loaded.
  // Inside the frame, the shared text skeleton (issue #186) stands in for
  // the form: it brings the screen-reader "Loading…" status and the
  // "Still loading" note, so no visible loading paragraph is needed.
  const loadingBody = document.createElement('div');
  const loadingShell = automationSectionShell(loadingBody);
  loadingShell.setAttribute('aria-busy', 'true');
  container.replaceChildren(loadingShell);
  showSkeletonText(loadingBody, 4);

  try {
    const config = await api.getRIExchangeConfig();
    renderAutomationSettings(container, config);
  } catch (error) {
    // Issue #1413: when the backend returns 403 permission denied the user
    // legitimately lacks access to the Exchange Automation section. Hide it
    // silently (same graceful-degradation pattern as loadGlobalSettings for
    // issue #979) instead of showing an error paragraph at the bottom of the
    // Purchasing Policies panel. Any other failure (network, 5xx) still
    // surfaces the error so operators can diagnose the problem.
    if (isPermissionDeniedError(error)) {
      container.textContent = '';
      return;
    }
    const err = error as Error;
    const errorP = document.createElement('p');
    errorP.className = 'error';
    errorP.textContent = 'Failed to load settings: ' + err.message;
    const retryBtn = document.createElement('button');
    retryBtn.type = 'button';
    retryBtn.className = 'btn btn-small mt-2';
    retryBtn.textContent = 'Retry';
    retryBtn.addEventListener('click', () => { void loadAutomationSettings(); });
    container.replaceChildren(automationSectionShell(errorP, retryBtn));
  }
}

/**
 * The "Exchange Automation" fieldset frame used while the real form is not
 * there yet (loading or failed), so the state is attributed to this section
 * rather than floating between its neighbours.
 */
function automationSectionShell(...children: HTMLElement[]): HTMLFieldSetElement {
  const fieldset = document.createElement('fieldset');
  fieldset.className = 'settings-category';
  const legend = document.createElement('legend');
  legend.textContent = 'Exchange Automation';
  fieldset.append(legend, ...children);
  return fieldset;
}

function buildAutoWarningBanner(): HTMLDivElement {
  const banner = document.createElement('div');
  banner.className = 'warning-message';
  const strong = document.createElement('strong');
  strong.textContent = 'Warning:';
  banner.appendChild(strong);
  // fourEyesExchangeMode runs every mode as manual while 4-eyes is on.
  banner.appendChild(document.createTextNode(' Fully Automated mode will execute RI exchanges without manual approval, '
    + 'unless Require different approver (4-eyes mode) is on. Ensure spending caps are configured properly.'));
  return banner;
}

/**
 * Render a config number into a double-quoted value="..." attribute. The
 * RIExchangeConfig number types are erased at runtime, so escape rather than
 * trust them (issue #37); a missing field renders as an empty input.
 */
function numberAttr(value: number | null | undefined): string {
  return escapeHtmlAttr(String(value ?? ''));
}

// Help text for each Exchange Automation field (issue #1689). The caps only
// gate scheduled and approved exchanges; executeExchange (the Exchange
// dialog) uses the limit entered in the dialog. The per-exchange cap is
// checked before the mode branch (pkg/exchange auto.go), so it limits what
// is proposed in both modes; approving refuses while either cap is 0
// (exchangeCapStrings). 4-eyes runs every mode as manual (fourEyesExchangeMode).
const DIALOG_NOTE = 'Exchanges started from the Exchange dialog use the limit entered there instead.';

const AUTOMATION_HINTS: Record<string, string> = {
  'ri-exchange-enabled': 'Lets CUDly check convertible RI utilization on its schedule and propose or run exchanges for underutilized RIs, depending on the mode below.',
  'ri-exchange-mode': 'Manual Approval queues each proposed exchange for someone to approve. Fully Automated runs it without approval, within the payment limits below. '
    + 'While Require different approver (4-eyes mode) is on, every exchange waits for approval in either mode.',
  'ri-exchange-threshold': 'Convertible RIs whose utilization over the lookback window is below this percentage are candidates for an exchange.',
  'ri-exchange-max-per-exchange': 'Largest payment due allowed for one scheduled or approved exchange. A recommendation with a higher payment due is not proposed in either mode. '
    + 'At 0, no exchange with a payment due is proposed, and approving any exchange is refused. ' + DIALOG_NOTE,
  'ri-exchange-max-daily': 'Total payment due allowed per UTC day across scheduled and approved exchanges. '
    + 'At 0, Fully Automated exchanges with a payment due do not run, and approving any exchange is refused. ' + DIALOG_NOTE,
  'ri-exchange-lookback': 'Days of utilization history used to judge whether a convertible RI is underutilized.',
};

// One settings row; the control must carry aria-describedby="<id>-hint".
function automationRow(id: string, label: string, controlHTML: string): string {
  return '<div class="setting-row">'
    + '<div class="setting-info"><label for="' + id + '">' + escapeHtml(label) + '</label>'
    + '<p class="setting-hint" id="' + id + '-hint">' + escapeHtml(AUTOMATION_HINTS[id]) + '</p></div>'
    + '<div class="setting-input">' + controlHTML + '</div>'
    + '</div>';
}

function renderAutomationSettings(container: HTMLElement, config: RIExchangeConfig): void {
  const modeOptions = Object.entries(MODE_LABELS)
    .map(([value, label]) => {
      const selected = value === config.mode ? ' selected' : '';
      return '<option value="' + escapeHtml(value) + '"' + selected + '>' + escapeHtml(label) + '</option>';
    })
    .join('');

  // Build the form via innerHTML since this is all developer-controlled markup
  // (all dynamic values are escaped via escapeHtml / numberAttr)
  const formHTML = '<form id="ri-exchange-settings-form" class="settings-form">'
    + '<fieldset class="settings-category">'
    + '<legend>Exchange Automation</legend>'
    + '<div id="ri-exchange-warning-slot"></div>'
    + automationRow('ri-exchange-enabled', 'Enable Automated Exchange',
        '<label class="toggle-label">'
        + '<input type="checkbox" id="ri-exchange-enabled" aria-describedby="ri-exchange-enabled-hint"' + (config.auto_exchange_enabled ? ' checked' : '') + '>'
        + '<span class="slider"></span>'
        + '</label>')
    + automationRow('ri-exchange-mode', 'Mode',
        '<select id="ri-exchange-mode" aria-describedby="ri-exchange-mode-hint">' + modeOptions + '</select>')
    + automationRow('ri-exchange-threshold', 'Utilization Threshold (%)',
        '<input type="number" id="ri-exchange-threshold" aria-describedby="ri-exchange-threshold-hint" value="' + numberAttr(config.utilization_threshold) + '" min="0.1" max="100" step="0.1">')
    + automationRow('ri-exchange-max-per-exchange', 'Max Payment Per Exchange (USD)',
        '<input type="number" id="ri-exchange-max-per-exchange" aria-describedby="ri-exchange-max-per-exchange-hint" value="' + numberAttr(config.max_payment_per_exchange_usd) + '" min="0" step="0.01">')
    + automationRow('ri-exchange-max-daily', 'Max Payment Daily (USD)',
        '<input type="number" id="ri-exchange-max-daily" aria-describedby="ri-exchange-max-daily-hint" value="' + numberAttr(config.max_payment_daily_usd) + '" min="0" step="0.01">')
    + automationRow('ri-exchange-lookback', 'Lookback Days',
        '<input type="number" id="ri-exchange-lookback" aria-describedby="ri-exchange-lookback-hint" value="' + numberAttr(config.lookback_days) + '" min="1" max="365">')
    + '</fieldset>'
    + '<div id="ri-exchange-settings-message" class="mt-3"></div>'
    + '</form>';

  container.textContent = '';
  const wrapper = document.createElement('div');
  wrapper.innerHTML = formHTML;
  while (wrapper.firstChild) {
    container.appendChild(wrapper.firstChild);
  }

  // Insert warning banner via DOM if mode is auto
  if (config.mode === 'auto') {
    const slot = document.getElementById('ri-exchange-warning-slot');
    if (slot) slot.appendChild(buildAutoWarningBanner());
  }

  // No per-form Save button: the Purchasing panel's sticky "Save Settings"
  // bar is the single source of truth and calls saveAutomationSettings()
  // alongside the global config save. We still stop any stray submit (e.g.
  // Enter inside a number field) so it doesn't navigate the page.
  const form = document.getElementById('ri-exchange-settings-form');
  if (form) {
    form.addEventListener('submit', (e) => {
      e.preventDefault();
    });
  }

  // Update warning banner when mode changes
  const modeSelect = document.getElementById('ri-exchange-mode') as HTMLSelectElement | null;
  if (modeSelect) {
    modeSelect.addEventListener('change', () => {
      const slot = document.getElementById('ri-exchange-warning-slot');
      if (!slot) return;
      if (modeSelect.value === 'auto') {
        if (!slot.firstChild) slot.appendChild(buildAutoWarningBanner());
      } else {
        slot.textContent = '';
      }
    });
  }

  // Issue #870: re-apply the purchasing-panel read-only gate after rendering
  // so the dynamically-injected RI Exchange inputs are covered for non-admin
  // sessions (loadGlobalSettings runs concurrently and may complete before
  // these inputs exist).
  applyReadOnlySettings(null);
}

export async function saveAutomationSettings(): Promise<void> {
  const messageContainer = document.getElementById('ri-exchange-settings-message');
  if (!messageContainer) return;

  const enabledInput = document.getElementById('ri-exchange-enabled') as HTMLInputElement | null;
  const modeInput = document.getElementById('ri-exchange-mode') as HTMLSelectElement | null;
  const thresholdInput = document.getElementById('ri-exchange-threshold') as HTMLInputElement | null;
  const maxPerExchangeInput = document.getElementById('ri-exchange-max-per-exchange') as HTMLInputElement | null;
  const maxDailyInput = document.getElementById('ri-exchange-max-daily') as HTMLInputElement | null;
  const lookbackInput = document.getElementById('ri-exchange-lookback') as HTMLInputElement | null;

  if (!enabledInput || !modeInput || !thresholdInput || !maxPerExchangeInput || !maxDailyInput || !lookbackInput) return;

  const threshold = parseFloat(thresholdInput.value);
  const maxPerExchange = parseFloat(maxPerExchangeInput.value);
  const maxDaily = parseFloat(maxDailyInput.value);
  const lookback = parseInt(lookbackInput.value, 10);
  const mode = modeInput.value as RIExchangeConfig['mode'];

  // Client-side validation
  if (isNaN(threshold) || threshold <= 0 || threshold > 100) {
    messageContainer.textContent = '';
    const p = document.createElement('p');
    p.className = 'error';
    p.textContent = 'Utilization threshold must be greater than 0 and at most 100.';
    messageContainer.appendChild(p);
    return;
  }
  if (isNaN(lookback) || lookback < 1 || lookback > 365) {
    messageContainer.textContent = '';
    const p = document.createElement('p');
    p.className = 'error';
    p.textContent = 'Lookback days must be between 1 and 365.';
    messageContainer.appendChild(p);
    return;
  }
  if (isNaN(maxPerExchange) || maxPerExchange < 0) {
    messageContainer.textContent = '';
    const p = document.createElement('p');
    p.className = 'error';
    p.textContent = 'Max payment per exchange must be >= 0.';
    messageContainer.appendChild(p);
    return;
  }
  if (isNaN(maxDaily) || maxDaily < 0) {
    messageContainer.textContent = '';
    const p = document.createElement('p');
    p.className = 'error';
    p.textContent = 'Max daily payment must be >= 0.';
    messageContainer.appendChild(p);
    return;
  }

  // Confirm auto mode — financial operations ship without further review
  // once enabled, so this gets the destructive-style confirm.
  if (mode === 'auto') {
    const confirmed = await confirmDialog({
      title: 'Enable Fully Automated mode?',
      body: 'RI exchanges will be executed automatically without manual approval. Make sure the payment caps below are set to values you are comfortable spending.',
      confirmLabel: 'Enable automation',
      destructive: true,
    });
    if (!confirmed) return;
  }

  const config: RIExchangeConfig = {
    auto_exchange_enabled: enabledInput.checked,
    mode,
    utilization_threshold: threshold,
    max_payment_per_exchange_usd: maxPerExchange,
    max_payment_daily_usd: maxDaily,
    lookback_days: lookback,
  };

  messageContainer.textContent = '';
  const loadingP = document.createElement('p');
  loadingP.className = 'loading';
  loadingP.textContent = 'Saving settings...';
  messageContainer.appendChild(loadingP);

  try {
    await api.updateRIExchangeConfig(config);
    messageContainer.textContent = '';
    const successP = document.createElement('p');
    successP.className = 'success-message';
    successP.textContent = 'Settings saved successfully.';
    messageContainer.appendChild(successP);
    setTimeout(() => {
      messageContainer.textContent = '';
    }, 3000);
  } catch (error) {
    const err = error as Error;
    messageContainer.textContent = '';
    const errorP = document.createElement('p');
    errorP.className = 'error';
    errorP.textContent = 'Failed to save settings: ' + err.message;
    messageContainer.appendChild(errorP);
  }
}
