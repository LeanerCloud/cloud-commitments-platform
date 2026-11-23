package email

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
)

const riExchangePendingApprovalTemplate = `CUDly - RI Exchange Approval Required
======================================

CUDly has identified convertible RI exchanges that need your approval.
{{if .RequestedByEmail}}
Requested by: {{if .RequestedByName}}{{.RequestedByName}} <{{.RequestedByEmail}}>{{else}}{{.RequestedByEmail}}{{end}}{{if .RequestedAt}} at {{.RequestedAt}}{{end}}
{{end}}
Proposed Exchanges:
{{range .Exchanges}}
- Source: {{.SourceRIID}} ({{.SourceInstanceType}}, {{printf "%.1f" .UtilizationPct}}% utilized)
  Target: {{.TargetInstanceType}} x{{.TargetCount}}
  Payment Due: ${{.PaymentDue}}
  Approve: {{$.DashboardURL}}/inventory/ri-exchange/approve/{{.RecordID}}?token={{urlquery .ApprovalToken}}
  Reject:  {{$.DashboardURL}}/inventory/ri-exchange/reject/{{.RecordID}}?token={{urlquery .ApprovalToken}}
{{end}}
Total Payment: ${{.TotalPayment}}
{{if .Skipped}}
Skipped (could not process):
{{range .Skipped}}
- {{.SourceRIID}} ({{.SourceInstanceType}}): {{.Reason}}
{{end}}{{end}}
{{if .CancellationWindowNote}}{{.CancellationWindowNote}}{{else}}Please approve within 6 hours (before the next analysis run).{{end}}

Review exchange history:
{{.DashboardURL}}/inventory/ri-exchange

This is an automated message from CUDly. Do not share these links.
`

// riExchangePendingApprovalHTMLTemplate renders the same approval request as
// the plain-text template above with inline-styled approve/reject CTAs and an
// exchange summary table. CSS classes are NOT honored by most email clients
// (Outlook, mobile Gmail) -- every visual rule lives in inline style=""
// attributes. Mirrors purchaseApprovalRequestHTMLTemplate. Issue #296.
const riExchangePendingApprovalHTMLTemplate = `<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>CUDly - RI Exchange Approval Required</title></head>
<body style="margin:0;padding:0;background:#f4f6f8;font-family:Arial,Helvetica,sans-serif;color:#1a202c;">
<table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="background:#f4f6f8;padding:24px 0;">
<tr><td align="center">
<table role="presentation" cellpadding="0" cellspacing="0" width="600" style="background:#ffffff;border-radius:8px;box-shadow:0 1px 3px rgba(0,0,0,0.06);">
<tr><td style="padding:32px 32px 16px 32px;">
<h1 style="margin:0;font-size:22px;color:#0f172a;">RI Exchange Approval Required</h1>
<p style="margin:8px 0 0 0;color:#475569;font-size:14px;">CUDly has identified <strong>{{len .Exchanges}}</strong> convertible RI exchange(s) that need your approval.</p>
</td></tr>

<tr><td style="padding:0 32px 8px 32px;">
<table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="border-collapse:collapse;font-size:14px;">
<tr><td style="padding:6px 0;color:#64748b;width:180px;">Total Payment</td><td style="padding:6px 0;color:#0f172a;font-weight:600;">${{.TotalPayment}}</td></tr>
{{if .RequestedByEmail}}<tr><td style="padding:6px 0;color:#64748b;">Requested by</td><td style="padding:6px 0;color:#0f172a;">{{if .RequestedByName}}{{.RequestedByName}} &lt;{{.RequestedByEmail}}&gt;{{else}}{{.RequestedByEmail}}{{end}}{{if .RequestedAt}} <span style="color:#94a3b8;">at {{.RequestedAt}}</span>{{end}}</td></tr>
{{end}}
</table>
</td></tr>

<tr><td style="padding:0 32px 16px 32px;">
<h2 style="margin:16px 0 8px 0;font-size:15px;color:#334155;text-transform:uppercase;letter-spacing:0.04em;">Proposed Exchanges</h2>
<table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="border-collapse:collapse;font-size:13px;border:1px solid #e2e8f0;border-radius:6px;overflow:hidden;">
<thead><tr style="background:#f1f5f9;">
<th align="left" style="padding:10px 12px;color:#475569;font-weight:600;">Source RI</th>
<th align="left" style="padding:10px 12px;color:#475569;font-weight:600;">Target</th>
<th align="right" style="padding:10px 12px;color:#475569;font-weight:600;">Utilization</th>
<th align="right" style="padding:10px 12px;color:#475569;font-weight:600;">Payment Due</th>
<th align="left" style="padding:10px 12px;color:#475569;font-weight:600;">Actions</th>
</tr></thead>
<tbody>
{{range .Exchanges}}<tr style="border-top:1px solid #e2e8f0;">
<td style="padding:10px 12px;color:#0f172a;"><div>{{.SourceInstanceType}}</div><div style="color:#94a3b8;font-size:11px;margin-top:2px;">{{.SourceRIID}}</div></td>
<td style="padding:10px 12px;color:#0f172a;">{{.TargetInstanceType}} x{{.TargetCount}}</td>
<td align="right" style="padding:10px 12px;color:#475569;">{{printf "%.1f" .UtilizationPct}}%</td>
<td align="right" style="padding:10px 12px;color:#0f172a;font-weight:600;">${{.PaymentDue}}</td>
<td style="padding:10px 12px;">
<a href="{{$.DashboardURL}}/inventory/ri-exchange/approve/{{.RecordID}}?token={{urlquery .ApprovalToken}}" style="display:inline-block;padding:6px 14px;background:#16a34a;color:#ffffff;text-decoration:none;font-weight:600;font-size:12px;border-radius:4px;margin-right:4px;">Approve</a>
<a href="{{$.DashboardURL}}/inventory/ri-exchange/reject/{{.RecordID}}?token={{urlquery .ApprovalToken}}" style="display:inline-block;padding:6px 14px;background:#ffffff;color:#dc2626;text-decoration:none;font-weight:600;font-size:12px;border-radius:4px;border:1px solid #dc2626;">Reject</a>
</td>
</tr>
{{end}}</tbody></table>
</td></tr>

{{if .Skipped}}
<tr><td style="padding:0 32px 16px 32px;">
<h2 style="margin:8px 0 8px 0;font-size:13px;color:#94a3b8;text-transform:uppercase;letter-spacing:0.04em;">Skipped (could not process)</h2>
<ul style="margin:0;padding-left:20px;color:#475569;font-size:13px;">
{{range .Skipped}}<li style="margin-bottom:4px;">{{.SourceRIID}} ({{.SourceInstanceType}}): {{.Reason}}</li>
{{end}}</ul>
</td></tr>
{{end}}

<tr><td style="padding:8px 32px 24px 32px;">
<p style="margin:8px 0 0 0;color:#64748b;font-size:12px;line-height:1.5;">
{{if .CancellationWindowNote}}{{.CancellationWindowNote}}{{else}}Please approve within 6 hours (before the next analysis run).{{end}}
</p>
<p style="margin:8px 0 0 0;color:#94a3b8;font-size:11px;">Clicking a link will require you to sign in if you aren't already; the action is then recorded against your logged-in account.</p>
</td></tr>

<tr><td style="padding:16px 32px;background:#f8fafc;border-top:1px solid #e2e8f0;border-radius:0 0 8px 8px;">
<p style="margin:0;color:#94a3b8;font-size:11px;">This is an automated message from CUDly. Do not share these links. <a href="{{.DashboardURL}}/inventory/ri-exchange" style="color:#64748b;">View exchange history</a></p>
</td></tr>

</table>
</td></tr></table>
</body></html>`

const riExchangeCompletedTemplate = `CUDly - RI Exchanges Completed
==============================
{{with .ReconciliationExchanges}}
NEEDS RECONCILIATION - verify in AWS before acting
--------------------------------------------------

CUDly sent the exchange(s) below to AWS but got no definite answer, so AWS
may or may not have executed them. They are not failures: do not retry them
until they are reconciled. The run stopped at the first one, and each one
keeps counting against the daily exchange cap until it is resolved. Check the
source RI in the AWS console, then record what happened with the resolve
request shown for each exchange (the API accepts it once the record has been
unchanged for a few hours).
{{range .}}
- Source: {{.SourceRIID}} ({{.SourceInstanceType}})
  Target: {{.TargetInstanceType}} x{{.TargetCount}}
  Quoted payment: ${{.PaymentDue}}
  Record: {{.RecordID}}
  Resolve: POST /api/ri-exchange/resolve/{{.RecordID}}
  Detail: {{.Error}}
{{end}}{{end}}
The following RI exchanges have been {{if eq .Mode "auto"}}automatically {{end}}completed:

{{range .Exchanges}}{{if and (eq .Error "") (not .OutcomeUnknown)}}
- Source: {{.SourceRIID}} ({{.SourceInstanceType}})
  Target: {{.TargetInstanceType}} x{{.TargetCount}}
  Payment: ${{.PaymentDue}}
  Exchange ID: {{.ExchangeID}}
{{end}}{{end}}
Total Payment: ${{.TotalPayment}}
{{if .Skipped}}
Skipped (could not process):
{{range .Skipped}}
- {{.SourceRIID}} ({{.SourceInstanceType}}): {{.Reason}}
{{end}}{{end}}
View exchange history:
{{.DashboardURL}}/inventory/ri-exchange

This is an automated message from CUDly.
`

// renderRIExchangePendingApproval composes the plain-text + HTML approval
// bodies. HTML render failures are non-fatal and degrade to single-part text.
// Shared by the SES and SMTP delivery paths.
func renderRIExchangePendingApproval(data RIExchangeNotificationData) (textBody, htmlBody string, err error) {
	textBody, err = RenderRIExchangePendingApprovalEmail(data)
	if err != nil {
		return "", "", fmt.Errorf("failed to render ri exchange pending approval email (text): %w", err)
	}
	htmlBody, htmlErr := RenderRIExchangePendingApprovalEmailHTML(data)
	if htmlErr != nil {
		logging.Warnf("email: HTML ri-exchange-pending render failed, falling back to text-only: %v", htmlErr)
		htmlBody = ""
	}
	return textBody, htmlBody, nil
}

// SendRIExchangePendingApproval sends an email with RI exchange approval links
// as multipart/alternative (plain-text + styled HTML). The rendered body
// contains per-exchange approve/reject links that carry live tokens; any
// subscriber of the SNS topic who received this body could approve spend they
// were never authorized for. This method therefore routes through targeted SES
// (SendToEmailWithCCMultipart), mirroring the hardened path used by
// SendPurchaseApprovalRequest, and never falls back to the SNS broadcast topic.
//
// Returns ErrNoRecipient when data.RecipientEmail is empty. Callers must
// resolve a recipient (e.g. the global notification email from GlobalConfig)
// before invoking this method. Issue #296.
func (s *Sender) SendRIExchangePendingApproval(ctx context.Context, data RIExchangeNotificationData) error {
	if data.RecipientEmail == "" {
		return ErrNoRecipient
	}
	scope := string(common.ScopeRIExchangeApprovals)
	filteredCC, unsubHdr, postHdr, muted := prepareMuteAwareDelivery(
		ctx, s.muteChecker, s.unsubscribeBaseURL, data.RecipientEmail, data.CCEmails, scope,
	)
	if muted {
		logging.Infof("email: RI exchange approval skipped for muted recipient (scope=%s)", scope)
		return nil
	}
	textBody, htmlBody, err := renderRIExchangePendingApproval(data)
	if err != nil {
		return err
	}
	subject := fmt.Sprintf("CUDly - RI Exchange Approval Required (%d exchanges)", len(data.Exchanges))
	extraHeaders := addListUnsubscribeHeaders(unsubHdr, postHdr)
	return s.sendToEmailWithCCMultipartHeaders(ctx, data.RecipientEmail, filteredCC, subject, textBody, htmlBody, extraHeaders)
}

// SendRIExchangeCompleted sends a notification about completed RI exchanges.
func (s *Sender) SendRIExchangeCompleted(ctx context.Context, data RIExchangeNotificationData) error {
	body, err := RenderRIExchangeCompletedEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render ri exchange completed email: %w", err)
	}

	return s.SendNotification(ctx, riExchangeCompletedSubject(data), body)
}
