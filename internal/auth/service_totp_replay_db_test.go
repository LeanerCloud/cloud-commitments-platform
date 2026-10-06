//go:build integration

package auth

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #442: a TOTP code is accepted once, whichever endpoint spends it.
func TestIntegration_TOTPCodeIsAcceptedOnce(t *testing.T) {
	store := NewPostgresStore(setupAuthTestDB(t))
	ctx := t.Context()
	step := func() int64 { return time.Now().Unix() / 30 }

	login := func(f *credentialRaceFixture, svc *Service, code string) error {
		_, err := svc.Login(ctx, LoginRequest{Email: f.user.Email, Password: credentialRacePassword, MFACode: code})
		return err
	}

	t.Run("login-replay-rejected-next-step-accepted", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "totp-replay-login@example.com")
		f.enrollMFA()
		secret := f.stored().MFASecret
		code := generateTOTP(secret, step())

		require.NoError(t, login(f, f.svc, code))
		require.ErrorIs(t, login(f, f.svc, code), ErrInvalidMFACode, "an accepted code must not log in twice")
		require.NoError(t, login(f, f.svc, generateTOTP(secret, step()+1)))
	})

	t.Run("older-step-than-last-accepted-rejected", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "totp-replay-older@example.com")
		f.enrollMFA()
		secret := f.stored().MFASecret

		require.NoError(t, login(f, f.svc, generateTOTP(secret, step()+1)))
		require.ErrorIs(t, login(f, f.svc, generateTOTP(secret, step())), ErrInvalidMFACode)
	})

	t.Run("wrong-code-does-not-spend-the-step", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "totp-replay-wrong@example.com")
		f.enrollMFA()
		secret := f.stored().MFASecret

		require.ErrorIs(t, login(f, f.svc, "000000"), ErrInvalidMFACode)
		require.NoError(t, login(f, f.svc, generateTOTP(secret, step())))
	})

	t.Run("login-code-cannot-disable-mfa", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "totp-replay-disable@example.com")
		f.enrollMFA()
		secret := f.stored().MFASecret
		code := generateTOTP(secret, step())

		require.NoError(t, login(f, f.svc, code))
		require.ErrorIs(t, f.svc.MFADisable(ctx, f.user.ID, credentialRacePassword, code), ErrMFAInvalidCode)
		assert.True(t, f.stored().MFAEnabled)
		require.NoError(t, f.svc.MFADisable(ctx, f.user.ID, credentialRacePassword, generateTOTP(secret, step()+1)))
		assert.False(t, f.stored().MFAEnabled)
	})

	t.Run("login-code-cannot-regenerate-recovery-codes", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "totp-replay-regen@example.com")
		codes := f.enrollMFA()
		secret := f.stored().MFASecret
		code := generateTOTP(secret, step())

		require.NoError(t, login(f, f.svc, code))
		_, err := f.svc.MFARegenerateRecoveryCodes(ctx, f.user.ID, code)
		require.ErrorIs(t, err, ErrMFAInvalidCode)
		assert.Len(t, f.stored().MFARecoveryCodes, len(codes), "a replayed code must not replace the recovery codes")
		fresh, err := f.svc.MFARegenerateRecoveryCodes(ctx, f.user.ID, generateTOTP(secret, step()+1))
		require.NoError(t, err)
		assert.Len(t, fresh, recoveryCodeCount)
	})

	t.Run("concurrent-logins-with-one-code-accept-one", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "totp-replay-race@example.com")
		f.enrollMFA()
		code := generateTOTP(f.stored().MFASecret, step())

		const racers = 8
		var accepted atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range racers {
			svc := f.newService(store, f.mail)
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if login(f, svc, code) == nil {
					accepted.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		assert.Equal(t, int32(1), accepted.Load(), "one code must be accepted at most once under concurrency")
	})
}
