import type { Permission, Provider } from './types';

// API Keys Management Types
export interface APIKeyInfo {
  id: string;
  name: string;
  key_prefix: string;
  is_active: boolean;
  expires_at?: string;
  created_at: string;
  last_used_at?: string;
  permissions?: Permission[];
  // Usage counters (issue #340/#344 deferred sub-task).
  // Optional in the type so older mock data / cached responses without
  // the columns don't blow up the renderer -- but the backend always
  // sends them as of migration 000094. request_count_window is a
  // FIXED/TUMBLING window count, not a true rolling 24h total --
  // request_count_window_start says exactly which period it covers, and
  // both come back as 0 / absent once that window has closed, so a key
  // that went idle reads as no current activity rather than replaying a
  // count from an old window.
  // null when the key was already in use before migration 000094 added the
  // counters, so its true lifetime volume is unrecoverable. Render it as
  // "no data", never as 0.
  request_count_total?: number | null;
  request_count_window?: number;
  request_count_window_start?: string;
}

export interface APIKeysUsageStatsTopKey {
  id: string;
  name: string;
  key_prefix: string;
  request_count_window: number;
}

export interface APIKeysUsageStats {
  // Keys that are neither revoked nor expired, i.e. the ones the API will
  // actually accept.
  total_active: number;
  // Sum of each key's fixed-window counter across still-open windows --
  // not a true rolling 24h total, and the windows are not aligned.
  total_requests_window: number;
  // Sums only the keys whose lifetime count is known. When lifetime_partial
  // is true at least one key predates the counters, so this is a lower bound
  // and must not be presented as an exact figure.
  total_requests_lifetime: number;
  lifetime_partial?: boolean;
  top_keys: APIKeysUsageStatsTopKey[];
}

export interface CreateAPIKeyRequest {
  name: string;
  // Base64-encoded, same convention as login/change-password/MFA (issue
  // #102: creation now re-verifies the caller's password).
  password: string;
  permissions?: Permission[];
  // Required and capped server-side (issue #102): a key can no longer be
  // created with no expiration.
  expires_at: string;
}

export interface CreateAPIKeyResponse {
  api_key: string;  // Full key shown only once
  key_id: string;
  // Backend wire field is `info` (see internal/auth/service_apikeys_api.go
  // APICreateAPIKeyResponse). Was previously typed as `key` which never
  // matched the response — same bug class as issue #9 above.
  info: APIKeyInfo;
}

export interface GetAPIKeysResponse {
  // Backend wire field is `api_keys` (see internal/auth/service_apikeys_api.go
  // APIListAPIKeysResponse). The previous `keys` declaration didn't match
  // any backend field, so `response.keys` was always undefined and
  // crashed renderApiKeysList() with "Cannot read properties of undefined
  // (reading 'length')" — see GitHub issue #9.
  api_keys: APIKeyInfo[];
}

// Savings Analytics Types
export interface SavingsAnalyticsResponse {
  start: string;
  end: string;
  interval: string;
  summary: SavingsAnalyticsSummary;
  data_points: SavingsDataPoint[];
}

// Mirrors the Go HistorySummary that GET /history/analytics actually sends
// (internal/api/types.go). The previous total_period_savings /
// average_savings_per_period / peak_savings keys belonged to an unused Go
// struct and never arrived on the wire (issue #276). Average and peak have no
// server-side counterpart; the client derives them from data_points.
export interface SavingsAnalyticsSummary {
  total_purchases: number;
  total_completed: number;
  total_pending: number;
  total_in_progress: number;
  total_failed: number;
  total_expired: number;
  total_revoked: number;
  // null when any purchase in the window has an unknown upfront cost.
  total_upfront: number | null;
  // Sum of per-bucket total_savings over the window, in monthly dollars.
  total_monthly_savings: number;
  total_annual_savings: number;
}

export interface SavingsDataPoint {
  timestamp: string;
  total_savings: number;
  total_upfront: number | null;
  purchase_count: number;
  cumulative_savings: number;
  by_service?: Record<string, number>;
  by_provider?: Record<string, number>;
}

export interface SavingsBreakdownResponse {
  dimension: string;
  start: string;
  end: string;
  data: Record<string, SavingsBreakdownValue>;
}

export interface SavingsBreakdownValue {
  total_savings: number;
  total_upfront: number | null;
  purchase_count: number;
  percentage: number;
}

export interface SavingsAnalyticsFilters {
  start?: string;
  end?: string;
  interval?: 'hourly' | 'daily' | 'weekly' | 'monthly';
  provider?: Provider;
  service?: string;
  account_ids?: string[];
}
