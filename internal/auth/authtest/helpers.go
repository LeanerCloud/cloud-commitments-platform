package authtest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// csrfKey is a fixed 32-byte key shared by test services so tests can
// reproduce the CSRF token without a real key-generation path.
var csrfKey = []byte("test-csrf-key-32-bytes-padded---")

// TestCSRFKey returns the fixed 32-byte CSRF key shared by test services.
// Pass it as auth.ServiceConfig.CSRFKey so a service built via auth.NewService
// derives CSRF tokens deterministically, letting tests reproduce the expected
// token with DeriveTestCSRFToken.
func TestCSRFKey() []byte {
	return append([]byte(nil), csrfKey...)
}

// TestPasswordHash returns a bcrypt hash of password at bcrypt.MinCost, for
// fixtures that need a User.PasswordHash the service will accept without the
// production work factor.
func TestPasswordHash(t testing.TB, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	return string(hash)
}

// DeriveTestCSRFToken returns the CSRF token a service configured with
// TestCSRFKey expects for rawSessionToken: hex(HMAC-SHA256(key, token)). It
// re-derives the value independently, so a change to the production
// derivation fails the tests that use it.
func DeriveTestCSRFToken(rawSessionToken string) string {
	mac := hmac.New(sha256.New, csrfKey)
	mac.Write([]byte(rawSessionToken))
	return hex.EncodeToString(mac.Sum(nil))
}
