package config

import (
	"strings"
	"time"
)

// PurchaseHistoryRecord is the response-layer representation for rows on the
// /api/history page. DB-backed rows always describe *completed* purchases; the
// handler additionally synthesizes rows for pending executions so users can
// see (and cancel) in-flight approvals. Status is the discriminator — the DB
// layer never writes it (tag `dynamodbav:"-"` keeps it out of persistence),
// and the API layer populates it as "completed" or "pending" before returning.
type PurchaseHistoryRecord struct {
	Timestamp time.Time `json:"timestamp" dynamodbav:"timestamp"`
	// --- Revocation window fields (issue #290) ---
	//
	// RevocationWindowClosesAt is set when the purchase_history row is
	// written: Timestamp + the provider-specific free-cancel window
	// (Azure: 7 days). NULL for AWS EC2 (no direct cancel API) and GCP
	// (no free-cancel window). Persisted in purchase_history.
	RevocationWindowClosesAt *time.Time `json:"revocation_window_closes_at,omitempty" dynamodbav:"revocation_window_closes_at,omitempty"`
	UpfrontCost              *float64   `json:"upfront_cost" dynamodbav:"upfront_cost"`
	// ReturnPostedAt is when the revoke handler recorded that Azure accepted
	// the Return (MarkRevocationReturnPosted, migration 000148). Set means the
	// refund was issued; unset proves nothing, because the stamp is best-effort
	// (issue #1522). Read only by GetPurchaseHistoryByPurchaseID and never
	// serialized, so history responses do not expose it.
	ReturnPostedAt *time.Time `json:"-" dynamodbav:"-"`
	// --- Refund-quote audit fields (issue #290 Finding #4, migration 000071) ---
	//
	// CalcRefundAmount is the amount Azure quoted at CalculateRefund time, captured
	// for audit and TOCTOU-divergence detection in the two-step revoke confirm flow.
	// NULL for revocations that predate this feature or where Azure returned no amount.
	CalcRefundAmount *float64 `json:"calc_refund_amount,omitempty" dynamodbav:"calc_refund_amount,omitempty"`
	// RevokedAt is set by the revoke endpoint when the provider API
	// confirmed the cancellation / refund. Persisted.
	RevokedAt      *time.Time `json:"revoked_at,omitempty" dynamodbav:"revoked_at,omitempty"`
	CloudAccountID *string    `json:"cloud_account_id,omitempty" dynamodbav:"cloud_account_id,omitempty"`
	// MonthlyCost is nil when the provider API did not return a monthly
	// recurring breakdown for this commitment (e.g. Azure all-upfront where
	// no recurring charge exists at the commitment layer). GCP commitments
	// are monthly-billed in this repo, so they always populate MonthlyCost.
	// The frontend renders "—" for nil, "$X.XX" when populated. Aggregations
	// must skip nil entries rather than treating them as $0 to avoid
	// distorting totals. Migration 000063 dropped the NOT NULL constraint so
	// new rows can carry NULL; existing rows with 0.0 are preserved as-is
	// (those are real zeros from AWS all-upfront commitments).
	MonthlyCost *float64 `json:"monthly_cost" dynamodbav:"monthly_cost"`
	// CreatedByUserEmail is the email address of the user who created the
	// underlying execution, resolved from CreatedByUserID via the auth
	// service. Populated only on synthesized execution rows (pending,
	// notified, failed, expired, canceled) when a valid user ID is
	// present; empty for scheduler-driven executions, legacy NULL-creator
	// rows, and completed purchase_history rows. Excluded from DB
	// persistence (resolved at read time). The UI renders this in the
	// Approval Queue "Created by" column instead of the raw UUID.
	CreatedByUserEmail string `json:"created_by_user_email,omitempty" dynamodbav:"-"`
	// SupportCaseID is non-empty when RevokedVia == "support-case".
	// Persisted.
	SupportCaseID string `json:"support_case_id,omitempty" dynamodbav:"support_case_id,omitempty"`
	// ListingState mirrors the AWS marketplace listing state (see the
	// ListingState* constants). Empty when not listed. Persisted in
	// purchase_history via migration 000087.
	ListingState string `json:"listing_state,omitempty" dynamodbav:"listing_state,omitempty"`
	// ListingID is the AWS ReservedInstancesListingId returned by
	// CreateReservedInstancesListing. Empty when the RI has not been
	// listed. Persisted in purchase_history via migration 000087.
	ListingID string `json:"listing_id,omitempty" dynamodbav:"listing_id,omitempty"`
	// OfferingClass records whether this commitment is a 'standard' or
	// 'convertible' RI. NULL on pre-migration rows. The Sell-on-Marketplace
	// button renders only when this equals "standard" (issue #292).
	// Persisted in purchase_history via migration 000087.
	OfferingClass string `json:"offering_class,omitempty" dynamodbav:"offering_class,omitempty"`
	PlanID        string `json:"plan_id,omitempty" dynamodbav:"plan_id,omitempty"`
	PlanName      string `json:"plan_name,omitempty" dynamodbav:"plan_name,omitempty"`
	Source        string `json:"source,omitempty" dynamodbav:"source,omitempty"`
	ResourceType  string `json:"resource_type" dynamodbav:"resource_type"`
	// AmountBasis is set from the amount_basis column; no writer names it.
	AmountBasis PurchaseAmountBasis `json:"amount_basis,omitempty" dynamodbav:"-"`
	Status      string              `json:"status,omitempty" dynamodbav:"-"`
	PurchaseID  string              `json:"purchase_id" dynamodbav:"purchase_id"`
	Provider    string              `json:"provider" dynamodbav:"provider"`
	// OpsHint is a short operator-actionable message rendered inline in
	// place of the Retry button when the failure reason on the row
	// matches a known-persistent-misconfiguration pattern (e.g.
	// "FROM_EMAIL not configured" → "Set FROM_EMAIL tfvar then retry").
	// Set only on `failed` rows whose Error matches the persistent map;
	// empty otherwise. Excluded from DB persistence (computed at read
	// time so updates to the persistent-failure map land instantly).
	OpsHint string `json:"ops_hint,omitempty" dynamodbav:"-"`
	// CreatedByUserID propagates the originating execution's
	// created_by_user_id so the History UI can decide whether to render
	// the inline Cancel button (issue #46): a non-admin user only sees
	// the button on their own pending rows. Set only for synthesized
	// pending/notified rows (executions); empty on completed history
	// rows (where the action has already completed). Excluded from DB
	// persistence.
	CreatedByUserID string `json:"created_by_user_id,omitempty" dynamodbav:"-"`
	// CalcRefundCurrency is the ISO-4217 currency code from the CalculateRefund quote
	// (e.g. "USD"). NULL when CalcRefundAmount is NULL.
	CalcRefundCurrency string `json:"calc_refund_currency,omitempty" dynamodbav:"calc_refund_currency,omitempty"`
	// RetryExecutionID propagates the originating execution's pointer to
	// its successor when the user retried it (issue #47). Set only on
	// the *original* failed row that has been retried; the History UI
	// renders an inline "Retried as #abc" link to the successor row when
	// this is non-empty. Excluded from DB persistence (synthesized from
	// purchase_executions).
	RetryExecutionID string `json:"retry_execution_id,omitempty" dynamodbav:"-"`
	Service          string `json:"service" dynamodbav:"service"`
	// StatusDescription carries a short human-readable explanation for non-
	// completed rows. For "failed", this is the stored Error message (e.g.
	// "send failed: Missing domain"). For "expired", a canned reminder that
	// the 7-day approval window elapsed. Empty on completed/pending rows —
	// those speak for themselves via Status alone.
	StatusDescription string `json:"status_description,omitempty" dynamodbav:"-"`
	Payment           string `json:"payment" dynamodbav:"payment"`
	// RevokedVia identifies how the revocation was completed: "direct-api"
	// (provider returned 2xx) or "support-case" (AWS Support case filed).
	// Persisted.
	RevokedVia string `json:"revoked_via,omitempty" dynamodbav:"revoked_via,omitempty"`
	// Approver holds the email address the approval request was sent to (or
	// would have been, if SES failed). Set only on pending rows, so the
	// History UI can show "awaiting approval from <addr>" and the user knows
	// exactly whose inbox to check. Excluded from DB persistence.
	Approver  string `json:"approver,omitempty" dynamodbav:"-"`
	AccountID string `json:"account_id" dynamodbav:"account_id"`
	Region    string `json:"region" dynamodbav:"region"`
	// RetryAttemptN propagates the originating execution's retry-chain
	// position so the History UI can render "↻ Retry of #xyz" inline
	// links on retry rows (n > 0) and gate the Retry button against the
	// soft-block threshold (n >= 5). Excluded from DB persistence.
	RetryAttemptN    int     `json:"retry_attempt_n,omitempty" dynamodbav:"-"`
	RampStep         int     `json:"ramp_step,omitempty" dynamodbav:"ramp_step,omitempty"`
	EstimatedSavings float64 `json:"estimated_savings" dynamodbav:"estimated_savings"`
	Count            int     `json:"count" dynamodbav:"count"`
	Term             int     `json:"term" dynamodbav:"term"`
	// IsAuditGap marks a synthesized "completed" row whose purchase_history
	// write failed after a successful purchase (issue #621). Such a row is
	// reconstructed from the execution so the purchase stays visible, but its
	// execution-level dollars are excluded from the committed totals: a
	// partially-saved multi-rec execution can have BOTH some real
	// purchase_history rows AND this synthesized row, so adding the full
	// execution total would double-count the recs that did save. The dollars
	// are surfaced via the individual purchase_history rows that succeeded;
	// this row is the audit flag, not a money source. Real purchase_history
	// rows loaded from the DB always leave this false. Excluded from DB
	// persistence (set only at read time on synthesized rows).
	IsAuditGap bool `json:"is_audit_gap,omitempty" dynamodbav:"-"`
	// RedriveUnsafe is true when OpsHint is a permanent refusal from
	// purchase.RetryRefusalReason (provider has no duplicate guard), not an
	// operator-fixable misconfiguration. Computed at read time, like OpsHint.
	RedriveUnsafe bool `json:"redrive_unsafe,omitempty" dynamodbav:"-"`
	// CreatedBySystem propagates the execution's scheduler-created marker (#233).
	CreatedBySystem bool `json:"created_by_system,omitempty" dynamodbav:"-"`
	// --- Partial-success reconciliation (issue #290 Finding #6, migration 000072) ---
	//
	// RevocationInFlight is set to true immediately before the Azure Return API call
	// and cleared (set to false) by a successful MarkPurchaseRevoked. When all DB
	// retries fail, the flag stays true so the finalize_revocations scheduled sweep
	// can detect and retry the MarkPurchaseRevoked write without re-calling Azure
	// (preventing a duplicate-refund error).
	RevocationInFlight bool `json:"revocation_in_flight,omitempty" dynamodbav:"revocation_in_flight,omitempty"`
}

// Validate rejects a record SavePurchaseHistory cannot store without losing
// it: one with no purchase id (see ErrEmptyPurchaseID).
func (r *PurchaseHistoryRecord) Validate() error {
	if strings.TrimSpace(r.PurchaseID) == "" {
		return ErrEmptyPurchaseID
	}
	return nil
}

// IsLegacyEstimate reports whether the row's dollar amounts are a legacy
// estimate that totals must skip.
func (r *PurchaseHistoryRecord) IsLegacyEstimate() bool {
	return r.AmountBasis == PurchaseAmountBasisLegacyWindow
}

// PurchaseHistoryFilter is the filter set consumed by
// StoreInterface.GetPurchaseHistoryFiltered. Each field is optional; a
// zero-valued filter selects all rows (same plan-shape as
// GetAllPurchaseHistory). See the implementation docstring for the per-field
// semantics and the dual-column account predicate.
type PurchaseHistoryFilter struct {
	// ExternalIDsByProvider matches purchase_history.account_id (the
	// cloud-provider external account number) scoped per provider. The caller
	// resolves AccountIDs to their (provider, external_id) pairs and groups the
	// external ids by provider, so the predicate matches each external id only
	// against rows of its own provider:
	//
	//	((provider = '' OR LOWER(provider) = LOWER($p)) AND account_id = ANY($extsForP))
	//
	// This keeps the (provider, external_id) pairing intact so a filter for
	// aws/123 never pulls azure/123 rows that reuse the same external number.
	// The provider comparison ignores case, and a row whose stored provider is
	// empty (unknown) matches on account_id alone (issue #24).
	// The "" provider key means "provider unknown" (legacy raw external number)
	// and matches account_id without a provider gate. Empty/nil skips this half
	// of the account predicate.
	ExternalIDsByProvider map[string][]string
	// Start/End bound purchase_history.timestamp. nil for both skips the clause;
	// nil for either leaves that side open (caller owns any range cap, see
	// api.MaxHistoryDateRangeDays).
	Start *time.Time
	End   *time.Time
	// Scope, when non-nil, keeps only rows whose account_id is one of
	// Scope.AccountIDs (UserID is not read: purchase_history rows have no
	// creator). Empty AccountIDs keeps nothing (fail closed); nil is an
	// unrestricted caller. Like the execution scope it is a superset of the
	// handler's Go scope check, which still runs on the rows.
	Scope *ExecutionScope
	// Provider matches purchase_history.provider exactly. Empty skips the clause.
	Provider string
	// PlanID matches purchase_history.plan_id exactly; HistorySummaryAggregate
	// also applies it to purchase_executions.plan_id. Empty skips the clause.
	PlanID string
	// AccountIDs matches purchase_history.cloud_account_id (the cloud_accounts
	// UUID FK) with ANY($). Empty/nil skips this half of the account predicate.
	AccountIDs []string
	// Limit caps the row count; clamped to [1, MaxListLimit] with a
	// DefaultListLimit fallback when <= 0.
	Limit int
}

// AzureRevocationWindowDays is the length of the Azure reservation free-cancel
// window: a reservation can be returned for a full refund within this many days
// of purchase (issue #290). It is the single source of truth for the window,
// referenced both at purchase-write time (to stamp
// PurchaseHistoryRecord.RevocationWindowClosesAt) and by the revoke endpoint's
// window check, so the two never drift.
const AzureRevocationWindowDays = 7

// RevocationWindowClosesAtFor returns the timestamp at which the in-app revoke
// button should stop being offered for a purchase of the given provider made at
// purchaseTime, or nil when the provider has no in-app free-cancel window.
//
// Only Azure has a direct-API free-cancel window in Phase 1. AWS EC2 RIs have a
// 24h window but no direct cancel API (revocation goes through an AWS Support
// case, out of Phase-1 scope), and GCP commitments have no free-cancel window
// at all, so both return nil and the History UI hides the button.
func RevocationWindowClosesAtFor(provider string, purchaseTime time.Time) *time.Time {
	if strings.EqualFold(provider, "azure") {
		closesAt := purchaseTime.AddDate(0, 0, AzureRevocationWindowDays)
		return &closesAt
	}
	return nil
}

// PurchaseAmountBasis says how a purchase_history row's dollar amounts were
// derived. Empty is a normal row. It is distinct from LadderAmountBasis, which
// describes ladder tranche amounts.
type PurchaseAmountBasis string

// PurchaseAmountBasisLegacyWindow marks an Azure row whose upfront_cost and
// estimated_savings were estimated over a window before the repricing fix
// (issue #669). Totals skip its dollars; the row stays listed and counted.
const PurchaseAmountBasisLegacyWindow PurchaseAmountBasis = "legacy_window"

// MarketplaceListingClaim is what ClaimMarketplaceListingSlot reads back from
// the row it just claimed, so the handler works from the row as it is at claim
// time instead of a copy read before the claim (issue #525).
type MarketplaceListingClaim struct {
	// ListingID is the listing recorded on the row when it was claimed.
	ListingID string
	// PriorState is the listing_state before the claim; empty when none.
	PriorState string
	// ClientToken is the token of this attempt: the one passed to the claim, or
	// the persisted one when an unresolved attempt already held a token.
	ClientToken string //nolint:gosec // G117: an idempotency token for AWS, not a credential
	// PriceSchedule is the JSON price schedule stored with ClientToken.
	PriceSchedule []byte
	// Resumed reports that the token and schedule were already persisted by an
	// earlier attempt that never recorded a listing. It is derived from the
	// returned row (its token differs from the one this claim tried to write),
	// so it stays correct when two claims race. PriorState is then "pending",
	// the state the earlier attempt left; releasing restores an empty state
	// and the row keeps its recorded listing id, which every list, cancel and
	// claim check treats the same as the original dead state.
	Resumed bool
}

// MarketplaceListingSettlement is what the marketplace status poller records
// when a listing reaches a terminal state on AWS (issue #27).
type MarketplaceListingSettlement struct {
	// ListedAt is the listing's AWS CreateDate; nil keeps the stored value.
	ListedAt *time.Time
	// ProceedsReceived is the USD the seller receives for the sold instances,
	// net of the AWS fee, and FeePaid that fee. Both nil when the sale price
	// could not be determined; the row is then polled again.
	ProceedsReceived *float64
	FeePaid          *float64
	// PurchaseID and ListingID identify the row and the listing it must still
	// record; a row that moved on to another listing is left alone.
	PurchaseID string
	ListingID  string
	// State is the terminal AWS state: ListingStateClosed or
	// ListingStateCancelled.
	State string
}

// MarketplaceListingProceedsTotal is one RI's RI Marketplace sales summed over
// every listing it has had (issue #1494), in one currency.
type MarketplaceListingProceedsTotal struct {
	// PurchaseID is the RI (reservation) id.
	PurchaseID string
	// Currency is the ISO code of the amounts; the poller records USD only.
	Currency string
	// ProceedsReceivedCents is the seller's net proceeds and FeePaidCents the
	// AWS fee, summed over the settled listings, in whole cents of Currency
	// (integer minor units: money is never summed or carried as a float).
	ProceedsReceivedCents int64
	FeePaidCents          int64
	// SettledListings counts the listings whose amounts are recorded, and
	// UnsettledListings those still without amounts (not ended yet, ended
	// with an unknown price, or ended before the poller saw them).
	SettledListings   int
	UnsettledListings int
	// IsEstimate is always true: the amounts are computed from the AWS
	// listing's sold count and price by the poller (D23), not read from an
	// AWS payout, so every place that shows or serializes them must label
	// them as estimates (issue #1586).
	IsEstimate bool
}

// AWS EC2 ReservedInstancesListing status values, mirroring the
// ec2types.ListingStatus enum. Stored verbatim in purchase_history.listing_state
// so the strings must match AWS exactly. ListingStatePending is additionally
// written by the marketplace-list handler as a transient claim that guards
// against concurrent listing creation (issue #292); the other three come
// straight from AWS.
const (
	ListingStateActive    = "active"
	ListingStatePending   = "pending"
	ListingStateCancelled = "cancelled" //nolint:misspell // AWS ListingStatus enum literal, not prose
	ListingStateClosed    = "closed"
)
