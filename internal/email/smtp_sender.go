// Package email provides email notification functionality using SMTP.
package email

import (
	"context"
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/redact"
)

// SMTPConfig holds configuration for SMTP email sender.
type SMTPConfig struct {
	Host          string
	Username      string
	Password      string //nolint:gosec // G101: field holds a user-supplied runtime password, not a hardcoded credential
	FromEmail     string
	FromName      string
	NotifyEmail   string
	Port          int
	UseTLS        bool
	AllowInsecure bool
}

// SMTPSender handles sending email via SMTP (works for SendGrid, Azure ACS, and others).
type SMTPSender struct {
	// muteChecker consults the muted_recipients table before each send.
	// Nil disables mute checking (e.g. when no DB is wired in tests).
	muteChecker MuteChecker
	host        string
	username    string
	password    string
	fromEmail   string
	fromName    string
	notifyEmail string
	// unsubscribeBaseURL is the dashboard base URL used to construct the
	// List-Unsubscribe header value. Empty disables the header.
	unsubscribeBaseURL string
	port               int
	useTLS             bool
	allowInsecure      bool
}

// WithMuteChecker returns a shallow copy of s with the given MuteChecker wired
// in, mirroring (*Sender).WithMuteChecker so the SMTP transport applies the same
// per-recipient mute suppression as SES.
func (s *SMTPSender) WithMuteChecker(mc MuteChecker) *SMTPSender {
	c := *s
	c.muteChecker = mc
	return &c
}

// WithUnsubscribeBaseURL returns a shallow copy of s with the given base URL
// set, mirroring (*Sender).WithUnsubscribeBaseURL. When non-empty the SMTP
// approval send emits RFC 8058 List-Unsubscribe headers.
func (s *SMTPSender) WithUnsubscribeBaseURL(u string) *SMTPSender {
	c := *s
	c.unsubscribeBaseURL = u
	return &c
}

// NewSMTPSender creates a new SMTP email sender.
func NewSMTPSender(cfg SMTPConfig) (*SMTPSender, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("SMTP host is required")
	}
	if cfg.FromEmail == "" {
		return nil, fmt.Errorf("from email is required")
	}

	// Set defaults
	if cfg.Port == 0 {
		cfg.Port = 587 // Default to TLS port
	}
	if !cfg.UseTLS && cfg.Port == 587 {
		cfg.UseTLS = true // Enable TLS for port 587 by default
	}

	notifyEmail := cfg.NotifyEmail
	if notifyEmail == "" {
		notifyEmail = cfg.FromEmail
	}

	return &SMTPSender{
		host:          cfg.Host,
		port:          cfg.Port,
		username:      cfg.Username,
		password:      cfg.Password,
		fromEmail:     cfg.FromEmail,
		fromName:      cfg.FromName,
		notifyEmail:   notifyEmail,
		useTLS:        cfg.UseTLS,
		allowInsecure: cfg.AllowInsecure,
	}, nil
}

// SendNotification is a no-op for SMTP senders (07-N4).
// SMTP has no pub/sub equivalent of SNS: there is no topic to publish to and
// no subscriber list to fan out to. As a result, GCP (SendGrid) and Azure
// (ACS SMTP) deployments do not receive broadcast notifications (new-recs,
// scheduled-purchase reminders without a recipient email, etc.). Callers that
// need broadcast behavior on non-AWS deployments must wire their own fan-out
// or configure an SNS-compatible endpoint. Targeted approval emails
// (SendPurchaseApprovalRequest, SendScheduledPurchaseNotification) are
// unaffected because they use SendToEmailWithCC directly.
func (s *SMTPSender) SendNotification(ctx context.Context, subject, message string) error {
	logging.Debug("SMTP broadcast notification is a no-op (SMTP has no pub/sub mechanism)")
	return nil
}

// sanitizeHeader strips CR and LF characters to prevent SMTP header injection.
func sanitizeHeader(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// SendToEmail sends an email directly to a specific email address via SMTP.
func (s *SMTPSender) SendToEmail(ctx context.Context, toEmail, subject, body string) error {
	return s.SendToEmailWithCC(ctx, toEmail, nil, subject, body)
}

// SendToEmailWithCCMultipart sends a multipart/alternative message (plain-
// text + HTML) via SMTP. htmlBody == "" degrades to a single-part text send
// for backwards compatibility with callers that don't have an HTML body.
func (s *SMTPSender) SendToEmailWithCCMultipart(ctx context.Context, toEmail string, ccEmails []string, subject, textBody, htmlBody string) error {
	if htmlBody == "" {
		return s.SendToEmailWithCC(ctx, toEmail, ccEmails, subject, textBody)
	}
	if s.fromEmail == "" {
		logging.Debug("No from email configured, skipping email")
		return nil
	}

	toEmail = sanitizeHeader(toEmail)
	subject = sanitizeHeader(subject)
	sanitizedCC := sanitizeCCList(toEmail, ccEmails)

	msg := s.buildSMTPMessageMultipart(toEmail, sanitizedCC, subject, textBody, htmlBody)
	rcpts := append([]string{toEmail}, sanitizedCC...)

	if err := s.dispatchSMTP(ctx, rcpts, msg); err != nil {
		return err
	}

	if len(sanitizedCC) > 0 {
		logging.Debugf("Sent multipart email via SMTP to %s (cc %d): %s", redact.Email(toEmail), len(sanitizedCC), subject)
	} else {
		logging.Debugf("Sent multipart email via SMTP to %s: %s", redact.Email(toEmail), subject)
	}
	return nil
}

// SendToEmailWithCC sends an email with To + optional Cc recipients via SMTP.
// The Cc header is included in the message envelope so recipients see one
// another, and the SMTP RCPT TO list carries every address so each inbox
// actually receives the message.
func (s *SMTPSender) SendToEmailWithCC(ctx context.Context, toEmail string, ccEmails []string, subject, body string) error {
	if s.fromEmail == "" {
		logging.Debug("No from email configured, skipping email")
		return nil
	}

	// Sanitize header values to prevent SMTP header injection
	toEmail = sanitizeHeader(toEmail)
	subject = sanitizeHeader(subject)

	sanitizedCC := sanitizeCCList(toEmail, ccEmails)

	msg := s.buildSMTPMessage(toEmail, sanitizedCC, subject, body)
	rcpts := append([]string{toEmail}, sanitizedCC...)

	if err := s.dispatchSMTP(ctx, rcpts, msg); err != nil {
		return err
	}

	if len(sanitizedCC) > 0 {
		logging.Debugf("Sent email via SMTP to %s (cc %d): %s", redact.Email(toEmail), len(sanitizedCC), subject)
	} else {
		logging.Debugf("Sent email via SMTP to %s: %s", redact.Email(toEmail), subject)
	}
	return nil
}

// SendPasswordResetEmail sends a password reset email as multipart/
// alternative (text + styled HTML). Issue #355.
func (s *SMTPSender) SendPasswordResetEmail(ctx context.Context, email, resetURL string) error {
	return sendMultipartVia(
		ctx, s, email, "Password Reset Request - CUDly", "password-reset",
		func() (string, error) { return RenderPasswordResetEmail(email, resetURL) },
		func() (string, error) { return RenderPasswordResetEmailHTML(email, resetURL) },
	)
}

// SendUserInviteEmail sends an invite-with-setup-link email to a user
// created without a password, as multipart/alternative (text + styled HTML).
// Issue #355.
func (s *SMTPSender) SendUserInviteEmail(ctx context.Context, email, setupURL string) error {
	return sendMultipartVia(
		ctx, s, email, "CUDly - Set your password", "user-invite",
		func() (string, error) { return RenderUserInviteEmail(email, setupURL) },
		func() (string, error) { return RenderUserInviteEmailHTML(email, setupURL) },
	)
}

// SendNewRecommendationsNotification sends a notification about new recommendations.
func (s *SMTPSender) SendNewRecommendationsNotification(ctx context.Context, data NotificationData) error {
	subject := "New CUDly Recommendations Available"
	body, err := RenderNewRecommendationsEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render new recommendations email: %w", err)
	}
	return s.SendToEmail(ctx, s.notifyEmail, subject, body)
}

// SendScheduledPurchaseNotification sends a notification about scheduled purchase.
// The body embeds a live cancel token, so it goes to data.RecipientEmail (plus
// data.CCEmails, honored for SES parity though the real caller sends none),
// never the shared notify inbox. Returns ErrNoRecipient when RecipientEmail is
// empty, matching the SES sender.
func (s *SMTPSender) SendScheduledPurchaseNotification(ctx context.Context, data NotificationData) error {
	if data.RecipientEmail == "" {
		return ErrNoRecipient
	}
	subject := fmt.Sprintf("CUDly Purchase Scheduled: %s", data.PlanName)
	body, err := RenderScheduledPurchaseEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render scheduled purchase email: %w", err)
	}
	return s.SendToEmailWithCC(ctx, data.RecipientEmail, data.CCEmails, subject, body)
}

// SendPurchaseConfirmation sends a confirmation email after successful purchase.
func (s *SMTPSender) SendPurchaseConfirmation(ctx context.Context, data NotificationData) error {
	subject := "CUDly Purchase Confirmation"
	body, err := RenderPurchaseConfirmationEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render purchase confirmation email: %w", err)
	}
	return s.SendToEmail(ctx, s.notifyEmail, subject, body)
}

// SendPurchaseFailedNotification sends a notification when a purchase fails,
// to data.RecipientEmail (the global notification address) when set and to
// the static notify address otherwise (issue #714).
func (s *SMTPSender) SendPurchaseFailedNotification(ctx context.Context, data NotificationData) error {
	subject := purchaseFailedSubject(data, "CUDly Purchase Failed")
	body, err := RenderPurchaseFailedEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render purchase failed email: %w", err)
	}
	to := data.RecipientEmail
	if to == "" {
		to = s.notifyEmail
	}
	return s.SendToEmailWithCC(ctx, to, data.CCEmails, subject, body)
}

// SendRIExchangePendingApproval sends an RI exchange approval email via SMTP
// as multipart/alternative (plain-text + styled HTML). The body carries live
// approval tokens, so it is sent only to data.RecipientEmail plus the
// deduplicated CC list; it never broadcasts. Returns ErrNoRecipient when
// data.RecipientEmail is empty, matching the SES sender: the static notify
// inbox is not a fallback for token-bearing mail (issue #111). Issue #296.
func (s *SMTPSender) SendRIExchangePendingApproval(ctx context.Context, data RIExchangeNotificationData) error {
	recipient := data.RecipientEmail
	if recipient == "" {
		return ErrNoRecipient
	}
	scope := string(common.ScopeRIExchangeApprovals)
	filteredCC, unsubHdr, postHdr, muted := prepareMuteAwareDelivery(
		ctx, s.muteChecker, s.unsubscribeBaseURL, recipient, data.CCEmails, scope,
	)
	if muted {
		logging.Infof("email/smtp: RI exchange approval skipped for muted recipient (scope=%s)", scope)
		return nil
	}
	textBody, htmlBody, err := renderRIExchangePendingApproval(data)
	if err != nil {
		return err
	}
	subject := fmt.Sprintf("CUDly - RI Exchange Approval Required (%d exchanges)", len(data.Exchanges))
	return s.sendMultipartWithUnsubscribe(ctx, recipient, filteredCC, subject, textBody, htmlBody, unsubHdr, postHdr)
}

// SendRIExchangeCompleted sends an RI exchange completion email via SMTP.
func (s *SMTPSender) SendRIExchangeCompleted(ctx context.Context, data RIExchangeNotificationData) error {
	subject := riExchangeCompletedSubject(data)
	body, err := RenderRIExchangeCompletedEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render ri exchange completed email: %w", err)
	}
	return s.SendToEmail(ctx, s.notifyEmail, subject, body)
}

// SendPurchaseApprovalRequest sends a purchase approval request email via SMTP
// to data.RecipientEmail (the submitter's notification email from app
// settings). Returns ErrNoRecipient when it is empty, matching the SES sender:
// the one-time approval token must not fall back to the static notify inbox
// (issue #111).
//
// Mute check + List-Unsubscribe mirror the SES (*Sender) path: if the recipient
// has opted out of purchase_approvals the email is silently skipped, muted CC
// addresses are dropped, and an RFC 8058 List-Unsubscribe header pair is added
// when an unsubscribe base URL is configured.
func (s *SMTPSender) SendPurchaseApprovalRequest(ctx context.Context, data NotificationData) error {
	recipient := data.RecipientEmail
	if recipient == "" {
		return ErrNoRecipient
	}

	scope := string(common.ScopePurchaseApprovals)

	filteredCC, unsubHdr, postHdr, muted := prepareMuteAwareDelivery(
		ctx, s.muteChecker, s.unsubscribeBaseURL, recipient, data.CCEmails, scope,
	)
	if muted {
		logging.Infof("email/smtp: purchase approval skipped for muted recipient (scope=%s)", scope)
		return nil
	}

	subject := fmt.Sprintf("CUDly - Purchase Approval Required (%d commitment(s))", len(data.Recommendations))

	textBody, err := RenderPurchaseApprovalRequestEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render purchase approval request email (text): %w", err)
	}
	htmlBody, htmlErr := RenderPurchaseApprovalRequestEmailHTML(data)
	if htmlErr != nil {
		logging.Warnf("email: HTML approval-request render failed, falling back to text-only: %v", htmlErr)
		htmlBody = ""
	}
	return s.sendMultipartWithUnsubscribe(ctx, recipient, filteredCC, subject, textBody, htmlBody, unsubHdr, postHdr)
}

// sendMultipartWithUnsubscribe sends a multipart/alternative (text + HTML)
// message via SMTP, optionally injecting the RFC 8058 List-Unsubscribe /
// List-Unsubscribe-Post headers. htmlBody == "" degrades to a single-part text
// send. unsubHdr == "" omits the unsubscribe headers entirely.
func (s *SMTPSender) sendMultipartWithUnsubscribe(ctx context.Context, toEmail string, ccEmails []string, subject, textBody, htmlBody, unsubHdr, postHdr string) error {
	if unsubHdr == "" {
		// No List-Unsubscribe headers required: reuse the existing send path.
		return s.SendToEmailWithCCMultipart(ctx, toEmail, ccEmails, subject, textBody, htmlBody)
	}
	if s.fromEmail == "" {
		logging.Debug("No from email configured, skipping email")
		return nil
	}

	toEmail = sanitizeHeader(toEmail)
	subject = sanitizeHeader(subject)
	sanitizedCC := sanitizeCCList(toEmail, ccEmails)

	extra := buildListUnsubscribeHeaderLines(unsubHdr, postHdr)
	var msg []byte
	if htmlBody == "" {
		msg = s.buildSMTPMessageWithHeaders(toEmail, sanitizedCC, subject, textBody, extra)
	} else {
		msg = s.buildSMTPMessageMultipartWithHeaders(toEmail, sanitizedCC, subject, textBody, htmlBody, extra)
	}
	rcpts := append([]string{toEmail}, sanitizedCC...)

	if err := s.dispatchSMTP(ctx, rcpts, msg); err != nil {
		return err
	}

	if len(sanitizedCC) > 0 {
		logging.Debugf("Sent approval email via SMTP to %s (cc %d): %s", redact.Email(toEmail), len(sanitizedCC), subject)
	} else {
		logging.Debugf("Sent approval email via SMTP to %s: %s", redact.Email(toEmail), subject)
	}
	return nil
}

// buildListUnsubscribeHeaderLines returns the RFC 8058 List-Unsubscribe header
// lines (each terminated with CRLF), already sanitized against header
// injection. Returns "" when headerValue is empty.
func buildListUnsubscribeHeaderLines(headerValue, postValue string) string {
	if headerValue == "" {
		return ""
	}
	lines := fmt.Sprintf("List-Unsubscribe: %s\r\n", sanitizeHeader(headerValue))
	if postValue != "" {
		lines += fmt.Sprintf("List-Unsubscribe-Post: %s\r\n", sanitizeHeader(postValue))
	}
	return lines
}

// SendPurchaseScheduledNotification sends the Gmail-style pre-fire delay
// notification email via SMTP. Mirrors the Sender implementation: the body
// carries a live, execution-scoped revoke link, so an empty
// data.RecipientEmail returns ErrNoRecipient instead of falling back to the
// static notify inbox (issue #111).
func (s *SMTPSender) SendPurchaseScheduledNotification(ctx context.Context, data NotificationData) error {
	if data.RecipientEmail == "" {
		return ErrNoRecipient
	}
	body, err := RenderPurchaseScheduledDelayEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render purchase scheduled delay email: %w", err)
	}
	subject := purchaseScheduledSubject(data)
	return s.SendToEmailWithCC(ctx, data.RecipientEmail, data.CCEmails, subject, body)
}

// SendPurchaseExecutedNotification sends the post-execution notification email
// via SMTP to data.RecipientEmail. The body can carry a revocation token, so an
// empty recipient returns ErrNoRecipient, matching the SES sender, instead of
// falling back to the static notify inbox (issue #111). Issue #291.
func (s *SMTPSender) SendPurchaseExecutedNotification(ctx context.Context, data NotificationData) error {
	if data.RecipientEmail == "" {
		return ErrNoRecipient
	}
	subject := buildExecutedNotificationSubject(data)
	return sendPurchaseExecutedNotificationVia(ctx, s, data.RecipientEmail, subject, data)
}

// SendRegistrationReceivedNotification sends an email to CUDly administrators
// for a new registration via SMTP. Prefers the caller-resolved
// data.RecipientEmail + CCEmails (admin emails + global notify) so the To /
// Cc semantics match the "authorized reviewers" block in the body; falls
// back to the legacy static s.notifyEmail when the caller didn't resolve
// recipients (e.g. no admin users configured yet).
func (s *SMTPSender) SendRegistrationReceivedNotification(ctx context.Context, data RegistrationNotificationData) error {
	// Sanitize user-controlled fields before interpolating into the Subject header
	// to prevent SMTP header injection (issue #401).
	subject := fmt.Sprintf("CUDly - New Account Registration: %s (%s)",
		sanitizeHeader(data.AccountName), sanitizeHeader(data.Provider))
	body, err := RenderRegistrationReceivedEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render registration received email: %w", err)
	}
	recipient := data.RecipientEmail
	if recipient == "" {
		recipient = s.notifyEmail
	}
	return s.SendToEmailWithCC(ctx, recipient, data.CCEmails, subject, body)
}

// SendRegistrationDecisionNotification sends approval/rejection to the registrant via SMTP.
func (s *SMTPSender) SendRegistrationDecisionNotification(ctx context.Context, toEmail string, data RegistrationDecisionData) error {
	subject := fmt.Sprintf("CUDly - Account Registration %s", data.Decision)
	body, err := RenderRegistrationDecisionEmail(data)
	if err != nil {
		return fmt.Errorf("failed to render registration decision email: %w", err)
	}
	return s.SendToEmail(ctx, toEmail, subject, body)
}

// Verify that SMTPSender implements SenderInterface.
var _ SenderInterface = (*SMTPSender)(nil)
