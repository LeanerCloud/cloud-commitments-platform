/**
 * Navigation module tests: canonical tab paths, skipDefaultLoad and the
 * settings and inventory sub-tab path parsing. Split from navigation.test.ts to
 * stay under the 500-line ceiling.
 */

// The harness registers the module mocks, so it must be imported first.
import { setPathname } from './navigation-harness';
import { switchTab, switchSettingsSubTab, getSettingsSubTabFromPath, canonicalTabPath } from '../navigation';
import { initHistoryDateRange, loadHistory } from '../history';
import { loadGlobalSettings } from '../settings';
import { canAccess } from '../permissions';
import { isAdmin } from '../auth';

describe('Navigation Module', () => {
  beforeEach(() => {
    // Setup DOM with tabs
    document.body.innerHTML = `
      <div class="tabs">
        <button class="tab-btn active" data-tab="home">Dashboard</button>
        <button class="tab-btn" data-tab="opportunities">Recommendations</button>
        <button class="tab-btn" data-tab="plans">Plans</button>
        <button class="tab-btn" data-tab="purchases">History</button>
        <button class="tab-btn" data-tab="inventory">Inventory &amp; Coverage</button>
        <button class="tab-btn" data-tab="admin">Settings</button>
      </div>
      <div id="home-tab" class="tab-content active"></div>
      <div id="opportunities-tab" class="tab-content"></div>
      <div id="plans-tab" class="tab-content"></div>
      <div id="purchases-tab" class="tab-content"></div>
      <div id="inventory-tab" class="tab-content"></div>
      <div id="admin-tab" class="tab-content">
        <div class="settings-tabs">
          <button class="sub-tab-btn active" data-settings-tab="general">General</button>
          <button class="sub-tab-btn" data-settings-tab="purchasing">Purchasing</button>
          <button class="sub-tab-btn" data-settings-tab="accounts">Accounts</button>
          <button class="sub-tab-btn" data-settings-tab="users">Users</button>
        </div>
        <section id="settings-section"></section>
        <section id="purchasing-panel" class="hidden"></section>
        <section id="accounts-section" class="hidden"></section>
        <section id="users-section" class="hidden"></section>
        <section id="apikeys-section" class="hidden"></section>
      </div>
    `;

    // Clear all mocks. clearAllMocks() only drops recorded calls, not
    // implementations, so restore the permissive defaults explicitly --
    // otherwise a test that flips isAdmin/canAccess to false leaks that into
    // every test declared after it.
    jest.clearAllMocks();
    (isAdmin as jest.Mock).mockReturnValue(true);
    (canAccess as jest.Mock).mockReturnValue(true);
  });

  // canonicalTabPath is what app.ts writes into the initial replaceState, so
  // it must keep the sub-tab segment switchTab reads back out of the URL.
  describe('canonicalTabPath', () => {
    test.each(['home', 'opportunities', 'plans', 'purchases'])(
      '%s has no sub-tab segment',
      (tab) => {
        window.history.replaceState(null, '', '/' + tab);
        expect(canonicalTabPath(tab)).toBe('/' + tab);
      },
    );

    test.each(['active-commitments', 'coverage', 'trends', 'ri-exchange'])(
      'a deep-linked /inventory/%s keeps its sub-tab segment',
      (sub) => {
        window.history.replaceState(null, '', '/inventory/' + sub);
        expect(canonicalTabPath('inventory')).toBe('/inventory/' + sub);
      },
    );

    test('a bare /inventory canonicalises to the default sub-tab', () => {
      window.history.replaceState(null, '', '/inventory');
      expect(canonicalTabPath('inventory')).toBe('/inventory/active-commitments');
    });

    test.each(['general', 'purchasing', 'accounts', 'users'])(
      'a deep-linked /admin/%s keeps its sub-tab segment',
      (sub) => {
        window.history.replaceState(null, '', '/admin/' + sub);
        // currentSettingsSubTab is module state; drive it through the real
        // entry point so the assertion reflects an actual navigation.
        switchSettingsSubTab(sub, { push: false });
        expect(canonicalTabPath('admin')).toBe('/admin/' + sub);
      },
    );

    // app.ts calls this at init, before anything has set currentSettingsSubTab,
    // so the URL is the only source for the segment. A fresh module instance is
    // the only way to observe that state from inside this file.
    test('falls back to the URL when no admin sub-tab has been visited yet', () => {
      window.history.replaceState(null, '', '/admin/users');
      jest.isolateModules(() => {
        // eslint-disable-next-line @typescript-eslint/no-require-imports
        const nav = require('../navigation') as typeof import('../navigation');
        expect(nav.canonicalTabPath('admin')).toBe('/admin/users');
      });
    });
  });

  describe('switchTab skipDefaultLoad', () => {
    test('purchases: reveals the tab without firing its default loads', () => {
      switchTab('purchases', { skipDefaultLoad: true, push: false });
      expect(document.getElementById('purchases-tab')?.classList.contains('active')).toBe(true);
      expect(initHistoryDateRange).not.toHaveBeenCalled();
      expect(loadHistory).not.toHaveBeenCalled();
    });

    test('purchases: the view:purchases gate still applies', () => {
      (canAccess as jest.Mock).mockReturnValue(false);
      switchTab('purchases', { skipDefaultLoad: true, push: false });
      expect(document.getElementById('purchases-tab')?.textContent).toContain('do not have access');
      expect(loadHistory).not.toHaveBeenCalled();
    });

    test('admin: reveals the tab without loading the default sub-tab', () => {
      switchTab('admin', { skipDefaultLoad: true, push: false });
      expect(document.getElementById('admin-tab')?.classList.contains('active')).toBe(true);
      expect(loadGlobalSettings).not.toHaveBeenCalled();
    });
  });

  describe('getSettingsSubTabFromPath', () => {
    let originalURL: string;
    let originalState: unknown;

    beforeEach(() => {
      originalURL = window.location.href;
      originalState = window.history.state;
    });

    afterEach(() => {
      window.history.replaceState(originalState, '', originalURL);
    });

    // Canonical /admin/* paths (issue #340 IA rename)
    test('returns general for root admin path', () => {
      setPathname('/admin');
      expect(getSettingsSubTabFromPath()).toBe('general');
    });

    test('returns accounts for /admin/accounts', () => {
      setPathname('/admin/accounts');
      expect(getSettingsSubTabFromPath()).toBe('accounts');
    });

    test('returns purchasing for /admin/purchasing', () => {
      setPathname('/admin/purchasing');
      expect(getSettingsSubTabFromPath()).toBe('purchasing');
    });

    test('returns users for /admin/users', () => {
      setPathname('/admin/users');
      expect(getSettingsSubTabFromPath()).toBe('users');
    });

    // Legacy /settings/* paths still work via LEGACY_PATH_REDIRECTS
    test('returns general for root settings path', () => {
      setPathname('/settings');
      expect(getSettingsSubTabFromPath()).toBe('general');
    });

    test('returns accounts for /settings/accounts', () => {
      setPathname('/settings/accounts');
      expect(getSettingsSubTabFromPath()).toBe('accounts');
    });

    test('returns purchasing for /settings/purchasing', () => {
      setPathname('/settings/purchasing');
      expect(getSettingsSubTabFromPath()).toBe('purchasing');
    });

    test('returns general for unknown sub-tab', () => {
      setPathname('/settings/foobar');
      expect(getSettingsSubTabFromPath()).toBe('general');
    });
  });
});
