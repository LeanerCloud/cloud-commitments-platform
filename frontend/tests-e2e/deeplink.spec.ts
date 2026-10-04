/**
 * Deep-link / refresh smoke for every route the SPA defines (issue #1775).
 *
 * The reported failure ("loading /plans directly renders the Home dashboard")
 * is only observable on the *initial load* path: a client-side nav to the same
 * route works, so any test that clicks the nav item stays green while the bug
 * is live. Each case therefore navigates the browser straight at the URL, and
 * then reloads it, asserting on what the user actually sees: which nav item is
 * highlighted, which panel is on screen, the tab title, and the canonical URL.
 *
 * `npx serve -s dist` (playwright.config.ts webServer) mirrors the SPA
 * fallback the Go static handler performs, so a failure here is a client
 * routing failure. The server half of the same contract is pinned by
 * TestSPAFallbackServesIndexForEveryAppRoute in internal/server/static_test.go.
 */

import { test, expect, type Page } from '@playwright/test';
import { mockApi, seedAuth } from './fixtures/recs';

interface RouteCase {
  /** URL the browser is pointed at. */
  url: string;
  /** data-tab of the nav item that must end up highlighted. */
  tab: string;
  /** document.title after the route resolves. */
  title: string;
  /** location.pathname after the app canonicalises the URL. */
  canonical: string;
}

const ROUTES: RouteCase[] = [
  { url: '/', tab: 'home', title: 'CUDly — Home', canonical: '/home' },
  { url: '/home', tab: 'home', title: 'CUDly — Home', canonical: '/home' },
  { url: '/opportunities', tab: 'opportunities', title: 'CUDly — Opportunities', canonical: '/opportunities' },
  { url: '/plans', tab: 'plans', title: 'CUDly — Plans', canonical: '/plans' },
  { url: '/purchases', tab: 'purchases', title: 'CUDly — Purchases', canonical: '/purchases' },

  // Inventory and Admin carry a sub-tab segment. The initial replaceState must
  // preserve it: it is the input switchTab reads to pick the sub-section.
  { url: '/inventory', tab: 'inventory', title: 'CUDly — Inventory & Coverage', canonical: '/inventory/active-commitments' },
  { url: '/inventory/active-commitments', tab: 'inventory', title: 'CUDly — Inventory & Coverage', canonical: '/inventory/active-commitments' },
  { url: '/inventory/coverage', tab: 'inventory', title: 'CUDly — Inventory & Coverage', canonical: '/inventory/coverage' },
  { url: '/inventory/ri-exchange', tab: 'inventory', title: 'CUDly — Inventory & Coverage', canonical: '/inventory/ri-exchange' },
  { url: '/admin', tab: 'admin', title: 'CUDly — Admin · General', canonical: '/admin/general' },
  { url: '/admin/general', tab: 'admin', title: 'CUDly — Admin · General', canonical: '/admin/general' },
  { url: '/admin/purchasing', tab: 'admin', title: 'CUDly — Admin · Purchasing policies', canonical: '/admin/purchasing' },
  { url: '/admin/accounts', tab: 'admin', title: 'CUDly — Admin · Accounts & onboarding', canonical: '/admin/accounts' },
  { url: '/admin/users', tab: 'admin', title: 'CUDly — Admin · Users, roles & API keys', canonical: '/admin/users' },

  // Pre-#340 bookmarks, kept alive by LEGACY_PATH_REDIRECTS.
  { url: '/dashboard', tab: 'home', title: 'CUDly — Home', canonical: '/home' },
  { url: '/recommendations', tab: 'opportunities', title: 'CUDly — Opportunities', canonical: '/opportunities' },
  { url: '/history', tab: 'purchases', title: 'CUDly — Purchases', canonical: '/purchases' },
  { url: '/settings', tab: 'admin', title: 'CUDly — Admin · General', canonical: '/admin/general' },
  { url: '/ri-exchange', tab: 'inventory', title: 'CUDly — Inventory & Coverage', canonical: '/inventory/active-commitments' },

  // Unknown paths land on Home rather than an unrouted shell.
  { url: '/not-a-route', tab: 'home', title: 'CUDly — Home', canonical: '/home' },

  // A first segment naming an Object.prototype member used to pass the
  // `segment in TABS` membership test; pushing the resulting non-tab value
  // threw DataCloneError out of init(), leaving the app on its unrouted
  // markup (Home panel, generic title, no event listeners bound).
  { url: '/constructor', tab: 'home', title: 'CUDly — Home', canonical: '/home' },
  { url: '/__proto__', tab: 'home', title: 'CUDly — Home', canonical: '/home' },
  { url: '/admin/constructor', tab: 'admin', title: 'CUDly — Admin · General', canonical: '/admin/general' },
];

/** What the user can see once routing has settled. */
async function observe(page: Page) {
  return page.evaluate(() => ({
    title: document.title,
    path: window.location.pathname,
    tab: document.querySelector('.tab-btn.active')?.getAttribute('data-tab') ?? null,
    panel: document.querySelector('.tab-content.active')?.id ?? null,
  }));
}

for (const route of ROUTES) {
  test(`${route.url} renders the ${route.tab} page on direct load and on refresh`, async ({ page }) => {
    // Uncaught exceptions only. The stub API fixture 404s endpoints this spec
    // does not care about, and those are reported as console errors by design.
    const pageErrors: string[] = [];
    page.on('pageerror', (err) => pageErrors.push(String(err)));

    await seedAuth(page);
    await mockApi(page);

    await page.goto(route.url);
    await expect(page.locator(`.tab-content.active#${route.tab}-tab`)).toBeAttached();

    const onLoad = await observe(page);
    expect(onLoad).toEqual({
      title: route.title,
      path: route.canonical,
      tab: route.tab,
      panel: `${route.tab}-tab`,
    });

    // Refresh from the canonical URL the app just wrote: the issue reports the
    // failure on refresh as well as on the first load, and the canonical URL is
    // what a user bookmarks or shares.
    await page.reload();
    await expect(page.locator(`.tab-content.active#${route.tab}-tab`)).toBeAttached();
    expect(await observe(page)).toEqual(onLoad);

    expect(pageErrors).toEqual([]);
  });
}

test('a deep-linked Inventory sub-tab opens that sub-section, not the default', async ({ page }) => {
  await seedAuth(page);
  await mockApi(page);

  await page.goto('/inventory/coverage');
  await expect(page.locator('#inventory-tab .sub-tab-btn.active')).toHaveText(/coverage/i);
  expect(new URL(page.url()).pathname).toBe('/inventory/coverage');
});

test.describe('authentication reloads', () => {
  test.beforeEach(async ({ page }) => {
    await mockApi(page);
    await page.route('**/api/info', route => route.fulfill({ json: { admin_exists: true } }));
    // Unlike seedAuth, this must not restore authentication after logout reloads.
    await page.addInitScript(() => {
      if (!sessionStorage.getItem('reload-smoke-seeded')) {
        localStorage.setItem('authToken', 'smoke-token');
        sessionStorage.setItem('reload-smoke-seeded', 'true');
      }
    });
    await page.goto('/home');
    await expect(page.locator('#user-email-display')).toHaveText('smoke@example.com');
  });

  test('UI logout reloads the document and returns to login', async ({ page }) => {
    await page.route('**/api/auth/logout', route => route.fulfill({ status: 204 }));
    const originalDocument = await page.evaluate(() => performance.timeOrigin);
    let documentRequests = 0;
    page.on('request', request => {
      if (request.isNavigationRequest() && request.frame() === page.mainFrame()
          && request.resourceType() === 'document') documentRequests++;
    });

    const [logoutRequest] = await Promise.all([
      page.waitForRequest(request => new URL(request.url()).pathname === '/api/auth/logout'
        && request.method() === 'POST'),
      page.waitForEvent('load'),
      page.locator('#logout-btn').click(),
    ]);

    expect(logoutRequest.method()).toBe('POST');
    await expect(page.locator('#login-form')).toBeVisible();
    expect(documentRequests).toBe(1);
    expect(await page.evaluate(() => performance.timeOrigin)).not.toBe(originalDocument);
    expect(await page.evaluate(() =>
      (performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming).type
    )).toBe('reload');
    expect(await page.evaluate(() => ['authToken', 'apiKey', 'csrfToken'].map(key =>
      [localStorage.getItem(key), sessionStorage.getItem(key)]
    ))).toEqual([[null, null], [null, null], [null, null]]);
  });

  test('cross-tab sign-out reloads only when an auth key is cleared', async ({ page, context }) => {
    const source = await context.newPage();
    await mockApi(source);
    await source.goto('/home');
    await expect(source.locator('#user-email-display')).toHaveText('smoke@example.com');
    const originalDocument = await page.evaluate(() => performance.timeOrigin);
    let documentRequests = 0;
    page.on('request', request => {
      if (request.isNavigationRequest() && request.frame() === page.mainFrame()
          && request.resourceType() === 'document') documentRequests++;
    });
    await page.evaluate(() => {
      window.addEventListener('storage', event => {
        document.documentElement.dataset.lastStorageEvent = JSON.stringify([event.key, event.newValue]);
      });
    });

    for (const [key, value] of [['unrelated-setting', 'changed'], ['authToken', 'refreshed-token']] as const) {
      await source.evaluate(([eventKey, eventValue]) => localStorage.setItem(eventKey, eventValue), [key, value] as const);
      await expect(page.locator('html')).toHaveAttribute('data-last-storage-event', JSON.stringify([key, value]));
      await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => resolve())));
      expect(documentRequests).toBe(0);
      expect(await page.evaluate(() => performance.timeOrigin)).toBe(originalDocument);
      await expect(page.locator('#login-modal')).toHaveCount(0);
    }

    await Promise.all([
      page.waitForEvent('load'),
      source.evaluate(() => localStorage.removeItem('authToken')),
    ]);

    await expect(page.locator('#login-form')).toBeVisible();
    expect(documentRequests).toBe(1);
    expect(await page.evaluate(() => performance.timeOrigin)).not.toBe(originalDocument);
    expect(await page.evaluate(() =>
      (performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming).type
    )).toBe('reload');
    expect(await page.evaluate(() => localStorage.getItem('authToken'))).toBeNull();
    await source.close();
  });
});
