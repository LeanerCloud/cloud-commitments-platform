/**
 * Admin pages UX nits (issue #1691).
 */
import fs from 'fs';
import path from 'path';
import { accountDisplayLabel } from '../lib/account-label';
import { initFederationPanel } from '../federation';
import { loadAccountsForProvider } from '../settings';

jest.mock('../api', () => ({
  listAccounts: jest.fn(),
  listAccountsMinimal: jest.fn(),
  getFederationIaC: jest.fn(),
}));
jest.mock('../toast', () => ({ showToast: jest.fn(() => ({ dismiss: jest.fn() })) }));
jest.mock('../confirmDialog', () => ({ confirmDialog: jest.fn() }));
jest.mock('../recommendations', () => ({ loadRecommendations: jest.fn() }));

import * as api from '../api';
import { initTopbarFilters } from '../topbar-filters';
import { renderGroups } from '../groups/groupList';
import { renderUsers } from '../users/userList';
import * as userState from '../users/state';
import type { APIGroup } from '../api';

describe('1. accountDisplayLabel', () => {
  test('appends the id when the name lacks it', () => {
    expect(accountDisplayLabel('Prod', '111')).toBe('Prod (111)');
  });
  test('does not repeat an id the name already contains', () => {
    expect(accountDisplayLabel('CUDly host (909)', '909')).toBe('CUDly host (909)');
  });
  test('returns the id alone for an empty name', () => {
    expect(accountDisplayLabel('', '909')).toBe('909');
  });

  test('accounts table action labels do not duplicate the id', async () => {
    document.body.innerHTML = '<div id="aws-accounts-list"></div>';
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'a1', name: 'CUDly host (909)', provider: 'aws', external_id: '909', enabled: true },
    ]);
    await loadAccountsForProvider('aws');
    expect(document.querySelector('button[aria-label="Edit CUDly host (909)"]')).not.toBeNull();
  });

  test('topbar account chip option does not duplicate the id', async () => {
    document.body.innerHTML = '<div id="topbar-filters"></div>';
    (api.listAccountsMinimal as jest.Mock).mockResolvedValue([
      { id: 'a1', name: 'CUDly host (909)', external_id: '909', provider: 'aws' },
    ]);
    initTopbarFilters();
    await new Promise((r) => setTimeout(r, 0));
    const triggers = document.querySelectorAll<HTMLButtonElement>('.chip-select');
    triggers[1]!.click();
    const labels = Array.from(document.querySelectorAll('.chip-select-option')).map((o) => o.textContent);
    expect(labels).toContain('CUDly host (909)');
  });
});

const readSrc = (rel: string): string => fs.readFileSync(path.join(__dirname, '..', rel), 'utf8');
const indexHtml = readSrc('index.html');

describe('2. target cloud radio group', () => {
  let pills: HTMLButtonElement[];
  const key = (el: HTMLElement, k: string): void => {
    el.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true }));
  };
  beforeEach(async () => {
    document.body.innerHTML =
      '<div id="federation-target-cloud-pills" role="radiogroup" aria-label="Target cloud"></div><div id="federation-setup-panel"></div>';
    (api.getFederationIaC as jest.Mock).mockResolvedValue({});
    await initFederationPanel('aws');
    pills = Array.from(document.querySelectorAll<HTMLButtonElement>('.target-cloud-pill'));
  });

  test('pills are radios with aria-checked and roving tabindex', () => {
    expect(pills.map((p) => p.getAttribute('role'))).toEqual(['radio', 'radio', 'radio']);
    expect(pills.map((p) => p.getAttribute('aria-checked'))).toEqual(['true', 'false', 'false']);
    expect(pills.map((p) => p.tabIndex)).toEqual([0, -1, -1]);
    expect(pills[0]!.hasAttribute('aria-pressed')).toBe(false);
  });

  test('arrow keys, Home and End move and select', () => {
    key(pills[0]!, 'ArrowRight');
    expect(pills[1]!.getAttribute('aria-checked')).toBe('true');
    expect(pills[1]!.classList.contains('selected')).toBe(true);
    expect(pills[1]!.tabIndex).toBe(0);
    expect(pills[0]!.tabIndex).toBe(-1);
    expect(document.activeElement).toBe(pills[1]);
    key(pills[1]!, 'ArrowLeft');
    key(pills[0]!, 'ArrowLeft');
    expect(pills[2]!.getAttribute('aria-checked')).toBe('true');
    key(pills[2]!, 'Home');
    expect(pills[0]!.getAttribute('aria-checked')).toBe('true');
    key(pills[0]!, 'End');
    expect(pills[2]!.getAttribute('aria-checked')).toBe('true');
    key(pills[2]!, 'ArrowDown');
    expect(pills[0]!.getAttribute('aria-checked')).toBe('true');
  });

  test('container is a radiogroup', () => {
    expect(indexHtml).toMatch(/id="federation-target-cloud-pills"[^>]*role="radiogroup"/);
  });
});

describe('3. account section titles are headings', () => {
  test.each([
    'Federation Setup (Add New Account)',
    'Account Registrations',
    '<span class="provider-badge aws">AWS</span> Accounts',
    '<span class="provider-badge azure">Azure</span> Accounts',
    '<span class="provider-badge gcp">GCP</span> Accounts',
  ])('legend "%s" wraps its title in an h4', (title) => {
    const escaped = title.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    expect(indexHtml).toMatch(new RegExp(`<legend><h4[^>]*>${escaped}</h4></legend>`));
  });
});

describe('4. accounts tables share one column layout', () => {
  test('rendered table has a fixed layout with a four-column colgroup', async () => {
    document.body.innerHTML = '<div id="gcp-accounts-list"></div>';
    (api.listAccounts as jest.Mock).mockResolvedValue([
      { id: 'a1', name: 'P', provider: 'gcp', external_id: '1', enabled: true },
    ]);
    await loadAccountsForProvider('gcp');
    expect(document.querySelectorAll('table.accounts-table > colgroup > col')).toHaveLength(4);
  });
  test('css sets table-layout: fixed on .accounts-table', () => {
    const css = readSrc('styles/components.css');
    const rule = css.match(/\.accounts-table \{[^}]*\}/);
    expect(rule?.[0]).toMatch(/table-layout:\s*fixed/);
  });
});

describe('6. account modal autofill hints', () => {
  const formHtml = indexHtml.match(/<form id="account-form"[^>]*>[\s\S]*?<\/form>/)![0];
  test('form opts out of autofill', () => {
    expect(formHtml).toMatch(/^<form id="account-form"[^>]*autocomplete="off"/);
  });
  test('every text field has an autocomplete hint and secrets use new-password', () => {
    const wrap = document.createElement('div');
    wrap.innerHTML = formHtml;
    const fields = wrap.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>(
      'input:not([type=hidden]):not([type=checkbox]):not([type=radio]), textarea',
    );
    expect(fields.length).toBeGreaterThan(8);
    fields.forEach((f) => {
      const want = (f as HTMLInputElement).type === 'password' ? 'new-password' : f.getAttribute('autocomplete') === 'email' ? 'email' : 'off';
      expect([f.id, f.getAttribute('autocomplete')]).toEqual([f.id, want]);
    });
  });
});

describe('7. group card header layout', () => {
  const longDesc = 'Runs RI exchanges '.repeat(20);
  test('every card puts the description in a shrinkable info block', () => {
    document.body.innerHTML = '<div id="groups-list"></div>';
    userState.setAllUsers([]);
    const g = (name: string, description: string): APIGroup => ({
      id: name, name, description, permissions: [], system_managed: false,
    });
    renderGroups([g('Short', 'x'), g('RI Exchanger', longDesc)]);
    document.querySelectorAll('.group-card-header').forEach((h) => {
      expect(h.firstElementChild?.classList.contains('group-card-info')).toBe(true);
    });
  });
  test('css lets the info block shrink and keeps actions from wrapping under it', () => {
    const css = readSrc('styles/settings.css');
    expect(css.match(/\.group-card-info \{[^}]*\}/)?.[0]).toMatch(/min-width:\s*0/);
    expect(css.match(/\.group-card-actions \{[^}]*\}/)?.[0]).toMatch(/flex:\s*0 0 auto/);
  });
});

describe('9. users table date cells do not wrap', () => {
  test('Created and Last Login cells carry the nowrap class', () => {
    document.body.innerHTML = '<div id="users-list"></div>';
    userState.setAllUsers([]);
    userState.setAvailableGroups([]);
    renderUsers([
      { id: 'u1', email: 'a@b.c', groups: [], mfa_enabled: false, created_at: '2026-06-05T10:00:00Z', last_login: '' } as never,
    ]);
    expect(document.querySelectorAll('td.user-date-cell')).toHaveLength(2);
    expect(readSrc('styles/settings.css') + readSrc('styles/tables.css') + readSrc('styles/components.css'))
      .toMatch(/\.user-date-cell\s*\{[^}]*white-space:\s*nowrap/);
  });
});
