package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"golang.org/x/crypto/bcrypt"
)

// mfaPendingExpiry is how long a setup-but-not-yet-enabled MFA secret
// stays valid before the user has to start over. Short window because
// the secret travels over the wire on setup and an abandoned
// enrollment shouldn't leave a usable secret lingering.
const mfaPendingExpiry = 5 * time.Minute

// mfaIssuer is the issuer label that appears in the authenticator app
// alongside the account email. Apps surface this in the entry name
// and the 6-digit code header so the user can tell which CUDly
// instance the code is for when they have multiple.
const mfaIssuer = "CUDly"

// recoveryCodeCount is the number of single-use recovery codes
// generated per enrollment / regeneration. 10 matches the Google /
// GitHub / GitLab default and is the smallest count that doesn't
// regularly leave a user locked out after losing one or two codes.
const recoveryCodeCount = 10

// recoveryCodeLength is the visible (formatted) length of a recovery
// code, before the dash. Codes are rendered as `XXXX-XXXX` so the
// user can read them aloud or type them with the dash for grouping;
// the dash is stripped on validation so an entered code is
// case-insensitively matched against the bcrypt hash.
const recoveryCodeLength = 8

// recoveryCodeAlphabet is a Crockford-style alphabet without
// confusable characters (no 0/O, 1/I/L). Recovery codes are
// shown to humans once and typed back, so visual clarity matters.
const recoveryCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// generateRecoveryCodes produces n plaintext recovery codes formatted
// as `XXXX-XXXX` for readability. Returns the plaintext slice — the
// caller is responsible for hashing for storage and surfacing the
// plaintext to the user exactly once.
func generateRecoveryCodes(n int) ([]string, error) {
	return generateRecoveryCodesFrom(rand.Reader, n)
}

// recoveryCodeByteLimit is the largest multiple of the alphabet size that fits
// in a byte. A byte at or above it is redrawn, so every character is equally
// likely; folding it with % would favor the first 256%31 characters (#1768).
const recoveryCodeByteLimit = 256 - 256%len(recoveryCodeAlphabet)

// generateRecoveryCodesFrom is generateRecoveryCodes drawing from src.
func generateRecoveryCodesFrom(src io.Reader, n int) ([]string, error) {
	out := make([]string, 0, n)
	b := make([]byte, 1)
	for i := 0; i < n; i++ {
		var sb strings.Builder
		sb.Grow(recoveryCodeLength + 1)
		for j := 0; j < recoveryCodeLength; {
			if _, err := io.ReadFull(src, b); err != nil {
				return nil, fmt.Errorf("failed to read random bytes for recovery code: %w", err)
			}
			if int(b[0]) >= recoveryCodeByteLimit {
				continue
			}
			if j == recoveryCodeLength/2 {
				sb.WriteByte('-')
			}
			sb.WriteByte(recoveryCodeAlphabet[int(b[0])%len(recoveryCodeAlphabet)])
			j++
		}
		out = append(out, sb.String())
	}
	return out, nil
}

// normalizeRecoveryCode strips formatting (dashes, spaces) and
// uppercases the input so a code entered with or without the visual
// dash separator hashes to the same bcrypt-comparable string.
func normalizeRecoveryCode(code string) string {
	var sb strings.Builder
	sb.Grow(len(code))
	for _, r := range code {
		if r == '-' || r == ' ' {
			continue
		}
		sb.WriteRune(r)
	}
	return strings.ToUpper(sb.String())
}

// hashRecoveryCode bcrypt-hashes a recovery code with the auth
// service's configured cost. Uses the same cost knob as password
// hashing so unit tests can drop to MinCost.
func (s *Service) hashRecoveryCode(code string) (string, error) {
	cost := bcryptCost
	if s.bcryptCostOverride > 0 {
		cost = s.bcryptCostOverride
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(normalizeRecoveryCode(code)), cost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// consumeRecoveryCode returns true and removes the matching hash from
// user.MFARecoveryCodes if entered matches any stored hash. The
// removal is what makes recovery codes single-use; the caller must
// persist the shortened slice through its own compare-and-swap write.
// The removal shifts the slice in place, so a caller that still needs
// the codes as read must clone them first.
//
// Fails closed on empty input: an empty string normalizes to "", which
// bcrypt.CompareHashAndPassword would compare against every stored hash.
// Reject it immediately to avoid leaking which hashes exist.
//
// Iterates over all stored hashes (bcrypt compare is constant-time
// for the matching hash but not across the slice — recovery codes
// are 8 chars of high-entropy alphabet, so timing leakage of which
// slot matched isn't usefully exploitable, but the comparison stays
// branch-free by ORing the bool into a single accumulator).
func (s *Service) consumeRecoveryCode(user *User, entered string) bool {
	normalized := normalizeRecoveryCode(entered)
	// Fail closed: reject empty input before any bcrypt comparison.
	if normalized == "" {
		return false
	}
	matchIdx := -1
	for i, hash := range user.MFARecoveryCodes {
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(normalized)); err == nil {
			matchIdx = i
			// Don't break — keep comparing the rest to equalize CPU
			// time across the slice. Recovery codes are typed by a
			// human one at a time, so the few-millisecond budget is
			// dwarfed by network latency anyway.
		}
	}
	if matchIdx < 0 {
		return false
	}
	// Remove the matched hash. Order-preserving delete; the slice is
	// small (10 entries) so the shift cost is negligible.
	user.MFARecoveryCodes = append(user.MFARecoveryCodes[:matchIdx], user.MFARecoveryCodes[matchIdx+1:]...)
	return true
}

// MFASetupResult is the user-facing payload returned by MFASetup.
// Carries the freshly-generated secret + the otpauth:// URI so the
// frontend can render a QR code and surface the secret for manual
// entry. The secret is also persisted server-side as the pending
// secret, so a stateless client-side carrier (signed token) is not
// needed.
type MFASetupResult struct {
	Secret          string //nolint:gosec // G117: intentional one-time MFA secret response -- returned to the caller exactly once during enrollment setup; persisted separately and not re-serialized
	ProvisioningURI string
}

// MFASetup begins an MFA enrollment for a user. The caller must
// re-verify the user's password (defense-in-depth against a session
// token being lifted from another tab). Returns the freshly-generated
// secret + provisioning URI; persists the secret in the user's
// pending fields with a short expiry. Does NOT flip MFAEnabled —
// that happens in MFAEnable after the user proves they have the
// secret loaded in their authenticator.
//
// Safe to call repeatedly while MFA is off: each call overwrites the
// previous pending secret and resets the expiry. Refused while MFA is
// on, so replacing a factor needs MFADisable's proof first (issue #227).
func (s *Service) MFASetup(ctx context.Context, userID, password string) (*MFASetupResult, error) {
	if err := s.ensureStore(); err != nil {
		return nil, err
	}
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("%w", ErrMFAAuthFailed)
	}
	if user == nil {
		return nil, fmt.Errorf("%w", ErrMFAAuthFailed)
	}
	if !s.verifyPassword(password, user.PasswordHash) {
		return nil, fmt.Errorf("%w", ErrMFAInvalidPassword)
	}
	if user.MFAEnabled {
		return nil, fmt.Errorf("%w", ErrMFAAlreadyEnabled)
	}

	secret, err := generateMFASecret()
	if err != nil {
		return nil, err
	}

	if err := s.store.SetPendingMFASecret(ctx, user.ID, secret, time.Now().Add(mfaPendingExpiry)); err != nil {
		return nil, err
	}

	return &MFASetupResult{
		Secret:          secret,
		ProvisioningURI: buildProvisioningURI(user.Email, secret),
	}, nil
}

// generateAndHashRecoveryCodes mints a fresh batch of plaintext
// recovery codes and returns both the plaintext slice (for the user)
// and the bcrypt-hashed slice (for storage). Pulled out as a helper
// so MFAEnable and MFARegenerateRecoveryCodes don't duplicate the
// loop.
func (s *Service) generateAndHashRecoveryCodes() (plaintext, hashes []string, err error) {
	plaintext, err = generateRecoveryCodes(recoveryCodeCount)
	if err != nil {
		return nil, nil, err
	}
	hashes = make([]string, 0, len(plaintext))
	for _, c := range plaintext {
		h, hashErr := s.hashRecoveryCode(c)
		if hashErr != nil {
			return nil, nil, hashErr
		}
		hashes = append(hashes, h)
	}
	return plaintext, hashes, nil
}

// validatePendingMFAEnrollment checks that a pending enrollment
// exists, hasn't expired, and the supplied code matches, returning the
// matched TOTP counter. It writes nothing: the next setup overwrites an
// expired pending secret.
func validatePendingMFAEnrollment(user *User, code string) (int64, error) {
	if user.MFAPendingSecret == "" || user.MFAPendingSecretExpiresAt == nil {
		return 0, fmt.Errorf("%w", ErrMFANoEnrollmentInProgress)
	}
	if time.Now().After(*user.MFAPendingSecretExpiresAt) {
		return 0, fmt.Errorf("%w", ErrMFAEnrollmentExpired)
	}
	counter, ok := matchTOTP(user.MFAPendingSecret, code)
	if !ok {
		return 0, fmt.Errorf("%w", ErrMFAInvalidCode)
	}
	return counter, nil
}

// MFAEnable finalizes an MFA enrollment. Validates the supplied TOTP
// code against the pending secret, then promotes the pending secret
// to the active MFASecret and flips MFAEnabled = true. Generates +
// returns plaintext recovery codes; stores bcrypt hashes server-side.
// The plaintext is returned exactly once.
//
// Errors out (without changing state) when:
//   - no pending enrollment exists
//   - the pending secret has expired
//   - the supplied code doesn't match the pending secret
//
// A user whose MFA is already on gets ErrMFAAlreadyEnabled, never a
// silent re-enable with a new secret or new recovery codes.
func (s *Service) MFAEnable(ctx context.Context, userID, code string) ([]string, error) {
	if err := s.ensureStore(); err != nil {
		return nil, err
	}
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil || user == nil {
		return nil, fmt.Errorf("%w", ErrMFAAuthFailed)
	}
	if lockErr := s.rejectLockedMFAUser(user); lockErr != nil {
		return nil, lockErr
	}
	if user.MFAEnabled {
		return nil, fmt.Errorf("%w", ErrMFAAlreadyEnabled)
	}
	counter, err := validatePendingMFAEnrollment(user, code)
	if errors.Is(err, ErrMFAInvalidCode) {
		return nil, s.rejectMFACode(ctx, user, code)
	}
	if err != nil {
		return nil, err
	}

	plaintext, hashes, err := s.generateAndHashRecoveryCodes()
	if err != nil {
		return nil, err
	}
	// EnableMFA records the enrollment code's counter in the same write, so the
	// code typed to turn MFA on cannot be replayed at Login inside its window
	// (#442).
	if err := s.store.EnableMFA(ctx, user.ID, user.MFAPendingSecret, counter, hashes); err != nil {
		return nil, err
	}
	return plaintext, nil
}

// disableMFAAlreadyOff is the idempotent path for MFADisable: the
// user already has MFA off, so we only need to clear any stale
// pending fields.
func (s *Service) disableMFAAlreadyOff(ctx context.Context, user *User) error {
	return s.store.ClearPendingMFASecret(ctx, user.ID)
}

// rejectLockedMFAUser refuses MFA lifecycle calls while the account is locked
// by failed attempts, using the same opaque error as a missing user so a
// stolen session learns nothing about the lock (same stance as Login).
func (s *Service) rejectLockedMFAUser(user *User) error {
	if user.LockedUntil != nil && time.Now().Before(*user.LockedUntil) {
		logging.Warnf("MFA operation refused for locked account (locked for %v more)", time.Until(*user.LockedUntil).Round(time.Minute))
		return fmt.Errorf("%w", ErrMFAAuthFailed)
	}
	return nil
}

// rejectMFACode counts a wrong or replayed code against the account lockout,
// the same counter Login uses, and returns the invalid-code error. An empty
// code is a missing field, not a guess, and (as in Login) is not counted.
func (s *Service) rejectMFACode(ctx context.Context, user *User, code string) error {
	if code != "" {
		s.recordFailedLogin(ctx, user)
	}
	return fmt.Errorf("%w", ErrMFAInvalidCode)
}

// claimTOTPOrReject spends an unused TOTP code for the user, counting a wrong
// or replayed one against the lockout.
func (s *Service) claimTOTPOrReject(ctx context.Context, user *User, code string) error {
	valid, err := s.verifyAndClaimTOTP(ctx, user.ID, user.MFASecret, code)
	if err != nil {
		return fmt.Errorf("failed to record TOTP use: %w", err)
	}
	if !valid {
		return s.rejectMFACode(ctx, user, code)
	}
	return nil
}

// verifyMFAProof accepts an unspent TOTP code, or else consumes a recovery code
// in memory; the caller persists the consumed slice through its own CAS write.
func (s *Service) verifyMFAProof(ctx context.Context, user *User, codeOrRecovery string) error {
	matched, err := s.verifyAndClaimTOTP(ctx, user.ID, user.MFASecret, codeOrRecovery)
	if err != nil {
		return fmt.Errorf("failed to record TOTP use: %w", err)
	}
	if !matched && !s.consumeRecoveryCode(user, codeOrRecovery) {
		return s.rejectMFACode(ctx, user, codeOrRecovery)
	}
	return nil
}

// MFADisable turns off MFA for a user. Requires both the current
// password AND a fresh proof-of-possession (either a TOTP code or
// an unused recovery code). Defense-in-depth: a stolen session
// alone shouldn't disable MFA, and a stolen authenticator alone
// shouldn't either.
//
// Idempotent: calling on an already-disabled user with the right
// password is a no-op (returns nil) so the UI can drive the button
// without an extra state-query round trip.
func (s *Service) MFADisable(ctx context.Context, userID, password, codeOrRecovery string) error {
	if err := s.ensureStore(); err != nil {
		return err
	}
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil || user == nil {
		return fmt.Errorf("%w", ErrMFAAuthFailed)
	}
	if lockErr := s.rejectLockedMFAUser(user); lockErr != nil {
		return lockErr
	}
	if !s.verifyPassword(password, user.PasswordHash) {
		return fmt.Errorf("%w", ErrMFAInvalidPassword)
	}
	if !user.MFAEnabled {
		return s.disableMFAAlreadyOff(ctx, user)
	}
	if codeOrRecovery == "" {
		return fmt.Errorf("%w", ErrMFACodeRequired)
	}

	// Try TOTP first (cheap), then fall back to recovery code (bcrypt
	// compare, ~constant-time per slot). Either path counts as a
	// fresh proof-of-possession. The codes are cloned because
	// consumeRecoveryCode removes a matched hash in place, which would
	// otherwise shift readCodes and fail DisableMFA's codes guard.
	readSecret, readCodes := user.MFASecret, slices.Clone(user.MFARecoveryCodes)
	if err := s.verifyMFAProof(ctx, user, codeOrRecovery); err != nil {
		return err
	}

	if err := s.store.DisableMFA(ctx, user.ID, readSecret, readCodes); err != nil {
		return fmt.Errorf("failed to disable MFA: %w", err)
	}
	return nil
}

// MFARegenerateRecoveryCodes replaces all stored recovery codes with
// a fresh batch. Requires a fresh TOTP code (NOT a recovery code —
// because the user could otherwise drain the pool one code at a time
// and never see the regenerated batch). Returns the plaintext codes
// exactly once.
func (s *Service) MFARegenerateRecoveryCodes(ctx context.Context, userID, code string) ([]string, error) {
	if err := s.ensureStore(); err != nil {
		return nil, err
	}
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil || user == nil {
		return nil, fmt.Errorf("%w", ErrMFAAuthFailed)
	}
	if lockErr := s.rejectLockedMFAUser(user); lockErr != nil {
		return nil, lockErr
	}
	if !user.MFAEnabled || user.MFASecret == "" {
		return nil, fmt.Errorf("%w", ErrMFANotEnabled)
	}
	if claimErr := s.claimTOTPOrReject(ctx, user, code); claimErr != nil {
		return nil, claimErr
	}

	plaintext, hashes, err := s.generateAndHashRecoveryCodes()
	if err != nil {
		return nil, err
	}
	if err := s.store.ReplaceMFARecoveryCodes(ctx, user.ID, user.MFASecret, user.MFARecoveryCodes, hashes); err != nil {
		return nil, fmt.Errorf("failed to regenerate recovery codes: %w", err)
	}
	return plaintext, nil
}
