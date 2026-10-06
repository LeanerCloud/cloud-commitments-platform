//go:build integration

package auth

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #442: wrong codes on the MFA lifecycle endpoints share Login's durable
// failed-attempt counter and lockout, so a stolen session cannot guess codes
// without limit.

// wrongMFACode returns a six-digit code outside the accepted TOTP window.
func wrongMFACode(t *testing.T, secret string) string {
	t.Helper()
	for n := 0; n < 1000; n++ {
		code := strconv.Itoa(100000 + n)
		if _, ok := matchTOTP(secret, code); !ok {
			return code
		}
	}
	t.Fatal("no wrong code found")
	return ""
}

func (f *credentialRaceFixture) currentTOTP() string {
	return generateTOTP(f.stored().MFASecret, time.Now().Unix()/30)
}

// mfaLockoutOps are the endpoints under test. Each takes a code and runs the
// operation against an account prepared by prepare.
var mfaLockoutOps = []struct {
	name    string
	enrolls bool // true: starts with MFA on; false: starts with a pending enrollment
	call    func(f *credentialRaceFixture, code string) error
}{
	{"disable", true, func(f *credentialRaceFixture, code string) error {
		return f.svc.MFADisable(f.t.Context(), f.user.ID, credentialRacePassword, code)
	}},
	{"regenerate", true, func(f *credentialRaceFixture, code string) error {
		_, err := f.svc.MFARegenerateRecoveryCodes(f.t.Context(), f.user.ID, code)
		return err
	}},
	{"enable", false, func(f *credentialRaceFixture, code string) error {
		_, err := f.svc.MFAEnable(f.t.Context(), f.user.ID, code)
		return err
	}},
}

// prepare returns the secret the endpoint checks codes against.
func prepareMFALockoutAccount(f *credentialRaceFixture, enrolled bool) string {
	if enrolled {
		f.enrollMFA()
		return f.stored().MFASecret
	}
	setup, err := f.svc.MFASetup(f.t.Context(), f.user.ID, credentialRacePassword)
	require.NoError(f.t, err)
	return setup.Secret
}

func TestIntegration_MFALifecycleWrongCodesLockAccount(t *testing.T) {
	store := NewPostgresStore(setupAuthTestDB(t))

	// Baseline: the same number of wrong codes at Login locks the account.
	t.Run("login-baseline", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "lockout-login@example.com")
		f.enrollMFA()
		secret := f.stored().MFASecret
		for i := 0; i < MaxFailedLoginAttempts; i++ {
			_, err := f.svc.Login(t.Context(), LoginRequest{Email: f.user.Email, Password: credentialRacePassword, MFACode: wrongMFACode(t, secret)})
			require.ErrorIs(t, err, ErrInvalidMFACode)
		}
		require.NotNil(t, f.stored().LockedUntil)
	})

	for _, op := range mfaLockoutOps {
		t.Run(op.name+"-locks-after-max-failures", func(t *testing.T) {
			f := newCredentialRaceFixture(t, store, "lockout-"+op.name+"@example.com")
			secret := prepareMFALockoutAccount(f, op.enrolls)
			before := f.stored()

			for i := 0; i < MaxFailedLoginAttempts; i++ {
				require.ErrorIs(t, op.call(f, wrongMFACode(t, secret)), ErrMFAInvalidCode, "attempt %d", i+1)
			}
			locked := f.stored()
			require.NotNil(t, locked.LockedUntil, "%d wrong codes must lock the account", MaxFailedLoginAttempts)
			assert.Equal(t, MaxFailedLoginAttempts, locked.FailedLoginAttempts)

			// A correct code is now refused, with no state change.
			require.ErrorIs(t, op.call(f, generateTOTP(secret, time.Now().Unix()/30)), ErrMFAAuthFailed)
			after := f.stored()
			assert.Equal(t, locked.FailedLoginAttempts, after.FailedLoginAttempts)
			assert.Equal(t, before.MFAEnabled, after.MFAEnabled)
			assert.Equal(t, before.MFARecoveryCodes, after.MFARecoveryCodes)

			// The lock is the account's, so Login refuses it too.
			if op.enrolls {
				_, err := f.svc.Login(t.Context(), LoginRequest{Email: f.user.Email, Password: credentialRacePassword, MFACode: generateTOTP(secret, time.Now().Unix()/30+1)})
				require.Error(t, err)
			}
		})
	}

	t.Run("below-threshold-correct-code-succeeds-and-keeps-count", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "lockout-below@example.com")
		f.enrollMFA()
		secret := f.stored().MFASecret
		for i := 0; i < MaxFailedLoginAttempts-1; i++ {
			require.ErrorIs(t, mfaLockoutOps[1].call(f, wrongMFACode(t, secret)), ErrMFAInvalidCode)
		}
		require.NoError(t, mfaLockoutOps[1].call(f, generateTOTP(secret, time.Now().Unix()/30)))
		stored := f.stored()
		assert.Nil(t, stored.LockedUntil)
		assert.Equal(t, MaxFailedLoginAttempts-1, stored.FailedLoginAttempts,
			"like Login, only a completed login resets the counter")

		// One more failure reaches the threshold: the correct code did not buy a fresh budget.
		require.ErrorIs(t, mfaLockoutOps[1].call(f, wrongMFACode(t, secret)), ErrMFAInvalidCode)
		require.NotNil(t, f.stored().LockedUntil)
	})

	t.Run("replayed-code-counts-as-failure", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "lockout-replay@example.com")
		f.enrollMFA()
		code := f.currentTOTP()
		require.NoError(t, mfaLockoutOps[1].call(f, code))
		require.Equal(t, 0, f.stored().FailedLoginAttempts)
		require.ErrorIs(t, mfaLockoutOps[1].call(f, code), ErrMFAInvalidCode)
		assert.Equal(t, 1, f.stored().FailedLoginAttempts)
	})

	t.Run("empty-code-is-not-counted", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "lockout-empty@example.com")
		f.enrollMFA()
		require.Error(t, mfaLockoutOps[1].call(f, ""))
		assert.Equal(t, 0, f.stored().FailedLoginAttempts)
	})

	t.Run("concurrent-failures-count-atomically", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "lockout-concurrent@example.com")
		f.enrollMFA()
		secret := f.stored().MFASecret
		var wg sync.WaitGroup
		for i := 0; i < MaxFailedLoginAttempts; i++ {
			wg.Add(1)
			go func(code string) {
				defer wg.Done()
				assert.ErrorIs(t, mfaLockoutOps[0].call(f, code), ErrMFAInvalidCode)
			}(wrongMFACode(t, secret))
		}
		wg.Wait()
		stored := f.stored()
		assert.Equal(t, MaxFailedLoginAttempts, stored.FailedLoginAttempts, "no failure may be lost")
		assert.NotNil(t, stored.LockedUntil)
	})
}
