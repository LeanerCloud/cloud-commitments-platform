// Package redact masks personal data before it reaches a log line.
package redact

import "strings"

// Email masks an email address for logs: the local part keeps at most two
// characters and the domain host keeps at most two, the TLD stays.
// "user@example.com" -> "us***@ex***.com". Anything that does not look like
// an address becomes "***".
func Email(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return "***"
	}
	local := maskPrefix(email[:at])
	domain := email[at+1:]
	dot := strings.LastIndex(domain, ".")
	if dot <= 0 {
		return local + "@***"
	}
	return local + "@" + maskPrefix(domain[:dot]) + domain[dot:]
}

func maskPrefix(s string) string {
	if len(s) <= 2 {
		return "***"
	}
	return s[:2] + "***"
}
