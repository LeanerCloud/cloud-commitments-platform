package email

import (
	"strconv"
	"strings"
)

// NotificationData holds data for rendering email templates.
type NotificationData struct {
	// Ladder, when non-nil, makes the scheduled-purchase notice describe an
	// automatic ladder purchase. Only the ladder sweep sets it; it is never
	// inferred from the other fields.
	Ladder                   *LadderNoticeData
	PlanName                 string
	CancellationWindowNote   string
	PlanID                   string
	RevokeURL                string
	RevocationWindowClosesAt string
	ArcheraEducationURL      string
	PurchaseDate             string
	// RevocationToken is the one-time token embedded in the revocation link
	// of a post-execution notification email. When non-empty, the template
	// renders a "Revoke this purchase" CTA that hits
	// /purchases/revoke/{ExecutionID}?token=<RevocationToken>, the SPA deep link
	// that posts to /api/purchases/revoke/{ExecutionID} with the session cookie.
	// Empty silently omits the revocation panel so other email flows are
	// unaffected.
	RevocationToken  string
	ExecutionID      string
	DashboardURL     string
	ApprovalToken    string
	RecipientEmail   string
	RequestedByEmail string
	RequestedByName  string
	// ExecutedBy is the email of the user who triggered execution (approved
	// the purchase). Used in the post-execution notification body.
	// Empty omits the field.
	ExecutedBy  string
	RequestedAt string
	// ExecutedAt is the ISO-8601 / RFC-3339 timestamp the purchase was
	// executed at. Used in the post-execution notification body.
	// Empty omits the timestamp from the body.
	ExecutedAt          string
	Recommendations     []RecommendationSummary
	AuthorizedApprovers []string
	CCEmails            []string
	TotalUpfrontCost    float64
	TotalUnits          int
	StepCap             float64
	TotalSavings        float64
	DaysUntilPurchase   int
	TotalRecurring      float64
	// TotalCommitment, TotalRecurring, TotalUnits and StepCap describe what a
	// scheduled plan step will buy. TotalCommitment is upfront plus the
	// recurring charges over each term; StepCap is the plan's per-step cap, 0
	// when none is shown. A zero TotalCommitment omits the block.
	TotalCommitment float64
	// NeedsApproval is set when the step does not run on its date but waits
	// for a human approval in the approval queue (manual plan, or 4-eyes on).
	NeedsApproval bool
	// PartialPurchase marks a post-execution notification for a
	// partially_completed run (issue #532): the template says some
	// commitments were not purchased instead of claiming all of them were.
	PartialPurchase bool
	// OutcomeUnknown switches the purchase-failed email to the outcome-unknown
	// variant, for a row a recovery sweep failed after its purchase run was
	// interrupted: the commitment may already exist, so the email asks the
	// operator to verify in the cloud console instead of retrying (issue #1415).
	OutcomeUnknown bool
	// UnrecordedCommitment, with OutcomeUnknown, says the provider returned a
	// commitment CUDly could not record (a recommendation carries its ID in
	// UnrecordedCommitmentID), instead of a recovery sweep having interrupted
	// the run (cloud-commitments-go#107).
	UnrecordedCommitment bool
}

// LadderNoticeData is what the scheduled notice of an automatic ladder
// purchase states: no person approved it, so the notice must show what is
// bought and for how long it commits the account.
type LadderNoticeData struct {
	AccountLabel     string
	TrancheID        string
	HourlyCommitment float64 // USD per hour.
	AnnualExposure   float64 // USD per year, hourly commitment x 8760.
	TermYears        int
}

// HourlyText is the hourly commitment at the precision a tranche can carry (5
// decimals), with trailing zeros trimmed to at least cents, so a small
// commitment never renders as $0.00.
func (l *LadderNoticeData) HourlyText() string {
	s := strconv.FormatFloat(l.HourlyCommitment, 'f', 5, 64)
	for strings.HasSuffix(s, "0") && len(s)-strings.Index(s, ".") > 3 {
		s = s[:len(s)-1]
	}
	return s
}

// RecommendationSummary is a simplified recommendation for email display.
type RecommendationSummary struct {
	// UnrecordedCommitmentID is a commitment the provider created but CUDly
	// could not record; the outcome-unknown email names it.
	UnrecordedCommitmentID string
	ResourceType           string
	Engine                 string
	Region                 string
	Payment                string
	AccountLabel           string
	Tenancy                string
	Platform               string
	Service                string
	AvailabilityZone       string
	Term                   int
	// MonthlyCost, Commitment, AvailabilityZone, Platform and Tenancy are the
	// per-row detail of a scheduled plan step. Commitment is upfront plus
	// monthly over the term, so a no-upfront (Azure monthly) row does not read
	// as $0.
	MonthlyCost    float64
	Commitment     float64
	UpfrontCost    float64
	MonthlySavings float64
	Count          int
}

// RIExchangeNotificationData holds data for RI exchange email templates.
type RIExchangeNotificationData struct {
	DashboardURL string
	Mode         string
	TotalPayment string
	// RecipientEmail is the primary (To) inbox for the approval-required flow.
	// Must be set when Exchanges contain ApprovalToken values; leave empty
	// only for completion/broadcast notifications that carry no tokens.
	// When non-empty, SendRIExchangePendingApproval routes through targeted
	// SES (not the SNS broadcast topic). Mirrors NotificationData.RecipientEmail.
	RecipientEmail string
	// RequestedByName is the human-readable display name of the user who
	// triggered the exchange run. Empty falls back to RequestedByEmail.
	RequestedByName string
	// RequestedByEmail is the requester's email address. Empty omits the
	// requested-by block from the approval email.
	RequestedByEmail string
	// RequestedAt is the ISO-8601 / RFC3339 timestamp the exchange was
	// submitted. Empty omits the timestamp from the summary.
	RequestedAt string
	// CancellationWindowNote is short text rendered below the approve/reject
	// buttons. Empty falls back to a generic 6-hour note.
	CancellationWindowNote string
	Exchanges              []RIExchangeItem
	Skipped                []SkippedExchange
	// CCEmails carries additional recipients informed of the pending exchanges
	// but not the authorized approvers. Deduplicated against RecipientEmail.
	CCEmails []string
}

// RIExchangeItem represents a single exchange in an email notification.
type RIExchangeItem struct {
	RecordID           string
	ApprovalToken      string
	SourceRIID         string
	SourceInstanceType string
	TargetInstanceType string
	PaymentDue         string
	ExchangeID         string
	Error              string
	TargetCount        int
	UtilizationPct     float64
	// OutcomeUnknown marks an exchange whose accept was sent but whose
	// response was lost, so AWS may or may not have executed it. Such an
	// exchange is neither completed nor failed: the completed email lists it
	// in its own reconciliation group so the operator verifies it in AWS
	// before retrying. Mirrors exchange.ExchangeOutcome.OutcomeUnknown.
	OutcomeUnknown bool
}

// SkippedExchange represents an exchange that was skipped.
type SkippedExchange struct {
	SourceRIID         string
	SourceInstanceType string
	Reason             string
}
