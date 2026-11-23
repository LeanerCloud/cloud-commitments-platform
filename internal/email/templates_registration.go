package email

import (
	"context"
	"fmt"
)

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
