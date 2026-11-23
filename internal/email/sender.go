// Package email provides email notification functionality using SNS/SES.
package email

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/redact"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
)

// Sentinel errors returned by send-path methods when preconditions aren't met.
// Callers use errors.Is to branch on these without pattern-matching on strings.
var (
	// ErrNoRecipient — the NotificationData has no RecipientEmail for a send
	// that requires a specific recipient (e.g. purchase approval for the user
	// who submitted the purchase, not a broadcast to SNS subscribers).
	ErrNoRecipient = errors.New("email: no recipient address")

	// ErrNoFromEmail — the sender has no FROM_EMAIL configured; nothing can go
	// out. Distinct from ErrNoRecipient so the caller can report which side
	// of the wire is unconfigured.
	ErrNoFromEmail = errors.New("email: no from address")

	// ErrTokenInBroadcast is returned by SendNotification when the message
	// body contains a "token=" query parameter. Broadcasting a body with an
	// approval token leaks it to every SNS subscriber. Use SendToEmailWithCC
	// (targeted SES) for any message that carries an approval URL.
	ErrTokenInBroadcast = errors.New("email: message body contains an approval token; use targeted SES send, not SNS broadcast")
)

// SenderConfig holds configuration for the email sender.
type SenderConfig struct {
	TopicARN     string
	FromEmail    string
	EmailAddress string // Legacy: for SNS notifications
}

// Sender handles sending email notifications.
type Sender struct {
	snsClient    SNSPublisher
	sesClient    SESEmailSender
	topicARN     string
	fromEmail    string
	emailAddress string
	// muteChecker consults the muted_recipients table before each send.
	// Nil disables mute checking (e.g. when no DB is wired in tests).
	muteChecker MuteChecker
	// unsubscribeBaseURL is the dashboard base URL used to construct the
	// List-Unsubscribe header value. Empty disables the header.
	unsubscribeBaseURL string
}

// NewSender creates a new email sender with default context.
func NewSender(cfg SenderConfig) (*Sender, error) {
	return NewSenderWithContext(context.Background(), cfg)
}

// isValidFromEmail checks that a FROM_EMAIL value has the minimum "x@y.z"
// shape SES will accept. We don't RFC5322-validate — just guard against the
// two misconfigurations we've actually seen:
//   - empty string (env var unset)
//   - "noreply@" with a trailing empty domain (Terraform template expanded
//     against an unset subdomain_zone_name tfvar)
//
// Anything else is handed to SES which will reject with a clear error.
func isValidFromEmail(addr string) bool {
	if addr == "" {
		return false
	}
	at := strings.IndexByte(addr, '@')
	if at <= 0 || at == len(addr)-1 {
		return false
	}
	domain := addr[at+1:]
	return strings.Contains(domain, ".")
}

// NewSenderWithContext creates a new email sender with the provided context.
func NewSenderWithContext(ctx context.Context, cfg SenderConfig) (*Sender, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &Sender{
		snsClient:    sns.NewFromConfig(awsCfg),
		sesClient:    sesv2.NewFromConfig(awsCfg),
		topicARN:     cfg.TopicARN,
		fromEmail:    cfg.FromEmail,
		emailAddress: cfg.EmailAddress,
	}, nil
}

// NewSenderWithClients creates a new email sender with custom clients (for testing).
func NewSenderWithClients(snsClient SNSPublisher, sesClient SESEmailSender, cfg SenderConfig) *Sender {
	return &Sender{
		snsClient:    snsClient,
		sesClient:    sesClient,
		topicARN:     cfg.TopicARN,
		fromEmail:    cfg.FromEmail,
		emailAddress: cfg.EmailAddress,
	}
}

// WithMuteChecker returns a shallow copy of s with the given MuteChecker wired
// in. Callers that have a DB-backed store use this to enable per-recipient mute
// suppression on outbound SES sends.
func (s *Sender) WithMuteChecker(mc MuteChecker) *Sender {
	c := *s
	c.muteChecker = mc
	return &c
}

// WithUnsubscribeBaseURL returns a shallow copy of s with the given base URL
// set. When non-empty the sender appends List-Unsubscribe / List-Unsubscribe-Post
// headers (RFC 8058) to outbound SES messages for applicable scopes.
func (s *Sender) WithUnsubscribeBaseURL(u string) *Sender {
	c := *s
	c.unsubscribeBaseURL = u
	return &c
}

// muteKey resolves the NOTIFICATION_MUTE_SECRET HMAC key via the shared
// fail-closed policy (common.ResolveMuteSecret). A missing secret yields a nil
// key, so the send path emits no List-Unsubscribe header rather than a
// forgeable one.
func muteKey() []byte {
	key, err := common.ResolveMuteSecret()
	if err != nil {
		logging.Warnf("email: %v; List-Unsubscribe header suppressed", err)
		return nil
	}
	return key
}

// buildUnsubscribeURL constructs the one-click unsubscribe URL for the given
// (email, scope) pair. Returns "" when unsubscribeBaseURL is empty.
func (s *Sender) buildUnsubscribeURL(email, scope string) string {
	return unsubscribeURLFor(s.unsubscribeBaseURL, email, scope)
}

// listUnsubscribeHeaders returns the List-Unsubscribe and List-Unsubscribe-Post
// header values for the given (email, scope) pair (RFC 8058).
// Returns ("", "") when no base URL is configured.
func (s *Sender) listUnsubscribeHeaders(email, scope string) (headerValue, postValue string) {
	return unsubscribeHeaderValuesFor(s.unsubscribeBaseURL, email, scope)
}

// snsMaxSubjectLen is the maximum byte length SNS accepts for a Subject.
// Subjects longer than 100 bytes are rejected with InvalidParameter at runtime.
const snsMaxSubjectLen = 100

// SendNotification sends a notification email via SNS.
//
// Guard: messages whose body contains "token=" (case-insensitive) are rejected
// with ErrTokenInBroadcast. Approval tokens must never reach the SNS broadcast
// topic because every subscriber would receive a working action link. Use
// SendToEmailWithCC for any message that carries an approval URL.
//
// Subject sanitization (07-M3): the subject is passed through sanitizeHeader
// (strips CR/LF to prevent SNS parameter injection) and truncated to 100 bytes
// (SNS limit). Subjects built from user-controlled fields such as PlanName can
// otherwise cause an InvalidParameter error at publish time.
func (s *Sender) SendNotification(ctx context.Context, subject, message string) error {
	if strings.Contains(strings.ToLower(message), "token=") {
		return ErrTokenInBroadcast
	}

	if s.topicARN == "" {
		logging.Debug("No SNS topic configured, skipping email notification")
		return nil
	}

	if s.snsClient == nil {
		return fmt.Errorf("SNS client not initialized")
	}

	// Sanitize and truncate the SNS Subject (07-M3).
	safeSubject := sanitizeHeader(subject)
	if len(safeSubject) > snsMaxSubjectLen {
		safeSubject = safeSubject[:snsMaxSubjectLen]
	}

	_, err := s.snsClient.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(s.topicARN),
		Subject:  aws.String(safeSubject),
		Message:  aws.String(message),
	})
	if err != nil {
		return fmt.Errorf("failed to publish to SNS: %w", err)
	}

	logging.Debugf("Sent notification: %s", safeSubject)
	return nil
}

// SendToEmail sends an email directly to a specific email address via SES
// If SES is in sandbox mode, it will automatically verify the recipient email if needed.
func (s *Sender) SendToEmail(ctx context.Context, toEmail, subject, body string) error {
	return s.SendToEmailWithCC(ctx, toEmail, nil, subject, body)
}

// SendToEmailWithCCMultipart is the multipart/alternative variant of
// SendToEmailWithCC: callers pass both a plain-text body and an HTML body,
// SES emits a multipart message, and the recipient's mail client picks
// whichever rendering it supports. Mirrors SendToEmailWithCC for the To/Cc
// dedupe + sandbox-recipient verification — the only delta is the email
// content shape. htmlBody == "" degrades to a single-part text send so
// callers that don't have an HTML body don't need a separate code path.
func (s *Sender) SendToEmailWithCCMultipart(ctx context.Context, toEmail string, ccEmails []string, subject, textBody, htmlBody string) error {
	if htmlBody == "" {
		return s.SendToEmailWithCC(ctx, toEmail, ccEmails, subject, textBody)
	}
	if !isValidFromEmail(s.fromEmail) {
		return ErrNoFromEmail
	}
	if s.sesClient == nil {
		return fmt.Errorf("SES client not initialized")
	}
	if err := s.ensureSandboxRecipientVerified(ctx, toEmail); err != nil {
		return err
	}

	cc := dedupeCCAgainstTo(toEmail, ccEmails)

	input := buildSESSendEmailInputMultipart(s.fromEmail, toEmail, cc, subject, textBody, htmlBody, nil)

	_, err := s.sesClient.SendEmail(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to send email via SES: %w", err)
	}

	if len(cc) > 0 {
		logging.Debugf("Sent multipart email to %s (cc %d): %s", redact.Email(toEmail), len(cc), subject)
	} else {
		logging.Debugf("Sent multipart email to %s: %s", redact.Email(toEmail), subject)
	}
	return nil
}

// SendToEmailWithCC sends an email with a primary To recipient plus optional
// Cc recipients. The To recipient is treated as the authorized actor for the
// message (verified in sandbox mode) and Cc recipients are informed of the
// action without carrying the "you must do something" burden. Duplicate
// entries across To/Cc are stripped so a single inbox is never addressed
// twice.
func (s *Sender) SendToEmailWithCC(ctx context.Context, toEmail string, ccEmails []string, subject, body string) error {
	// Empty or malformed FROM_EMAIL means nothing can go out. Return
	// ErrNoFromEmail rather than nil so callers (password reset, invites)
	// don't report a send that never happened (#270).
	if !isValidFromEmail(s.fromEmail) {
		return ErrNoFromEmail
	}
	if s.sesClient == nil {
		return fmt.Errorf("SES client not initialized")
	}
	if err := s.ensureSandboxRecipientVerified(ctx, toEmail); err != nil {
		return err
	}

	cc := dedupeCCAgainstTo(toEmail, ccEmails)

	input := buildSESSendEmailInput(s.fromEmail, toEmail, cc, subject, body, nil)

	_, err := s.sesClient.SendEmail(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to send email via SES: %w", err)
	}

	if len(cc) > 0 {
		logging.Debugf("Sent email to %s (cc %d): %s", redact.Email(toEmail), len(cc), subject)
	} else {
		logging.Debugf("Sent email to %s: %s", redact.Email(toEmail), subject)
	}
	return nil
}

// addListUnsubscribeHeaders returns the RFC 8058 List-Unsubscribe pair as
// sesv2 MessageHeader values. Returns nil when headerValue is empty.
func addListUnsubscribeHeaders(headerValue, postValue string) []types.MessageHeader {
	if headerValue == "" {
		return nil
	}
	hdrs := []types.MessageHeader{
		{Name: aws.String("List-Unsubscribe"), Value: aws.String(headerValue)},
	}
	if postValue != "" {
		hdrs = append(hdrs, types.MessageHeader{
			Name:  aws.String("List-Unsubscribe-Post"),
			Value: aws.String(postValue),
		})
	}
	return hdrs
}

// sendToEmailWithCCMultipartHeaders is the internal variant of
// SendToEmailWithCCMultipart that also accepts custom message headers (e.g.
// List-Unsubscribe). It is used by the mute-aware send path in
// SendPurchaseApprovalRequest so we don't expose a wider public API.
func (s *Sender) sendToEmailWithCCMultipartHeaders(
	ctx context.Context,
	toEmail string,
	ccEmails []string,
	subject, textBody, htmlBody string,
	extraHeaders []types.MessageHeader,
) error {
	if htmlBody == "" {
		// Degrade to plain text; headers still carried via the non-multipart path.
		return s.sendToEmailWithCCHeaders(ctx, toEmail, ccEmails, subject, textBody, extraHeaders)
	}
	if s.fromEmail == "" {
		logging.Debug("No from email configured, skipping direct email")
		return nil
	}
	if s.sesClient == nil {
		return fmt.Errorf("SES client not initialized")
	}
	if err := s.ensureSandboxRecipientVerified(ctx, toEmail); err != nil {
		return err
	}
	cc := dedupeCCAgainstTo(toEmail, ccEmails)
	input := buildSESSendEmailInputMultipart(s.fromEmail, toEmail, cc, subject, textBody, htmlBody, extraHeaders)
	if _, err := s.sesClient.SendEmail(ctx, input); err != nil {
		return fmt.Errorf("failed to send email via SES: %w", err)
	}
	if len(cc) > 0 {
		logging.Debugf("Sent multipart email to %s (cc %d): %s", redact.Email(toEmail), len(cc), subject)
	} else {
		logging.Debugf("Sent multipart email to %s: %s", redact.Email(toEmail), subject)
	}
	return nil
}

// sendToEmailWithCCHeaders is the plain-text variant of
// sendToEmailWithCCMultipartHeaders.
func (s *Sender) sendToEmailWithCCHeaders(
	ctx context.Context,
	toEmail string,
	ccEmails []string,
	subject, body string,
	extraHeaders []types.MessageHeader,
) error {
	if s.fromEmail == "" {
		logging.Debug("No from email configured, skipping direct email")
		return nil
	}
	if s.sesClient == nil {
		return fmt.Errorf("SES client not initialized")
	}
	if err := s.ensureSandboxRecipientVerified(ctx, toEmail); err != nil {
		return err
	}
	cc := dedupeCCAgainstTo(toEmail, ccEmails)
	input := buildSESSendEmailInput(s.fromEmail, toEmail, cc, subject, body, extraHeaders)
	if _, err := s.sesClient.SendEmail(ctx, input); err != nil {
		return fmt.Errorf("failed to send email via SES: %w", err)
	}
	if len(cc) > 0 {
		logging.Debugf("Sent email to %s (cc %d): %s", redact.Email(toEmail), len(cc), subject)
	} else {
		logging.Debugf("Sent email to %s: %s", redact.Email(toEmail), subject)
	}
	return nil
}

// dedupeCCAgainstTo returns cc with the to-address removed (case-insensitive)
// and duplicate entries collapsed, preserving input order. Empty strings are
// dropped so a caller can freely pass optional slots without sanitizing.
func dedupeCCAgainstTo(to string, cc []string) []string {
	if len(cc) == 0 {
		return nil
	}
	seen := map[string]bool{strings.ToLower(strings.TrimSpace(to)): true}
	out := make([]string, 0, len(cc))
	for _, addr := range cc {
		norm := strings.ToLower(strings.TrimSpace(addr))
		if norm == "" || seen[norm] {
			continue
		}
		seen[norm] = true
		out = append(out, addr)
	}
	return out
}
