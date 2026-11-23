package email

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
)

// Email templates
//
// The Archera signup link in the purchase confirmation and approval request
// templates is spliced in from common.ArcheraSignupURL, the constant the CLI
// also uses, so every surface sends buyers to the same attribution URL. The
// frontend keeps its own copy in frontend/src/archera.ts (ARCHERA_SIGNUP_URL).

const newRecommendationsTemplate = `CUDly - New Commitment Recommendations Available
================================================

We've identified potential savings across your cloud accounts.

Summary:
--------
Estimated Monthly Savings: ${{printf "%.2f" .TotalSavings}}
{{if gt .TotalUpfrontCost 0.0}}Total Upfront Cost: ${{printf "%.2f" .TotalUpfrontCost}}{{end}}

Top Recommendations:
{{range .Recommendations}}
- {{.Count}}x {{.ResourceType}}{{if .Engine}} ({{.Engine}}){{end}} in {{.Region}}
  Service: {{.Service}} | Est. Savings: ${{printf "%.2f" .MonthlySavings}}/month
{{end}}

Review and configure your purchases:
{{.DashboardURL}}

This is an automated message from CUDly.
`

const scheduledPurchaseTemplate = `CUDly - Scheduled Purchase in {{.DaysUntilPurchase}} Days
=========================================

Based on your Purchase Plan "{{.PlanName}}", the following commitments {{if .NeedsApproval}}are due on{{else}}will be purchased on{{end}} {{.PurchaseDate}}:
{{if gt .TotalCommitment 0.0}}
Will buy {{len .Recommendations}} recommendation(s) ({{.TotalUnits}} units) for ${{printf "%.2f" .TotalCommitment}} total commitment (upfront ${{printf "%.2f" .TotalUpfrontCost}} + ${{printf "%.2f" .TotalRecurring}} recurring over the terms){{if gt .StepCap 0.0}}, cap ${{printf "%.2f" .StepCap}}{{end}}, {{if .NeedsApproval}}due on {{.PurchaseDate}}. It is bought once approved. Review & Edit below and approve it in the approval queue.{{else}}on {{.PurchaseDate}} unless canceled.{{end}}
{{end}}
{{range .Recommendations}}
- {{.Count}}x {{.ResourceType}}{{if .Engine}} ({{.Engine}}){{end}} in {{.Region}}{{if .AvailabilityZone}} ({{.AvailabilityZone}}){{end}}{{if .Platform}} | {{.Platform}}{{end}}{{if .Tenancy}} | {{.Tenancy}}{{end}}
  Service: {{.Service}} | Est. Savings: ${{printf "%.2f" .MonthlySavings}}/month{{if gt .Commitment 0.0}}
  Term: {{.Term}} yr {{.Payment}} | Upfront ${{printf "%.2f" .UpfrontCost}} + ${{printf "%.2f" .MonthlyCost}}/month | Commitment ${{printf "%.2f" .Commitment}}{{end}}
{{end}}

Summary:
--------
Estimated Monthly Savings: ${{printf "%.2f" .TotalSavings}}
{{if gt .TotalUpfrontCost 0.0}}Total Upfront Cost: ${{printf "%.2f" .TotalUpfrontCost}}{{end}}

Actions:
--------
[Review & Edit] {{.DashboardURL}}/purchases#history?execution={{.ExecutionID}}

[Pause Plan] {{.DashboardURL}}/plans?plan={{.PlanID}}

[Cancel This Purchase] {{.DashboardURL}}/purchases/cancel/{{.ExecutionID}}?token={{urlquery .ApprovalToken}}

{{if .NeedsApproval}}This purchase is not bought automatically. It needs a human approval in the approval queue; you have {{.DaysUntilPurchase}} days to review, modify or cancel it.{{else}}You have {{.DaysUntilPurchase}} days to modify or cancel before automatic execution.{{end}}

This is an automated message from CUDly.
`

const purchaseConfirmationTemplate = `CUDly - Purchases Completed Successfully
========================================

Your commitment purchases have been completed:

{{range .Recommendations}}
- {{.Count}}x {{.ResourceType}}{{if .Engine}} ({{.Engine}}){{end}} in {{.Region}}
  Service: {{.Service}} | Est. Savings: ${{printf "%.2f" .MonthlySavings}}/month
{{end}}

Summary:
--------
Total Monthly Savings: ${{printf "%.2f" .TotalSavings}}
{{if gt .TotalUpfrontCost 0.0}}Total Upfront Cost: ${{printf "%.2f" .TotalUpfrontCost}}{{end}}

View purchase history in the dashboard:
{{.DashboardURL}}/history
{{if .ArcheraEducationURL}}
---
ARCHERA INSURANCE (optional, 7-day enrollment window now active)

Archera Insurance covers the gap if your committed cloud capacity goes
unused. Optional, set up on Archera's site, and doesn't affect the
commitments you just bought.

When it makes sense:
  - You want the deepest discount tier (3-year) but aren't sure the
    workload will still fit in 18 months, or you want to be covered in
    case your usage drops.
  - You're moving to a new service or region and historical utilization
    data is thin.

How it works (7-day enrollment window from each purchase):
  1. Sign up at Archera: create an account using the link below. The
     CUDly signup link tells Archera you came from us; CUDly is
     compensated for the referral, and the link unlocks a dedicated
     onboarding path.
  2. Archera starts ingesting cost data: once access is granted, the
     insurance policy activates and covers any overcommitment from that
     point forward.
  3. Purchase commitments normally through CUDly: Archera tracks
     utilization independently and pays out on shortfalls per your
     policy.

Archera charges an insurance premium for the coverage you select, a
separate fee paid to Archera. The cloud commitment you bought through
CUDly is unaffected: same price, same billing.

Full disclosure: Archera sponsors CUDly's development with a share of
their insurance revenue; we surface the option because we think it's
useful, but you should know about the financial relationship. Insurance
terms, coverage, and pricing are set entirely by Archera. CUDly has no
visibility into your Archera account or policy. Review Archera's terms
of service and privacy policy before signing up.

Sign up:   ` + common.ArcheraSignupURL + `
Permalink: {{.ArcheraEducationURL}}
{{end}}
This is an automated message from CUDly.
`

const purchaseFailedTemplate = `CUDly - Purchase Failed
=======================

Some purchases could not be completed. Please review and retry manually.

Failed Purchases:
{{range .Recommendations}}
- {{.Count}}x {{.ResourceType}}{{if .Engine}} ({{.Engine}}){{end}} in {{.Region}}
  Service: {{.Service}}
{{end}}

Review failed purchases:
{{.DashboardURL}}/history

This is an automated message from CUDly.
`

const passwordResetTemplate = "" +
	`CUDly - Password Reset Request
==============================

Hello {{.Email}},

We received a request to reset your password for CUDly.

Click the link below to set a new password:
{{.ResetURL}}

This link will expire in 1 hour.

If you didn't request a password reset, you can safely ignore this email.
Your password will remain unchanged.

This is an automated message from CUDly.
`

// passwordResetHTMLTemplate renders the same content as the plain-text
// password reset template with a styled CTA button and CUDly branding.
// Modeled on purchaseApprovalRequestHTMLTemplate (line 367) — inline styles
// because most email clients (Outlook, mobile Gmail) ignore class-based CSS.
// Issue #355.
const passwordResetHTMLTemplate = "" +
	`<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>CUDly - Password Reset Request</title></head>
<body style="margin:0;padding:0;background:#f4f6f8;font-family:Arial,Helvetica,sans-serif;color:#1a202c;">
<table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="background:#f4f6f8;padding:24px 0;">
<tr><td align="center">
<table role="presentation" cellpadding="0" cellspacing="0" width="600" style="background:#ffffff;border-radius:8px;box-shadow:0 1px 3px rgba(0,0,0,0.06);">
<tr><td style="padding:32px 32px 16px 32px;">
<h1 style="margin:0;font-size:22px;color:#0f172a;">Password Reset Request</h1>
<p style="margin:16px 0 0 0;color:#475569;font-size:14px;line-height:1.5;">Hello {{.Email}},</p>
<p style="margin:12px 0 0 0;color:#475569;font-size:14px;line-height:1.5;">We received a request to reset your password for CUDly. Click the button below to choose a new password.</p>
</td></tr>

<tr><td align="center" style="padding:8px 32px 8px 32px;">
<a href="{{.ResetURL}}" style="display:inline-block;padding:12px 28px;background:#2563eb;color:#ffffff;text-decoration:none;font-weight:600;font-size:14px;border-radius:6px;">Reset your password</a>
</td></tr>

<tr><td style="padding:8px 32px 8px 32px;">
<p style="margin:8px 0 0 0;color:#64748b;font-size:12px;line-height:1.5;">This link will expire in 1 hour.</p>
<p style="margin:8px 0 0 0;color:#64748b;font-size:12px;line-height:1.5;">If the button doesn't work, copy and paste this URL into your browser:</p>
<p style="margin:4px 0 0 0;color:#475569;font-size:12px;word-break:break-all;"><a href="{{.ResetURL}}" style="color:#2563eb;text-decoration:underline;">{{.ResetURL}}</a></p>
<p style="margin:16px 0 0 0;color:#64748b;font-size:12px;line-height:1.5;">If you didn't request a password reset, you can safely ignore this email — your password will remain unchanged.</p>
</td></tr>

<tr><td style="padding:16px 32px;background:#f8fafc;border-top:1px solid #e2e8f0;border-radius:0 0 8px 8px;">
<p style="margin:0;color:#94a3b8;font-size:11px;">This is an automated message from CUDly.</p>
</td></tr>

</table>
</td></tr></table>
</body></html>`

const userInviteTemplate = `Welcome to CUDly
================

Hello {{.Email}},

An administrator has created a CUDly account for you. To activate it,
click the link below to set your password:

{{.SetupURL}}

This link will expire in 7 days. If it expires before you set a password,
ask your administrator to invite you again or use the "Forgot password?"
link on the sign-in page.

This is an automated message from CUDly.
`

// userInviteHTMLTemplate is the HTML half of userInviteTemplate.
// Inline-styled per email-client constraints. Issue #355.
const userInviteHTMLTemplate = `<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>Welcome to CUDly</title></head>
<body style="margin:0;padding:0;background:#f4f6f8;font-family:Arial,Helvetica,sans-serif;color:#1a202c;">
<table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="background:#f4f6f8;padding:24px 0;">
<tr><td align="center">
<table role="presentation" cellpadding="0" cellspacing="0" width="600" style="background:#ffffff;border-radius:8px;box-shadow:0 1px 3px rgba(0,0,0,0.06);">
<tr><td style="padding:32px 32px 16px 32px;">
<h1 style="margin:0;font-size:22px;color:#0f172a;">You've been invited to CUDly</h1>
<p style="margin:16px 0 0 0;color:#475569;font-size:14px;line-height:1.5;">Hello {{.Email}},</p>
<p style="margin:12px 0 0 0;color:#475569;font-size:14px;line-height:1.5;">An administrator has created a CUDly account for you. Click the button below to activate it and set your password.</p>
</td></tr>

<tr><td align="center" style="padding:8px 32px 8px 32px;">
<a href="{{.SetupURL}}" style="display:inline-block;padding:12px 28px;background:#16a34a;color:#ffffff;text-decoration:none;font-weight:600;font-size:14px;border-radius:6px;">Set your password</a>
</td></tr>

<tr><td style="padding:8px 32px 8px 32px;">
<p style="margin:8px 0 0 0;color:#64748b;font-size:12px;line-height:1.5;">This link will expire in 7 days. If it expires before you set a password, ask your administrator to invite you again or use the "Forgot password?" link on the sign-in page.</p>
<p style="margin:16px 0 0 0;color:#64748b;font-size:12px;line-height:1.5;">If the button doesn't work, paste this URL into your browser:</p>
<p style="margin:4px 0 0 0;color:#475569;font-size:12px;word-break:break-all;"><a href="{{.SetupURL}}" style="color:#2563eb;text-decoration:underline;">{{.SetupURL}}</a></p>
</td></tr>

<tr><td style="padding:16px 32px;background:#f8fafc;border-top:1px solid #e2e8f0;border-radius:0 0 8px 8px;">
<p style="margin:0;color:#94a3b8;font-size:11px;">This is an automated message from CUDly.</p>
</td></tr>

</table>
</td></tr></table>
</body></html>`

// SendNewRecommendationsNotification sends an email about new recommendations.
func (s *Sender) SendNewRecommendationsNotification(ctx context.Context, data NotificationData) error {
	body, err := RenderNewRecommendationsEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render new recommendations email: %w", err)
	}

	subject := fmt.Sprintf("CUDly - New Recommendations: $%.0f/month potential savings", data.TotalSavings)
	return s.SendNotification(ctx, subject, body)
}

// SendScheduledPurchaseNotification sends a notification about an upcoming
// automated purchase. The rendered body embeds approve/pause/cancel links
// that carry a live token, so it must be delivered to a specific recipient
// via targeted SES, never broadcast through the SNS topic.
//
// Returns ErrNoRecipient when data.RecipientEmail is empty so the caller can
// surface a precise reason rather than silently dropping the notification.
func (s *Sender) SendScheduledPurchaseNotification(ctx context.Context, data NotificationData) error {
	if data.RecipientEmail == "" {
		return ErrNoRecipient
	}
	body, err := RenderScheduledPurchaseEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render scheduled purchase email: %w", err)
	}

	subject := fmt.Sprintf("CUDly - Scheduled Purchase in %d Days: %s", data.DaysUntilPurchase, data.PlanName)
	return s.SendToEmailWithCC(ctx, data.RecipientEmail, data.CCEmails, subject, body)
}

// SendPurchaseConfirmation sends a confirmation after successful purchases.
func (s *Sender) SendPurchaseConfirmation(ctx context.Context, data NotificationData) error {
	body, err := RenderPurchaseConfirmationEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render purchase confirmation email: %w", err)
	}

	subject := fmt.Sprintf("CUDly - Purchases Completed: $%.0f/month in savings", data.TotalSavings)
	return s.SendNotification(ctx, subject, body)
}

// SendPurchaseFailedNotification sends a notification when purchases fail.
// A non-empty data.RecipientEmail (the global notification address) gets a
// targeted SES send; otherwise the body is broadcast through the SNS topic,
// which is a no-op when no topic is configured (issue #714).
func (s *Sender) SendPurchaseFailedNotification(ctx context.Context, data NotificationData) error {
	body, err := RenderPurchaseFailedEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render purchase failed email: %w", err)
	}

	subject := purchaseFailedSubject(data, "CUDly - Purchase Failed - Action Required")
	if data.RecipientEmail != "" {
		return s.SendToEmailWithCC(ctx, data.RecipientEmail, data.CCEmails, subject, body)
	}
	return s.SendNotification(ctx, subject, body)
}

// PasswordResetData holds data for password reset emails.
type PasswordResetData struct {
	Email    string
	ResetURL string
}

// SendPasswordResetEmail sends a password reset email as multipart/
// alternative (plain text + styled HTML with a CTA button). Issue #355.
func (s *Sender) SendPasswordResetEmail(ctx context.Context, email, resetURL string) error {
	return sendMultipartVia(
		ctx, s, email, "CUDly - Password Reset Request", "password-reset",
		func() (string, error) { return RenderPasswordResetEmail(email, resetURL) },
		func() (string, error) { return RenderPasswordResetEmailHTML(email, resetURL) },
	)
}

// UserInviteData holds data for invite emails sent to users that an admin
// created without a password. The recipient sets their own password by
// following SetupURL.
type UserInviteData struct {
	Email    string
	SetupURL string
}

// SendUserInviteEmail sends an invitation that links to the password-setup
// page as multipart/alternative (plain text + styled HTML with a CTA button).
// Used when an admin creates a user without supplying a password. Issue #355.
func (s *Sender) SendUserInviteEmail(ctx context.Context, email, setupURL string) error {
	return sendMultipartVia(
		ctx, s, email, "CUDly - Set your password", "user-invite",
		func() (string, error) { return RenderUserInviteEmail(email, setupURL) },
		func() (string, error) { return RenderUserInviteEmailHTML(email, setupURL) },
	)
}

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

// ---------------------------------------------------------------------------
// Gmail-style pre-fire delay email (issue #291 wave-2)
// ---------------------------------------------------------------------------

// purchaseScheduledDelayTemplate is the plain-text body for the
// "your purchase is scheduled — revoke before X" email sent immediately after
// an approval when PurchaseDelayHours > 0.
var purchaseScheduledDelayTemplate = `
Your CUDly purchase has been approved and is scheduled to execute.

Scheduled execution time: {{.RevocationWindowClosesAt}} (UTC)

You can revoke this purchase at zero cost until it executes:
{{.RevokeURL}}

After that time the cloud SDK call will have been made and revocation
requires a provider support case (for supported providers).

Purchase summary:
- Total upfront cost: ${{printf "%.2f" .TotalUpfrontCost}}
- Estimated monthly savings: ${{printf "%.2f" .TotalSavings}}
- Commitment count: {{len .Recommendations}}

To view or manage this purchase:
{{.DashboardURL}}/purchases#history?execution={{.ExecutionID}}

-- CUDly
`

// purchaseLadderScheduledTemplate is the body of the scheduled notice of an
// automatic ladder purchase. Nobody approved it and its upfront cost and
// savings are not estimated, so it states the hourly commitment, the annual
// exposure and who can revoke instead. "no upfront" mirrors
// config.LadderPayment, the only payment option automated ladder buys use.
var purchaseLadderScheduledTemplate = `
An automatic ladder purchase has been scheduled. It was not approved by a person.

What will be bought:
- Hourly commitment: ${{.Ladder.HourlyText}} per hour
- Annual exposure: ${{printf "%.2f" .Ladder.AnnualExposure}} (hourly commitment x 8760 hours)
- Term: {{if eq .Ladder.TermYears 1}}1 year{{else}}{{.Ladder.TermYears}} years{{end}}, no upfront
- Account: {{.Ladder.AccountLabel}}
- Ladder tranche: {{.Ladder.TrancheID}}

Scheduled execution time: {{.RevocationWindowClosesAt}} (UTC)

You can revoke this purchase at zero cost until it executes:
{{.RevokeURL}}

Only users with the revoke-any permission and the admin API key can revoke
it. Users who can revoke only their own purchases cannot, because no person
created it.

After that time the cloud SDK call will have been made and revocation
requires a provider support case (for supported providers).

To view or manage this purchase:
{{.DashboardURL}}/purchases#history?execution={{.ExecutionID}}

-- CUDly
`

// RenderPurchaseScheduledDelayEmail renders the plain-text scheduled-delay
// notification email, or the ladder variant when data.Ladder is set.
func RenderPurchaseScheduledDelayEmail(data NotificationData) (string, error) {
	if data.Ladder != nil {
		return renderTextTemplate("purchase_ladder_scheduled", purchaseLadderScheduledTemplate, data)
	}
	return renderTextTemplate("purchase_scheduled_delay", purchaseScheduledDelayTemplate, data)
}

// purchaseScheduledSubject is the subject of the scheduled-purchase notice.
func purchaseScheduledSubject(data NotificationData) string {
	if data.Ladder != nil {
		return fmt.Sprintf("CUDly - Automatic ladder purchase scheduled for %s", data.RevocationWindowClosesAt)
	}
	return fmt.Sprintf("CUDly - Purchase Scheduled for %s", data.RevocationWindowClosesAt)
}

// SendPurchaseScheduledNotification sends the Gmail-style pre-fire delay
// notification email immediately after an approval with delay > 0.
// The email tells the user when the purchase will execute and includes a
// one-click revoke link. Route: direct To/CC (same as approval request)
// because the revoke link is scoped to the execution ID.
//
// Returns ErrNoRecipient when data.RecipientEmail is empty. The body carries a
// live, execution-scoped revoke link, so it must never fall back to the
// broadcast SendNotification path (which would leak the action link to every
// alert subscriber and break the ownership/RBAC model around revocation).
// Mirrors SendScheduledPurchaseNotification and the SMTP sender, which both
// require a resolved recipient for this email.
func (s *Sender) SendPurchaseScheduledNotification(ctx context.Context, data NotificationData) error {
	if data.RecipientEmail == "" {
		return ErrNoRecipient
	}
	body, err := RenderPurchaseScheduledDelayEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render purchase scheduled delay email: %w", err)
	}
	subject := purchaseScheduledSubject(data)
	return s.SendToEmailWithCCMultipart(ctx, data.RecipientEmail, data.CCEmails, subject, body, "")
}

// ---------------------------------------------------------------------------
// Account registration email templates
// ---------------------------------------------------------------------------

// RegistrationNotificationData is used to render the admin notification when a
// new account registers via the federation IaC.
type RegistrationNotificationData struct {
	AccountName  string
	Provider     string
	ExternalID   string
	ContactEmail string
	DashboardURL string
	// RecipientEmail is the primary (To) inbox — the first admin email,
	// or the global notification email if no admin has an email
	// configured. Leave empty to fall back to the SNS broadcast path.
	RecipientEmail string
	// CCEmails carry the remaining admin emails plus the global
	// notification email, deduped against RecipientEmail.
	CCEmails []string
	// AdminApprovers is the full set of admin emails that can approve or
	// reject this registration — rendered verbatim in the message body
	// so CC'd recipients know the action isn't theirs to take. The
	// account's own ContactEmail is intentionally NOT on this list
	// because the submitter can't self-approve their own registration.
	AdminApprovers []string
}

// RegistrationDecisionData is used to render the registrant notification when
// their registration is approved or rejected.
type RegistrationDecisionData struct {
	AccountName     string
	Provider        string
	ExternalID      string
	Decision        string // "approved" or "rejected"
	RejectionReason string
}

const registrationReceivedTemplate = `CUDly - New Account Registration
==================================

A new target account has requested to join your CUDly deployment.
{{if .AdminApprovers}}
Authorized reviewer(s):
{{range .AdminApprovers}}  - {{.}}
{{end}}
Only CUDly administrators listed above can approve or reject this
registration. Other recipients are CC'd for visibility only.
{{end}}
Account Details:
  Name:        {{.AccountName}}
  Provider:    {{.Provider}}
  External ID: {{.ExternalID}}
  Contact:     {{.ContactEmail}}

Review and approve/reject in the dashboard:
{{.DashboardURL}}

This is an automated message from CUDly.
`

const registrationDecisionTemplate = `CUDly - Account Registration {{.Decision}}
==================================

Your registration for account "{{.AccountName}}" ({{.Provider}} / {{.ExternalID}}) has been {{.Decision}}.
{{if .RejectionReason}}
Reason: {{.RejectionReason}}
{{end}}{{if eq .Decision "approved"}}
Next steps:
Your CUDly administrator will configure cross-account credentials.
You may be asked to deploy additional IaC templates to complete federation setup.
{{end}}
This is an automated message from CUDly.
`

// SendRegistrationReceivedNotification sends an email notifying CUDly
// administrators that a new account registration has been submitted. When
// data.RecipientEmail is set (caller resolved admin + global-notify
// recipients) the send routes through the targeted SES path so To / Cc
// semantics match the approver/visibility distinction embedded in the
// body. When RecipientEmail is empty the send falls back to the legacy
// SNS broadcast path so deployments that never configured admin users
// still get notified.
func (s *Sender) SendRegistrationReceivedNotification(ctx context.Context, data RegistrationNotificationData) error {
	body, err := RenderRegistrationReceivedEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render registration received email: %w", err)
	}
	// sanitizeHeader strips CR/LF from the attacker-controlled AccountName /
	// Provider (sourced from the unauthenticated POST /api/register endpoint)
	// before they are interpolated into the SES email subject, preventing
	// email header injection (#544 / #401). Mirrors the SMTP path fix.
	subject := fmt.Sprintf("CUDly - New Account Registration: %s (%s)",
		sanitizeHeader(data.AccountName), sanitizeHeader(data.Provider))
	if data.RecipientEmail == "" {
		return s.SendNotification(ctx, subject, body)
	}
	return s.SendToEmailWithCC(ctx, data.RecipientEmail, data.CCEmails, subject, body)
}

// SendRegistrationDecisionNotification sends an email to the registrant when
// their registration is approved or rejected.
func (s *Sender) SendRegistrationDecisionNotification(ctx context.Context, toEmail string, data RegistrationDecisionData) error {
	body, err := RenderRegistrationDecisionEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render registration decision email: %w", err)
	}
	subject := fmt.Sprintf("CUDly - Account Registration %s", data.Decision)
	return s.SendToEmail(ctx, toEmail, subject, body)
}
