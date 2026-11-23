package config

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

// PlanWriteGate decides whether a plan write may proceed. The store runs it with
// the plan row locked, handing it the plan as stored and its account ids
// (sorted), both read on the write's own transaction. It is pure on purpose: it
// gets no context and no store, so it cannot take a second pool connection
// while the transaction holds one. Whatever needs the database or the auth
// service is resolved before the write and compared here against what the lock
// shows. A non-nil error aborts the write and is returned unchanged.
type PlanWriteGate func(locked *PurchasePlan, accountIDs []string) error

// StoreInterface defines the methods required for configuration storage.
type StoreInterface interface {
	// Global configuration
	GetGlobalConfig(ctx context.Context) (*GlobalConfig, error)
	SaveGlobalConfig(ctx context.Context, config *GlobalConfig) error
	// UpdateGlobalConfigAtomic serializes a read-modify-write of the
	// global_config singleton under an advisory-locked transaction so
	// concurrent partial PUTs cannot lose each other's updates. apply mutates
	// the loaded config in place; its error aborts the write and is propagated.
	// Each default_* value apply changes is copied onto every service config
	// in the same transaction.
	UpdateGlobalConfigAtomic(ctx context.Context, apply func(*GlobalConfig) error) (*GlobalConfig, error)

	// Service configuration
	GetServiceConfig(ctx context.Context, provider, service string) (*ServiceConfig, error)
	SaveServiceConfig(ctx context.Context, config *ServiceConfig) error
	// UpdateServiceConfigAtomic serializes a read-merge-write of one service
	// config row under a locked transaction so concurrent partial PUTs of the
	// same row cannot lose each other's updates. apply receives the stored row
	// (nil when none exists) and returns the full row to write; its error
	// aborts the write and is propagated.
	UpdateServiceConfigAtomic(ctx context.Context, provider, service string, apply func(existing *ServiceConfig) (*ServiceConfig, error)) (*ServiceConfig, error)
	ListServiceConfigs(ctx context.Context) ([]ServiceConfig, error)

	PlanStore

	// Purchase executions
	SavePurchaseExecution(ctx context.Context, execution *PurchaseExecution) error
	// SaveExecutionIfStatus saves only while the stored status is one of
	// expected (ErrExecutionNotInExpectedStatus otherwise), so a stale
	// in-memory copy cannot rewrite a status another writer moved on.
	SaveExecutionIfStatus(ctx context.Context, execution *PurchaseExecution, expected []string) error
	// RecoverReapedExecution persists a late completed/partially_completed
	// outcome over a reaper-failed row that has no retry successor linked
	// (ErrExecutionRetryLinked when one is).
	RecoverReapedExecution(ctx context.Context, execution *PurchaseExecution) error
	// PinExecutionFanOutShape records shape (FanOutShapeSingle or
	// FanOutShapeMulti) for the execution lineage lineageKey unless one is
	// already recorded, and returns the recorded shape (issue #97).
	PinExecutionFanOutShape(ctx context.Context, lineageKey, shape string) (string, error)
	// GetExecutionFanOutShape returns the shape pinned for lineageKey, or ""
	// when none is pinned yet (issue #97).
	GetExecutionFanOutShape(ctx context.Context, lineageKey string) (string, error)
	GetPendingExecutions(ctx context.Context) ([]PurchaseExecution, error)
	// GetExecutionsByStatuses returns executions in any of the given states,
	// newest first, capped at `limit`, except that pending rows dated in the
	// future (not-yet-due ramp steps) sort last, soonest first, so they cannot
	// push a due row past the cap (issue #680). Used by the History handler to render
	// pending + failed + expired alongside completed purchases; the scheduler
	// keeps using GetPendingExecutions (which is narrower and doesn't share
	// this method's status filter) to avoid accidental double-processing of
	// failed / expired rows.
	GetExecutionsByStatuses(ctx context.Context, statuses []string, limit int) ([]PurchaseExecution, error)
	// GetExecutionsByStatusesFiltered is GetExecutionsByStatuses with the
	// History filters and account scope in q applied in SQL before the limit,
	// so the cap counts only rows the caller can be shown (issue #632).
	GetExecutionsByStatusesFiltered(ctx context.Context, q ExecutionsByStatusesQuery) ([]PurchaseExecution, error)
	// ListHistoryExecutionsPage and ListPurchaseHistoryPage read one keyset
	// page each of the History view's paged stream (issue #674): rows strictly
	// after q.After / after (nil starts at the top) in the stream order of
	// HistoryPageKey, capped at the limit. The executions half excludes clean
	// completed rows like GetExecutionsByStatuses and bounds scheduled_date
	// inclusively; the purchase_history half applies GetPurchaseHistoryFiltered's
	// predicate. Rows inserted or changed between two page reads never shift
	// the rows a later page returns.
	ListHistoryExecutionsPage(ctx context.Context, q HistoryExecutionsPageQuery) ([]PurchaseExecution, error)
	ListPurchaseHistoryPage(ctx context.Context, filter PurchaseHistoryFilter, after *HistoryPageKey) ([]PurchaseHistoryPageRow, error)
	// CountExecutionsByPlanAndStatus returns, keyed by plan ID, the exact
	// number of executions in each of the supplied statuses that last
	// changed state (updated_at) at or after `since` -- not those merely
	// scheduled since then, which for a canceled row is a future date. Rows
	// with a NULL plan_id (direct-execute purchases, and executions orphaned
	// by a deleted plan) belong to no plan and are excluded.
	//
	// Deliberately NOT derived from a GetExecutionsByStatuses page: that
	// method is capped by `limit` across ALL plans, so any caller counting
	// per-plan rows out of it silently understates plans whose executions
	// fall outside the newest `limit` rows. The plan health score
	// (internal/api/plan_health.go) needs exact counts -- an understated
	// count renders a confidently-healthy badge for an unhealthy plan --
	// so the aggregation happens in SQL and the result is bounded by
	// plans x statuses rather than by execution volume.
	CountExecutionsByPlanAndStatus(ctx context.Context, statuses []string, since time.Time) (map[string]ExecutionStatusCounts, error)
	// GetPlannedExecutions returns executions in any of the given states
	// ordered by scheduled_date ASC (soonest first), the order the Planned
	// Purchases UI lists rows so the user acts on imminent purchases first.
	// Distinct from GetExecutionsByStatuses (which is DESC for History's
	// "newest first" semantics): when the result set exceeds `limit`, an
	// ORDER-BY-DESC + LIMIT in SQL truncates away the soonest rows, exactly
	// the rows this list must surface. Secondary sort by id ASC stabilizes
	// ordering when multiple rows share a scheduled_date.
	GetPlannedExecutions(ctx context.Context, statuses []string, limit int) ([]PurchaseExecution, error)
	// GetStaleApprovedExecutions returns executions stuck in the "approved"
	// status with updated_at older than olderThan — strands left behind when a
	// synchronous purchase run was interrupted before finalizing (issue #632).
	// The recovery sweep in the purchase manager re-drives these into a
	// terminal "failed" state so they can never sit permanently approved.
	GetStaleApprovedExecutions(ctx context.Context, olderThan time.Duration) ([]PurchaseExecution, error)
	// GetExecutionByID retrieves a purchase execution by execution ID.
	// Returns an error wrapping ErrNotFound when no execution exists;
	// never returns (nil, nil). A nil error guarantees a non-nil execution.
	GetExecutionByID(ctx context.Context, executionID string) (*PurchaseExecution, error)
	GetExecutionByPlanAndDate(ctx context.Context, planID string, scheduledDate time.Time) (*PurchaseExecution, error)
	// GetUserEmailByID resolves the email address of the auth user identified
	// by userID (the `users` table, owned by internal/auth). Read-only helper
	// used by the 4-eyes approval policy (issue #1005) to compare the acting
	// approver's identity against a PurchaseExecution.CreatedByUserID without
	// internal/purchase importing internal/auth (which would create an import
	// cycle: internal/auth already imports internal/config). Returns ("", nil)
	// when no user matches userID — the caller treats an empty result as
	// "identity unresolved" and fails closed, never as "email intentionally
	// blank".
	GetUserEmailByID(ctx context.Context, userID string) (string, error)
	// CountPendingExecutionsForAccount returns the number of purchase_executions
	// in status 'pending' or 'notified' that reference the given cloud account.
	// Used by the deleteAccount handler to preflight DB-level FK violations
	// (migration 000053 tightened the FK to ON DELETE RESTRICT) and emit a
	// 409 with a count so the frontend can offer Cancel-All-Then-Delete UX
	// instead of surfacing a raw constraint error. See issue #606.
	CountPendingExecutionsForAccount(ctx context.Context, accountID string) (int, error)
	// CountLiveExecutionsForAccount returns the number of purchase_executions
	// referencing the given cloud account whose status is not in
	// TerminalExecutionStatuses (pending, notified, approved, running,
	// scheduled, paused, or any status not yet classified). These are the
	// rows that block DeleteCloudAccount; terminal rows are detached and kept
	// as history instead (issue #265).
	CountLiveExecutionsForAccount(ctx context.Context, accountID string) (int, error)
	// ListPendingExecutionIDsForAccount returns the execution IDs of all
	// pending / notified executions referencing this account, used by the
	// frontend's Cancel-All-Then-Delete flow when the operator opts to
	// cancel everything in one go after a 409. Capped at 1000 rows; if a
	// single account has more pending executions than that, the cleanup
	// is a one-off operator task rather than a button click anyway.
	ListPendingExecutionIDsForAccount(ctx context.Context, accountID string) ([]string, error)
	CleanupOldExecutions(ctx context.Context, retentionDays int) (int64, error)
	// TransitionExecutionStatus atomically transitions an execution status.
	// actor is the UUID of the user performing the transition (nil for system-initiated paths).
	// When non-nil the actor is stamped onto transitioned_by + transitioned_at; when nil,
	// transitioned_by is set to NULL and transitioned_at is still set to NOW() for ordering.
	TransitionExecutionStatus(ctx context.Context, executionID string, fromStatuses []string, toStatus string, actor *string) (*PurchaseExecution, error)
	// TransitionExecutionStatusPinned is TransitionExecutionStatus that also
	// requires the row's recommendations to be empty (sawEmptyRecommendations)
	// or non-empty exactly as the caller saw them, so a claim cannot land on a
	// row whose recommendations were attached after the caller's gate check. A
	// lost pin returns ErrExecutionNotInExpectedStatus like any lost CAS.
	TransitionExecutionStatusPinned(ctx context.Context, executionID string, fromStatuses []string, toStatus string, actor *string, sawEmptyRecommendations bool) (*PurchaseExecution, error)
	// TransitionExecutionStatusPinnedTx is TransitionExecutionStatusPinned
	// inside a caller-owned transaction, so the status change commits or
	// rolls back with the caller's other writes (issue #1052).
	TransitionExecutionStatusPinnedTx(ctx context.Context, tx pgx.Tx, executionID string, fromStatuses []string, toStatus string, actor *string, sawEmptyRecommendations bool) (*PurchaseExecution, error)
	// TransitionExecutionStatusTokenPinned is TransitionExecutionStatus that
	// also requires the row's stored approval token hash to match rawToken and
	// to be unexpired, in the same statement as the status change, so a token
	// rotated or expired after the caller validated it cannot win the
	// transition (issue #590). A lost token pin on a row still in an allowed
	// status returns ErrApprovalTokenChanged; a lost status CAS returns
	// ErrExecutionNotInExpectedStatus as usual.
	TransitionExecutionStatusTokenPinned(ctx context.Context, executionID string, fromStatuses []string, toStatus string, actor *string, rawToken string) (*PurchaseExecution, error)
	// ScheduleApprovedExecution moves a delayed approval to "scheduled" and
	// stamps scheduled_execution_at (and approved_by when non-nil) in one
	// transaction, so a failed stamp can never leave a scheduled row that no
	// scheduler run picks up (#348 F6). It pins no approval token (session
	// path); the errors match the two transition methods.
	ScheduleApprovedExecution(ctx context.Context, executionID string, fromStatuses []string, transitionedBy *string, scheduledAt time.Time, approvedBy *string) (*PurchaseExecution, error)
	// ScheduleApprovedExecutionTokenPinned is ScheduleApprovedExecution for the
	// email-link path: it pins the approval token as
	// TransitionExecutionStatusTokenPinned does and fails closed
	// (ErrApprovalTokenChanged, no write) on an empty rawToken. The split keeps
	// a future caller that passes "" from silently skipping the pin (#1794).
	ScheduleApprovedExecutionTokenPinned(ctx context.Context, executionID string, fromStatuses []string, transitionedBy *string, rawToken string, scheduledAt time.Time, approvedBy *string) (*PurchaseExecution, error)
	// RescheduleAwaitingExecution sets scheduled_date on an execution that is
	// still pending or notified and touches no other column. It returns false
	// (and writes nothing) when the row has left those statuses, so a cancel or
	// approve that lands first is never overwritten.
	RescheduleAwaitingExecution(ctx context.Context, executionID string, scheduledDate time.Time) (bool, error)
	// SetCancelledBy stamps canceled_by on an execution without overwriting
	// any other columns. Used after TransitionExecutionStatus to fold the actor
	// attribution into the same logical write without the full-row
	// SavePurchaseExecution clobber risk (Finding #5 / PR #889).
	SetCancelledBy(ctx context.Context, executionID string, cancelledBy string) error
	// LinkRetryExecutionAtomic stamps retry_execution_id on a failed
	// execution row without a full-row overwrite, conditional on
	// status = 'failed' AND retry_execution_id IS NULL. Replaces a
	// SavePurchaseExecutionTx upsert of a stale in-memory copy of the
	// failed row, which let two concurrent retries of the same row both
	// pass the in-memory already-retried guard and both persist an
	// approvable successor (issue #220). Returns (true, nil) when this
	// call won the race, (false, nil) when a concurrent retry already
	// claimed the row (or it left 'failed'), and (false, err) on a real
	// DB error. Must be called inside the same tx that inserts the
	// successor row, after that insert.
	LinkRetryExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID, retryExecutionID string) (linked bool, err error)
	// LinkUnsentRetryExecutionAtomic is LinkRetryExecutionAtomic with the extra
	// condition no_submission AND retry_attempt_n = 0 (a proven-unsent first
	// attempt). The handler uses it for retries it allowed only on that proof.
	LinkUnsentRetryExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID, retryExecutionID string) (linked bool, err error)
	// LockLadderTrancheForRetryTx row-locks the ladder tranche whose id equals
	// idempotencyKey (matched as text; found=false for any non-ladder key) so
	// Retry can refuse a settled tranche before inserting a successor.
	LockLadderTrancheForRetryTx(ctx context.Context, tx pgx.Tx, idempotencyKey string) (status ladder.TrancheStatus, found bool, err error)
	// GetLadderExecutionGate returns the ladder config switches and run status
	// behind the tranche whose id equals idempotencyKey (matched as text), or
	// (nil, nil) for any non-ladder key. The executor rechecks it before a
	// provider call and fails closed on error.
	GetLadderExecutionGate(ctx context.Context, idempotencyKey string) (*LadderExecutionGate, error)
	// RotatePendingApprovalToken sets approval_token_hash and its expiry only
	// while the row is still pending/notified, touching no other column, so a
	// re-notification can never clobber a concurrent approve/cancel the way a
	// full-row upsert of a stale copy would. Returns false when the row left
	// pending/notified (issue #103).
	RotatePendingApprovalToken(ctx context.Context, executionID, tokenHash string, expiresAt time.Time) (rotated bool, err error)
	// CancelExecutionAtomic atomically flips status from pending / notified
	// to 'canceled' (canonical US spelling), setting canceled_by. The
	// 'scheduled' status is NOT accepted here; scheduled rows are revoked via
	// CancelScheduledExecutionAtomic (Gmail-style pre-fire delay revoke
	// path, issue #291 wave-2) so the two flows surface distinct CAS race
	// outcomes. Returns (true, "canceled", nil) on success and (false,
	// currentStatus, nil) when zero rows were affected (the execution had
	// already been approved or otherwise transitioned). Must be called
	// inside a WithTx block so the suppression cleanup and the status flip
	// commit atomically.
	CancelExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy *string) (canceled bool, currentStatus string, err error)
	// CancelExecutionAtomicTokenPinned is CancelExecutionAtomic that also
	// requires the row's stored approval token hash to match rawToken and to
	// be unexpired, in the same statement as the status change, so a token
	// rotated or expired after the email-link cancel validated it cannot
	// cancel the row (issue #1190). A lost token pin on a row still
	// pending/notified returns an error wrapping ErrApprovalTokenChanged; a
	// lost status CAS returns (false, currentStatus, nil) as usual.
	CancelExecutionAtomicTokenPinned(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy *string, rawToken string) (canceled bool, currentStatus string, err error)
	// CancelScheduledExecutionAtomic atomically flips status from 'scheduled' to
	// 'canceled' (canonical US spelling), setting canceled_by. Used by the
	// Gmail-style pre-fire delay revoke path (issue #291 wave-2) to cancel a
	// scheduled execution at $0 before the scheduler fires the SDK call. The
	// 'pending'/'notified' set accepted by CancelExecutionAtomic is
	// intentionally not extended here so the two revoke flows surface distinct
	// CAS race outcomes -- a scheduled row that the scheduler has already
	// transitioned to 'approved' / 'running' must surface as a 410 ("window
	// closed") rather than a 409 ("not pending"). Returns (true, "canceled",
	// nil) on success and (false, currentStatus, nil) when zero rows were
	// affected. Must be called inside a WithTx block.
	CancelScheduledExecutionAtomic(ctx context.Context, tx pgx.Tx, executionID string, cancelledBy *string) (canceled bool, currentStatus string, err error)
	// ListStuckExecutions returns executions in any of the given statuses
	// whose updated_at is older than the given duration. Used by the
	// reaper sweep (issue #678) to find rows stuck in approved/running
	// after the synchronous executor failed mid-flight without flipping
	// them to a terminal state. Oldest-stuck-first (ORDER BY updated_at
	// ASC), capped at MaxListLimit per sweep.
	ListStuckExecutions(ctx context.Context, statuses []string, olderThan time.Duration) ([]PurchaseExecution, error)

	// GetScheduledExecutionsDue returns purchase_executions with
	// status='scheduled' whose scheduled_execution_at is in the past
	// (scheduled_execution_at <= NOW()). Used by the Gmail-style pre-fire
	// delay scheduler tick (issue #291 wave-2) to find rows ready to fire.
	// Oldest-due-first (ORDER BY scheduled_execution_at ASC), capped at
	// MaxListLimit per sweep.
	GetScheduledExecutionsDue(ctx context.Context) ([]PurchaseExecution, error)

	HistoryStore

	// GetMarketplaceListingAttempt returns the listing id, state and the token
	// of an unresolved list attempt of the row for purchaseID, or (nil, nil)
	// when there is none. The cancel endpoint uses it to recover a pending row
	// (issue #525).
	GetMarketplaceListingAttempt(ctx context.Context, purchaseID string) (*MarketplaceListingAttempt, error)

	// ResolveMarketplaceListingAttempt records listingID in an ended (or, to
	// clear a stuck pending row without a listing, empty) state and drops the
	// attempt token, only while the row still matches seen or already holds
	// listingID. It reports whether the row was written (issue #525).
	ResolveMarketplaceListingAttempt(ctx context.Context, purchaseID string, seen MarketplaceListingAttempt, listingID, state string) (bool, error)

	// StampOfferingClass writes the offering_class value to a purchase_history
	// row identified by purchase_id. Called by the marketplace-list handler
	// when offering_class is absent in the DB (pre-migration 000087 rows and
	// externally-created Standard RIs): after fetching the class from AWS
	// DescribeReservedInstances it is persisted so subsequent requests do not
	// incur an extra AWS API call.
	StampOfferingClass(ctx context.Context, purchaseID, offeringClass string) error

	// ClaimMarketplaceListingSlot atomically reserves the marketplace-listing
	// slot for a purchase_history row so two concurrent marketplace-list
	// requests cannot both proceed to create a duplicate AWS listing (issue
	// #292). It transitions listing_state to ListingStatePending only when the
	// row is not listed, and in the same UPDATE persists clientToken and
	// priceSchedule (JSON) unless an unresolved attempt already stored a token,
	// in which case that token and schedule are kept so the retry is idempotent
	// on AWS (issue #525). A pending row that holds a token is claimable again:
	// the same token makes a concurrent or crashed attempt safe to resume.
	// It returns the claimed row's values, or (nil, nil) when the row is active,
	// pending without a token, or absent (the caller maps that to a 409). The
	// caller must persist the real listing on success or call
	// ReleaseMarketplaceListingClaim on failure.
	ClaimMarketplaceListingSlot(ctx context.Context, purchaseID, clientToken string, priceSchedule []byte) (*MarketplaceListingClaim, error)

	// ReleaseMarketplaceListingClaim moves a pending row back to priorState
	// (empty for none) after a failed attempt. It only acts on a row whose
	// persisted token is clientToken, so a request never releases or drops the
	// attempt of another request. keepAttempt keeps the persisted token and
	// schedule so the retry resends them (ambiguous AWS outcome); otherwise
	// they are cleared because AWS created nothing reusable.
	ReleaseMarketplaceListingClaim(ctx context.Context, purchaseID, clientToken, priorState string, keepAttempt bool) error

	// ListMarketplaceListingsToPoll returns the AWS listings the marketplace
	// status poller reads (issue #27): active listings, listings recorded as
	// pending with no list attempt in flight (issue #1494), and
	// closed/canceled ones it has seen whose proceeds are not recorded yet,
	// both the RI's current listing and the listings it was relisted from
	// (issue #1494).
	ListMarketplaceListingsToPoll(ctx context.Context) ([]PurchaseHistoryRecord, error)

	// RecordMarketplaceListingActive records a listing AWS reports active:
	// it stamps listed_at (a zero listedAt keeps the stored one) while the row
	// holds listingID as active, and also moves the state to active while the
	// row holds it as pending with no list attempt in flight (issue #1494).
	// It never writes the state of any other row. Reports whether the row
	// changed.
	RecordMarketplaceListingActive(ctx context.Context, purchaseID, listingID string, listedAt time.Time) (bool, error)

	// SettleMarketplaceListing records a listing that closed or was canceled
	// on AWS, with its proceeds and AWS fee when known, in the listing's own
	// settlement row and, while the RI still holds the listing, on its
	// purchase_history row (issue #1494). It applies once per listing, only
	// while the listing is active, pending with no list attempt in flight, or
	// already in the same terminal state. Reports whether a row was written.
	SettleMarketplaceListing(ctx context.Context, settlement MarketplaceListingSettlement) (bool, error)

	// GetMarketplaceListingProceedsTotals sums each RI's marketplace proceeds
	// and fees over all of its listings (issue #1494), one total per purchase
	// and currency. RIs with no recorded listing are left out. Only purchases
	// of the given accounts count (same account arguments as
	// GetActivePurchaseHistory; empty scope is an error), and each total is an
	// estimate (issue #1586).
	GetMarketplaceListingProceedsTotals(ctx context.Context, purchaseIDs, accountIDs []string, externalIDsByProvider map[string][]string) ([]MarketplaceListingProceedsTotal, error)

	RIExchangeStore

	AccountStore

	RecommendationStore
	// SavePurchaseExecutionTx is the tx-accepting variant of
	// SavePurchaseExecution. Used from executePurchase's WithTx block
	// so the execution insert + suppression writes commit atomically.
	SavePurchaseExecutionTx(ctx context.Context, tx pgx.Tx, execution *PurchaseExecution) error

	// SaveExecutionIfStatusTx and RecoverReapedExecutionTx are the
	// tx-accepting variants of SaveExecutionIfStatus and RecoverReapedExecution.
	SaveExecutionIfStatusTx(ctx context.Context, tx pgx.Tx, execution *PurchaseExecution, expected []string) error
	RecoverReapedExecutionTx(ctx context.Context, tx pgx.Tx, execution *PurchaseExecution) error

	// StampFirstDispatchTx sets the lineage's first_dispatch_at on the
	// running row (kept once set; minted only per the mint rule) and reports
	// whether it is within horizon on the database clock.
	StampFirstDispatchTx(ctx context.Context, tx pgx.Tx, executionID string, horizon time.Duration) (StampResult, error)

	// LockRetryFamilyTx takes a transaction-scoped advisory lock on a fan-out
	// key family so a root retry and an account-row retry serialize.
	LockRetryFamilyTx(ctx context.Context, tx pgx.Tx, family string) error
	// FamilyRetryClaimedTx reports whether a row of the opposite kind (account
	// rows when selfIsRoot, the root otherwise) in the same plan and key family
	// already has a retry successor. Call it under LockRetryFamilyTx.
	FamilyRetryClaimedTx(ctx context.Context, tx pgx.Tx, family, planID, selfID string, selfIsRoot bool) (claimed bool, err error)

	// PredecessorProvenUnsent reports whether executionID's retry predecessor
	// is a no_submission attempt 0 row (the lineage provably sent nothing).
	PredecessorProvenUnsent(ctx context.Context, executionID string) (bool, error)

	// LockRedshiftTuplesTx takes a transaction-scoped advisory lock per
	// distinct tuple, in sorted order, serializing dispatches that share one.
	LockRedshiftTuplesTx(ctx context.Context, tx pgx.Tx, tuples []RedshiftTuple) error

	// FindOpenRedshiftLineageConflictsTx returns the other open executions
	// holding a Redshift rec for one of tuples that a dispatch of family must
	// not race (see the implementation for the predicate).
	FindOpenRedshiftLineageConflictsTx(ctx context.Context, tx pgx.Tx, selfID, family string, tuples []RedshiftTuple, accountID *string, horizon time.Duration, stamp time.Time, window, settle time.Duration) ([]RedshiftLineageConflict, error)

	// GetRedshiftRetryClock returns how long ago an execution was last written
	// and first dispatched, both measured on the database clock.
	GetRedshiftRetryClock(ctx context.Context, executionID string) (RedshiftRetryClock, error)

	// ListRecentSubmitsTx takes a per-creator transaction-scoped lock and
	// returns the creator's web-submitted executions scheduled at or after
	// since that are pending, in flight or bought. executePurchase's
	// duplicate guard reads it inside the WithTx block that inserts the new
	// execution, so concurrent identical submits serialize and a retry
	// that arrives after a direct execute left "pending" is still seen.
	ListRecentSubmitsTx(ctx context.Context, tx pgx.Tx, creatorID string, since time.Time) ([]PurchaseExecution, error)

	// WithTx opens a pgx transaction, runs fn, and commits on success or
	// rolls back on error. fn can call any *Tx method on the store to
	// participate in the transaction. Nested transactions are not
	// supported — fn must not call WithTx recursively.
	WithTx(ctx context.Context, fn func(tx pgx.Tx) error) error

	LadderStore

	// Notification mutes (issue #297 / migration 000091).
	// UpsertNotificationMute inserts or updates a mute row for (email, scope).
	// Idempotent for row existence: calling it again for an already-muted
	// address refreshes muted_at and replaces unmute_token if the token changes.
	UpsertNotificationMute(ctx context.Context, recipientEmail, scope, unmuteToken string) error
	// IsNotificationMuted returns true when (email, scope) has a row in
	// muted_recipients. The email comparison is case-insensitive.
	IsNotificationMuted(ctx context.Context, recipientEmail, scope string) (bool, error)
}
