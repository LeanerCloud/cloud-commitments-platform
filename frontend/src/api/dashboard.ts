/**
 * Dashboard API functions
 */

import { apiRequest } from './client';
import type { DashboardSummary, UpcomingPurchasesResponse, Provider } from './types';

/**
 * Get dashboard summary
 */
export async function getDashboardSummary(provider: Provider | '' = '', accountIDs: string[] = []): Promise<DashboardSummary> {
  const params = new URLSearchParams({ provider });
  if (accountIDs.length > 0) params.set('account_ids', accountIDs.join(','));
  return apiRequest<DashboardSummary>(`/dashboard/summary?${params}`);
}

/**
 * Get upcoming purchases, scoped by the topbar chips (#713). The endpoint returns `{ purchases: [...] }`, not a
 * bare array (#1189).
 */
export async function getUpcomingPurchases(provider: Provider | '' = '', accountIDs: string[] = []): Promise<UpcomingPurchasesResponse> {
  const params = new URLSearchParams();
  if (provider) params.set('provider', provider);
  if (accountIDs.length > 0) params.set('account_ids', accountIDs.join(','));
  const qs = params.toString();
  return apiRequest<UpcomingPurchasesResponse>(qs ? `/dashboard/upcoming?${qs}` : '/dashboard/upcoming');
}
