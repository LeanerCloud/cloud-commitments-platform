//go:build integration

package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegration_LoginRecoveryPersistenceFailure(t *testing.T) {
	db := setupAuthTestDB(t)
	store := NewPostgresStore(db)
	service := NewService(ServiceConfig{Store: store})
	service.bcryptCostOverride = 4
	ctx := t.Context()
	password, err := service.hashPassword("SyntheticPassword123!")
	require.NoError(t, err)
	code := "ABCD-2345"
	codeHash, err := service.hashRecoveryCode(code)
	require.NoError(t, err)
	user := &User{
		Email: "recovery-persistence@example.com", PasswordHash: password, Active: true,
		GroupIDs: []string{DefaultPurchaserGroupID}, FailedLoginAttempts: 2,
		MFAEnabled: true, MFASecret: "JBSWY3DPEHPK3PXP", MFARecoveryCodes: []string{codeHash},
	}
	require.NoError(t, store.CreateUser(ctx, user))
	_, err = db.Exec(ctx, `ALTER TABLE users ADD CONSTRAINT test_recovery_consumption_failure
		CHECK (email <> 'recovery-persistence@example.com' OR cardinality(mfa_recovery_codes) > 0)`)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, cleanupErr := db.Exec(cleanupCtx, "ALTER TABLE users DROP CONSTRAINT IF EXISTS test_recovery_consumption_failure")
		assert.NoError(t, cleanupErr)
	})
	request := LoginRequest{Email: user.Email, Password: "SyntheticPassword123!", MFACode: code}
	for range 2 {
		response, loginErr := service.Login(ctx, request)
		assert.ErrorIs(t, loginErr, ErrInvalidMFACode)
		assert.True(t, response == nil, "failed consumption must not return a token")
		stored, readErr := store.GetUserByID(ctx, user.ID)
		require.NoError(t, readErr)
		assert.Equal(t, []string{codeHash}, stored.MFARecoveryCodes)
		assert.Equal(t, 2, stored.FailedLoginAttempts)
		assert.Nil(t, stored.LastLoginAt)
		var sessions int
		require.NoError(t, db.QueryRow(ctx, "SELECT count(*) FROM sessions WHERE user_id=$1", user.ID).Scan(&sessions))
		assert.Zero(t, sessions)
	}
	_, err = db.Exec(ctx, "ALTER TABLE users DROP CONSTRAINT test_recovery_consumption_failure")
	require.NoError(t, err)
	response, err := service.Login(ctx, request)
	require.NoError(t, err)
	require.NotNil(t, response)
	_, err = service.ValidateSession(ctx, response.Token)
	require.NoError(t, err)
	stored, err := store.GetUserByID(ctx, user.ID)
	require.NoError(t, err)
	assert.Empty(t, stored.MFARecoveryCodes)
	assert.Zero(t, stored.FailedLoginAttempts)
	assert.NotNil(t, stored.LastLoginAt)
	response, err = service.Login(ctx, request)
	assert.ErrorIs(t, err, ErrInvalidMFACode)
	assert.True(t, response == nil, "consumed code must not return another token")
	var sessions int
	require.NoError(t, db.QueryRow(ctx, "SELECT count(*) FROM sessions WHERE user_id=$1", user.ID).Scan(&sessions))
	assert.Equal(t, 1, sessions)
}
