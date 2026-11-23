package mocks

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/stretchr/testify/mock"
)

// MockAuthStore is a mock implementation of auth.Store.
type MockAuthStore struct {
	// reporter fails the registered test on a mistyped .Return value. See
	// report.go.
	reporter testReporter
	mock.Mock
}

// GetUserByID mocks the GetUserByID operation.
func (m *MockAuthStore) GetUserByID(ctx context.Context, userID string) (*auth.User, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*auth.User)
	if !ok {
		m.reporter.wrongReturnType("GetUserByID", "*auth.User", args.Get(0))
	}
	return v, args.Error(1)
}

// GetUserByEmail mocks the GetUserByEmail operation.
func (m *MockAuthStore) GetUserByEmail(ctx context.Context, email string) (*auth.User, error) {
	args := m.Called(ctx, email)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*auth.User)
	if !ok {
		m.reporter.wrongReturnType("GetUserByEmail", "*auth.User", args.Get(0))
	}
	return v, args.Error(1)
}

// CreateUser mocks the CreateUser operation.
func (m *MockAuthStore) CreateUser(ctx context.Context, user *auth.User) error {
	args := m.Called(ctx, user)
	return args.Error(0)
}

func (m *MockAuthStore) UpdateUserAdminFields(ctx context.Context, user *auth.User, readEmail string, readGroupIDs []string, readActive bool) error {
	return m.Called(ctx, user, readEmail, readGroupIDs, readActive).Error(0)
}

func (m *MockAuthStore) UpdateUserCredentials(ctx context.Context, user *auth.User, readEmail, readPasswordHash string) error {
	return m.Called(ctx, user, readEmail, readPasswordHash).Error(0)
}

func (m *MockAuthStore) SetPasswordResetToken(ctx context.Context, user *auth.User, readExpiry *time.Time) error {
	return m.Called(ctx, user, readExpiry).Error(0)
}

func (m *MockAuthStore) CompletePasswordReset(ctx context.Context, user *auth.User, readResetToken, readPasswordHash string) error {
	return m.Called(ctx, user, readResetToken, readPasswordHash).Error(0)
}

func (m *MockAuthStore) ConsumePasswordResetToken(ctx context.Context, userID, readResetToken string) error {
	return m.Called(ctx, userID, readResetToken).Error(0)
}

func (m *MockAuthStore) ConsumeMFARecoveryCode(ctx context.Context, userID string, readCodes, remaining []string) error {
	return m.Called(ctx, userID, readCodes, remaining).Error(0)
}

func (m *MockAuthStore) ReplaceMFARecoveryCodes(ctx context.Context, userID, readSecret string, readCodes, newHashes []string) error {
	return m.Called(ctx, userID, readSecret, readCodes, newHashes).Error(0)
}

func (m *MockAuthStore) DisableMFA(ctx context.Context, userID, readSecret string, readCodes []string) error {
	return m.Called(ctx, userID, readSecret, readCodes).Error(0)
}

func (m *MockAuthStore) SetPendingMFASecret(ctx context.Context, userID, secret string, expiresAt time.Time) error {
	return m.Called(ctx, userID, secret, expiresAt).Error(0)
}

func (m *MockAuthStore) EnableMFA(ctx context.Context, userID, pendingSecret string, totpCounter int64, recoveryHashes []string) error {
	return m.Called(ctx, userID, pendingSecret, totpCounter, recoveryHashes).Error(0)
}

func (m *MockAuthStore) ClearPendingMFASecret(ctx context.Context, userID string) error {
	return m.Called(ctx, userID).Error(0)
}

func (m *MockAuthStore) ClaimTOTPCounter(ctx context.Context, userID string, counter int64) (bool, error) {
	args := m.Called(ctx, userID, counter)
	return args.Bool(0), args.Error(1)
}

func (m *MockAuthStore) CreateMFALoginToken(ctx context.Context, tokenHash, userID string, passwordVersion int64, ttl time.Duration) (time.Time, error) {
	args := m.Called(ctx, tokenHash, userID, passwordVersion, ttl)
	expiresAt, ok := args.Get(0).(time.Time)
	if !ok {
		m.reporter.wrongReturnType("CreateMFALoginToken", "time.Time", args.Get(0))
	}
	return expiresAt, args.Error(1)
}

func (m *MockAuthStore) ClaimMFALoginTokenAttempt(ctx context.Context, tokenHash string, maxAttempts int) (*auth.MFALoginToken, error) {
	args := m.Called(ctx, tokenHash, maxAttempts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	tok, ok := args.Get(0).(*auth.MFALoginToken)
	if !ok {
		m.reporter.wrongReturnType("ClaimMFALoginTokenAttempt", "*auth.MFALoginToken", args.Get(0))
	}
	return tok, args.Error(1)
}

func (m *MockAuthStore) ConsumeMFALoginToken(ctx context.Context, tokenHash string) (bool, error) {
	args := m.Called(ctx, tokenHash)
	return args.Bool(0), args.Error(1)
}

func (m *MockAuthStore) RecordFailedLogin(ctx context.Context, userID string) error {
	return m.Called(ctx, userID).Error(0)
}

func (m *MockAuthStore) RecordSuccessfulLogin(ctx context.Context, userID string) error {
	return m.Called(ctx, userID).Error(0)
}

// DeleteUser mocks the DeleteUser operation.
func (m *MockAuthStore) DeleteUser(ctx context.Context, userID string) error {
	args := m.Called(ctx, userID)
	return args.Error(0)
}

// ListUsers mocks the ListUsers operation.
func (m *MockAuthStore) ListUsers(ctx context.Context) ([]auth.User, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]auth.User)
	if !ok {
		m.reporter.wrongReturnType("ListUsers", "[]auth.User", args.Get(0))
	}
	return v, args.Error(1)
}

// GetUserByResetToken mocks the GetUserByResetToken operation.
func (m *MockAuthStore) GetUserByResetToken(ctx context.Context, token string) (*auth.User, error) {
	args := m.Called(ctx, token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*auth.User)
	if !ok {
		m.reporter.wrongReturnType("GetUserByResetToken", "*auth.User", args.Get(0))
	}
	return v, args.Error(1)
}

// AdminExists mocks the AdminExists operation.
func (m *MockAuthStore) AdminExists(ctx context.Context) (bool, error) {
	args := m.Called(ctx)
	return args.Bool(0), args.Error(1)
}

// CreateAdminIfNone mocks the CreateAdminIfNone operation.
func (m *MockAuthStore) CreateAdminIfNone(ctx context.Context, user *auth.User) (bool, error) {
	args := m.Called(ctx, user)
	return args.Bool(0), args.Error(1)
}
