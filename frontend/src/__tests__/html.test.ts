/**
 * HTML structure validation tests
 * These tests verify that the HTML structure is correct and accessible
 */
import { htmlContent, setupHtmlDom } from './html-harness';

describe('HTML Structure', () => {
  beforeEach(setupHtmlDom);

  describe('Document Structure', () => {
    test('has DOCTYPE declaration', () => {
      expect(htmlContent).toMatch(/<!DOCTYPE html>/i);
    });

    test('has html lang attribute', () => {
      expect(htmlContent).toMatch(/<html[^>]+lang=["']en["']/);
    });

    test('has head element', () => {
      expect(document.head).toBeTruthy();
    });

    test('has body element', () => {
      expect(document.body).toBeTruthy();
    });

    test('has meta charset', () => {
      const charset = document.querySelector('meta[charset]');
      expect(charset).toBeTruthy();
      expect(charset?.getAttribute('charset')?.toLowerCase()).toBe('utf-8');
    });

    test('has viewport meta tag', () => {
      const viewport = document.querySelector('meta[name="viewport"]');
      expect(viewport).toBeTruthy();
      expect(viewport?.getAttribute('content')).toContain('width=device-width');
    });

    test('has title containing CUDly', () => {
      expect(htmlContent).toMatch(/<title>[^<]*CUDly[^<]*<\/title>/);
    });
  });

  describe('Main Layout', () => {
    test('has app container', () => {
      const app = document.getElementById('app');
      expect(app).toBeTruthy();
    });

    test('has header', () => {
      const header = document.querySelector('header');
      expect(header).toBeTruthy();
    });

    test('has main content area', () => {
      const main = document.querySelector('main');
      expect(main).toBeTruthy();
    });

    test('has tab navigation', () => {
      // After the issue #340 action-center reskin, primary nav lives in the
      // sidebar's .app-sidebar-nav container instead of the old top
      // .tabs horizontal strip.
      const nav = document.querySelector('.app-sidebar-nav');
      expect(nav).toBeTruthy();
    });
  });

  describe('Tab Structure', () => {
    test('has tab buttons', () => {
      const tabs = document.querySelectorAll('.tab-btn');
      expect(tabs.length).toBeGreaterThan(0);
    });

    test('has home tab button', () => {
      const homeTab = document.querySelector('[data-tab="home"]');
      expect(homeTab).toBeTruthy();
    });

    test('has opportunities tab button', () => {
      const opportunitiesTab = document.querySelector('[data-tab="opportunities"]');
      expect(opportunitiesTab).toBeTruthy();
    });

    test('has plans tab button', () => {
      const plansTab = document.querySelector('[data-tab="plans"]');
      expect(plansTab).toBeTruthy();
    });

    test('has purchases tab button', () => {
      const purchasesTab = document.querySelector('[data-tab="purchases"]');
      expect(purchasesTab).toBeTruthy();
    });

    test('has inventory tab button', () => {
      const inventoryTab = document.querySelector('[data-tab="inventory"]');
      expect(inventoryTab).toBeTruthy();
    });

    test('has admin tab button', () => {
      const adminTab = document.querySelector('[data-tab="admin"]');
      expect(adminTab).toBeTruthy();
    });

    test('has tab content for each tab', () => {
      const tabContents = document.querySelectorAll('.tab-content');
      expect(tabContents.length).toBe(6);
    });
  });

  describe('Dashboard Tab', () => {
    test('has summary section', () => {
      const summary = document.getElementById('summary');
      expect(summary).toBeTruthy();
    });

    // Issue #908 merged the two per-service charts into one; the standalone
    // #savings-chart-section / #savings-chart "Potential Savings by Service"
    // chart was removed in favour of the merged range chart below.
    test('has the merged per-service savings range section', () => {
      const chartSection = document.getElementById('savings-by-service-section');
      expect(chartSection).toBeTruthy();
    });

    test('has the merged per-service savings range canvas', () => {
      const canvas = document.getElementById('savings-by-service-chart');
      expect(canvas).toBeTruthy();
      expect(canvas?.tagName.toLowerCase()).toBe('canvas');
    });

    test('no longer renders the old standalone savings chart (issue #908)', () => {
      expect(document.getElementById('savings-chart-section')).toBeNull();
      expect(document.getElementById('savings-chart')).toBeNull();
    });

    test('has upcoming purchases section', () => {
      const upcoming = document.getElementById('upcoming-purchases');
      expect(upcoming).toBeTruthy();
    });

    // Issue #52: the longest savings-trend range is capped at 365 days by the
    // analytics API, so its button must not promise "All" history.
    test('labels the longest savings-trend range 1y, not All (issue #52)', () => {
      const labels = Array.from(document.querySelectorAll<HTMLButtonElement>('.trend-range'))
        .map(b => b.textContent?.trim());
      expect(labels).toEqual(['7d', '30d', '90d', '1y']);
      const longest = document.querySelector<HTMLButtonElement>('.trend-range[data-range="all"]');
      expect(longest?.textContent?.trim()).toBe('1y');
      expect(longest?.title).toBe('Last 365 days');
    });
  });

  describe('Recommendations Tab', () => {
    // Bundle B (column-filter UX overhaul) deleted the legacy top filter bar
    // and the #recommendations-controls section that hosted it. The
    // service-filter / region-filter / min-savings-filter / provider-filter /
    // account-filter <select>s are gone — replaced by per-column header-mounted
    // popovers driven by recsflow/popover.ts:openColumnPopover. Negative-guard
    // tests below ensure no regression accidentally re-introduces them.
    test('legacy top filter bar is absent (Bundle B)', () => {
      const recsTab = document.getElementById('opportunities-tab');
      expect(recsTab).toBeTruthy();
      expect(document.getElementById('recommendations-controls')).toBeNull();
      // .controls-bar still exists on other tabs (Dashboard, Plans, …).
      // Only assert the rec tab is clean.
      expect(recsTab?.querySelector('.controls-bar')).toBeNull();
      expect(document.getElementById('service-filter')).toBeNull();
      expect(document.getElementById('region-filter')).toBeNull();
      expect(document.getElementById('min-savings-filter')).toBeNull();
      expect(document.getElementById('recommendations-account-filter')).toBeNull();
      expect(document.getElementById('recommendations-provider-filter')).toBeNull();
    });

    test('has recommendations list container', () => {
      const list = document.getElementById('recommendations-list');
      expect(list).toBeTruthy();
    });
  });

  describe('Plans Tab', () => {
    test('has plans header', () => {
      const header = document.getElementById('plans-header');
      expect(header).toBeTruthy();
    });

    test('has plans list container', () => {
      const list = document.getElementById('plans-list');
      expect(list).toBeTruthy();
    });
  });

  describe('History Tab', () => {
    test('has history controls', () => {
      const controls = document.getElementById('history-controls');
      expect(controls).toBeTruthy();
    });

    test('has date range inputs', () => {
      const startDate = document.getElementById('history-start') as HTMLInputElement | null;
      const endDate = document.getElementById('history-end') as HTMLInputElement | null;
      expect(startDate).toBeTruthy();
      expect(endDate).toBeTruthy();
      expect(startDate?.getAttribute('type')).toBe('date');
      expect(endDate?.getAttribute('type')).toBe('date');
    });

    // Issue #344 T2: provider/account filters moved out of the History
    // tab into the global topbar. The history page now only carries the
    // date-range inputs + Load History button. Coverage for the topbar
    // slot lives in the "Topbar" describe block below.

    test('has history list container', () => {
      const list = document.getElementById('history-list');
      expect(list).toBeTruthy();
    });

    // Issue #340 sub-task: the Approval queue card sits ABOVE the
    // Savings History + Purchase History sections so pending
    // approvals jump out without scrolling. Verify both the wrapper
    // section and the inner mount point are present, AND that the
    // queue card precedes both history sections in document order.
    // CR-feedback (PR #387): existence alone doesn't guard the
    // "at the top of the Purchases tab" requirement.
    test('has approval queue section + mount point + appears before history sections', () => {
      const section = document.getElementById('purchases-approval-queue-section');
      const mount = document.getElementById('purchases-approval-queue');
      const savings = document.getElementById('savings-history-section');
      const history = document.getElementById('purchase-history-section');
      expect(section).toBeTruthy();
      expect(mount).toBeTruthy();
      expect(savings).toBeTruthy();
      expect(history).toBeTruthy();
      // compareDocumentPosition returns a bitmask; FOLLOWING means
      // the argument is positioned AFTER the receiver in document order.
      const FOLLOWING = Node.DOCUMENT_POSITION_FOLLOWING;
      expect(section!.compareDocumentPosition(savings!) & FOLLOWING).toBeTruthy();
      expect(section!.compareDocumentPosition(history!) & FOLLOWING).toBeTruthy();
    });
  });

  describe('Settings Tab', () => {
    test('has settings section', () => {
      const section = document.getElementById('settings-section');
      expect(section).toBeTruthy();
    });

    test('has settings form', () => {
      const form = document.getElementById('global-settings-form');
      expect(form).toBeTruthy();
    });

    test('has provider checkboxes', () => {
      const awsCheck = document.getElementById('provider-aws');
      const azureCheck = document.getElementById('provider-azure');
      const gcpCheck = document.getElementById('provider-gcp');
      expect(awsCheck).toBeTruthy();
      expect(azureCheck).toBeTruthy();
      expect(gcpCheck).toBeTruthy();
    });

    test('has notification email input', () => {
      const email = document.getElementById('setting-notification-email') as HTMLInputElement | null;
      expect(email).toBeTruthy();
      expect(email?.getAttribute('type')).toBe('email');
    });

    test('has default term select', () => {
      const term = document.getElementById('setting-default-term');
      expect(term).toBeTruthy();
    });

    test('has default payment select', () => {
      const payment = document.getElementById('setting-default-payment');
      expect(payment).toBeTruthy();
    });

    // Issue #345: settings coverage only drives the Dashboard target and its
    // savings scaling; purchase plans size by their own Target Coverage.
    test('labels settings coverage as a Dashboard target, not purchase sizing', () => {
      const labelFor = (id: string) =>
        document.querySelector(`label[for="${id}"]`)?.textContent?.trim()
        ?? document.getElementById(id)?.closest('label')?.textContent?.trim();

      expect(labelFor('setting-default-coverage')).toBe('Dashboard coverage target (%)');
      for (const sp of ['compute', 'ec2instance', 'sagemaker', 'database']) {
        expect(labelFor(`aws-savings-plans-${sp}-coverage`)).toBe('Dashboard coverage %:');
      }
      expect(labelFor('override-coverage')).toBe('Dashboard coverage (%):');

      const help = document.getElementById('coverage-display-only-help');
      expect(help?.textContent).toContain('do not change how much CUDly buys');
      expect(document.getElementById('setting-default-coverage')?.getAttribute('aria-describedby'))
        .toBe('coverage-display-only-help');
    });

    test('keeps the plan form Target Coverage label, which sizes plan steps', () => {
      const plan = document.getElementById('plan-coverage');
      expect(plan?.closest('label')?.textContent?.trim()).toBe('Target coverage (%):');
    });
  });

  describe('Modals', () => {
    test('has plan modal', () => {
      const modal = document.getElementById('plan-modal');
      expect(modal).toBeTruthy();
      expect(modal?.classList.contains('hidden')).toBe(true);
    });

    test('has plan form', () => {
      const form = document.getElementById('plan-form');
      expect(form).toBeTruthy();
    });

    test('has purchase modal', () => {
      const modal = document.getElementById('purchase-modal');
      expect(modal).toBeTruthy();
    });
  });

  describe('Form Elements', () => {
    test('plan name input exists', () => {
      const input = document.getElementById('plan-name') as HTMLInputElement | null;
      expect(input).toBeTruthy();
      expect(input?.hasAttribute('required')).toBe(true);
    });

    test('plan provider select exists', () => {
      const select = document.getElementById('plan-provider');
      expect(select).toBeTruthy();
      expect(select?.querySelectorAll('option').length).toBeGreaterThan(0);
    });

    test('plan service select exists', () => {
      const select = document.getElementById('plan-service');
      expect(select).toBeTruthy();
    });

    test('ramp schedule options exist', () => {
      const radios = document.querySelectorAll('input[name="ramp-schedule"]');
      expect(radios.length).toBe(4);
    });
  });

  describe('Accessibility', () => {
    test('buttons have text content', () => {
      const buttons = document.querySelectorAll('button');
      buttons.forEach(button => {
        const hasText = (button.textContent?.trim().length ?? 0) > 0;
        const hasAriaLabel = button.hasAttribute('aria-label');
        expect(hasText || hasAriaLabel).toBe(true);
      });
    });

    test('images have alt attributes', () => {
      const images = document.querySelectorAll('img');
      images.forEach(img => {
        expect(img.hasAttribute('alt')).toBe(true);
      });
    });

    test('links have href attributes', () => {
      const links = document.querySelectorAll('a');
      links.forEach(link => {
        const href = link.getAttribute('href');
        expect(href).toBeTruthy();
      });
    });

    // Issue #729 (UX-36): aria-label on a role-less div is ignored by
    // assistive tech (axe: aria-allowed-attr / aria-prohibited-attr).
    test('the global filter container is a labelled group', () => {
      const filters = document.getElementById('topbar-filters');
      expect(filters?.getAttribute('role')).toBe('group');
      expect(filters?.getAttribute('aria-label')).toBe('Global filters');
    });
  });

  describe('Select Options', () => {
    // Issue #344 T2: per-section provider filter <select>s removed.
    // The provider filter now lives in the global topbar as a chip-
    // select (custom popover listbox, not a <select>). The slot exists
    // at #topbar-filters; chip-select option coverage lives in
    // chip-select.test.ts and topbar-filters.test.ts.
    test('topbar filters slot exists', () => {
      const slot = document.getElementById('topbar-filters');
      expect(slot).toBeTruthy();
    });

    // Bundle B: #service-filter <select> deleted; service filtering happens
    // via the per-column header popover. See tests/recommendations.test.ts
    // for the popover behaviour.

    test('term select has 1 and 3 year options', () => {
      const selector = document.getElementById('setting-default-term');
      const options = selector?.querySelectorAll('option') ?? [];
      const values = Array.from(options).map(o => o.value);

      expect(values).toContain('1');
      expect(values).toContain('3');
    });

    test('payment select has all options', () => {
      const selector = document.getElementById('setting-default-payment');
      const options = selector?.querySelectorAll('option') ?? [];
      const values = Array.from(options).map(o => o.value);

      expect(values).toContain('no-upfront');
      expect(values).toContain('partial-upfront');
      expect(values).toContain('all-upfront');
    });

    // Bundle B: legacy service-filter optgroups deleted along with the
    // <select id="service-filter">. The Service column-header popover lists
    // distinct service values from the loaded recs at runtime, no static
    // optgroup tree.

    // Refs #45 — admins land on the Accounts page primarily to action the
    // pending registration queue, so the filter must default to "pending".
    test('registrations status filter defaults to "pending"', () => {
      const selector = document.getElementById('registrations-status-filter') as HTMLSelectElement | null;
      expect(selector).toBeTruthy();
      // value reflects the initial `selected` option in static HTML.
      expect(selector?.value).toBe('pending');
      const selected = selector?.querySelector('option[selected]') as HTMLOptionElement | null;
      expect(selected?.value).toBe('pending');
    });
  });

  describe('Accounts Section Layout', () => {
    // Refs #45 — Account Registrations must render above all per-provider
    // account tables so pending registrations across providers stay visible
    // at the top of the Accounts page.
    test('registrations fieldset precedes per-provider account fieldsets in DOM order', () => {
      const registrations = document.getElementById('accounts-registrations');
      const awsBlock = document.getElementById('accounts-aws-block');
      const azureBlock = document.getElementById('accounts-azure-block');
      const gcpBlock = document.getElementById('accounts-gcp-block');

      expect(registrations).toBeTruthy();
      expect(awsBlock).toBeTruthy();
      expect(azureBlock).toBeTruthy();
      expect(gcpBlock).toBeTruthy();

      // DOCUMENT_POSITION_FOLLOWING (0x04) means the argument follows `node`.
      const FOLLOWING = Node.DOCUMENT_POSITION_FOLLOWING;
      expect(registrations!.compareDocumentPosition(awsBlock!) & FOLLOWING).toBeTruthy();
      expect(registrations!.compareDocumentPosition(azureBlock!) & FOLLOWING).toBeTruthy();
      expect(registrations!.compareDocumentPosition(gcpBlock!) & FOLLOWING).toBeTruthy();
    });
  });

  describe('Security Headers', () => {
    test('has Content Security Policy meta tag', () => {
      const csp = document.querySelector('meta[http-equiv="Content-Security-Policy"]');
      expect(csp).toBeTruthy();
      expect(csp?.getAttribute('content')).toContain("default-src 'self'");
    });

    // Regression guard for issue #8 — these three directives must NOT creep
    // back into the meta tag. frame-ancestors is header-only (browsers ignore
    // it in <meta>), and the two execute-api / lambda-url patterns are
    // invalid mid-hostname wildcards that browsers silently drop.
    test('meta CSP omits frame-ancestors and invalid wildcard hosts', () => {
      const csp = document.querySelector('meta[http-equiv="Content-Security-Policy"]');
      const content = csp?.getAttribute('content') ?? '';
      expect(content).not.toContain('frame-ancestors');
      expect(content).not.toContain('*.execute-api.*.amazonaws.com');
      expect(content).not.toContain('*.lambda-url.*.on.aws');
    });

    test('has referrer policy meta tag', () => {
      const referrer = document.querySelector('meta[name="referrer"]');
      expect(referrer).toBeTruthy();
      expect(referrer?.getAttribute('content')).toBe('strict-origin-when-cross-origin');
    });
  });
});
