package config

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// HashApprovalToken returns the SHA-256 hex digest of a raw approval /
// revocation / rejection token, in the exact format persisted to the
// approval_token column (purchase_executions, ri_exchange_history).
// Mirrors internal/auth's unexported hashSessionToken so every long-lived
// secret token in this codebase is hashed the same way before it touches
// storage (issue #103: these columns previously held the raw, directly
// usable secret).
//
// Empty input returns "" so "no token" continues to mean an empty/NULL
// column value rather than the hash of an empty string -- callers must not
// treat HashApprovalToken("") as a valid stored value.
func HashApprovalToken(rawToken string) string {
	if rawToken == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}

// ApprovalTokenMatches reports whether rawSupplied is the token that hashes
// to storedHash, using a constant-time comparison of the two fixed-length
// hex digests (so neither the digest length nor a byte-by-byte early exit
// leaks timing information). Returns false whenever either side is empty --
// an empty stored hash (no token set) must never be treated as satisfied by
// an empty supplied token.
func ApprovalTokenMatches(storedHash, rawSupplied string) bool {
	if storedHash == "" || rawSupplied == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(HashApprovalToken(rawSupplied))) == 1
}
