/**
 * HTML structure validation tests, part 2: admin-only sections, modals, the
 * header and form validation attributes. Part 1 is html.test.ts; the shared
 * markup and per-test DOM restore live in html-harness.ts.
 */
import { bodyContent, htmlContent, setupHtmlDom } from './html-harness';

describe('HTML Structure', () => {
  beforeEach(setupHtmlDom);

  describe('Admin-Only Sections', () => {
    test('has API keys section', () => {
      const section = document.getElementById('apikeys-section');
      expect(section).toBeTruthy();
      expect(section?.classList.contains('admin-only')).toBe(true);
    });

    test('has users management section', () => {
      const section = document.getElementById('users-section');
      expect(section).toBeTruthy();
      expect(section?.classList.contains('admin-only')).toBe(true);
    });

    // Issue #365: sidebar Admin tab is admin-gated so a non-admin
    // session never sees a link into a page they can't use.
    test('sidebar Admin button is admin-only', () => {
      const btn = document.getElementById('admin-tab-btn');
      expect(btn).toBeTruthy();
      expect(btn?.classList.contains('admin-only')).toBe(true);
    });

    test('has create user button', () => {
      const btn = document.getElementById('create-user-btn');
      expect(btn).toBeTruthy();
    });

    test('has create group button', () => {
      const btn = document.getElementById('create-group-btn');
      expect(btn).toBeTruthy();
    });

    test('has create API key button', () => {
      const btn = document.getElementById('create-apikey-btn');
      expect(btn).toBeTruthy();
    });
  });

  describe('User Management Modal', () => {
    test('has user modal', () => {
      const modal = document.getElementById('user-modal');
      expect(modal).toBeTruthy();
      expect(modal?.classList.contains('hidden')).toBe(true);
    });

    test('has user form with email field', () => {
      const email = document.getElementById('user-email') as HTMLInputElement | null;
      expect(email).toBeTruthy();
      expect(email?.getAttribute('type')).toBe('email');
      expect(email?.hasAttribute('required')).toBe(true);
    });

    test('user role select is absent (PR #912: role concept dropped)', () => {
      // PR #912 removed the role column. The role selector was replaced by
      // a required group multi-select. Verify the old element is gone so a
      // regression that re-adds it is caught.
      const role = document.getElementById('user-role') as HTMLSelectElement | null;
      expect(role).toBeNull();
    });

    test('has user groups multi-select (required, PR #912)', () => {
      const groups = document.getElementById('user-groups') as HTMLSelectElement | null;
      expect(groups).toBeTruthy();
      expect(groups?.hasAttribute('multiple')).toBe(true);
      // PR #912: groups is now required (>= 1 group enforced by backend DB CHECK).
      expect(groups?.hasAttribute('required')).toBe(true);
    });
  });

  describe('Group Management Modal', () => {
    test('has group modal', () => {
      const modal = document.getElementById('group-modal');
      expect(modal).toBeTruthy();
    });

    test('has group form with name field', () => {
      const name = document.getElementById('group-name') as HTMLInputElement | null;
      expect(name).toBeTruthy();
      expect(name?.hasAttribute('required')).toBe(true);
    });

    test('has group description textarea', () => {
      const desc = document.getElementById('group-description');
      expect(desc).toBeTruthy();
      expect(desc?.tagName.toLowerCase()).toBe('textarea');
    });

    test('has permissions list container', () => {
      const list = document.getElementById('permissions-list');
      expect(list).toBeTruthy();
    });
  });

  describe('API Key Modal', () => {
    test('has create API key modal', () => {
      const modal = document.getElementById('create-apikey-modal');
      expect(modal).toBeTruthy();
    });

    test('has API key name input', () => {
      const input = document.getElementById('apikey-name') as HTMLInputElement | null;
      expect(input).toBeTruthy();
      expect(input?.hasAttribute('required')).toBe(true);
    });

    test('has required password input (issue #102)', () => {
      const input = document.getElementById('apikey-password') as HTMLInputElement | null;
      expect(input).toBeTruthy();
      expect(input?.type).toBe('password');
      expect(input?.hasAttribute('required')).toBe(true);
    });

    test('has required expiration date field (issue #102)', () => {
      const field = document.getElementById('apikey-expires-at') as HTMLInputElement | null;
      expect(field).toBeTruthy();
      expect(field?.hasAttribute('required')).toBe(true);
    });
  });

  describe('Savings History Section', () => {
    test('has savings history section', () => {
      const section = document.getElementById('savings-history-section');
      expect(section).toBeTruthy();
    });

    // Issue #344 follow-up: page-hero rolled out to the remaining
    // section titles so the typographic hierarchy reads consistently
    // across Plans / Purchases / Inventory / Admin.
    test('uses the page-hero treatment for its title', () => {
      const section = document.getElementById('savings-history-section');
      expect(section?.classList.contains('page-hero')).toBe(true);
    });

    test('has savings period selector', () => {
      const select = document.getElementById('savings-period') as HTMLSelectElement | null;
      expect(select).toBeTruthy();
      const options = Array.from(select?.querySelectorAll('option') ?? []).map(o => o.value);
      expect(options).toContain('24h');
      expect(options).toContain('7d');
      expect(options).toContain('30d');
      expect(options).toContain('90d');
    });

    test('has savings stats cards', () => {
      const statsGrid = document.getElementById('savings-stats');
      expect(statsGrid).toBeTruthy();
      const statCards = statsGrid?.querySelectorAll('.stat-card');
      expect(statCards?.length).toBeGreaterThanOrEqual(3);
    });

    test('has savings history chart canvas', () => {
      const canvas = document.getElementById('savings-history-chart');
      expect(canvas).toBeTruthy();
      expect(canvas?.tagName.toLowerCase()).toBe('canvas');
    });

    test('has empty state for savings history', () => {
      const empty = document.getElementById('savings-history-empty');
      expect(empty).toBeTruthy();
      expect(empty?.classList.contains('hidden')).toBe(true);
    });
  });

  describe('Planned Purchases Section', () => {
    test('has planned purchases header', () => {
      const header = document.getElementById('planned-purchases-header');
      expect(header).toBeTruthy();
    });

    // Issue #344 follow-up: page-hero rolled out. The help-text
    // paragraph picks up page-hero-description so it inherits the
    // muted-body styling used on the other hero sections.
    test('uses the page-hero treatment for its title + description', () => {
      const header = document.getElementById('planned-purchases-header');
      expect(header?.classList.contains('page-hero')).toBe(true);
      const desc = header?.querySelector('.page-hero-description');
      expect(desc).toBeTruthy();
    });

    test('has planned purchases list container', () => {
      const list = document.getElementById('planned-purchases-list');
      expect(list).toBeTruthy();
    });
  });

  describe('Header Elements', () => {
    test('has user info container', () => {
      const userInfo = document.getElementById('user-info');
      expect(userInfo).toBeTruthy();
    });

    test('has user email span', () => {
      const email = document.getElementById('user-email');
      expect(email).toBeTruthy();
    });

    test('has logout button', () => {
      const btn = document.getElementById('logout-btn');
      expect(btn).toBeTruthy();
    });

    test('has API docs link', () => {
      const link = document.querySelector('a[href="/docs/"]');
      expect(link).toBeTruthy();
      expect(link?.getAttribute('target')).toBe('_blank');
    });

    test('has feedback link', () => {
      const link = document.getElementById('feedback-link');
      expect(link).toBeTruthy();
    });
  });

  describe('Form Validation Attributes', () => {
    test('number inputs have min/max constraints', () => {
      const coverage = document.getElementById('setting-default-coverage') as HTMLInputElement | null;
      expect(coverage?.getAttribute('min')).toBe('0');
      expect(coverage?.getAttribute('max')).toBe('100');

      const notifyDays = document.getElementById('setting-notification-days') as HTMLInputElement | null;
      expect(notifyDays?.getAttribute('min')).toBe('1');
      expect(notifyDays?.getAttribute('max')).toBe('30');

      // #702: ramp-interval-days must carry max=365 so the browser spinner
      // stays bounded and wireRangeInput uses the correct upper limit.
      const intervalDays = document.getElementById('ramp-interval-days') as HTMLInputElement | null;
      expect(intervalDays?.getAttribute('min')).toBe('1');
      expect(intervalDays?.getAttribute('max')).toBe('365');
    });

    test('password inputs have minlength', () => {
      const password = document.getElementById('user-password') as HTMLInputElement | null;
      expect(password?.getAttribute('minlength')).toBe('12');
    });

    test('azure client-secret input carries a guidance hint (issue #15)', () => {
      const secretFields = document.getElementById('account-azure-secret-fields');
      expect(secretFields).not.toBeNull();
      const small = secretFields?.querySelector('small');
      expect(small).not.toBeNull();
      // Confirm the copy steers operators away from storing a long-lived
      // credential without spelling out a specific expiry policy the
      // hint might drift from.
      expect(small?.textContent).toMatch(/Workload Identity Federation/i);
      expect(small?.textContent).toMatch(/rotate/i);
    });

    test('azure cognitive search has service-default term + payment selectors (issue #16)', () => {
      // The recommendation filter offered "search" as an Azure service,
      // but the purchasing settings panel lacked a matching card.
      // providers/azure/services/search supports Cognitive Search
      // reservations, so the right fix is to expose the defaults.
      const termSel = document.getElementById('azure-search-term') as HTMLSelectElement | null;
      const paySel = document.getElementById('azure-search-payment') as HTMLSelectElement | null;
      expect(termSel).not.toBeNull();
      expect(paySel).not.toBeNull();
      expect(termSel?.querySelector('option[value="1"]')).not.toBeNull();
      expect(termSel?.querySelector('option[value="3"]')).not.toBeNull();
      // Azure payment values are the backend's Azure tokens (issue #545).
      expect(paySel?.querySelector('option[value="upfront"]')).not.toBeNull();
      expect(paySel?.querySelector('option[value="monthly"]')).not.toBeNull();
    });

    test('aws external-id input is readonly with a copy button + trust-policy hint (issue #18)', () => {
      const input = document.getElementById('account-aws-external-id') as HTMLInputElement | null;
      expect(input).not.toBeNull();
      expect(input?.hasAttribute('readonly')).toBe(true);
      const copyBtn = document.getElementById('account-aws-external-id-copy');
      expect(copyBtn).not.toBeNull();
      expect(copyBtn?.classList.contains('copy-btn')).toBe(true);
      // The hint must point operators at sts:ExternalId so they don't
      // paste the wrong value into the trust policy.
      const roleFields = document.getElementById('account-aws-role-fields');
      const small = roleFields?.querySelector('small');
      expect(small?.textContent).toMatch(/sts:ExternalId/);
    });

    test('aws trust-policy snippet block + copy button exist in role-fields (issue #19)', () => {
      const roleFields = document.getElementById('account-aws-role-fields');
      expect(roleFields).not.toBeNull();
      const pre = document.getElementById('account-aws-trust-policy');
      expect(pre?.tagName.toLowerCase()).toBe('pre');
      const copyBtn = document.getElementById('account-aws-trust-policy-copy');
      expect(copyBtn).not.toBeNull();
      expect(copyBtn?.classList.contains('copy-btn')).toBe(true);
      // Hint is rendered dynamically by settings.ts; confirm the
      // placeholder element exists so the render target is stable.
      const hint = document.getElementById('account-aws-trust-policy-hint');
      expect(hint).not.toBeNull();
    });

    test('aws bastion-fields render an external-id input + trust-policy block (issue #129)', () => {
      const bastionFields = document.getElementById('account-aws-bastion-fields');
      expect(bastionFields).not.toBeNull();

      const input = document.getElementById('account-aws-external-id-bastion') as HTMLInputElement | null;
      expect(input).not.toBeNull();
      expect(input?.hasAttribute('readonly')).toBe(true);
      // The input must live inside the bastion-fields section so it's
      // hidden in non-bastion modes via the parent's `hidden` class.
      expect(bastionFields?.contains(input!)).toBe(true);

      const copyBtn = document.getElementById('account-aws-external-id-bastion-copy');
      expect(copyBtn).not.toBeNull();
      expect(copyBtn?.classList.contains('copy-btn')).toBe(true);

      const pre = document.getElementById('account-aws-trust-policy-bastion');
      expect(pre?.tagName.toLowerCase()).toBe('pre');
      const policyCopyBtn = document.getElementById('account-aws-trust-policy-bastion-copy');
      expect(policyCopyBtn?.classList.contains('copy-btn')).toBe(true);
      const hint = document.getElementById('account-aws-trust-policy-bastion-hint');
      expect(hint).not.toBeNull();
    });

    test('aws IAM console deep link opens the role-creation wizard in a new tab (issue #21)', () => {
      const link = document.getElementById('account-aws-iam-console-link') as HTMLAnchorElement | null;
      expect(link).not.toBeNull();
      expect(link?.tagName.toLowerCase()).toBe('a');
      // Must open in a new tab so operators don't lose the modal
      // state they just configured. rel="noopener" is mandatory for
      // target="_blank" to avoid reverse-tabnabbing.
      expect(link?.getAttribute('target')).toBe('_blank');
      expect(link?.getAttribute('rel') ?? '').toMatch(/noopener/);
      // Link target must be the AWS console role-creation wizard.
      expect(link?.getAttribute('href')).toBe('https://console.aws.amazon.com/iam/home#/roles$new');
    });

    test('profile entry point is a keyboard-focusable button (#715)', () => {
      const el = document.getElementById('user-email-display');
      expect(el?.tagName.toLowerCase()).toBe('button');
      expect(el?.getAttribute('type')).toBe('button');
      // Inert until updateUserUI shows it with the signed-in email.
      expect(el?.hasAttribute('hidden')).toBe(true);
    });

    test('every target="_blank" link sets rel="noopener noreferrer" (#136)', () => {
      const links = Array.from(document.querySelectorAll<HTMLAnchorElement>('a[target="_blank"]'));
      expect(links.length).toBeGreaterThan(0);
      for (const link of links) {
        expect(link.getAttribute('rel')).toBe('noopener noreferrer');
      }
    });
  });

  describe('Accounts page section titles (#1691)', () => {
    test('fieldset legends on the Accounts page contain headings', () => {
      document.body.innerHTML = bodyContent;
      for (const id of ['accounts-federation', 'accounts-registrations', 'accounts-aws-block', 'accounts-azure-block', 'accounts-gcp-block']) {
        expect(document.querySelector(`#${id} > legend > h4`)).not.toBeNull();
      }
      expect(document.querySelector('#accounts-federation h4')?.textContent).toBe('Federation setup (add new account)');
      expect(document.querySelector('#accounts-registrations h4')?.textContent).toBe('Account registrations');
    });

    test('target cloud control is a radiogroup', () => {
      document.body.innerHTML = bodyContent;
      expect(document.getElementById('federation-target-cloud-pills')?.getAttribute('role')).toBe('radiogroup');
    });
  });

  describe('Help text copy (#1691 item 10)', () => {
    const visibleText = bodyContent.replace(/<!--[\s\S]*?-->/g, '');

    test('uses no ASCII double-hyphen as a dash in help text', () => {
      expect(visibleText).not.toMatch(/ -- /);
    });

    test('lookback hint does not expose the API parameter name', () => {
      expect(htmlContent).not.toContain('LookbackPeriodInDays');
    });

    test('Reserved Instance class options stay short', () => {
      const opts = [...htmlContent.matchAll(/<option value="(?:convertible|standard)"[^>]*>([^<]*)<\/option>/g)]
        .map((m) => m[1] ?? '');
      expect(opts).toHaveLength(2);
      for (const o of opts) {
        expect(o.length).toBeLessThanOrEqual(50);
      }
    });
  });
});
