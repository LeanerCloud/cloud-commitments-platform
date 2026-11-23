/**
 * Shared setup for the settings purchasing grace-period suite: the module mocks,
 * the confirm/toast mock handles and the settings form DOM builder. Import this
 * module FIRST so the mocks register before the suite loads ../settings. This
 * file is not a test (jest only collects *.test.ts).
 */
jest.mock('../api', () => ({
  getConfig: jest.fn(),
  updateConfig: jest.fn(),
  updateServiceConfig: jest.fn().mockResolvedValue(undefined),
}));
jest.mock('../federation', () => ({
  initFederationPanel: jest.fn().mockResolvedValue(undefined),
}));
export const mockConfirmDialog = jest.fn<Promise<boolean>, [unknown]>(() => Promise.resolve(true));
jest.mock('../confirmDialog', () => ({
  confirmDialog: (opts: unknown) => mockConfirmDialog(opts),
}));
export const mockShowToast = jest.fn<{ dismiss: () => void }, [unknown]>(() => ({ dismiss: jest.fn() }));
jest.mock('../toast', () => ({
  showToast: (opts: unknown) => mockShowToast(opts),
}));

function addInput(id: string, type: string, value: string, parent: HTMLElement, attrs: Record<string, string> = {}): HTMLInputElement {
  const el = document.createElement('input');
  el.id = id;
  el.type = type;
  el.value = value;
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
  parent.appendChild(el);
  return el;
}

function addSelect(id: string, options: string[], selected: string, parent: HTMLElement): HTMLSelectElement {
  const el = document.createElement('select');
  el.id = id;
  for (const v of options) {
    const opt = document.createElement('option');
    opt.value = v;
    opt.textContent = v;
    if (v === selected) opt.selected = true;
    el.appendChild(opt);
  }
  parent.appendChild(el);
  return el;
}

export function seedDOM(): void {
  while (document.body.firstChild) document.body.removeChild(document.body.firstChild);
  const loading = document.createElement('div');
  loading.id = 'settings-loading';
  loading.className = 'hidden';
  document.body.appendChild(loading);

  const form = document.createElement('form');
  form.id = 'global-settings-form';
  form.className = 'hidden';
  document.body.appendChild(form);

  addInput('provider-aws', 'checkbox', '', form, { checked: 'checked' });
  (document.getElementById('provider-aws') as HTMLInputElement).checked = true;
  addInput('provider-azure', 'checkbox', '', form);
  addInput('provider-gcp', 'checkbox', '', form);
  addInput('setting-notification-email', 'email', '', form);
  const auto = addInput('setting-auto-collect', 'checkbox', '', form);
  auto.checked = true;
  addSelect('setting-collection-schedule', ['daily'], 'daily', form);
  addSelect('setting-default-term', ['1', '3'], '3', form);
  addSelect('setting-default-payment', ['no-upfront', 'all-upfront'], 'all-upfront', form);
  addInput('setting-default-coverage', 'number', '80', form);
  addInput('setting-notification-days', 'number', '3', form);
  addInput('setting-grace-aws', 'number', '7', form, { min: '0', max: '30' });
  addInput('setting-grace-azure', 'number', '7', form, { min: '0', max: '30' });
  addInput('setting-grace-gcp', 'number', '7', form, { min: '0', max: '30' });

  const errDiv = document.createElement('div');
  errDiv.id = 'settings-error';
  errDiv.className = 'hidden';
  document.body.appendChild(errDiv);
}
