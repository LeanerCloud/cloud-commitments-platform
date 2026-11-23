/**
 * Navigation module tests
 */

// The harness registers the module mocks, so it must be imported first.
import './navigation-harness';
import { switchTab, switchSettingsSubTab, switchInventorySubTab, getSettingsSubTabFromPath, applyTabFromPath } from '../navigation';
import { loadDashboard } from '../dashboard';
import { loadRecommendations } from '../recommendations';
import { loadPlans } from '../plans';
import { initHistoryDateRange, loadHistory } from '../history';
import { loadGlobalSettings } from '../settings';
import { loadAutomationSettings } from '../riexchange';
import { canAccess } from '../permissions';
import { loadInventory } from '../inventory';
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

  describe('switchTab', () => {
    test('records the active tab on body for CSS hooks (#1691)', () => {
      switchTab('admin');
      expect(document.body.dataset['activeTab']).toBe('admin');
      switchTab('home');
      expect(document.body.dataset['activeTab']).toBe('home');
    });

    test('switches to dashboard tab and loads dashboard', () => {
      switchTab('home');

      // Check button is active
      const dashboardBtn = document.querySelector('[data-tab="home"]');
      expect(dashboardBtn?.classList.contains('active')).toBe(true);

      // Check other buttons are not active
      const recsBtn = document.querySelector('[data-tab="opportunities"]');
      expect(recsBtn?.classList.contains('active')).toBe(false);

      // Check content is active
      const dashboardContent = document.getElementById('home-tab');
      expect(dashboardContent?.classList.contains('active')).toBe(true);

      // Check loadDashboard was called
      expect(loadDashboard).toHaveBeenCalled();
    });

    test('switches to recommendations tab and loads recommendations', () => {
      switchTab('opportunities');

      const recsBtn = document.querySelector('[data-tab="opportunities"]');
      expect(recsBtn?.classList.contains('active')).toBe(true);

      const recsContent = document.getElementById('opportunities-tab');
      expect(recsContent?.classList.contains('active')).toBe(true);

      expect(loadRecommendations).toHaveBeenCalled();
    });

    test('switches to plans tab and loads plans', () => {
      switchTab('plans');

      const plansBtn = document.querySelector('[data-tab="plans"]');
      expect(plansBtn?.classList.contains('active')).toBe(true);

      const plansContent = document.getElementById('plans-tab');
      expect(plansContent?.classList.contains('active')).toBe(true);

      expect(loadPlans).toHaveBeenCalled();
    });

    test('switches to history tab and initializes date range + auto-loads history', () => {
      switchTab('purchases');

      const historyBtn = document.querySelector('[data-tab="purchases"]');
      expect(historyBtn?.classList.contains('active')).toBe(true);

      const historyContent = document.getElementById('purchases-tab');
      expect(historyContent?.classList.contains('active')).toBe(true);

      expect(initHistoryDateRange).toHaveBeenCalled();
      // Issue #340 sub-task: switchTab('purchases') auto-loads
      // history so the Approval queue card and the Purchase History
      // table populate on first visit. Guards against a regression
      // that re-introduces "user must click Load History" friction.
      expect(loadHistory).toHaveBeenCalled();
    });

    test('switches to settings tab and loads settings', () => {
      switchTab('admin');

      const settingsBtn = document.querySelector('[data-tab="admin"]');
      expect(settingsBtn?.classList.contains('active')).toBe(true);

      const settingsContent = document.getElementById('admin-tab');
      expect(settingsContent?.classList.contains('active')).toBe(true);

      expect(loadGlobalSettings).toHaveBeenCalled();
    });

    test('switches to inventory tab', () => {
      switchTab('inventory');

      const inventoryBtn = document.querySelector('[data-tab="inventory"]');
      expect(inventoryBtn?.classList.contains('active')).toBe(true);

      const inventoryContent = document.getElementById('inventory-tab');
      expect(inventoryContent?.classList.contains('active')).toBe(true);

      // Other tabs must be deactivated
      const homeBtn = document.querySelector('[data-tab="home"]');
      expect(homeBtn?.classList.contains('active')).toBe(false);
    });

    // QA A.4: a bare inventory switch lands on the default sub-tab and the
    // canonical URL carries the sub-tab segment (/inventory/active-commitments),
    // mirroring how the admin switch pushes /admin/<subtab>.
    test('switching to inventory pushes /inventory/<default-subtab>', () => {
      // currentTab is module state that may already be 'inventory' from a
      // prior test; switch away first so the inventory switch is genuine
      // (a self-switch would correctly skip the push).
      switchTab('home');
      window.history.replaceState(null, '', '/');
      switchTab('inventory');
      expect(window.location.pathname).toBe('/inventory/active-commitments');
    });

    // A deep link to a specific inventory sub-tab is honoured: switchTab
    // reads the path and the canonical URL keeps that sub-tab.
    test('switching to inventory honours a /inventory/<subtab> deep link', () => {
      switchTab('home');
      window.history.replaceState(null, '', '/inventory/coverage');
      switchTab('inventory');
      expect(window.location.pathname).toBe('/inventory/coverage');
    });

    test('deactivates previously active tab', () => {
      // Dashboard is initially active
      const dashboardBtn = document.querySelector('[data-tab="home"]');
      const dashboardContent = document.getElementById('home-tab');
      expect(dashboardBtn?.classList.contains('active')).toBe(true);
      expect(dashboardContent?.classList.contains('active')).toBe(true);

      // Switch to recommendations
      switchTab('opportunities');

      // Dashboard should no longer be active
      expect(dashboardBtn?.classList.contains('active')).toBe(false);
      expect(dashboardContent?.classList.contains('active')).toBe(false);

      // Recommendations should be active
      const recsBtn = document.querySelector('[data-tab="opportunities"]');
      const recsContent = document.getElementById('opportunities-tab');
      expect(recsBtn?.classList.contains('active')).toBe(true);
      expect(recsContent?.classList.contains('active')).toBe(true);
    });

    test('falls back to home for unknown tab', () => {
      // Unknown tab names are normalized to 'home' rather than
      // leaving the UI in a broken all-deactivated state.
      expect(() => switchTab('unknown-tab')).not.toThrow();

      const homeBtn = document.querySelector('[data-tab="home"]');
      expect(homeBtn?.classList.contains('active')).toBe(true);
      expect(loadDashboard).toHaveBeenCalled();
    });

    test('multiple tab switches work correctly', () => {
      // Switch through all tabs
      switchTab('opportunities');
      expect(loadRecommendations).toHaveBeenCalledTimes(1);

      switchTab('plans');
      expect(loadPlans).toHaveBeenCalledTimes(1);

      switchTab('purchases');
      expect(initHistoryDateRange).toHaveBeenCalledTimes(1);

      switchTab('admin');
      expect(loadGlobalSettings).toHaveBeenCalledTimes(1);

      switchTab('home');
      expect(loadDashboard).toHaveBeenCalledTimes(1);

      // Final state should have home active
      const dashboardBtn = document.querySelector('[data-tab="home"]');
      expect(dashboardBtn?.classList.contains('active')).toBe(true);
    });

    // issue #1000: users without view:purchases must not trigger API calls
    // and must see the no-access placeholder instead of the real page.
    describe('view:purchases gate', () => {
      beforeEach(() => {
        // Default mock returns true; override to false for these tests.
        (canAccess as jest.Mock).mockReturnValue(false);
      });

      afterEach(() => {
        // Restore default so other tests are unaffected.
        (canAccess as jest.Mock).mockReturnValue(true);
      });

      test('purchases tab: does not fire loadHistory or initHistoryDateRange when user lacks view:purchases', () => {
        switchTab('purchases');
        expect(initHistoryDateRange).not.toHaveBeenCalled();
        expect(loadHistory).not.toHaveBeenCalled();
      });

      test('purchases tab: renders no-access placeholder when user lacks view:purchases', () => {
        switchTab('purchases');
        const container = document.getElementById('purchases-tab');
        expect(container?.textContent).toContain('You do not have access to this page');
      });

      test('inventory tab: does not fire loadInventory when user lacks view:purchases', () => {
        switchTab('inventory');
        expect(loadInventory).not.toHaveBeenCalled();
      });

      test('inventory tab: renders no-access placeholder when user lacks view:purchases', () => {
        switchTab('inventory');
        const container = document.getElementById('inventory-tab');
        expect(container?.textContent).toContain('You do not have access to this page');
      });

      // #1650: the placeholder must keep the shipped page header (the route-change
      // focus target) and drop everything else, which the header-less fixture
      // above cannot show.
      describe.each([
        ['purchases', 'Purchases'],
        ['inventory', 'Inventory & Coverage'],
      ])('%s tab on the real index.html', (tab, heading) => {
        beforeEach(() => {
          const fs = require('fs') as typeof import('fs');
          const path = require('path') as typeof import('path');
          const html = fs.readFileSync(path.join(__dirname, '..', 'index.html'), 'utf8');
          const body = /<body[^>]*>([\s\S]*)<\/body>/.exec(html)![1]!.replace(/<script[\s\S]*?<\/script>/g, '');
          document.body.innerHTML = body;
        });

        test('keeps its page header and replaces the rest with the placeholder', () => {
          const container = document.getElementById(`${tab}-tab`)!;
          const header = container.querySelector(':scope > .page-header');
          expect(header).not.toBeNull();
          expect(container.children.length).toBeGreaterThan(2);

          switchTab(tab);

          expect(Array.from(container.children).map(c => c.className)).toEqual(['page-header', 'empty-state']);
          expect(container.firstElementChild).toBe(header);
          expect(container.querySelector('.page-heading')?.textContent).toBe(heading);
          expect(container.querySelector('.empty-state')?.textContent).toBe(
            'You do not have access to this page. Contact your administrator if you need access.',
          );
        });
      });

      test('purchases tab: fires normally when user has view:purchases', () => {
        (canAccess as jest.Mock).mockReturnValue(true);
        switchTab('purchases');
        expect(initHistoryDateRange).toHaveBeenCalled();
        expect(loadHistory).toHaveBeenCalled();
      });

      test('inventory tab: fires normally when user has view:purchases', () => {
        (canAccess as jest.Mock).mockReturnValue(true);
        switchTab('inventory');
        expect(loadInventory).toHaveBeenCalled();
      });
    });
  });

  describe('switchSettingsSubTab', () => {
    // Sections are hidden via the `.hidden` utility class (not inline
    // style.display), so CSP's `style-src 'self'` doesn't block initial
    // parse of index.html. See issues/8.
    const isHidden = (id: string) =>
      document.getElementById(id)?.classList.contains('hidden') ?? null;

    test('shows general sections and hides others', () => {
      switchSettingsSubTab('general');

      expect(isHidden('settings-section')).toBe(false);
      expect(isHidden('purchasing-panel')).toBe(true);
      expect(isHidden('accounts-section')).toBe(true);
      expect(isHidden('users-section')).toBe(true);
      expect(isHidden('apikeys-section')).toBe(true);
    });

    test('shows purchasing panel and hides others', () => {
      switchSettingsSubTab('purchasing');

      expect(isHidden('purchasing-panel')).toBe(false);
      expect(isHidden('settings-section')).toBe(true);
      expect(isHidden('accounts-section')).toBe(true);
      expect(isHidden('users-section')).toBe(true);
      expect(isHidden('apikeys-section')).toBe(true);
    });

    test('purchasing sub-tab loads settings and automation', () => {
      switchSettingsSubTab('purchasing');

      expect(loadGlobalSettings).toHaveBeenCalled();
      expect(loadAutomationSettings).toHaveBeenCalled();
    });

    test('shows accounts section and hides others', () => {
      switchSettingsSubTab('accounts');

      expect(isHidden('settings-section')).toBe(true);
      expect(isHidden('purchasing-panel')).toBe(true);
      expect(isHidden('accounts-section')).toBe(false);
      expect(isHidden('users-section')).toBe(true);
      expect(isHidden('apikeys-section')).toBe(true);
    });

    test('shows users and apikeys sections for users sub-tab', () => {
      switchSettingsSubTab('users');

      expect(isHidden('settings-section')).toBe(true);
      expect(isHidden('purchasing-panel')).toBe(true);
      expect(isHidden('accounts-section')).toBe(true);
      expect(isHidden('users-section')).toBe(false);
      expect(isHidden('apikeys-section')).toBe(false);
    });

    test('falls back to general for unknown sub-tab', () => {
      switchSettingsSubTab('foobar');

      expect(isHidden('settings-section')).toBe(false);
      expect(isHidden('purchasing-panel')).toBe(true);
      expect(isHidden('accounts-section')).toBe(true);
    });

    test('toggles active class on sub-tab buttons', () => {
      switchSettingsSubTab('accounts');

      const generalBtn = document.querySelector('[data-settings-tab="general"]');
      const accountsBtn = document.querySelector('[data-settings-tab="accounts"]');
      expect(generalBtn?.classList.contains('active')).toBe(false);
      expect(accountsBtn?.classList.contains('active')).toBe(true);
    });

    test('redirects non-admin to general for admin-only sub-tabs', () => {
      const { isAdmin } = require('../auth');
      (isAdmin as jest.Mock).mockReturnValue(false);

      switchSettingsSubTab('accounts');

      expect(document.getElementById('settings-section')?.classList.contains('hidden')).toBe(false);
      expect(document.getElementById('accounts-section')?.classList.contains('hidden')).toBe(true);
    });
  });

  // QA A.4: switchInventorySubTab owns the /inventory/<subtab> history push,
  // mirroring switchSettingsSubTab. The DOM switch is delegated to the
  // (mocked) inventory module. Placed BEFORE the *FromPath describes, which
  // destructively replace window.location with a plain object and would
  // otherwise break the real history.pushState these tests rely on.
  describe('switchInventorySubTab', () => {
    beforeEach(() => {
      window.history.replaceState(null, '', '/inventory/active-commitments');
    });

    test('(c) pushes /inventory/<subtab> on a real switch', () => {
      switchInventorySubTab('coverage');
      expect(window.location.pathname).toBe('/inventory/coverage');
    });

    test('(c) preserves existing query params and hash', () => {
      window.history.replaceState(null, '', '/inventory/active-commitments?provider=aws#frag');
      switchInventorySubTab('ri-exchange');
      expect(window.location.pathname).toBe('/inventory/ri-exchange');
      expect(window.location.search).toBe('?provider=aws');
      expect(window.location.hash).toBe('#frag');
    });

    test('(d) an unknown sub-tab resolves to the default in the URL', () => {
      window.history.replaceState(null, '', '/inventory/coverage');
      switchInventorySubTab('bogus');
      expect(window.location.pathname).toBe('/inventory/active-commitments');
    });

    test('does NOT push a duplicate entry when already on the target sub-tab', () => {
      window.history.replaceState(null, '', '/inventory/coverage');
      const before = window.history.length;
      switchInventorySubTab('coverage');
      expect(window.location.pathname).toBe('/inventory/coverage');
      expect(window.history.length).toBe(before);
    });

    test('push: false switches the view without touching history', () => {
      switchInventorySubTab('coverage', { push: false });
      // URL unchanged: the caller (initial load / popstate) owns the URL.
      expect(window.location.pathname).toBe('/inventory/active-commitments');
    });
  });

  // Issue #1775: resolving the URL on a direct load / refresh. Placed BEFORE
  // the *FromPath describes for the same reason as switchInventorySubTab --
  // those replace window.location with a plain object.
  describe('applyTabFromPath', () => {
    test.each([
      ['/home', 'home'],
      ['/opportunities', 'opportunities'],
      ['/plans', 'plans'],
      ['/purchases', 'purchases'],
      ['/inventory', 'inventory'],
      ['/admin', 'admin'],
      ['/', 'home'],
      ['/plans/', 'plans'],
      ['/PLANS', 'plans'],
      ['/plans/extra/segments', 'plans'],
      ['/not-a-route', 'home'],
    ])('%s resolves to the %s tab', (path, expected) => {
      window.history.replaceState(null, '', path);
      expect(applyTabFromPath()).toBe(expected);
    });

    test.each([
      ['/dashboard', 'home'],
      ['/recommendations', 'opportunities'],
      ['/history', 'purchases'],
      ['/settings', 'admin'],
      ['/ri-exchange', 'inventory'],
    ])('legacy %s redirects to the %s tab and rewrites the URL', (path, expected) => {
      window.history.replaceState(null, '', path);
      expect(applyTabFromPath()).toBe(expected);
      expect(window.location.pathname).toBe('/' + expected);
    });

    // A path whose first segment names an Object.prototype member used to pass
    // the `segment in TABS` membership test, so switchTab then read an
    // undefined TabMeta (or the Object constructor) and threw out of init(),
    // stranding the app on its unrouted default markup.
    test.each(['/constructor', '/toString', '/valueOf', '/hasOwnProperty', '/__proto__'])(
      '%s is not a known route and falls back to home',
      (path) => {
        window.history.replaceState(null, '', path);
        expect(applyTabFromPath()).toBe('home');
      },
    );

    test('an Object.prototype sub-tab name is not a known admin sub-tab', () => {
      window.history.replaceState(null, '', '/admin/constructor');
      expect(getSettingsSubTabFromPath()).toBe('general');
      switchSettingsSubTab(getSettingsSubTabFromPath(), { push: false });
      expect(document.title).toBe('CUDly — Admin · General');
    });
  });
});
