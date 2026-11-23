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
