// Core types
export type Provider = 'aws' | 'azure' | 'gcp';
export type PaymentOption = 'no-upfront' | 'partial-upfront' | 'all-upfront';
export type RampSchedule = 'immediate' | 'weekly-25pct' | 'monthly-10pct' | 'custom';

// Internal types
export interface ApiError extends Error {
  status?: number;
  // Structured detail fields the backend attaches to a 4xx response
  // alongside the human `error` message (e.g. `ops_hint`,
  // `retry_attempt_n`, `threshold`, `retry_execution_id`). Callers
  // can branch on these without substring-matching the message.
  // See internal/api/handler.go for the flattening — keys are
  // promoted to the top level of the JSON body.
  details?: Record<string, unknown>;
  // Raw technical text behind a user-facing message (e.g. "HTTP 502 from
  // /api/x", "timed out after 90000ms", the browser's fetch error), kept
  // for the console and support while `message` stays readable (#748).
  detail?: string;
}

export interface RequestOptions extends RequestInit {
  headers?: Record<string, string>;
  /**
   * Hard timeout for the request in milliseconds. Defaults to
   * DEFAULT_API_TIMEOUT_MS (90 000) in client.ts; pass `0` to disable
   * the timeout (e.g. for long-running admin flows that genuinely
   * need to block) or a smaller value for cheap endpoints.
   */
  timeoutMs?: number;
}
