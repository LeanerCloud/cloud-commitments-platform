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

// The three ramp-step status classes below drive the advance gate in
// CompletePlanStep and the stuck-ramp report GetStuckRampSteps (issue #1861).
// They are separate exported lists rather than one, because the gate asks two
// different questions of the same rows and the health report asks a third:
//
//   - RampStepSucceededStatuses answers "did this fan-out unit buy?". A step
//     no unit bought must not advance the ramp at all.
//   - RampStepSettledStatuses answers "is this fan-out unit done holding the
//     step open?". It is deliberately an ALLOWLIST: a status added to the
//     schema later holds the step open until someone classifies it, which is
//     the fail-closed direction on a money path.
//   - RampStepStuckStatuses answers "is this unit stuck rather than in
//     flight?". Only these produce the plan-health ramp_blocked factor, so a
//     retry still working its way through pending/running reads as recovery
//     rather than as a frozen ramp.
//
// Canceled counts as settled but neither succeeded nor stuck: to cancel a
// step's row is an operator deciding that unit will not buy, and that decision
// is what releases a ramp an unrecoverable account would otherwise freeze.
// Both spellings are listed for the duration of the expand-contract rename
// (migration 000089, contract in #1278), same as HealthScoredExecutionStatuses.
//
// RampStepSucceededStatuses is the same predicate migration 000098 spells out
// inline as status IN ('completed','partially_completed') when it decides which
// rows a sibling has already bought for. The migration is frozen SQL and cannot
// import this, so the two are kept identical by hand: changing one without the
// other would make the backfill and the advance gate disagree about what
// "bought" means.
var (
	RampStepSucceededStatuses = []string{StatusCompleted, StatusPartiallyCompleted}
	RampStepSettledStatuses   = []string{StatusCompleted, StatusPartiallyCompleted, StatusCanceled}
	RampStepStuckStatuses     = []string{StatusFailed, StatusExpired}
)

// RampStepBlock reports a plan's next ramp step and how many of that step's
// executions are stuck on it. Returned by GetStuckRampSteps and rendered by the
// plan-health ramp_blocked factor (issue #1861).
//
// StuckExecutions counts executions rather than accounts because that is what
// the query can prove: a fanned-out step writes one row per cloud account, so
// the two coincide for the multi-account plans the factor exists for, but a
// step that never fanned out (an approval that expired before it ran) has one
// row standing for the whole step. Naming the count for the rows keeps the
// tooltip from asserting an account total it did not measure.
type RampStepBlock struct {
	StepNumber      int
	StuckExecutions int
}

// HealthScoredExecutionStatuses are the execution statuses the plan health
// score counts (internal/api/plan_health.go). It lives here, in the package
// that owns both the status constants and the retention sweep, because two
// unrelated-looking pieces of code have to agree on it exactly:
//
//   - CountExecutionsByPlanAndStatus is asked for these statuses and windows
//     them on updated_at.
//   - CleanupOldExecutions must not delete a row in one of these statuses
//     while that window still covers it. Retaining on any other clock lets
//     the sweep purge a row the score is still counting, which shows up as a
//     plan's health score jumping overnight with no operator action.
//
// Keeping one exported slice rather than a literal list on each side is what
// makes that agreement structural: adding a status to the score
// automatically extends the sweep's exclusion, so the two cannot drift.
var HealthScoredExecutionStatuses = []string{
	StatusFailed,
	StatusCanceled,
}

// StatusRevocationRequested records that an operator asked for a completed
// purchase to be revoked. The cloud-side revocation happens outside this
// system (an AWS support case), and no code moves a row out of this status.
const StatusRevocationRequested = "revocation_requested"

// TerminalExecutionStatuses are the execution statuses that no executor,
// scheduler or reaper will act on again. DeleteCloudAccount detaches rows in
// these statuses from the account being deleted (cloud_account_id = NULL, the
// same treatment purchase_history gets from its ON DELETE SET NULL FK) so the
// history survives the delete, and CountLiveExecutionsForAccount counts every
// other row as one that still needs the account (issue #265).
//
// It is an ALLOWLIST on purpose: a status added to the schema later counts as
// live, and so blocks the delete, until someone classifies it here. Blocking
// a delete is recoverable; detaching an execution that will still run is not,
// because a row with no account falls back to plan fan-out or ambient
// credentials.
//
// A failed row is listed even though it can be retried. A retry of a row
// detached here still carries the deleted account on its recommendations, so
// the executor refuses it before submission ("account not found") instead of
// buying elsewhere.
var TerminalExecutionStatuses = []string{
	StatusCompleted,
	StatusPartiallyCompleted,
	StatusFailed,
	StatusCanceled,
	StatusExpired,
	StatusRevocationRequested,
}

// IsCancelable reports whether an execution may still be canceled. Only the
// pre-approval states ("pending"/"notified") qualify. A "scheduled" row is not
// canceled: it is revoked through the revoke endpoint
// (CancelScheduledExecutionAtomic), which carries its own RBAC. Once a row
// reaches "approved" or "running" the AWS commitment is being or has been
// created, so canceling would leave the DB and the cloud out of sync;
// "canceled", "completed", "failed", "expired", and "paused" are likewise
// non-cancelable.
// Both cancel paths (purchase.Manager.CancelExecution on the email-token flow
// and the session-authed cancelPurchaseViaSession) call this single predicate
// so the policy can never drift between them (issue #645).
func (e *PurchaseExecution) IsCancelable() bool {
	return e.Status == "pending" || e.Status == "notified"
}

// RIExchangeRecord represents a record in the ri_exchange_history table.
type RIExchangeRecord struct {
	UpdatedAt time.Time `json:"updated_at"`
	CreatedAt time.Time `json:"created_at"`
	// CreatedByUserID is the UUID of the session user who submitted the exchange.
	// Always nil today: every exchange is created by the scheduler or the ladder
	// engine, and pkg/exchange.ExchangeRecord carries no creator (the 4-eyes
	// policy for creator-less rows is requireDifferentRIExchangeApprover in
	// internal/api, issue #518). Exposed to the frontend so the Approve button
	// can apply the approve-own ownership check client-side.
	CreatedByUserID *string    `json:"created_by_user_id,omitempty"`
	CloudAccountID  *string    `json:"cloud_account_id,omitempty"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	// LadderRunID links this exchange record to the ladder run that created it
	// (cudly-ladder engine). Nil for standalone ri_exchange_reshape task records.
	// The database column ri_exchange_history.ladder_run_id was added in migration
	// 000080 and is the authoritative source for origin scoping in
	// CancelPendingExchangesByOrigin.
	//
	// KNOWN/ACCEPTABLE: the FK is ON DELETE SET NULL (migration 000080), so
	// deleting a ladder_runs row nulls this column and reclassifies the record
	// as standalone. A still-pending reshape then becomes standalone-cancellable
	// (the standalone-origin sweep would cancel it). This is acceptable: a
	// deleted run has no owner to approve its pendings, so canceling them on the
	// next standalone sweep is the safe outcome, not a leak.
	LadderRunID *string `json:"ladder_run_id,omitempty"`
	// ApprovedBy carries the email of the session user who approved the exchange
	// via the dashboard Approve button (issue #300). Nil when no session resolves (pure email-link approval).
	ApprovedBy         *string  `json:"approved_by,omitempty"`
	SourceInstanceType string   `json:"source_instance_type"`
	TargetInstanceType string   `json:"target_instance_type"`
	PaymentDue         string   `json:"payment_due"`
	Status             string   `json:"status"`
	ApprovalToken      string   `json:"approval_token,omitempty"` // raw when passed to SaveRIExchangeRecord (hashed there); the hash when read back
	Error              string   `json:"error,omitempty"`
	Mode               string   `json:"mode"`
	AccountID          string   `json:"account_id"`
	TargetOfferingID   string   `json:"target_offering_id"`
	ExchangeID         string   `json:"exchange_id"`
	ID                 string   `json:"id"`
	Region             string   `json:"region"`
	SourceRIIDs        []string `json:"source_ri_ids"`
	SourceCount        int      `json:"source_count"`
	TargetCount        int      `json:"target_count"`
}
