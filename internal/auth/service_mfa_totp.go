package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- SHA-1 retained for broad authenticator app compatibility and existing otpauth provisioning; RFC 6238 permits SHA-256/SHA-512 but most authenticator apps default to SHA-1
	"crypto/subtle"
	"encoding/base32"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// verifyTOTP validates a TOTP code against the secret.
// Implements RFC 6238 TOTP with 30-second time steps.
// Uses constant-time comparison to prevent timing attacks.
//
// Fails closed: returns false immediately when code or secret is empty.
// An empty code can arrive from a missing form field; an empty secret signals
// a data-integrity anomaly (MFA enabled but no secret stored). In both cases
// the correct behavior is to reject, not to accidentally match because
// generateTOTP("","") returns "" and ConstantTimeCompare("","") == 1.
func verifyTOTP(secret, code string) bool {
	_, ok := matchTOTP(secret, code)
	return ok
}

// matchTOTP is verifyTOTP that also returns the matched time-step counter, so
// a caller can record it and refuse the same code afterwards (RFC 6238 5.2).
// When two steps in the window produce the same code the later counter wins.
func matchTOTP(secret, code string) (int64, bool) {
	// Fail closed: reject empty code and empty secret up front.
	// generateTOTP returns "" on base32-decode failure; ConstantTimeCompare("","")
	// evaluates to 1 and would bypass MFA for any caller that passes code="".
	if code == "" || secret == "" {
		return 0, false
	}

	// Allow for time skew by checking current and adjacent time windows.
	currentTime := time.Now().Unix()
	timeStep := int64(config.MFATimeStep)

	// Check current time step and one step before/after for clock skew tolerance.
	// Use constant-time comparison and avoid early returns to prevent timing attacks.
	var matched int64
	found := 0
	for _, offset := range []int64{-1, 0, 1} {
		counter := (currentTime / timeStep) + offset
		expected := generateTOTP(secret, counter)
		// generateTOTP returns "" on base32-decode failure; a non-empty code can
		// never equal "" so a bad secret produces no match.
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			matched = counter
			found = 1
		}
	}
	return matched, found == 1
}

// verifyAndClaimTOTP accepts a TOTP code at most once per user: the matched
// counter must be above the last one the store accepted. A replayed or
// concurrently raced code returns false with no error.
func (s *Service) verifyAndClaimTOTP(ctx context.Context, userID, secret, code string) (bool, error) {
	counter, ok := matchTOTP(secret, code)
	if !ok {
		return false, nil
	}
	return s.store.ClaimTOTPCounter(ctx, userID, counter)
}

// generateTOTP generates a TOTP code for the given counter.
func generateTOTP(secret string, counter int64) string {
	// Decode base32 secret
	secretBytes, err := base32Decode(secret)
	if err != nil {
		return ""
	}

	// Convert counter to 8-byte big-endian
	counterBytes := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		counterBytes[i] = byte(counter & 0xff)
		counter >>= 8
	}

	// Compute HMAC-SHA1
	h := hmac.New(sha1.New, secretBytes)
	h.Write(counterBytes)
	hash := h.Sum(nil)

	// Dynamic truncation (RFC 4226)
	offset := hash[len(hash)-1] & 0x0f
	binary := (int32(hash[offset])&0x7f)<<24 |
		(int32(hash[offset+1])&0xff)<<16 |
		(int32(hash[offset+2])&0xff)<<8 |
		(int32(hash[offset+3]) & 0xff)

	// Generate 6-digit code
	otp := binary % 1000000
	return fmt.Sprintf("%06d", otp)
}

// base32Decode decodes a base32 string (RFC 4648) using the stdlib encoder.
func base32Decode(s string) ([]byte, error) {
	// Normalize: uppercase and add padding if needed
	s = strings.ToUpper(s)
	if m := len(s) % 8; m != 0 {
		s += strings.Repeat("=", 8-m)
	}
	return base32.StdEncoding.DecodeString(s)
}

// generateMFASecret produces a fresh 160-bit (20-byte) secret encoded
// as unpadded base32. 160 bits matches the RFC 4226 / Google
// Authenticator recommendation and yields a 32-character secret that
// fits in one screen of an authenticator-app entry.
func generateMFASecret() (string, error) {
	bytes := make([]byte, 20)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to read random bytes for MFA secret: %w", err)
	}
	// Use the no-padding encoding — authenticator apps reject '='
	// padding and so does the verifier above (base32Decode adds the
	// padding it needs back at decode time).
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(bytes), nil
}

// buildProvisioningURI constructs the otpauth:// URI authenticator
// apps consume from a scanned QR code or paste. RFC sketch in
// https://github.com/google/google-authenticator/wiki/Key-Uri-Format.
//
// The label encodes the issuer twice — once as the path prefix
// ("CUDly:user@example.com") and once as the issuer query param —
// because legacy apps read one or the other. Algorithm/digits/period
// are spelled out even though they're the defaults so older apps that
// don't assume the defaults still work.
func buildProvisioningURI(accountEmail, secret string) string {
	label := url.PathEscape(mfaIssuer + ":" + accountEmail)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", mfaIssuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return fmt.Sprintf("otpauth://totp/%s?%s", label, q.Encode())
}
