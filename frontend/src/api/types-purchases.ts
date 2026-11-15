import type { Provider, Recommendation } from './types';

// Purchase types

/**
 * PaymentAdjustment is one payment-option coercion notice returned by the
 * purchase-execute endpoint (#1503 follow-up). Mirrors the backend
 * PaymentAdjustment struct (internal/api/validation.go) field for field.
 *
 * Named (rather than inlined into PurchaseResult) so the presentation helper
 * that turns these into user-facing copy
 * (commitmentOptions.ts:formatPaymentAdjustmentNotice) can be typed and unit
 * tested against the same shape the API actually returns.
 */
export interface PaymentAdjustment {
  rec_index: number;
  provider: string;
  service: string;
  requested_payment_option: string;
  applied_payment_option: string;
  reason: string;
}

export interface PurchaseResult {
  execution_id: string;
  status: string;
  recommendation_count?: number;
  total_upfront_cost?: number;
  estimated_savings?: number;
  message?: string;
  // Whether the approval email was actually sent. When false, email_reason
  // carries a human-readable explanation (e.g. "no notification email set in
  // Settings → General") so the UI can tell the user to approve/cancel from
  // History instead of waiting for an inbox.
  email_sent?: boolean;
  email_reason?: string;
  // Resolved To address that received the approval email. Surfaced so the
  // post-submit toast can name the approver ("Approval request sent to
  // alice@acme.com") per the CR pass on PR #294 / issue #288. Absent when
  // recipient resolution itself failed (no approvers configured).
  approval_recipient?: string;
  // True when the request was handled via the direct-execute path (issue
  // #289). Absent (undefined) on the standard approval-required flow.
  direct_execute?: boolean;
  // True when this submit collapsed onto an earlier execution with the same
  // creator and recommendations (#644, #758). execution_id and status then
  // describe that earlier row, whatever mode this submit used.
  duplicate?: boolean;
  // Per-rec payment-option coercion notices (#1503 follow-up). Present only
  // when the backend normalized a requested payment option onto a token that
  // bills on a DIFFERENT schedule (e.g. Azure "partial-upfront" -> "monthly"):
  // each entry names what was requested, what was actually applied, and why,
  // so the UI can tell the user instead of silently changing the billing
  // schedule. Absent (never an empty array) when every option was already
  // canonical, and also when the backend only respelled a token into the
  // provider's own vocabulary for the same schedule (Azure "all-upfront" ->
  // "upfront") — that changes nothing the user pays, so warning about it
  // would just teach them to dismiss the notice that matters.
  payment_adjustments?: PaymentAdjustment[];
  results?: Array<{
    recommendation_id: string;
    status: string;
    error?: string;
  }>;
}

// PurchaseDetails matches the shape produced by backend
// buildPurchaseDetailsResponse (internal/api/handler_purchases_details.go).
// `recommendations` carries the per-rec snapshot that was captured at
// approval-request time so callers can render the deal without
// re-querying provider APIs. plan_id / plan_name / scheduled_date are
// only present when the execution belongs to a plan (direct-execute
// purchases from the recommendations page have plan_id="" and
// plan_name="").
export interface PurchaseDetails {
  execution_id: string;
  status: string;
  plan_id?: string;
  plan_name?: string;
  step_number?: number;
  scheduled_date?: string;
  total_upfront_cost: number;
  estimated_savings: number;
  recommendations: Recommendation[];
  notification_sent?: string;
  completed_at?: string;
  error?: string;
}

export interface PlannedPurchasesResponse {
  purchases: PlannedPurchase[];
}

export interface PlannedPurchase {
  id: string;
  plan_id: string;
  plan_name: string;
  scheduled_date: string;
  provider: Provider;
  // Set only for a bound plan: its distinct enabled group providers (see UpcomingPurchase).
  providers?: string[];
  service: string;
  resource_type: string;
  region: string;
  count: number;
  term: number;
  payment: string;
  estimated_savings: number;
  upfront_cost: number;
  // False for a step that carries no recommendations: count, resource_type,
  // upfront_cost and estimated_savings are then placeholders, not data
  // (platform#609).
  has_recommendations: boolean;
  // True when the figures come from the current matching recommendations, not
  // an attached step; the purchase recomputes them. estimate_note says why a
  // step has none. Both are omitted for callers without view:recommendations.
  estimate?: boolean;
  estimate_note?: string;
  status: 'pending' | 'notified' | 'paused' | 'running' | 'completed' | 'failed';
  step_number: number;
  total_steps: number;
  // created_by_user_id is the UUID of the user who scheduled the purchase.
  // The row action buttons are gated on creator-scope ownership (issue #950):
  // a non-creator without update-any:purchases sees no actionable buttons.
  // Omitted (undefined) for legacy rows with a NULL creator.
  created_by_user_id?: string;
}
