package email

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
)

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
