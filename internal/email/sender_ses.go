package email

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/redact"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// isInSandbox checks if SES is in sandbox mode.
func (s *Sender) isInSandbox(ctx context.Context) (bool, error) {
	if s.sesClient == nil {
		return false, fmt.Errorf("SES client not initialized")
	}

	output, err := s.sesClient.GetAccount(ctx, &sesv2.GetAccountInput{})
	if err != nil {
		return false, fmt.Errorf("failed to get SES account info: %w", err)
	}

	// ProductionAccessEnabled is false when in sandbox mode
	return !output.ProductionAccessEnabled, nil
}

// isEmailVerified checks if an email identity is verified in SES.
func (s *Sender) isEmailVerified(ctx context.Context, email string) (bool, error) {
	if s.sesClient == nil {
		return false, fmt.Errorf("SES client not initialized")
	}

	output, err := s.sesClient.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{
		EmailIdentity: aws.String(email),
	})
	if err != nil {
		// If identity doesn't exist, it's not verified
		return false, nil
	}

	return output.VerifiedForSendingStatus, nil
}

// createVerificationRequest initiates email verification for an email address.
func (s *Sender) createVerificationRequest(ctx context.Context, email string) error {
	if s.sesClient == nil {
		return fmt.Errorf("SES client not initialized")
	}

	_, err := s.sesClient.CreateEmailIdentity(ctx, &sesv2.CreateEmailIdentityInput{
		EmailIdentity: aws.String(email),
	})
	if err != nil {
		return fmt.Errorf("failed to create email identity verification: %w", err)
	}

	logging.Infof("Created email verification request for %s - check inbox for verification email", redact.Email(email))
	return nil
}

// ensureSandboxRecipientVerified short-circuits the SES send path when
// the account is in sandbox mode and the recipient isn't yet verified —
// returning a user-facing error asking the recipient to click the
// verification link SES will have just sent. Non-sandbox accounts always
// proceed. Errors from the sandbox probe itself are logged and ignored
// so a transient SES control-plane hiccup doesn't take down the data-
// plane send path.
func (s *Sender) ensureSandboxRecipientVerified(ctx context.Context, toEmail string) error {
	inSandbox, err := s.isInSandbox(ctx)
	if err != nil {
		logging.Warnf("Failed to check SES sandbox status: %v", err)
		return nil
	}
	if !inSandbox {
		return nil
	}
	logging.Infof("SES is in sandbox mode - checking if recipient %s is verified", redact.Email(toEmail))

	verified, err := s.isEmailVerified(ctx, toEmail)
	if err != nil {
		logging.Warnf("Failed to check email verification status: %v", err)
		return nil
	}
	if verified {
		logging.Infof("Recipient email %s is verified - proceeding with send", redact.Email(toEmail))
		return nil
	}
	logging.Warnf("Recipient email %s is not verified in sandbox mode - creating verification request", redact.Email(toEmail))
	if err := s.createVerificationRequest(ctx, toEmail); err != nil {
		logging.Warnf("Failed to create verification request: %v", err)
		// Use the real address in the user-facing message so the recipient
		// knows which inbox to check; log only the redacted form (07-M2).
		return fmt.Errorf("recipient email %s is not verified in SES sandbox mode. A verification email has been sent - please check inbox and click the verification link before trying again", toEmail)
	}
	// The user-facing message intentionally includes the full address so the
	// recipient can verify the correct inbox. Wrapped errors/logs use the
	// redacted form per the package's PII stance (07-M2).
	return fmt.Errorf("recipient email %s is not verified in SES sandbox mode. A verification email has been sent - please check inbox and click the verification link, then try again", toEmail)
}

// buildSESSendEmailInputMultipart constructs a sesv2.SendEmailInput with
// both a plain-text and an HTML alternative body. SES handles the
// multipart/alternative MIME assembly server-side when both Text and Html
// fields are populated on types.Body. extraHeaders are appended as-is; use
// addListUnsubscribeHeaders to build the RFC 8058 pair.
//
// Header injection note (07-M1): SES v2 SendEmail accepts structured fields
// (Subject.Data, Body.Text.Data, Body.Html.Data) and builds the MIME envelope
// server-side. CR/LF injection via Subject.Data is not possible through the
// structured API. Any future raw-MIME path MUST sanitize the subject with
// sanitizeHeader before composing the header string, matching the SMTP path.
func buildSESSendEmailInputMultipart(fromEmail, toEmail string, cc []string, subject, textBody, htmlBody string, extraHeaders []types.MessageHeader) *sesv2.SendEmailInput {
	destination := &types.Destination{
		ToAddresses: []string{toEmail},
	}
	if len(cc) > 0 {
		destination.CcAddresses = cc
	}
	msg := &types.Message{
		Subject: &types.Content{
			Charset: aws.String("UTF-8"),
			Data:    aws.String(subject),
		},
		Body: &types.Body{
			Text: &types.Content{
				Charset: aws.String("UTF-8"),
				Data:    aws.String(textBody),
			},
			Html: &types.Content{
				Charset: aws.String("UTF-8"),
				Data:    aws.String(htmlBody),
			},
		},
	}
	if len(extraHeaders) > 0 {
		msg.Headers = extraHeaders
	}
	return &sesv2.SendEmailInput{
		Destination:      destination,
		Content:          &types.EmailContent{Simple: msg},
		FromEmailAddress: aws.String(fromEmail),
	}
}

// buildSESSendEmailInput constructs a sesv2.SendEmailInput with the
// destination To + (optional) Cc list and a plain-text body. extraHeaders are
// appended as-is; use addListUnsubscribeHeaders to build the RFC 8058 pair.
// See buildSESSendEmailInputMultipart for the header-injection safety note (07-M1).
func buildSESSendEmailInput(fromEmail, toEmail string, cc []string, subject, body string, extraHeaders []types.MessageHeader) *sesv2.SendEmailInput {
	destination := &types.Destination{
		ToAddresses: []string{toEmail},
	}
	if len(cc) > 0 {
		destination.CcAddresses = cc
	}
	msg := &types.Message{
		Subject: &types.Content{
			Charset: aws.String("UTF-8"),
			Data:    aws.String(subject),
		},
		Body: &types.Body{
			Text: &types.Content{
				Charset: aws.String("UTF-8"),
				Data:    aws.String(body),
			},
		},
	}
	if len(extraHeaders) > 0 {
		msg.Headers = extraHeaders
	}
	return &sesv2.SendEmailInput{
		Destination:      destination,
		Content:          &types.EmailContent{Simple: msg},
		FromEmailAddress: aws.String(fromEmail),
	}
}
