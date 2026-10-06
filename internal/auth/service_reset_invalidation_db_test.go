//go:build integration

package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegration_CredentialChangeInvalidatesResetToken(t *testing.T) {
	store := NewPostgresStore(setupAuthTestDB(t))
	ctx := t.Context()
	const changed = "ChangedPassword789!"

	cases := map[string]func(f *credentialRaceFixture) error{
		"change-password": func(f *credentialRaceFixture) error {
			return f.svc.ChangePassword(ctx, f.user.ID, ChangePasswordRequest{CurrentPassword: credentialRacePassword, NewPassword: changed})
		},
		"profile-update": func(f *credentialRaceFixture) error {
			return f.svc.UpdateUserProfile(ctx, f.user.ID, "", credentialRacePassword, changed)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newCredentialRaceFixture(t, store, name+"-reset-invalidated@example.com")
			token := f.issueResetToken()
			require.NoError(t, change(f))

			stored := f.stored()
			assert.Empty(t, stored.PasswordResetToken)
			assert.Nil(t, stored.PasswordResetExpiry)

			err := f.svc.ConfirmPasswordReset(ctx, PasswordResetConfirm{Token: token, NewPassword: credentialRaceNew})
			require.ErrorContains(t, err, "invalid or expired reset token")
			assert.True(t, f.svc.verifyPassword(changed, f.stored().PasswordHash), "the newer password must survive")
		})
	}
}

func TestIntegration_CompletePasswordResetEnforcesExpiryInSQL(t *testing.T) {
	store := NewPostgresStore(setupAuthTestDB(t))
	ctx := t.Context()

	t.Run("expired-token-refused-by-the-write", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "reset-expired-sql@example.com")
		token := f.issueResetToken()
		_, err := store.db.Exec(ctx, "UPDATE users SET password_reset_expiry = NOW() - interval '1 minute' WHERE id = $1", f.user.ID)
		require.NoError(t, err)

		read, err := store.GetUserByResetToken(ctx, hashSessionToken(token))
		require.NoError(t, err)
		read.PasswordHash = "replacement-hash"
		err = store.CompletePasswordReset(ctx, read, read.PasswordResetToken, f.user.PasswordHash)
		require.ErrorIs(t, err, ErrUserChanged)
		assert.Equal(t, f.user.PasswordHash, f.stored().PasswordHash)
	})

	t.Run("fresh-token-completes-reset", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "reset-fresh@example.com")
		token := f.issueResetToken()
		require.NoError(t, f.svc.ConfirmPasswordReset(ctx, PasswordResetConfirm{Token: token, NewPassword: credentialRaceNew}))
		stored := f.stored()
		assert.True(t, f.svc.verifyPassword(credentialRaceNew, stored.PasswordHash))
		assert.Empty(t, stored.PasswordResetToken)
	})

	t.Run("stale-password-hash-still-reports-user-changed", func(t *testing.T) {
		f := newCredentialRaceFixture(t, store, "reset-stale-hash@example.com")
		token := f.issueResetToken()
		read, err := store.GetUserByResetToken(ctx, hashSessionToken(token))
		require.NoError(t, err)
		err = store.CompletePasswordReset(ctx, read, read.PasswordResetToken, "not-the-stored-hash")
		require.ErrorIs(t, err, ErrUserChanged)
	})
}
