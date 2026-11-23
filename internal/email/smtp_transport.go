package email

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
)

// dispatchSMTP runs the SMTP conversation, routing through STARTTLS when
// s.useTLS is true. The rcpts list carries every address that should receive
// the message (To + Cc). ctx bounds the whole conversation (see sendMail).
//
// Security (07-H2): if credentials are configured but TLS is disabled the
// call is refused unless AllowInsecure was explicitly set. Sending SMTP AUTH
// over a non-TLS connection exposes credentials (SendGrid API key, Azure ACS
// password) and token-bearing message bodies in cleartext. AllowInsecure must
// never be set in production; it exists solely for integration tests against a
// local plaintext stub server.
func (s *SMTPSender) dispatchSMTP(ctx context.Context, rcpts []string, msg []byte) error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	var auth smtp.Auth
	if s.username != "" && s.password != "" {
		auth = smtp.PlainAuth("", s.username, s.password, s.host)
	}
	if auth != nil && !s.useTLS && !s.allowInsecure {
		return fmt.Errorf("SMTP auth over non-TLS connection is refused: set UseTLS=true or, for test-only stubs, AllowInsecure=true")
	}
	if err := s.sendMail(ctx, addr, auth, s.fromEmail, rcpts, msg); err != nil {
		return fmt.Errorf("failed to send email via SMTP: %w", err)
	}
	return nil
}

// smtpAuthenticate performs SMTP AUTH with user-friendly 535 error translation.
func smtpAuthenticate(c *smtp.Client, auth smtp.Auth) error {
	if auth == nil {
		return nil
	}
	// Same refusal smtp.SendMail made, so a server without AUTH fails fast.
	if ok, _ := c.Extension("AUTH"); !ok {
		return errors.New("smtp: server doesn't support AUTH")
	}
	if err := c.Auth(auth); err != nil {
		if strings.Contains(err.Error(), "535") {
			return fmt.Errorf("SMTP authentication failed - check username/password")
		}
		return err
	}
	return nil
}

// smtpSendBody performs the MAIL/RCPT/DATA sequence on an already-authenticated client.
func smtpSendBody(c *smtp.Client, from string, to []string, msg []byte) error {
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, addr := range to {
		if err := c.Rcpt(addr); err != nil {
			return err
		}
	}

	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err = w.Write(msg); err != nil {
		return err
	}
	return w.Close()
}

// sendMail sends msg over one SMTP connection that ctx bounds (issue #1433).
// net/smtp's Dial and SendMail take no context and set no deadline, so a mail
// server that accepted the TCP connection and then stopped answering held the
// caller until the OS gave up, well past the 30s failure-alert budget. Here
// the dial honors ctx, and once ctx is done (deadline or cancel) the
// connection's deadline is moved into the past, which fails any read or write
// in flight, TLS handshake included. The error then wraps ctx.Err().
//
// With s.useTLS the server must offer STARTTLS. Without it (AllowInsecure test
// stubs, or no credentials), STARTTLS is used when the server offers it, as
// smtp.SendMail did.
func (s *SMTPSender) sendMail(ctx context.Context, addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	stop := context.AfterFunc(ctx, func() {
		// SetDeadline only fails on a closed connection, which has nothing
		// left to unblock.
		if err := conn.SetDeadline(time.Unix(1, 0)); err != nil {
			logging.Debugf("SMTP: expire connection deadline: %v", err)
		}
	})
	defer stop()

	if err := smtpConverse(conn, host, s.useTLS, auth, from, to, msg); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%w: %w", ctxErr, err)
		}
		return err
	}
	return nil
}

// smtpConverse runs greeting, STARTTLS, AUTH, MAIL/RCPT/DATA and QUIT on conn.
func smtpConverse(conn net.Conn, host string, requireTLS bool, auth smtp.Auth, from string, to []string, msg []byte) error {
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok || requireTLS {
		// MinVersion guards against TLS 1.0/1.1 negotiation (issue #410).
		if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}

	if err := smtpAuthenticate(c, auth); err != nil {
		return err
	}

	if err := smtpSendBody(c, from, to, msg); err != nil {
		return err
	}

	return c.Quit()
}
