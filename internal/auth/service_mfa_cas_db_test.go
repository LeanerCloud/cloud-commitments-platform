//go:build integration

package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func totpNow(secret string) string { return generateTOTP(secret, time.Now().Unix()/30) }

// Issue #527: disable and recovery-code regeneration write only the MFA
// columns, guarded by the secret and codes they read.
func TestIntegration_MFADisableAndRegenerateAreColumnScoped(t *testing.T) {
	store := NewPostgresStore(setupAuthTestDB(t))
	ctx := t.Context()

	spendRecoveryCode := func(f *credentialRaceFixture, code string) func(context.Context, *Service) {
		return func(ctx context.Context, other *Service) {
			_, err := other.Login(ctx, LoginRequest{Email: f.user.Email, Password: credentialRacePassword, MFACode: code})
			require.NoError(t, err)
		}
	}

	t.Run("disable-and-regenerate-work-when-uncontended", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "mfa-cas-plain@example.com")
		codes := f.enrollMFA()
		before := f.stored()

		fresh, err := f.svc.MFARegenerateRecoveryCodes(ctx, f.user.ID, totpNow(before.MFASecret))
		require.NoError(t, err)
		after := f.stored()
		assert.Len(t, after.MFARecoveryCodes, len(fresh))
		assert.NotEqual(t, before.MFARecoveryCodes, after.MFARecoveryCodes)
		assertOnlyChanged(t, before, after, func(w *User) { w.MFARecoveryCodes = after.MFARecoveryCodes })

		_, err = f.svc.Login(ctx, LoginRequest{Email: f.user.Email, Password: credentialRacePassword, MFACode: codes[0]})
		require.ErrorIs(t, err, ErrInvalidMFACode, "pre-regeneration codes must stop working")
		_, err = f.svc.Login(ctx, LoginRequest{Email: f.user.Email, Password: credentialRacePassword, MFACode: fresh[0]})
		require.NoError(t, err)

		require.NoError(t, f.svc.MFADisable(ctx, f.user.ID, credentialRacePassword, fresh[1]))
		off := f.stored()
		assert.False(t, off.MFAEnabled)
		assert.Empty(t, off.MFASecret)
		assert.Empty(t, off.MFARecoveryCodes)
		_, err = f.svc.Login(ctx, LoginRequest{Email: f.user.Email, Password: credentialRacePassword})
		require.NoError(t, err)
	})

	t.Run("stale-regenerate-after-concurrent-recovery-login", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "mfa-cas-regen-spend@example.com")
		codes := f.enrollMFA()
		secret := f.stored().MFASecret
		winner := f.onRead(spendRecoveryCode(f, codes[0]))
		fresh, err := f.svc.MFARegenerateRecoveryCodes(ctx, f.user.ID, totpNow(secret))
		require.ErrorIs(t, err, ErrUserChanged)
		assert.Nil(t, fresh)
		assertOnlyChanged(t, winner(), f.stored(), func(*User) {})
		assert.Equal(t, winner().MFARecoveryCodes, f.stored().MFARecoveryCodes, "the stale write must not replace the codes the login left")
		assert.Len(t, f.stored().MFARecoveryCodes, len(codes)-1)
	})

	t.Run("stale-disable-after-concurrent-recovery-login", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "mfa-cas-disable-spend@example.com")
		codes := f.enrollMFA()
		secret := f.stored().MFASecret
		winner := f.onRead(spendRecoveryCode(f, codes[0]))
		err := f.svc.MFADisable(ctx, f.user.ID, credentialRacePassword, totpNow(secret))
		require.ErrorIs(t, err, ErrUserChanged)
		assertOnlyChanged(t, winner(), f.stored(), func(*User) {})
		assert.True(t, f.stored().MFAEnabled)
	})

	t.Run("stale-disable-with-recovery-code-after-concurrent-spend-of-same-code", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "mfa-cas-disable-same@example.com")
		codes := f.enrollMFA()
		enrolled := f.stored()
		winner := f.onRead(spendRecoveryCode(f, codes[0]))
		err := f.svc.MFADisable(ctx, f.user.ID, credentialRacePassword, codes[0])
		require.ErrorIs(t, err, ErrUserChanged, "a recovery code must be spent once")
		assert.Equal(t, winner(), f.stored())
		f.requireFactorEnforced(f.user.Email, credentialRacePassword, &User{
			MFASecret: enrolled.MFASecret, MFARecoveryCodes: winner().MFARecoveryCodes,
		})
	})

	for name, act := range map[string]func(f *credentialRaceFixture) error{
		"disable": func(f *credentialRaceFixture) error {
			return f.svc.MFADisable(ctx, f.user.ID, credentialRacePassword, totpNow(f.stored().MFASecret))
		},
		"regenerate": func(f *credentialRaceFixture) error {
			_, err := f.svc.MFARegenerateRecoveryCodes(ctx, f.user.ID, totpNow(f.stored().MFASecret))
			return err
		},
	} {
		t.Run("concurrent-password-change-is-kept-by-"+name, func(t *testing.T) {
			f := newCredentialRaceFixture(t, store, "mfa-cas-pw-"+name+"@example.com")
			f.enrollMFA()
			enrolled := f.stored()
			winner := f.onRead(func(ctx context.Context, other *Service) {
				require.NoError(t, other.ChangePassword(ctx, f.user.ID, ChangePasswordRequest{
					CurrentPassword: credentialRacePassword, NewPassword: credentialRaceNew,
				}))
			})
			require.NoError(t, act(f))
			stored := f.stored()
			assert.Equal(t, winner().PasswordHash, stored.PasswordHash, "the stale write reverted the password change")
			assert.Equal(t, winner().PasswordVersion, stored.PasswordVersion)
			assert.Equal(t, winner().PasswordHistory, stored.PasswordHistory)
			assert.True(t, f.svc.verifyPassword(credentialRaceNew, stored.PasswordHash))
			assert.NotEqual(t, enrolled.MFARecoveryCodes, stored.MFARecoveryCodes, "the MFA write itself must still land")
		})
	}
}

// Issue #527: the pending-secret expiry is enforced by the write itself, not
// only on the value read before it.
func TestIntegration_MFAEnableExpiryEnforcedInWrite(t *testing.T) {
	store := NewPostgresStore(setupAuthTestDB(t))
	ctx := t.Context()
	f := newCredentialRaceFixture(t, store, "mfa-cas-expiry@example.com")

	setup, err := f.svc.MFASetup(ctx, f.user.ID, credentialRacePassword)
	require.NoError(t, err)
	f.onRead(func(ctx context.Context, _ *Service) {
		_, execErr := store.db.Exec(ctx, "UPDATE users SET mfa_pending_secret_expires_at = NOW() - interval '1 minute' WHERE id = $1", f.user.ID)
		require.NoError(t, execErr)
	})
	codes, err := f.svc.MFAEnable(ctx, f.user.ID, totpNow(setup.Secret))
	require.ErrorIs(t, err, ErrUserChanged)
	assert.Nil(t, codes)
	stored := f.stored()
	assert.False(t, stored.MFAEnabled, "an expired enrollment must not be promoted")
	assert.Empty(t, stored.MFASecret)
}
