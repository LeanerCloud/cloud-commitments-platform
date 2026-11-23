/**
 * Shared setup for the settings module suites (settings.test.ts and its splits
 * settings-*.test.ts that import this harness): the module mocks, the mock handles
 * and the shared DOM reset. Import this module FIRST so the mocks register before
 * the suites load ../settings. This file is not a test (jest only collects
 * *.test.ts).
 */

// Mock the api module
jest.mock('../api', () => ({
  getConfig: jest.fn(),
  updateConfig: jest.fn(),
  updateServiceConfig: jest.fn().mockResolvedValue(undefined),
  listAccounts: jest.fn(),
  createAccount: jest.fn(),
  updateAccount: jest.fn(),
  deleteAccount: jest.fn(),
  testAccountCredentials: jest.fn(),
  saveAccountCredentials: jest.fn(),
  getLadderConfigs: jest.fn().mockResolvedValue([]),
}));

// Mock federation module — loadGlobalSettings fires initFederationPanel void-style,
// and we don't want real federation code touching the test DOM.
jest.mock('../federation', () => ({
  initFederationPanel: jest.fn().mockResolvedValue(undefined),
}));

// Mock confirmDialog so tests can control confirm/cancel without driving
// the real modal UI. mockConfirmDialog default is "confirmed" (true);
// individual tests can mockResolvedValueOnce(false) to simulate cancel.
export const mockConfirmDialog = jest.fn<Promise<boolean>, [unknown]>(() => Promise.resolve(true));
jest.mock('../confirmDialog', () => ({
  confirmDialog: (opts: unknown) => mockConfirmDialog(opts),
}));

// Q7: saveGlobalSettings + error paths now use showToast.
export const mockShowToast = jest.fn<{ dismiss: () => void }, [unknown]>(() => ({ dismiss: jest.fn() }));
jest.mock('../toast', () => ({
  showToast: (opts: unknown) => mockShowToast(opts),
}));

// The outer beforeEach of every settings.test.ts piece, moved here verbatim.
export function setupSettingsTest(): void {
    // Reset DOM
    document.body.innerHTML = `
      <div id="settings-loading" class="hidden"></div>
      <form id="global-settings-form" class="hidden">
        <input type="checkbox" id="provider-aws">
        <input type="checkbox" id="provider-azure">
        <input type="checkbox" id="provider-gcp">
        <div id="aws-settings" class="hidden"></div>
        <div id="azure-settings" class="hidden"></div>
        <div id="gcp-settings" class="hidden"></div>
        <input type="email" id="setting-notification-email">
        <input type="checkbox" id="setting-auto-collect">
        <div id="collection-schedule-row" class="hidden">
          <select id="setting-collection-schedule">
            <option value="daily">Daily</option>
            <option value="weekly">Weekly</option>
          </select>
        </div>
        <select id="setting-default-term">
          <option value="1">1</option>
          <option value="3">3</option>
        </select>
        <select id="setting-default-payment">
          <option value="no-upfront">No Upfront</option>
          <option value="partial-upfront">Partial Upfront</option>
          <option value="all-upfront">All Upfront</option>
        </select>
        <input type="number" id="setting-default-coverage" min="0" max="100">
        <input type="number" id="setting-notification-days" min="1" max="30">
        <input type="number" id="setting-recs-stale-hours" value="24" min="0" max="8760">
        <select id="setting-recs-lookback-days">
          <option value="7" selected>7 days</option>
          <option value="30">30 days</option>
          <option value="60">60 days</option>
        </select>
        <!-- AWS term/payment selects -->
        <select id="aws-ec2-term"><option value="1">1</option><option value="3">3</option></select>
        <select id="aws-rds-term"><option value="1">1</option><option value="3">3</option></select>
        <select id="aws-elasticache-term"><option value="1">1</option><option value="3">3</option></select>
        <select id="aws-opensearch-term"><option value="1">1</option><option value="3">3</option></select>
        <select id="aws-redshift-term"><option value="1">1</option><option value="3">3</option></select>
        <!-- Issue #22 follow-up: per-plan-type Savings Plans cards
             (Compute / EC2 Instance / SageMaker / Database). The earlier
             umbrella savingsplans and PR #71 sagemaker cards have
             been replaced. -->
        <select id="aws-savings-plans-compute-term"><option value="1">1</option><option value="3">3</option></select>
        <select id="aws-savings-plans-ec2instance-term"><option value="1">1</option><option value="3">3</option></select>
        <select id="aws-savings-plans-sagemaker-term"><option value="1">1</option><option value="3">3</option></select>
        <select id="aws-savings-plans-database-term"><option value="1">1</option><option value="3">3</option></select>
        <!-- Issue #136: per-product SP coverage and enabled controls -->
        <input type="number" id="aws-savings-plans-compute-coverage" min="0" max="100" value="80">
        <input type="checkbox" id="aws-savings-plans-compute-enabled" checked>
        <input type="number" id="aws-savings-plans-ec2instance-coverage" min="0" max="100" value="80">
        <input type="checkbox" id="aws-savings-plans-ec2instance-enabled" checked>
        <input type="number" id="aws-savings-plans-sagemaker-coverage" min="0" max="100" value="80">
        <input type="checkbox" id="aws-savings-plans-sagemaker-enabled" checked>
        <input type="number" id="aws-savings-plans-database-coverage" min="0" max="100" value="80">
        <input type="checkbox" id="aws-savings-plans-database-enabled" checked>
        <select id="aws-ec2-payment"><option value="no-upfront">No</option><option value="partial-upfront">Partial</option><option value="all-upfront">All</option></select>
        <select id="aws-rds-payment"><option value="no-upfront">No</option><option value="partial-upfront">Partial</option><option value="all-upfront">All</option></select>
        <select id="aws-elasticache-payment"><option value="no-upfront">No</option><option value="partial-upfront">Partial</option><option value="all-upfront">All</option></select>
        <select id="aws-opensearch-payment"><option value="no-upfront">No</option><option value="partial-upfront">Partial</option><option value="all-upfront">All</option></select>
        <select id="aws-redshift-payment"><option value="no-upfront">No</option><option value="partial-upfront">Partial</option><option value="all-upfront">All</option></select>
        <select id="aws-savings-plans-compute-payment"><option value="no-upfront">No</option><option value="partial-upfront">Partial</option><option value="all-upfront">All</option></select>
        <select id="aws-savings-plans-ec2instance-payment"><option value="no-upfront">No</option><option value="partial-upfront">Partial</option><option value="all-upfront">All</option></select>
        <select id="aws-savings-plans-sagemaker-payment"><option value="no-upfront">No</option><option value="partial-upfront">Partial</option><option value="all-upfront">All</option></select>
        <select id="aws-savings-plans-database-payment"><option value="no-upfront">No</option><option value="partial-upfront">Partial</option><option value="all-upfront">All</option></select>
        <!-- Azure term selects -->
        <select id="azure-vm-term"><option value="1">1</option><option value="3">3</option></select>
        <select id="azure-sql-term"><option value="1">1</option><option value="3">3</option></select>
        <select id="azure-cosmos-term"><option value="1">1</option><option value="3">3</option></select>
        <!-- GCP term selects -->
        <select id="gcp-compute-term"><option value="1">1</option><option value="3">3</option></select>
        <select id="gcp-sql-term"><option value="1">1</option><option value="3">3</option></select>
        <!-- Per-provider grace-period inputs (issue #466 reset-to-defaults assertions
             call populateGraceInput which needs these in the DOM). -->
        <input type="number" id="setting-grace-aws" value="7" min="0" max="30">
        <input type="number" id="setting-grace-azure" value="7" min="0" max="30">
        <input type="number" id="setting-grace-gcp" value="7" min="0" max="30">
        <!-- Issue #466: reflectDirtyState (in settings-subnav.ts) toggles
             .has-unsaved on #admin-tab-btn and .dirty/.settings-savebar on
             every .settings-buttons element. Seed both so the reset-to-
             defaults expansion + unsaved-indicator assertions have DOM to
             check. -->
        <div class="settings-buttons"></div>
      </form>
      <button id="admin-tab-btn"></button>
      <div id="settings-error" class="hidden"></div>
      <!-- Test element for copyToClipboard -->
      <code id="test-copy-element">test-value</code>
      <button class="copy-btn"><span class="copy-icon">Copy</span></button>
    `;
    // Seed the default selects to match prod's <option selected> markup so
    // cascade-confirm tests have an unambiguous baseline ("3 Years" term,
    // "all-upfront" payment) to transition away from.
    (document.getElementById('setting-default-term') as HTMLSelectElement).value = '3';
    (document.getElementById('setting-default-payment') as HTMLSelectElement).value = 'all-upfront';
    // Mirror the same baseline onto every per-service select so the cascade
    // diff only reports genuinely-changed rows.
    ['aws-ec2-term','aws-rds-term','aws-elasticache-term','aws-opensearch-term','aws-redshift-term','aws-savings-plans-compute-term','aws-savings-plans-ec2instance-term','aws-savings-plans-sagemaker-term','aws-savings-plans-database-term','azure-vm-term','azure-sql-term','azure-cosmos-term','gcp-compute-term','gcp-sql-term'].forEach(id => {
      const el = document.getElementById(id) as HTMLSelectElement | null;
      if (el) el.value = '3';
    });
    ['aws-ec2-payment','aws-rds-payment','aws-elasticache-payment','aws-opensearch-payment','aws-redshift-payment','aws-savings-plans-compute-payment','aws-savings-plans-ec2instance-payment','aws-savings-plans-sagemaker-payment','aws-savings-plans-database-payment'].forEach(id => {
      const el = document.getElementById(id) as HTMLSelectElement | null;
      if (el) el.value = 'all-upfront';
    });

    jest.clearAllMocks();
    // clearAllMocks only clears call history; flush the queued
    // mockResolvedValueOnce stack too so a test that doesn't consume a
    // queued value can't leak it into the next test.
    mockConfirmDialog.mockReset();
    mockConfirmDialog.mockImplementation(() => Promise.resolve(true));
}
