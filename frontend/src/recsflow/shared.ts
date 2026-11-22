import { canAccess } from '../permissions';
import { formatPaymentOption } from '../lib/table-format';
import { recsState } from './recsState';

// Issue #869: true when the current session can take any action on
// recommendations (purchase or plan). Readonly/viewer sessions have neither
// permission, so checkboxes and row-click selection are meaningless for them.
export function canActOnRecommendations(): boolean {
  return canAccess('execute', 'purchases') || canAccess('create', 'plans');
}

/**
 * Returns the display name for a cloud account ID, falling back to the raw
 * ID when the name has not been cached yet. Exported so other modules (e.g.
 * history.ts Approval Queue) can resolve account names without importing the
 * full recommendations data model.
 */
export function getAccountName(accountId: string): string {
  return recsState.accountNamesCache.get(accountId) || accountId;
}

export function providerDisplayName(provider: string): string {
  switch (provider.toLowerCase()) {
    case 'aws': return 'AWS';
    case 'azure': return 'Azure';
    case 'gcp': return 'GCP';
    default: return provider;
  }
}

// CR #253 finding: whitelist provider for CSS class injection.
// rec.provider comes from the API and is injected into class attributes via
// template literals. A non-whitelisted value falls back to '' (no badge class).
export function providerBadgeClass(provider: string): string {
  const n = provider.toLowerCase();
  return n === 'aws' || n === 'azure' || n === 'gcp' ? n : '';
}

// Map raw payment_option values to display labels for the Payment column.
export const PAYMENT_DISPLAY_LABELS: Record<string, string> = {
  'all-upfront':     'All Upfront',
  'partial-upfront': 'Partial Upfront',
  'no-upfront':      'No Upfront',
  'upfront':         'Upfront',
  'monthly':         'Monthly',
};

export function formatPayment(payment: string | undefined): string {
  if (!payment) return '\u2014';
  return formatPaymentOption(payment);
}
