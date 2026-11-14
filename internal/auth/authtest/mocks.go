package authtest

import (
	"context"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/stretchr/testify/mock"
)

// MockStore is a testify mock of auth.StoreInterface for tests outside
// package auth. Tests inside package auth use their own copy in
// test_helpers_test.go (importing this package from there would be an import
// cycle), so a new store method must be added to both.
type MockStore struct {
	mock.Mock
}

func (m *MockStore) GetUserByID(ctx context.Context, userID string) (*auth.User, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	u, ok := args.Get(0).(*auth.User)
	if !ok {
		panic(fmt.Sprintf("MockStore.GetUserByID: expected *auth.User, got %T", args.Get(0)))
	}
	return u, args.Error(1)
}

func (m *MockStore) GetUserByEmail(ctx context.Context, email string) (*auth.User, error) {
	args := m.Called(ctx, email)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	u, ok := args.Get(0).(*auth.User)
	if !ok {
		panic(fmt.Sprintf("MockStore.GetUserByEmail: expected *auth.User, got %T", args.Get(0)))
	}
	return u, args.Error(1)
}

func (m *MockStore) CreateUser(ctx context.Context, user *auth.User) error {
	args := m.Called(ctx, user)
	return args.Error(0)
}

func (m *MockStore) UpdateUserAdminFields(ctx context.Context, user *auth.User, readEmail string, readGroupIDs []string, readActive bool) error {
	return m.Called(ctx, user, readEmail, readGroupIDs, readActive).Error(0)
}

func (m *MockStore) UpdateUserCredentials(ctx context.Context, user *auth.User, readEmail, readPasswordHash string) error {
	return m.Called(ctx, user, readEmail, readPasswordHash).Error(0)
}

func (m *MockStore) SetPasswordResetToken(ctx context.Context, user *auth.User, readExpiry *time.Time) error {
	return m.Called(ctx, user, readExpiry).Error(0)
}

func (m *MockStore) CompletePasswordReset(ctx context.Context, user *auth.User, readResetToken, readPasswordHash string) error {
	return m.Called(ctx, user, readResetToken, readPasswordHash).Error(0)
}

func (m *MockStore) ConsumePasswordResetToken(ctx context.Context, userID, readResetToken string) error {
	return m.Called(ctx, userID, readResetToken).Error(0)
}

func (m *MockStore) ConsumeMFARecoveryCode(ctx context.Context, userID string, readCodes, remaining []string) error {
	return m.Called(ctx, userID, readCodes, remaining).Error(0)
}

func (m *MockStore) SetPendingMFASecret(ctx context.Context, userID, secret string, expiresAt time.Time) error {
	return m.Called(ctx, userID, secret, expiresAt).Error(0)
}

func (m *MockStore) EnableMFA(ctx context.Context, userID, pendingSecret string, totpCounter int64, recoveryHashes []string) error {
	return m.Called(ctx, userID, pendingSecret, totpCounter, recoveryHashes).Error(0)
}

func (m *MockStore) ReplaceMFARecoveryCodes(ctx context.Context, userID, readSecret string, readCodes, newHashes []string) error {
	return m.Called(ctx, userID, readSecret, readCodes, newHashes).Error(0)
}

func (m *MockStore) DisableMFA(ctx context.Context, userID, readSecret string, readCodes []string) error {
	return m.Called(ctx, userID, readSecret, readCodes).Error(0)
}

func (m *MockStore) ClearPendingMFASecret(ctx context.Context, userID string) error {
	return m.Called(ctx, userID).Error(0)
}

func (m *MockStore) ClaimTOTPCounter(ctx context.Context, userID string, counter int64) (bool, error) {
	args := m.Called(ctx, userID, counter)
	return args.Bool(0), args.Error(1)
}

func (m *MockStore) CreateMFALoginToken(ctx context.Context, tokenHash, userID string, passwordVersion int64, ttl time.Duration) (time.Time, error) {
	args := m.Called(ctx, tokenHash, userID, passwordVersion, ttl)
	expiresAt, ok := args.Get(0).(time.Time)
	if !ok {
		panic(fmt.Sprintf("MockStore.CreateMFALoginToken: expected time.Time, got %T", args.Get(0)))
	}
	return expiresAt, args.Error(1)
}

func (m *MockStore) ClaimMFALoginTokenAttempt(ctx context.Context, tokenHash string, maxAttempts int) (*auth.MFALoginToken, error) {
	args := m.Called(ctx, tokenHash, maxAttempts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	tok, ok := args.Get(0).(*auth.MFALoginToken)
	if !ok {
		panic(fmt.Sprintf("MockStore.ClaimMFALoginTokenAttempt: expected *auth.MFALoginToken, got %T", args.Get(0)))
	}
	return tok, args.Error(1)
}

func (m *MockStore) ConsumeMFALoginToken(ctx context.Context, tokenHash string) (bool, error) {
	args := m.Called(ctx, tokenHash)
	return args.Bool(0), args.Error(1)
}

func (m *MockStore) RecordFailedLogin(ctx context.Context, userID string) error {
	return m.Called(ctx, userID).Error(0)
}

func (m *MockStore) RecordSuccessfulLogin(ctx context.Context, userID string) error {
	return m.Called(ctx, userID).Error(0)
}

func (m *MockStore) DeleteUser(ctx context.Context, userID string) error {
	args := m.Called(ctx, userID)
	return args.Error(0)
}

func (m *MockStore) ListUsers(ctx context.Context) ([]auth.User, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	u, ok := args.Get(0).([]auth.User)
	if !ok {
		panic(fmt.Sprintf("MockStore.ListUsers: expected []auth.User, got %T", args.Get(0)))
	}
	return u, args.Error(1)
}

func (m *MockStore) GetUserByResetToken(ctx context.Context, token string) (*auth.User, error) {
	args := m.Called(ctx, token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	u, ok := args.Get(0).(*auth.User)
	if !ok {
		panic(fmt.Sprintf("MockStore.GetUserByResetToken: expected *auth.User, got %T", args.Get(0)))
	}
	return u, args.Error(1)
}

func (m *MockStore) AdminExists(ctx context.Context) (bool, error) {
	args := m.Called(ctx)
	return args.Bool(0), args.Error(1)
}

func (m *MockStore) CreateAdminIfNone(ctx context.Context, user *auth.User) (bool, error) {
	args := m.Called(ctx, user)
	return args.Bool(0), args.Error(1)
}

func (m *MockStore) GetGroup(ctx context.Context, groupID string) (*auth.Group, error) {
	args := m.Called(ctx, groupID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	g, ok := args.Get(0).(*auth.Group)
	if !ok {
		panic(fmt.Sprintf("MockStore.GetGroup: expected *auth.Group, got %T", args.Get(0)))
	}
	return g, args.Error(1)
}

func (m *MockStore) CreateGroup(ctx context.Context, group *auth.Group) error {
	args := m.Called(ctx, group)
	return args.Error(0)
}

func (m *MockStore) UpdateGroup(ctx context.Context, group *auth.Group) error {
	args := m.Called(ctx, group)
	return args.Error(0)
}

func (m *MockStore) DeleteGroup(ctx context.Context, groupID string) error {
	args := m.Called(ctx, groupID)
	return args.Error(0)
}

func (m *MockStore) ListGroups(ctx context.Context) ([]auth.Group, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	g, ok := args.Get(0).([]auth.Group)
	if !ok {
		panic(fmt.Sprintf("MockStore.ListGroups: expected []auth.Group, got %T", args.Get(0)))
	}
	return g, args.Error(1)
}

func (m *MockStore) CountGroupMembers(ctx context.Context, groupID string) (int, error) {
	args := m.Called(ctx, groupID)
	return args.Int(0), args.Error(1)
}

func (m *MockStore) CreateSession(ctx context.Context, session *auth.Session) error {
	args := m.Called(ctx, session)
	return args.Error(0)
}

func (m *MockStore) GetSession(ctx context.Context, token string) (*auth.Session, error) {
	args := m.Called(ctx, token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	s, ok := args.Get(0).(*auth.Session)
	if !ok {
		panic(fmt.Sprintf("MockStore.GetSession: expected *auth.Session, got %T", args.Get(0)))
	}
	return s, args.Error(1)
}

func (m *MockStore) DeleteSession(ctx context.Context, token string) error {
	args := m.Called(ctx, token)
	return args.Error(0)
}

func (m *MockStore) DeleteUserSessions(ctx context.Context, userID string) error {
	args := m.Called(ctx, userID)
	return args.Error(0)
}

func (m *MockStore) CleanupExpiredSessions(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// API Key operations.
func (m *MockStore) CreateAPIKey(ctx context.Context, key *auth.UserAPIKey) error {
	args := m.Called(ctx, key)
	return args.Error(0)
}

func (m *MockStore) GetAPIKeyByID(ctx context.Context, keyID string) (*auth.UserAPIKey, error) {
	args := m.Called(ctx, keyID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	k, ok := args.Get(0).(*auth.UserAPIKey)
	if !ok {
		panic(fmt.Sprintf("MockStore.GetAPIKeyByID: expected *auth.UserAPIKey, got %T", args.Get(0)))
	}
	return k, args.Error(1)
}

func (m *MockStore) GetAPIKeyByHash(ctx context.Context, keyHash string) (*auth.UserAPIKey, error) {
	args := m.Called(ctx, keyHash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	k, ok := args.Get(0).(*auth.UserAPIKey)
	if !ok {
		panic(fmt.Sprintf("MockStore.GetAPIKeyByHash: expected *auth.UserAPIKey, got %T", args.Get(0)))
	}
	return k, args.Error(1)
}

func (m *MockStore) ListAPIKeysByUser(ctx context.Context, userID string) ([]*auth.UserAPIKey, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	k, ok := args.Get(0).([]*auth.UserAPIKey)
	if !ok {
		panic(fmt.Sprintf("MockStore.ListAPIKeysByUser: expected []*auth.UserAPIKey, got %T", args.Get(0)))
	}
	return k, args.Error(1)
}

func (m *MockStore) UpdateAPIKey(ctx context.Context, key *auth.UserAPIKey) error {
	args := m.Called(ctx, key)
	return args.Error(0)
}

func (m *MockStore) RecordAPIKeyUsage(ctx context.Context, keyID string, delta int64) error {
	args := m.Called(ctx, keyID, delta)
	return args.Error(0)
}

func (m *MockStore) DeleteAPIKey(ctx context.Context, keyID string) error {
	args := m.Called(ctx, keyID)
	return args.Error(0)
}

func (m *MockStore) Ping(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// MockEmailSender is a mock implementation of the email sender for testing.
type MockEmailSender struct {
	mock.Mock
}

func (m *MockEmailSender) SendPasswordResetEmail(ctx context.Context, email, resetURL string) error {
	args := m.Called(ctx, email, resetURL)
	return args.Error(0)
}

func (m *MockEmailSender) SendUserInviteEmail(ctx context.Context, email, setupURL string) error {
	args := m.Called(ctx, email, setupURL)
	return args.Error(0)
}

// Verify that MockStore implements auth.StoreInterface.
var _ auth.StoreInterface = (*MockStore)(nil)

// Verify that MockEmailSender implements auth.EmailSenderInterface.
var _ auth.EmailSenderInterface = (*MockEmailSender)(nil)
