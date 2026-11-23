package config

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// RIExchangeStore holds the RI exchange methods of StoreInterface; StoreInterface embeds it.
type RIExchangeStore interface {
	// ClaimRIExchangeIdempotencyKey atomically claims key for an RI exchange
	// submit, so a client that retries a timed-out execute request cannot
	// commit the same exchange twice (issue #1642). key is a fingerprint of
	// what the request buys; the caller derives it and claims it immediately
	// before the irreversible provider call.
	//
	// Returns (true, nil) when this call won the claim and may proceed to
	// commit, and (false, nil) when another submit of the same fingerprint
	// claimed it less than window ago (the caller maps this to a 409).
	// A claim older than window is reclaimable, so a genuine intentional
	// repeat of the same exchange is not blocked forever.
	//
	// There is no release: once taken, a claim stands for the rest of the
	// window whatever the provider call does, including when it fails. Past
	// the point of submission the outcome is ambiguous, and holding the claim
	// is the fail-closed choice. The consequence for the caller's 409 is that
	// it cannot promise the earlier submit committed, only that it claimed the
	// window.
	ClaimRIExchangeIdempotencyKey(ctx context.Context, key string, window time.Duration) (bool, error)

	// RI Exchange history
	SaveRIExchangeRecord(ctx context.Context, record *RIExchangeRecord) error
	ReserveRIExchange(ctx context.Context, record *RIExchangeRecord, dailyCapUSD, perExchangeCapUSD string) (string, error)
	// ReserveApprovedRIExchange reserves the execution ceiling for an already
	// processing (approved) exchange under the shared daily-cap lock, excluding
	// the row's own amount, and stores the ceiling as its payment_due.
	ReserveApprovedRIExchange(ctx context.Context, id, dailyCapUSD, perExchangeCapUSD string) (string, error)
	// GetRIExchangeRecord returns a wrapped ErrNotFound when no record has id.
	GetRIExchangeRecord(ctx context.Context, id string) (*RIExchangeRecord, error)
	GetRIExchangeRecordByToken(ctx context.Context, token string) (*RIExchangeRecord, error)
	GetRIExchangeHistory(ctx context.Context, since time.Time, limit int) ([]RIExchangeRecord, error)
	// GetRIExchangeHistoryForAccounts is GetRIExchangeHistory restricted to
	// records whose account_id is in accountIDs, with the limit applied after
	// that restriction. An empty accountIDs matches no records.
	GetRIExchangeHistoryForAccounts(ctx context.Context, since time.Time, limit int, accountIDs []string) ([]RIExchangeRecord, error)
	// TransitionRIExchangeStatus atomically transitions an RI exchange record status.
	// actor is the UUID of the user performing the transition (nil for system-initiated paths).
	// A lost transition returns a wrapped ErrRIExchangeExpired when the record
	// has expired, or ErrRIExchangeNotInExpectedStatus when its status moved on.
	TransitionRIExchangeStatus(ctx context.Context, id string, fromStatus string, toStatus string, actor *string) (*RIExchangeRecord, error)
	CompleteRIExchange(ctx context.Context, id string, exchangeID string) error
	// CompleteRIExchangeWithPayment marks an RI exchange as completed and
	// updates payment_due to the amount AWS actually accepted. Use this instead
	// of CompleteRIExchange on the manual-approval path so the daily-spend
	// ledger (GetRIExchangeDailySpend) reflects the real accepted amount rather
	// than the stale pre-execution quote (H3 fix).
	CompleteRIExchangeWithPayment(ctx context.Context, id string, exchangeID string, acceptedPaymentDue string) error
	// StampRIExchangeApprovedBy sets the approved_by column on a completed
	// exchange row (issue #300). Called after CompleteRIExchangeWithPayment when
	// the approval came from a session-authed user rather than an email token.
	StampRIExchangeApprovedBy(ctx context.Context, id string, approverEmail string) error
	FailRIExchange(ctx context.Context, id string, errorMsg string) error
	// NoteRIExchangeOutcomeUnknown sets the error note on a row that is still
	// processing, leaving it processing for the operator to resolve (#1050).
	NoteRIExchangeOutcomeUnknown(ctx context.Context, id, note string) error
	GetRIExchangeDailySpend(ctx context.Context, date time.Time) (string, error)
	CancelAllPendingExchanges(ctx context.Context) (int64, error)
	// CancelPendingExchangesByOrigin cancels only pending records whose origin
	// matches:
	//   - common.ExchangeOriginStandalone: cancels WHERE ladder_run_id IS NULL
	//   - common.ExchangeOriginLadder:     cancels WHERE ladder_run_id IS NOT NULL
	// The origin is validated at the boundary and an unknown value fails loud.
	// This prevents the standalone ri_exchange_reshape task from wiping out
	// ladder-linked pending reshapes and vice versa (gap G10 / issue #1348).
	CancelPendingExchangesByOrigin(ctx context.Context, origin common.ExchangeOrigin) (int64, error)
	GetStaleProcessingExchanges(ctx context.Context, olderThan time.Duration) ([]RIExchangeRecord, error)

	// RI exchange direct-execute audit trail (issue #82). Exchanges executed
	// directly through the API, not through the approval flow above, are
	// recorded in their own table: SaveRIExchangeDirectExecution inserts the
	// row as "submitted" before the provider call, and
	// FinalizeRIExchangeDirectExecution records the outcome on it exactly
	// once (ErrRIExchangeDirectExecutionNotSubmitted when the row is absent or
	// already finalized). They are not in ri_exchange_history, but the daily
	// spend query counts them (#145): completed rows by net payable, unresolved
	// submitted/unknown rows by their max_payment_due.
	SaveRIExchangeDirectExecution(ctx context.Context, record *RIExchangeDirectExecution) error
	FinalizeRIExchangeDirectExecution(ctx context.Context, id string, outcome RIExchangeDirectOutcome) error
}
