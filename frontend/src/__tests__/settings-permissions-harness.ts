/**
 * Shared setup for the settings permission-gating suite: the module mocks, the
 * mockUser() session builder and the settings page DOM builder. Import this
 * module FIRST so the mocks register before the suite loads ../settings. This
 * file is not a test (jest only collects *.test.ts).
 */
jest.mock('../api', () => ({
  getConfig: jest.fn().mockResolvedValue({
    global: {
      enabled_providers: ['aws'],
      notification_email: 'ops@example.com',
      auto_collect: true,
      default_term: 3,
      default_payment: 'no-upfront',
      default_coverage: 80,
      notification_days_before: 3,
      recommendations_cache_stale_hours: 24,
      recommendations_lookback_days: 7,
    },
    services: [],
    source_cloud: 'aws',
  }),
}));

jest.mock('../federation', () => ({
  initFederationPanel: jest.fn().mockResolvedValue(undefined),
}));

jest.mock('../state', () => ({
  getCurrentUser: jest.fn(),
}));

import * as state from '../state';

export const mockUser = (role: string | null) => {
  (state.getCurrentUser as jest.Mock).mockReturnValue(
    role === null ? null : { id: 'u', email: 'u@example.com', groups: role === 'admin' ? ['00000000-0000-5000-8000-000000000001'] : [] },
  );
};

interface ControlSpec {
  tag: 'input' | 'select' | 'button';
  id: string;
  type?: string;
  options?: ReadonlyArray<{ value: string; label: string; selected?: boolean }>;
  value?: string;
  buttonType?: 'button' | 'submit';
}

// Controls that live inside #global-settings-form (General sub-tab).
// Issue #1410: term/payment/coverage/lookback belong in #purchasing-panel in the
// real HTML; they are added there in setupDom below. Do NOT put them here to
// avoid duplicate IDs and to correctly test that applyReadOnlySettings disables
// them via the #purchasing-panel path (not the #global-settings-form path).
const FORM_CONTROLS: ReadonlyArray<ControlSpec> = [
  { tag: 'input', id: 'provider-aws', type: 'checkbox' },
  { tag: 'input', id: 'provider-azure', type: 'checkbox' },
  { tag: 'input', id: 'provider-gcp', type: 'checkbox' },
  { tag: 'input', id: 'setting-notification-email', type: 'email' },
  { tag: 'input', id: 'setting-auto-collect', type: 'checkbox' },
  {
    tag: 'select',
    id: 'setting-collection-schedule',
    options: [{ value: 'daily', label: 'Daily' }],
  },
  { tag: 'input', id: 'setting-notification-days', type: 'number', value: '3' },
  { tag: 'input', id: 'setting-recs-stale-hours', type: 'number', value: '24' },
  { tag: 'button', id: 'reset-settings-btn', buttonType: 'button' },
  { tag: 'button', id: 'save-settings-btn', buttonType: 'submit' },
];

export const setupDom = () => {
  document.body.replaceChildren();
  const loading = document.createElement('div');
  loading.id = 'settings-loading';
  loading.className = 'hidden';

  const form = document.createElement('form');
  form.id = 'global-settings-form';
  form.className = 'hidden';

  for (const spec of FORM_CONTROLS) {
    if (spec.tag === 'input') {
      const el = document.createElement('input');
      el.id = spec.id;
      if (spec.type) el.type = spec.type;
      if (spec.value !== undefined) el.value = spec.value;
      form.appendChild(el);
    } else if (spec.tag === 'select') {
      const el = document.createElement('select');
      el.id = spec.id;
      for (const opt of spec.options ?? []) {
        const o = document.createElement('option');
        o.value = opt.value;
        o.textContent = opt.label;
        if (opt.selected) o.selected = true;
        el.appendChild(o);
      }
      form.appendChild(el);
    } else {
      const el = document.createElement('button');
      el.id = spec.id;
      if (spec.buttonType) el.type = spec.buttonType;
      form.appendChild(el);
    }
  }

  const err = document.createElement('div');
  err.id = 'settings-error';
  err.className = 'hidden';

  // Purchasing Policies panel: a sibling <section> outside #global-settings-form.
  // Matches the real HTML structure: term/payment/coverage/lookback live HERE
  // (in #purchasing-global-defaults and #purchasing-recommendations-lookback
  // fieldsets inside #purchasing-panel), not in #global-settings-form.
  // applyReadOnlySettings queries #purchasing-panel independently so these
  // controls are disabled via that path. Adding them here verifies issue #1410.
  const purchasingPanel = document.createElement('section');
  purchasingPanel.id = 'purchasing-panel';

  // Global Purchase Defaults (in real HTML: fieldset#purchasing-global-defaults)
  const termSel = document.createElement('select');
  termSel.id = 'setting-default-term';
  for (const v of ['1', '3']) {
    const o = document.createElement('option');
    o.value = v;
    o.textContent = v + ' Year';
    if (v === '3') o.selected = true;
    termSel.appendChild(o);
  }
  purchasingPanel.appendChild(termSel);

  const paymentSel = document.createElement('select');
  paymentSel.id = 'setting-default-payment';
  for (const v of ['no-upfront', 'all-upfront']) {
    const o = document.createElement('option');
    o.value = v;
    o.textContent = v;
    if (v === 'no-upfront') o.selected = true;
    paymentSel.appendChild(o);
  }
  purchasingPanel.appendChild(paymentSel);

  const coverageInput = document.createElement('input');
  coverageInput.id = 'setting-default-coverage';
  coverageInput.type = 'number';
  coverageInput.value = '80';
  purchasingPanel.appendChild(coverageInput);

  // Recommendations Lookback (in real HTML: fieldset#purchasing-recommendations-lookback)
  const lookbackSel = document.createElement('select');
  lookbackSel.id = 'setting-recs-lookback-days';
  for (const v of ['7', '30', '60']) {
    const o = document.createElement('option');
    o.value = v;
    o.textContent = v + ' days';
    if (v === '7') o.selected = true;
    lookbackSel.appendChild(o);
  }
  purchasingPanel.appendChild(lookbackSel);

  // Grace period inputs (unique to the purchasing panel)
  for (const id of ['setting-grace-aws', 'setting-grace-azure', 'setting-grace-gcp']) {
    const inp = document.createElement('input');
    inp.id = id;
    inp.type = 'number';
    inp.value = '7';
    purchasingPanel.appendChild(inp);
  }
  const savePurchBtn = document.createElement('button');
  savePurchBtn.id = 'save-purchasing-btn';
  savePurchBtn.type = 'button';
  const resetPurchBtn = document.createElement('button');
  resetPurchBtn.id = 'reset-purchasing-btn';
  resetPurchBtn.type = 'button';
  purchasingPanel.append(savePurchBtn, resetPurchBtn);

  document.body.append(loading, form, err, purchasingPanel);
};
