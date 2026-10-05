//go:build integration

package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #227: a session plus the password must not replace an enabled factor,
// sequentially or by racing the victim's enrollment.
func TestIntegration_MFAReplacementNeedsCurrentFactor(t *testing.T) {
	store := NewPostgresStore(setupAuthTestDB(t))
	ctx := t.Context()

	t.Run("setup-and-enable-refused-while-enabled", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "mfa-replace@example.com")
		victimCodes := f.enrollMFA()
		enrolled := f.stored()

		_, err := f.svc.MFASetup(ctx, f.user.ID, credentialRacePassword)
		require.ErrorIs(t, err, ErrMFAAlreadyEnabled)
		assert.Equal(t, enrolled, f.stored())

		// Pending material left behind (e.g. by the pre-fix setup) must not be promotable.
		attacker, err := generateMFASecret()
		require.NoError(t, err)
		_, err = store.db.Exec(ctx, "UPDATE users SET mfa_pending_secret = $2, mfa_pending_secret_expires_at = NOW() + interval '5 minutes' WHERE id = $1", f.user.ID, attacker)
		require.NoError(t, err)
		withPending := f.stored()
		_, err = f.svc.MFAEnable(ctx, f.user.ID, generateTOTP(attacker, time.Now().Unix()/30))
		require.ErrorIs(t, err, ErrMFAAlreadyEnabled)
		assert.Equal(t, withPending, f.stored())

		f.requireFactorEnforced(f.user.Email, credentialRacePassword, enrolled)
		_, err = f.svc.Login(ctx, LoginRequest{Email: f.user.Email, Password: credentialRacePassword, MFACode: victimCodes[0]})
		require.NoError(t, err, "the victim's recovery codes must survive")
	})

	t.Run("re-enroll-after-disable-with-current-factor", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "mfa-reenroll@example.com")
		f.enrollMFA()
		first := f.stored()
		require.NoError(t, f.svc.MFADisable(ctx, f.user.ID, credentialRacePassword, generateTOTP(first.MFASecret, time.Now().Unix()/30)))
		f.enrollMFA()
		second := f.stored()
		assert.NotEqual(t, first.MFASecret, second.MFASecret)
		f.requireFactorEnforced(f.user.Email, credentialRacePassword, second)
	})

	t.Run("stale-setup-after-concurrent-enrollment", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "mfa-stale-setup@example.com")
		enrolled := f.enrollOnRead()
		_, err := f.svc.MFASetup(ctx, f.user.ID, credentialRacePassword)
		require.ErrorIs(t, err, ErrUserChanged)
		assert.Equal(t, enrolled(), f.stored())
		f.requireFactorEnforced(f.user.Email, credentialRacePassword, enrolled())
	})

	t.Run("stale-password-only-disable-after-concurrent-enrollment", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "mfa-stale-disable@example.com")
		enrolled := f.enrollOnRead()
		err := f.svc.MFADisable(ctx, f.user.ID, credentialRacePassword, "")
		require.ErrorIs(t, err, ErrUserChanged)
		assert.Equal(t, enrolled(), f.stored())
		f.requireFactorEnforced(f.user.Email, credentialRacePassword, enrolled())
	})

	t.Run("stale-enable-after-concurrent-setup", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "mfa-stale-enable@example.com")
		setupA, err := f.svc.MFASetup(ctx, f.user.ID, credentialRacePassword)
		require.NoError(t, err)
		winner := f.onRead(func(ctx context.Context, other *Service) {
			_, setupErr := other.MFASetup(ctx, f.user.ID, credentialRacePassword)
			require.NoError(t, setupErr)
		})
		codes, err := f.svc.MFAEnable(ctx, f.user.ID, generateTOTP(setupA.Secret, time.Now().Unix()/30))
		require.ErrorIs(t, err, ErrUserChanged)
		assert.Nil(t, codes)
		assert.Equal(t, winner(), f.stored())
	})

	t.Run("stale-enable-after-concurrent-enable", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "mfa-double-enable@example.com")
		setup, err := f.svc.MFASetup(ctx, f.user.ID, credentialRacePassword)
		require.NoError(t, err)
		code := generateTOTP(setup.Secret, time.Now().Unix()/30)
		winner := f.onRead(func(ctx context.Context, other *Service) {
			_, enableErr := other.MFAEnable(ctx, f.user.ID, code)
			require.NoError(t, enableErr)
		})
		codes, err := f.svc.MFAEnable(ctx, f.user.ID, code)
		require.ErrorIs(t, err, ErrUserChanged)
		assert.Nil(t, codes)
		assert.Equal(t, winner(), f.stored(), "the loser must not replace the winner's recovery codes")
	})

	t.Run("expired-enable-after-concurrent-enrollment", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "mfa-expired-enable@example.com")
		stale, err := generateMFASecret()
		require.NoError(t, err)
		_, err = store.db.Exec(ctx, "UPDATE users SET mfa_pending_secret = $2, mfa_pending_secret_expires_at = NOW() - interval '1 minute' WHERE id = $1", f.user.ID, stale)
		require.NoError(t, err)
		enrolled := f.enrollOnRead()
		_, err = f.svc.MFAEnable(ctx, f.user.ID, generateTOTP(stale, time.Now().Unix()/30))
		require.ErrorIs(t, err, ErrMFAEnrollmentExpired)
		assert.Equal(t, enrolled(), f.stored())
		f.requireFactorEnforced(f.user.Email, credentialRacePassword, enrolled())
	})
}
