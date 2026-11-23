/**
 * Archera comparison API functions (read-only, explicit user action).
 */

import { apiRequest } from './client';
import type { ArcheraComparison, ArcheraRecommendationComparison, InsuranceStatus } from './types';

/** Reports whether the comparison is configured. Makes no vendor call. */
export async function getInsuranceStatus(): Promise<InsuranceStatus> {
  return apiRequest<InsuranceStatus>('/insurance/status');
}

/** Fetches the Archera comparison now. One vendor request per call, no caching. */
export async function getInsuranceComparison(): Promise<ArcheraComparison> {
  return apiRequest<ArcheraComparison>('/insurance/comparison');
}

/**
 * Fetches the Archera quote for one stored recommendation. One vendor
 * request per call, no caching; the caller may abort it.
 */
export async function getInsuranceRecommendation(
  id: string,
  signal?: AbortSignal,
): Promise<ArcheraRecommendationComparison> {
  return apiRequest<ArcheraRecommendationComparison>(
    `/insurance/recommendations/${encodeURIComponent(id)}`,
    signal ? { signal } : {},
  );
}
