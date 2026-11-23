package email

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// sanitizeCCList dedupes cc against toEmail and applies header
// sanitization to each surviving entry.
func sanitizeCCList(toEmail string, ccEmails []string) []string {
	cc := dedupeCCAgainstTo(toEmail, ccEmails)
	if len(cc) == 0 {
		return nil
	}
	out := make([]string, 0, len(cc))
	for _, addr := range cc {
		out = append(out, sanitizeHeader(addr))
	}
	return out
}

// mimeRandBoundary returns a per-message random MIME boundary (07-N2).
// Using a fixed constant boundary risks corruption if a message body
// literally contains that string. crypto/rand gives a collision-free 8-byte
// hex suffix; falls back to a fixed literal on the (exceptional) rand failure
// so messages still deliver in degraded environments.
func mimeRandBoundary() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "cudly-mp-fallback-7e3b1c89af04d2"
	}
	return "cudly-mp-" + hex.EncodeToString(b)
}

// buildSMTPMessageMultipart assembles a multipart/alternative RFC-5322
// message with both a plain-text and an HTML part. A per-message random
// boundary is generated via mimeRandBoundary to eliminate the theoretical
// body-collision risk of a fixed literal (07-N2). `cc` is already sanitized
// and deduped by the caller.
func (s *SMTPSender) buildSMTPMessageMultipart(toEmail string, cc []string, subject, textBody, htmlBody string) []byte {
	boundary := mimeRandBoundary()
	from := s.fromEmail
	if s.fromName != "" {
		from = fmt.Sprintf("%s <%s>", sanitizeHeader(s.fromName), s.fromEmail)
	}
	headers := fmt.Sprintf("From: %s\r\nTo: %s\r\n", from, toEmail)
	if len(cc) > 0 {
		headers += fmt.Sprintf("Cc: %s\r\n", strings.Join(cc, ", "))
	}
	headers += fmt.Sprintf("Subject: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n", subject, boundary)

	return []byte(headers + buildMultipartBody(boundary, textBody, htmlBody))
}

// buildSMTPMessageMultipartWithHeaders is buildSMTPMessageMultipart with extra
// pre-sanitized header lines (e.g. List-Unsubscribe) inserted before the
// header-terminating blank line. extraHeaders must already be CRLF-terminated.
// A per-message random boundary is generated via mimeRandBoundary to avoid
// the body-collision risk of a fixed literal (07-N2).
func (s *SMTPSender) buildSMTPMessageMultipartWithHeaders(toEmail string, cc []string, subject, textBody, htmlBody, extraHeaders string) []byte {
	boundary := mimeRandBoundary()
	from := s.fromEmail
	if s.fromName != "" {
		from = fmt.Sprintf("%s <%s>", sanitizeHeader(s.fromName), s.fromEmail)
	}
	headers := fmt.Sprintf("From: %s\r\nTo: %s\r\n", from, toEmail)
	if len(cc) > 0 {
		headers += fmt.Sprintf("Cc: %s\r\n", strings.Join(cc, ", "))
	}
	headers += fmt.Sprintf("Subject: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%q\r\n", subject, boundary)
	headers += extraHeaders
	headers += "\r\n"

	return []byte(headers + buildMultipartBody(boundary, textBody, htmlBody))
}

// buildMultipartBody assembles the multipart/alternative body (text + HTML
// parts) for the given boundary. Shared by the plain and header-aware builders.
func buildMultipartBody(boundary, textBody, htmlBody string) string {
	var body strings.Builder
	body.WriteString("--")
	body.WriteString(boundary)
	body.WriteString("\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	body.WriteString(textBody)
	body.WriteString("\r\n--")
	body.WriteString(boundary)
	body.WriteString("\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	body.WriteString(htmlBody)
	body.WriteString("\r\n--")
	body.WriteString(boundary)
	body.WriteString("--\r\n")

	return body.String()
}

// buildSMTPMessageWithHeaders is buildSMTPMessage with extra pre-sanitized
// header lines (e.g. List-Unsubscribe) inserted before the header-terminating
// blank line. extraHeaders must already be CRLF-terminated.
func (s *SMTPSender) buildSMTPMessageWithHeaders(toEmail string, cc []string, subject, body, extraHeaders string) []byte {
	from := s.fromEmail
	if s.fromName != "" {
		from = fmt.Sprintf("%s <%s>", sanitizeHeader(s.fromName), s.fromEmail)
	}
	headers := fmt.Sprintf("From: %s\r\nTo: %s\r\n", from, toEmail)
	if len(cc) > 0 {
		headers += fmt.Sprintf("Cc: %s\r\n", strings.Join(cc, ", "))
	}
	headers += fmt.Sprintf("Subject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n", subject)
	headers += extraHeaders
	headers += "\r\n"
	return []byte(headers + body + "\r\n")
}

// buildSMTPMessage assembles the RFC-5322 message bytes (headers + blank
// line + body) for sending via SMTP. `cc` is already sanitized and
// deduped by the caller.
func (s *SMTPSender) buildSMTPMessage(toEmail string, cc []string, subject, body string) []byte {
	from := s.fromEmail
	if s.fromName != "" {
		from = fmt.Sprintf("%s <%s>", sanitizeHeader(s.fromName), s.fromEmail)
	}
	headers := fmt.Sprintf("From: %s\r\nTo: %s\r\n", from, toEmail)
	if len(cc) > 0 {
		headers += fmt.Sprintf("Cc: %s\r\n", strings.Join(cc, ", "))
	}
	headers += fmt.Sprintf("Subject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n", subject)
	return []byte(headers + body + "\r\n")
}
