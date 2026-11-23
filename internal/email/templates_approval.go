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

// purchaseApprovalRequestHTMLTemplate renders the same approval request
// as the plain-text template above with inline-styled CTAs and a richer
// summary table. CSS classes are NOT honored by most email clients
// (Outlook, mobile Gmail) — every visual rule lives in inline `style=""`
// attributes on the elements themselves. Issue #287.
const purchaseApprovalRequestHTMLTemplate = `<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>CUDly - Purchase Approval Required</title></head>
<body style="margin:0;padding:0;background:#f4f6f8;font-family:Arial,Helvetica,sans-serif;color:#1a202c;">
<table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="background:#f4f6f8;padding:24px 0;">
<tr><td align="center">
<table role="presentation" cellpadding="0" cellspacing="0" width="600" style="background:#ffffff;border-radius:8px;box-shadow:0 1px 3px rgba(0,0,0,0.06);">
<tr><td style="padding:32px 32px 16px 32px;">
<h1 style="margin:0;font-size:22px;color:#0f172a;">Purchase Approval Required</h1>
<p style="margin:8px 0 0 0;color:#475569;font-size:14px;">A direct purchase of <strong>{{len .Recommendations}}</strong> commitment(s) has been submitted and requires approval.</p>
</td></tr>

{{if .AuthorizedApprovers}}
<tr><td style="padding:0 32px 16px 32px;">
<div style="background:#fef9c3;border-left:4px solid #facc15;padding:12px 16px;font-size:13px;color:#713f12;border-radius:4px;">
<strong>Authorized approver(s):</strong>
<ul style="margin:6px 0 0 18px;padding:0;">
{{range .AuthorizedApprovers}}<li>{{.}}</li>
{{end}}</ul>
<p style="margin:6px 0 0 0;">Only the inbox(es) above can approve or cancel this purchase. Other recipients are CC'd for visibility — clicking from any other account will fail the authorisation check.</p>
</div>
</td></tr>
{{end}}

<tr><td style="padding:0 32px 8px 32px;">
<table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="border-collapse:collapse;font-size:14px;">
<tr><td style="padding:6px 0;color:#64748b;width:180px;">Total Upfront Cost</td><td style="padding:6px 0;color:#0f172a;font-weight:600;">${{printf "%.2f" .TotalUpfrontCost}}</td></tr>
<tr><td style="padding:6px 0;color:#64748b;">Estimated Monthly Savings</td><td style="padding:6px 0;color:#16a34a;font-weight:700;font-size:18px;">${{printf "%.2f" .TotalSavings}}</td></tr>
{{if .RequestedByEmail}}<tr><td style="padding:6px 0;color:#64748b;">Requested by</td><td style="padding:6px 0;color:#0f172a;">{{if .RequestedByName}}{{.RequestedByName}} &lt;{{.RequestedByEmail}}&gt;{{else}}{{.RequestedByEmail}}{{end}}{{if .RequestedAt}} <span style="color:#94a3b8;">at {{.RequestedAt}}</span>{{end}}</td></tr>
{{end}}
</table>
</td></tr>

<tr><td style="padding:0 32px 16px 32px;">
<h2 style="margin:16px 0 8px 0;font-size:15px;color:#334155;text-transform:uppercase;letter-spacing:0.04em;">Commitments</h2>
<table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="border-collapse:collapse;font-size:13px;border:1px solid #e2e8f0;border-radius:6px;overflow:hidden;">
<thead><tr style="background:#f1f5f9;">
<th align="left" style="padding:10px 12px;color:#475569;font-weight:600;">Service / SKU</th>
<th align="left" style="padding:10px 12px;color:#475569;font-weight:600;">Region</th>
<th align="right" style="padding:10px 12px;color:#475569;font-weight:600;">Term · Payment</th>
<th align="right" style="padding:10px 12px;color:#475569;font-weight:600;">Upfront</th>
<th align="right" style="padding:10px 12px;color:#475569;font-weight:600;">Savings/mo</th>
</tr></thead>
<tbody>
{{range .Recommendations}}<tr style="border-top:1px solid #e2e8f0;">
<td style="padding:10px 12px;color:#0f172a;">{{.Count}}× {{.ResourceType}}{{if .Engine}} ({{.Engine}}){{end}}<div style="color:#94a3b8;font-size:11px;margin-top:2px;">{{.Service}}{{if .AccountLabel}} · {{.AccountLabel}}{{end}}</div></td>
<td style="padding:10px 12px;color:#0f172a;">{{.Region}}</td>
<td align="right" style="padding:10px 12px;color:#475569;">{{if .Term}}{{.Term}}yr{{end}}{{if .Payment}} · {{.Payment}}{{end}}</td>
<td align="right" style="padding:10px 12px;color:#0f172a;">${{printf "%.2f" .UpfrontCost}}</td>
<td align="right" style="padding:10px 12px;color:#16a34a;font-weight:600;">${{printf "%.2f" .MonthlySavings}}</td>
</tr>
{{end}}</tbody></table>
</td></tr>

<tr><td align="center" style="padding:8px 32px 4px 32px;">
<table role="presentation" cellpadding="0" cellspacing="0"><tr>
<td style="padding-right:12px;"><a href="{{.DashboardURL}}/purchases/approve/{{.ExecutionID}}?token={{urlquery .ApprovalToken}}" style="display:inline-block;padding:12px 28px;background:#16a34a;color:#ffffff;text-decoration:none;font-weight:600;font-size:14px;border-radius:6px;">Approve this purchase</a></td>
<td><a href="{{.DashboardURL}}/purchases/cancel/{{.ExecutionID}}?token={{urlquery .ApprovalToken}}" style="display:inline-block;padding:12px 28px;background:#ffffff;color:#dc2626;text-decoration:none;font-weight:600;font-size:14px;border-radius:6px;border:1px solid #dc2626;">Cancel this purchase</a></td>
</tr></table>
</td></tr>

<tr><td style="padding:8px 32px 24px 32px;">
<p style="margin:8px 0 0 0;color:#64748b;font-size:12px;line-height:1.5;">
{{if .CancellationWindowNote}}{{.CancellationWindowNote}}{{else}}Cloud commitments are charged once you approve. Cancellation windows after approval are limited and provider-dependent — see your cloud provider's billing console (e.g. AWS Account &amp; Billing → Refund) for the current policy on the resource type you're approving.{{end}}
</p>
<p style="margin:8px 0 0 0;color:#94a3b8;font-size:11px;">Clicking a link will require you to sign in if you aren't already; the action is then recorded against your logged-in account.</p>
</td></tr>

{{if .ArcheraEducationURL}}
<tr><td style="padding:16px 32px;border-top:1px solid #e2e8f0;color:#334155;font-size:13px;line-height:1.55;">
<h2 style="margin:0 0 8px 0;font-size:14px;color:#0369a1;text-transform:uppercase;letter-spacing:0.04em;">Archera Insurance</h2>
<p style="margin:0 0 10px 0;color:#0f172a;font-weight:500;">Optional commitment-overuse coverage. After approving, the buyer has <strong>7&nbsp;days</strong> from each purchase to enroll.</p>
<p style="margin:6px 0;">Archera Insurance covers the gap if their committed cloud capacity goes unused. Optional, set up on Archera's site, and doesn't affect the commitments submitted here.</p>

<h3 style="margin:14px 0 6px 0;font-size:12px;color:#334155;text-transform:uppercase;letter-spacing:0.04em;">When it makes sense</h3>
<ul style="margin:6px 0 10px 0;padding-left:22px;color:#475569;">
<li style="margin-bottom:4px;">The buyer wants the deepest discount tier (3-year) but isn't sure the workload will still fit in 18 months, or wants to be covered in case usage drops.</li>
<li style="margin-bottom:4px;">They're moving to a new service or region and historical utilization data is thin.</li>
</ul>

<h3 style="margin:10px 0 6px 0;font-size:12px;color:#334155;text-transform:uppercase;letter-spacing:0.04em;">How it works <span style="font-weight:400;color:#64748b;text-transform:none;letter-spacing:0;">(7-day enrollment window from each purchase)</span></h3>
<ol style="margin:6px 0 10px 0;padding-left:22px;color:#475569;">
<li style="margin-bottom:4px;"><strong>Sign up at Archera:</strong> create an account using the link below. The CUDly signup link tells Archera the buyer came from us; CUDly is compensated for the referral, and the link unlocks a dedicated onboarding path.</li>
<li style="margin-bottom:4px;"><strong>Archera starts ingesting cost data:</strong> once access is granted, the insurance policy activates and covers any overcommitment from that point forward.</li>
<li style="margin-bottom:4px;"><strong>Continue purchasing commitments normally through CUDly:</strong> Archera tracks utilization independently and pays out on shortfalls per the policy.</li>
</ol>
<p style="margin:6px 0;">Archera charges an insurance premium for the coverage selected, a separate fee paid to Archera. The cloud commitment purchased through CUDly is unaffected: same price, same billing.</p>

<p style="margin:12px 0 4px;"><a href="` + common.ArcheraSignupURL + `" style="display:inline-block;padding:10px 22px;background:#1a73e8;color:#ffffff;text-decoration:none;font-weight:600;font-size:13px;border-radius:6px;">Sign up at Archera &rarr;</a></p>

<p style="color:#64748b;font-size:12px;font-style:italic;border-top:1px dashed #e2e8f0;padding-top:10px;margin-top:12px;"><strong>Full disclosure:</strong> Archera sponsors CUDly's development with a share of their insurance revenue; we surface the option because we think it's useful, but you should know about the financial relationship. Insurance terms, coverage, and pricing are set entirely by Archera. CUDly has no visibility into the buyer's Archera account or policy.</p>

<p style="color:#64748b;font-size:11px;margin-top:8px;">Permalink: <a href="{{.ArcheraEducationURL}}" style="color:#0369a1;">{{.ArcheraEducationURL}}</a></p>
</td></tr>
{{end}}

<tr><td style="padding:16px 32px;background:#f8fafc;border-top:1px solid #e2e8f0;border-radius:0 0 8px 8px;">
<p style="margin:0;color:#94a3b8;font-size:11px;">This is an automated message from CUDly. Do not share these links.</p>
</td></tr>

</table>
</td></tr></table>
</body></html>`

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
