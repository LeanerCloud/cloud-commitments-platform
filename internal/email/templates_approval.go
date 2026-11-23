package email

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

const purchaseApprovalRequestTemplate = `CUDly - Purchase Approval Required
====================================

A direct purchase of {{len .Recommendations}} commitment(s) has been submitted and requires approval.
{{if .AuthorizedApprovers}}
Authorized approver(s):
{{range .AuthorizedApprovers}}  - {{.}}
{{end}}
Only the inbox(es) listed above can approve or cancel this purchase.
Other recipients are CC'd for visibility only — clicking the links
below from any other account will fail the authorisation check.
{{end}}
Summary:
--------
Total Upfront Cost: ${{printf "%.2f" .TotalUpfrontCost}}
Estimated Monthly Savings: ${{printf "%.2f" .TotalSavings}}
{{if .RequestedByEmail}}
Requested by: {{if .RequestedByName}}{{.RequestedByName}} <{{.RequestedByEmail}}>{{else}}{{.RequestedByEmail}}{{end}}{{if .RequestedAt}} at {{.RequestedAt}}{{end}}
{{end}}
Commitments:
{{range .Recommendations}}
- {{.Count}}x {{.ResourceType}}{{if .Engine}} ({{.Engine}}){{end}} in {{.Region}}
  Service: {{.Service}}{{if .AccountLabel}} | Account: {{.AccountLabel}}{{end}}{{if .Term}} | Term: {{.Term}}yr{{end}}{{if .Payment}} | Payment: {{.Payment}}{{end}}
  Upfront: ${{printf "%.2f" .UpfrontCost}} | Est. Savings: ${{printf "%.2f" .MonthlySavings}}/month
{{end}}
Approve: {{.DashboardURL}}/purchases/approve/{{.ExecutionID}}?token={{urlquery .ApprovalToken}}

Cancel:  {{.DashboardURL}}/purchases/cancel/{{.ExecutionID}}?token={{urlquery .ApprovalToken}}
{{if .CancellationWindowNote}}
{{.CancellationWindowNote}}
{{else}}
Note: cloud commitments are charged to your account once you approve.
Cancellation windows after approval are limited and provider-dependent —
see your cloud provider's billing console (e.g. AWS Account & Billing →
Refund) for the current policy on the resource type you're approving.
{{end}}
Clicking a link will require you to sign in if you aren't already; the
action is then recorded against your logged-in account.
{{if .ArcheraEducationURL}}
---
ARCHERA INSURANCE (optional, 7-day enrollment window from each purchase)

After approving, the buyer has 7 days from each purchase to enroll for
commitment-overuse coverage. Archera Insurance covers the gap if their
committed cloud capacity goes unused. Optional, set up on Archera's site,
and doesn't affect the commitments submitted here.

When it makes sense:
  - The buyer wants the deepest discount tier (3-year) but isn't sure
    the workload will still fit in 18 months, or wants to be covered in
    case usage drops.
  - They're moving to a new service or region and historical utilization
    data is thin.

How it works (within the 7-day enrollment window from each purchase):
  1. Sign up at Archera: create an account using the link below. The
     CUDly signup link tells Archera the buyer came from us; CUDly is
     compensated for the referral, and the link unlocks a dedicated
     onboarding path.
  2. Archera starts ingesting cost data: once access is granted, the
     insurance policy activates and covers any overcommitment from that
     point forward.
  3. Continue purchasing commitments normally through CUDly: Archera
     tracks utilization independently and pays out on shortfalls per
     the policy.

Archera charges an insurance premium for the coverage selected, a
separate fee paid to Archera. The cloud commitment purchased through
CUDly is unaffected: same price, same billing.

Full disclosure: Archera sponsors CUDly's development with a share of
their insurance revenue; we surface the option because we think it's
useful, but you should know about the financial relationship. Insurance
terms, coverage, and pricing are set entirely by Archera. CUDly has no
visibility into the buyer's Archera account or policy.

Sign up:   ` + common.ArcheraSignupURL + `
Permalink: {{.ArcheraEducationURL}}
{{end}}
This is an automated message from CUDly. Do not share these links.
`

// sendMultipartVia is the generic dual-render send helper used by the
// invite and password-reset flows. The two render closures are
// invoked back-to-back; an HTML render failure is non-fatal and degrades
// to text-only delivery (matching sendPurchaseApprovalRequestVia's contract).
// kind is a short label like "user-invite" used in the warn log on degrade.
// Issue #355.
func sendMultipartVia(
	ctx context.Context,
	s SenderInterface,
	recipient, subject, kind string,
	renderText, renderHTML func() (string, error),
) error {
	textBody, err := renderText()
	if err != nil {
		return fmt.Errorf("failed to render %s email (text): %w", kind, err)
	}
	htmlBody, htmlErr := renderHTML()
	if htmlErr != nil {
		logging.Warnf("email: HTML %s render failed, falling back to text-only: %v", kind, htmlErr)
		htmlBody = ""
	}
	return s.SendToEmailWithCCMultipart(ctx, recipient, nil, subject, textBody, htmlBody)
}

// SendPurchaseApprovalRequest sends an email asking the user to approve a direct
// purchase. Routes through SES SendEmail (not the SNS alerts topic) because the
// approval URL carries a one-time token scoped to the submitter — broadcasting
// that to every SNS subscriber would leak the authorisation. Returns
// ErrNoRecipient when data.RecipientEmail is empty and ErrNoFromEmail when
// FROM_EMAIL is unconfigured, so the caller can surface a precise reason in
// the API response instead of the prior silent no-op.
//
// Mute check: if the recipient has opted out of purchase_approvals via the
// List-Unsubscribe link, the email is silently skipped and nil is returned.
// A List-Unsubscribe / List-Unsubscribe-Post header pair (RFC 8058) is added
// to the outbound SES message when an unsubscribe base URL is configured.
func (s *Sender) SendPurchaseApprovalRequest(ctx context.Context, data NotificationData) error {
	if data.RecipientEmail == "" {
		return ErrNoRecipient
	}
	// Both empty and malformed FROM_EMAIL (e.g. "noreply@" when the
	// subdomain_zone_name tfvar is unset) map to ErrNoFromEmail so the
	// handler can report "FROM_EMAIL not configured" — the prior behavior
	// handed the bad string to SES and surfaced a BadRequestException stack
	// trace ("Missing domain") to the user.
	if !isValidFromEmail(s.fromEmail) {
		return ErrNoFromEmail
	}

	scope := string(common.ScopePurchaseApprovals)

	filteredCC, unsubHdr, postHdr, muted := prepareMuteAwareDelivery(
		ctx, s.muteChecker, s.unsubscribeBaseURL, data.RecipientEmail, data.CCEmails, scope,
	)
	if muted {
		logging.Infof("email: purchase approval skipped for muted recipient (scope=%s)", scope)
		return nil
	}
	extraHeaders := addListUnsubscribeHeaders(unsubHdr, postHdr)

	subject := fmt.Sprintf("CUDly - Purchase Approval Required (%d commitment(s))", len(data.Recommendations))
	return sendPurchaseApprovalRequestWithCC(ctx, s, data.RecipientEmail, filteredCC, subject, data, extraHeaders)
}

// sendPurchaseApprovalRequestWithCC is the low-level send helper that accepts
// a pre-filtered CC list and extra message headers. Extracted from
// sendPurchaseApprovalRequestVia so the mute/unsub path can inject headers
// without duplicating the render logic.
func sendPurchaseApprovalRequestWithCC(
	ctx context.Context,
	s *Sender,
	recipient string,
	ccEmails []string,
	subject string,
	data NotificationData,
	extraHeaders []types.MessageHeader,
) error {
	textBody, err := RenderPurchaseApprovalRequestEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render purchase approval request email (text): %w", err)
	}
	htmlBody, htmlErr := RenderPurchaseApprovalRequestEmailHTML(data)
	if htmlErr != nil {
		logging.Warnf("email: HTML approval-request render failed, falling back to text-only: %v", htmlErr)
		htmlBody = ""
	}
	return s.sendToEmailWithCCMultipartHeaders(ctx, recipient, ccEmails, subject, textBody, htmlBody, extraHeaders)
}
