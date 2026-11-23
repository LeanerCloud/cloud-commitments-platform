package email

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
)

// ---------------------------------------------------------------------------
// Post-execution notification templates (issue #291)
// ---------------------------------------------------------------------------

// purchaseExecutedNotificationTemplate is the plain-text half of the
// post-execution notification email. Rendered alongside
// purchaseExecutedNotificationHTMLTemplate for multipart/alternative delivery.
const purchaseExecutedNotificationTemplate = `[CUDly] Purchase {{if .PartialPurchase}}partially {{end}}executed{{if .Recommendations}} ({{len .Recommendations}} commitment(s)){{end}}
=============================================================================
{{if .PartialPurchase}}
Not every commitment in this purchase went through. See the execution in the
dashboard for which ones failed.
{{end}}{{if .RequestedByEmail}}
Requested by: {{if .RequestedByName}}{{.RequestedByName}} <{{.RequestedByEmail}}>{{else}}{{.RequestedByEmail}}{{end}}{{if .RequestedAt}} at {{.RequestedAt}}{{end}}
{{end}}{{if .ExecutedBy}}
Executed by:  {{.ExecutedBy}}{{if .ExecutedAt}} at {{.ExecutedAt}}{{end}}
{{end}}
Summary:
--------
Total Upfront Cost:       ${{printf "%.2f" .TotalUpfrontCost}}
Estimated Monthly Savings: ${{printf "%.2f" .TotalSavings}}

Commitments:
{{range .Recommendations}}
- {{.Count}}x {{.ResourceType}}{{if .Engine}} ({{.Engine}}){{end}} in {{.Region}}
  Service: {{.Service}}{{if .AccountLabel}} | Account: {{.AccountLabel}}{{end}}{{if .Term}} | Term: {{.Term}}yr{{end}}{{if .Payment}} | Payment: {{.Payment}}{{end}}
  Upfront: ${{printf "%.2f" .UpfrontCost}} | Est. Savings: ${{printf "%.2f" .MonthlySavings}}/month
{{end}}
{{if .RevocationToken}}
------------------------------------------------------------
REVOCATION WINDOW
{{if .RevocationWindowClosesAt}}You can revoke this purchase until {{.RevocationWindowClosesAt}}.
After that, contact AWS Support.
{{end}}
One-click revoke:
{{.DashboardURL}}/purchases/revoke/{{.ExecutionID}}?token={{urlquery .RevocationToken}}

Plain-text URL (copy + paste if the link above is broken):
{{.DashboardURL}}/purchases/revoke/{{.ExecutionID}}?token={{urlquery .RevocationToken}}
{{end}}
View in dashboard:
{{.DashboardURL}}/purchases#history?execution={{.ExecutionID}}

This is an automated message from CUDly.
`

// purchaseExecutedNotificationHTMLTemplate is the HTML half of the
// post-execution notification email. Inline-styled per email-client constraints
// (Outlook, mobile Gmail ignore class-based CSS). Issue #291.
const purchaseExecutedNotificationHTMLTemplate = `<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>[CUDly] Purchase executed</title></head>
<body style="margin:0;padding:0;background:#f4f6f8;font-family:Arial,Helvetica,sans-serif;color:#1a202c;">
<table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="background:#f4f6f8;padding:24px 0;">
<tr><td align="center">
<table role="presentation" cellpadding="0" cellspacing="0" width="600" style="background:#ffffff;border-radius:8px;box-shadow:0 1px 3px rgba(0,0,0,0.06);">
<tr><td style="padding:32px 32px 16px 32px;">
<h1 style="margin:0;font-size:22px;color:#0f172a;">Purchase {{if .PartialPurchase}}Partially {{end}}Executed</h1>
{{if .PartialPurchase}}<p style="margin:8px 0 0 0;color:#b45309;font-size:14px;">Not every commitment in this purchase went through. See the execution in the dashboard for which ones failed.</p>{{else}}<p style="margin:8px 0 0 0;color:#475569;font-size:14px;"><strong>{{len .Recommendations}}</strong> commitment(s) were purchased successfully.</p>{{end}}
</td></tr>

<tr><td style="padding:0 32px 8px 32px;">
<table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="border-collapse:collapse;font-size:14px;">
<tr><td style="padding:6px 0;color:#64748b;width:180px;">Total Upfront Cost</td><td style="padding:6px 0;color:#0f172a;font-weight:600;">${{printf "%.2f" .TotalUpfrontCost}}</td></tr>
<tr><td style="padding:6px 0;color:#64748b;">Estimated Monthly Savings</td><td style="padding:6px 0;color:#16a34a;font-weight:700;font-size:18px;">${{printf "%.2f" .TotalSavings}}</td></tr>
{{if .RequestedByEmail}}<tr><td style="padding:6px 0;color:#64748b;">Requested by</td><td style="padding:6px 0;color:#0f172a;">{{if .RequestedByName}}{{.RequestedByName}} &lt;{{.RequestedByEmail}}&gt;{{else}}{{.RequestedByEmail}}{{end}}{{if .RequestedAt}} <span style="color:#94a3b8;">at {{.RequestedAt}}</span>{{end}}</td></tr>{{end}}
{{if .ExecutedBy}}<tr><td style="padding:6px 0;color:#64748b;">Executed by</td><td style="padding:6px 0;color:#0f172a;">{{.ExecutedBy}}{{if .ExecutedAt}} <span style="color:#94a3b8;">at {{.ExecutedAt}}</span>{{end}}</td></tr>{{end}}
</table>
</td></tr>

<tr><td style="padding:0 32px 16px 32px;">
<h2 style="margin:16px 0 8px 0;font-size:15px;color:#334155;text-transform:uppercase;letter-spacing:0.04em;">Commitments</h2>
<table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="border-collapse:collapse;font-size:13px;border:1px solid #e2e8f0;border-radius:6px;overflow:hidden;">
<thead><tr style="background:#f1f5f9;">
<th align="left" style="padding:10px 12px;color:#475569;font-weight:600;">Service / SKU</th>
<th align="left" style="padding:10px 12px;color:#475569;font-weight:600;">Region</th>
<th align="right" style="padding:10px 12px;color:#475569;font-weight:600;">Term &middot; Payment</th>
<th align="right" style="padding:10px 12px;color:#475569;font-weight:600;">Upfront</th>
<th align="right" style="padding:10px 12px;color:#475569;font-weight:600;">Savings/mo</th>
</tr></thead>
<tbody>
{{range .Recommendations}}<tr style="border-top:1px solid #e2e8f0;">
<td style="padding:10px 12px;color:#0f172a;">{{.Count}}&times; {{.ResourceType}}{{if .Engine}} ({{.Engine}}){{end}}<div style="color:#94a3b8;font-size:11px;margin-top:2px;">{{.Service}}{{if .AccountLabel}} &middot; {{.AccountLabel}}{{end}}</div></td>
<td style="padding:10px 12px;color:#0f172a;">{{.Region}}</td>
<td align="right" style="padding:10px 12px;color:#475569;">{{if .Term}}{{.Term}}yr{{end}}{{if .Payment}} &middot; {{.Payment}}{{end}}</td>
<td align="right" style="padding:10px 12px;color:#0f172a;">${{printf "%.2f" .UpfrontCost}}</td>
<td align="right" style="padding:10px 12px;color:#16a34a;font-weight:600;">${{printf "%.2f" .MonthlySavings}}</td>
</tr>
{{end}}</tbody></table>
</td></tr>

{{if .RevocationToken}}
<tr><td style="padding:0 32px 24px 32px;border-top:1px solid #e2e8f0;">
<h2 style="margin:16px 0 8px 0;font-size:15px;color:#b45309;text-transform:uppercase;letter-spacing:0.04em;">Revocation Window</h2>
{{if .RevocationWindowClosesAt}}<p style="margin:0 0 12px 0;color:#475569;font-size:13px;">You can revoke this purchase until <strong>{{.RevocationWindowClosesAt}}</strong>. After that, contact AWS Support.</p>{{end}}
<table role="presentation" cellpadding="0" cellspacing="0"><tr>
<td><a href="{{.DashboardURL}}/purchases/revoke/{{.ExecutionID}}?token={{urlquery .RevocationToken}}" style="display:inline-block;padding:12px 28px;background:#dc2626;color:#ffffff;text-decoration:none;font-weight:600;font-size:14px;border-radius:6px;">Revoke this purchase</a></td>
</tr></table>
<p style="margin:10px 0 0 0;color:#64748b;font-size:11px;word-break:break-all;">If the button does not work, copy and paste this URL: <a href="{{.DashboardURL}}/purchases/revoke/{{.ExecutionID}}?token={{urlquery .RevocationToken}}" style="color:#2563eb;text-decoration:underline;">{{.DashboardURL}}/purchases/revoke/{{.ExecutionID}}?token={{urlquery .RevocationToken}}</a></p>
</td></tr>
{{end}}

<tr><td style="padding:16px 32px;background:#f8fafc;border-top:1px solid #e2e8f0;border-radius:0 0 8px 8px;">
<p style="margin:0;color:#94a3b8;font-size:11px;">This is an automated message from CUDly.</p>
</td></tr>

</table>
</td></tr></table>
</body></html>`

// RenderPurchaseExecutedNotificationEmail renders the plain-text half of
// the post-execution notification email (issue #291).
func RenderPurchaseExecutedNotificationEmail(data NotificationData) (string, error) {
	return renderTextTemplate("purchase-executed-notification", purchaseExecutedNotificationTemplate, data)
}

// RenderPurchaseExecutedNotificationEmailHTML renders the HTML half of the
// post-execution notification email. Pair with
// RenderPurchaseExecutedNotificationEmail for multipart/alternative delivery.
func RenderPurchaseExecutedNotificationEmailHTML(data NotificationData) (string, error) {
	return renderTemplate("purchase-executed-notification-html", purchaseExecutedNotificationHTMLTemplate, data)
}

// sendPurchaseExecutedNotificationVia composes the plain-text + HTML bodies
// and ships them through s.SendToEmailWithCCMultipart. HTML render failures
// are non-fatal and degrade to single-part text. Shared by Sender and
// SMTPSender so the two transports stay in sync (same pattern as
// sendPurchaseApprovalRequestVia). Issue #291.
func sendPurchaseExecutedNotificationVia(ctx context.Context, s SenderInterface, recipient, subject string, data NotificationData) error {
	textBody, err := RenderPurchaseExecutedNotificationEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render purchase executed notification (text): %w", err)
	}
	// HTML render failure is non-fatal: degrade to single-part text.
	htmlBody, htmlErr := RenderPurchaseExecutedNotificationEmailHTML(data)
	if htmlErr != nil {
		logging.Warnf("email: HTML executed-notification render failed, falling back to text-only: %v", htmlErr)
		htmlBody = ""
	}
	return s.SendToEmailWithCCMultipart(ctx, recipient, data.CCEmails, subject, textBody, htmlBody)
}

// SendPurchaseExecutedNotification sends the post-execution notification email
// to the configured recipients (global notification_email, per-account contact
// emails, and the requester). data.RecipientEmail must be set to the primary To
// address; data.CCEmails carries additional recipients. data.RevocationToken
// and data.RevocationWindowClosesAt control the revocation-link panel in the
// template. Issue #291.
func (s *Sender) SendPurchaseExecutedNotification(ctx context.Context, data NotificationData) error {
	if data.RecipientEmail == "" {
		return ErrNoRecipient
	}
	if !isValidFromEmail(s.fromEmail) {
		return ErrNoFromEmail
	}
	subject := buildExecutedNotificationSubject(data)
	return sendPurchaseExecutedNotificationVia(ctx, s, data.RecipientEmail, subject, data)
}

// buildExecutedNotificationSubject constructs the subject line for the
// post-execution notification, including a brief SKU summary when the
// recommendation list is small enough to fit. Extracted so both Sender
// and SMTPSender use the same subject format.
func buildExecutedNotificationSubject(data NotificationData) string {
	verb := "executed"
	if data.PartialPurchase {
		verb = "partially executed"
	}
	if len(data.Recommendations) == 1 {
		r := data.Recommendations[0]
		return fmt.Sprintf("[CUDly] Purchase %s: %s %s in %s",
			verb, r.Service, r.ResourceType, r.Region)
	}
	return fmt.Sprintf("[CUDly] Purchase %s (%d commitment(s))", verb, len(data.Recommendations))
}
