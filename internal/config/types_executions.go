package config

import (
	"time"
)

// ExecutionStatusCounts maps an execution status value to the number of
// executions in that status, for a single plan. Returned by
// CountExecutionsByPlanAndStatus; a status with no rows is simply absent
// from the map, so a plain lookup yields the correct zero.
type ExecutionStatusCounts map[string]int

// PurchaseExecution represents a single execution of a purchase plan.
type PurchaseExecution struct {
	ScheduledDate time.Time  `json:"scheduled_date" dynamodbav:"scheduled_date"`
	CompletedAt   *time.Time `json:"completed_at,omitempty" dynamodbav:"completed_at,omitempty"`
	// ApprovedBy / CancelledBy carry the email of the session-authenticated
	// user who acted on this execution via the auth-gated deep-link flow
	// (frontend /purchases/{action}/:id → login-if-needed → session-authed
	// endpoint). Nil on legacy token-only approve/cancel paths — the
	// handler / History UI falls back to the notification email as the
	// accountable party in that case. Nullable TEXT in Postgres.
	ApprovedBy *string `json:"approved_by,omitempty" dynamodbav:"approved_by,omitempty"`
	// ScheduledExecutionAt is set by the Gmail-style pre-fire delay path
	// (issue #291 wave-2) when an approve defers the cloud SDK call. The
	// scheduler fires the actual SDK call when this timestamp is in the past.
	// NULL on every immediate-execute row. Migration 000065.
	ScheduledExecutionAt *time.Time `json:"scheduled_execution_at,omitempty" dynamodbav:"scheduled_execution_at,omitempty"`
	// RetryExecutionID points from a *failed* execution to the new
	// execution created when the user clicked Retry (issue #47). Set
	// only on the original failed row; NULL on every other row including
	// the retry itself (the retry's own RetryAttemptN > 0 is the
	// "this is a retry" marker). Forms a forward-pointing chain:
	// failed_v1.retry_execution_id = failed_v2.execution_id, etc.
	// Migration 000042 self-FKs the column ON DELETE SET NULL so a
	// cleanup of a successor doesn't cascade-delete its predecessor.
	RetryExecutionID *string    `json:"retry_execution_id,omitempty" dynamodbav:"retry_execution_id,omitempty"`
	NotificationSent *time.Time `json:"notification_sent,omitempty" dynamodbav:"notification_sent,omitempty"`
	// FirstDispatchAt is the time of the first dispatch of a purchase lineage.
	// Server-only; insert-only in the upsert, set once by the dispatch stamp (migration 000154).
	FirstDispatchAt *time.Time `json:"-" dynamodbav:"-"`
	// ExecutedByUserID is the UUID of the session user who triggered a
	// direct-execute (issue #289, execute-any/execute-own) or a run-now. NULL on rows
	// that went through the normal approval flow. Together with
	// pre_approval_skip_reason it marks a skipped approval email/wait (see ApprovedBy).
	// Migration 000058 adds the column.
	ExecutedByUserID *string `json:"executed_by_user_id,omitempty" dynamodbav:"executed_by_user_id,omitempty"`
	// CreatedByUserID is the UUID of the session-authenticated user who
	// triggered this execution (e.g. clicked Execute on the Recommendations
	// page or submitted the bulk-purchase modal). NULL on rows created
	// before the column was introduced (migration 000041) and on
	// scheduler-driven executions where there is no human creator. Used
	// by the session-authed cancel handler to enforce cancel:own_executions
	// — a non-admin may cancel only executions they themselves created.
	// NULL is treated as "not the current user".
	CreatedByUserID *string `json:"created_by_user_id,omitempty" dynamodbav:"created_by_user_id,omitempty"`
	// ApprovalTokenExpiresAt is the UTC deadline after which the
	// ApprovalToken must be rejected by ApproveExecution and
	// loadCancelableExecution (issue #397). Set at execution creation to
	// ScheduledDate + ApprovalTokenTTL. NULL on rows created before
	// migration 000051 — legacy rows are treated as not-yet-expired
	// (backward-compatible: the TTL-checking gate only fires when the
	// field is non-nil). Migration 000051 adds the column; new rows
	// always carry a non-nil value.
	ApprovalTokenExpiresAt *time.Time `json:"approval_token_expires_at,omitempty" dynamodbav:"approval_token_expires_at,omitempty"`
	// PreApprovalSkipReason is a human-readable token describing why the
	// approval email/scheduled wait was skipped: "direct-execute permission" or
	// "run-now". NULL on every normal-flow row.
	// Migration 000058.
	PreApprovalSkipReason *string `json:"pre_approval_skip_reason,omitempty" dynamodbav:"pre_approval_skip_reason,omitempty"`
	CloudAccountID        *string `json:"cloud_account_id,omitempty" dynamodbav:"cloud_account_id,omitempty"`
	// ExecutedAt is the UTC timestamp when the direct-execute or run-now path fired.
	// NULL for rows on the normal approval flow. Migration 000058.
	ExecutedAt  *time.Time `json:"executed_at,omitempty" dynamodbav:"executed_at,omitempty"`
	CancelledBy *string    `json:"cancelled_by,omitempty" dynamodbav:"cancelled_by,omitempty"`
	Status      string     `json:"status" dynamodbav:"status"` // pending, notified, approved, canceled, completed, failed
	// Source identifies the CUDly surface that triggered this execution
	// ("cudly-cli" or "cudly-web"). Propagated into PurchaseOptions and
	// stamped as a tag/label onto every commitment this execution buys.
	Source string `json:"source,omitempty" dynamodbav:"source,omitempty"`
	Error  string `json:"error,omitempty" dynamodbav:"error,omitempty"`
	// IdempotencyKey is the stable lineage anchor the per-rec provider
	// idempotency token is derived from (issue #1012). Unlike ExecutionID
	// it is NOT regenerated on Retry or multi-account fan-out: it is
	// generated once at first creation, copied verbatim onto every Retry
	// successor, and combined with the account ID to seed each per-account
	// fan-out row. This makes DeriveIdempotencyToken reproduce the same
	// token across a strand-and-re-drive so the provider dedupes and the
	// commitment is never bought twice. Empty on rows created before
	// migration 000066 — the derivation falls back to ExecutionID for those
	// (identical to the pre-fix behavior for a single un-retried execution).
	IdempotencyKey  string                 `json:"idempotency_key,omitempty" dynamodbav:"idempotency_key,omitempty"`
	PlanID          string                 `json:"plan_id" dynamodbav:"plan_id"`
	ApprovalToken   string                 `json:"approval_token,omitempty" dynamodbav:"approval_token,omitempty"` // SHA-256 hex digest (approval_token_hash column), never the raw token
	ExecutionID     string                 `json:"execution_id" dynamodbav:"execution_id"`
	Recommendations []RecommendationRecord `json:"recommendations" dynamodbav:"recommendations"`
	// CapacityPercent records what fraction of the originally-recommended
	// counts the user chose when the bulk Purchase flow submitted this
	// execution (1..100). Audit-only: the Recommendations slice already
	// carries the scaled counts, so backend math is unaffected by this
	// field. Defaults to 100 for legacy and scheduler-driven executions.
	CapacityPercent int `json:"capacity_percent,omitempty" dynamodbav:"capacity_percent,omitempty"`
	// RetryAttemptN is the position of this execution in a retry chain.
	// 0 (default) on every fresh execution; 1 on the first retry of any
	// failed row; n+1 on the n+1-th retry. The handler reads the
	// predecessor's count and stamps n+1 atomically with the new
	// INSERT inside the retry transaction. The History UI uses this to
	// soft-block retries past a threshold so an obviously-stuck
	// configuration doesn't accumulate dozens of dead retry rows.
	// Migration 000042 added the column with default 0 so legacy rows
	// look exactly like fresh first-retry candidates.
	RetryAttemptN    int     `json:"retry_attempt_n,omitempty" dynamodbav:"retry_attempt_n,omitempty"`
	TTL              int64   `json:"ttl,omitempty" dynamodbav:"ttl,omitempty"`
	EstimatedSavings float64 `json:"estimated_savings" dynamodbav:"estimated_savings"`
	TotalUpfrontCost float64 `json:"total_upfront_cost" dynamodbav:"total_upfront_cost"`
	StepNumber       int     `json:"step_number" dynamodbav:"step_number"`
	// CreatedBySystem marks a row the scheduler sweep minted with no human
	// initiator (issue #233). Server-only and insert-only: never bound from a
	// request, never rewritten by the upsert. Implies CreatedByUserID == nil
	// (CHECK in migration 000130).
	CreatedBySystem bool `json:"-" dynamodbav:"-"`
	// NoSubmission is the persisted proof that this execution failed before
	// any cloud purchase call was made (migration 000113). Only the
	// pre-submission failure paths set it; the reaper and every
	// outcome-unknown failure leave it false. It is monotone: the upsert can
	// set it but never clear it, so a stale in-memory copy cannot erase it.
	// The ladder reconciler treats a failed execution as "nothing was bought"
	// only when this is true.
	NoSubmission bool `json:"no_submission,omitempty" dynamodbav:"no_submission,omitempty"`
}

// StatusCanceled is the canonical US-spelling status value new code writes.
const StatusCanceled = "canceled"

// StatusFailed is the terminal status written when an execution's purchase
// attempt errors out, and by the stuck-purchase reaper (internal/purchase/
// reaper.go) for rows left in approved/running past its threshold.
const StatusFailed = "failed"

// StatusCompleted / StatusPartiallyCompleted are the two terminal statuses
// that mean commitment was actually bought. A partially-completed row bought
// some of its recommendations and failed others (issue #642); it is a real
// purchase, which is why re-approving one would double-buy.
const (
	StatusCompleted          = "completed"
	StatusPartiallyCompleted = "partially_completed"
)

// StatusExpired is written when an approval lapsed before anyone acted on it.
const StatusExpired = "expired"
