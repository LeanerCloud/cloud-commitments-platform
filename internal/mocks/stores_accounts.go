package mocks

import (
	"context"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
)

func (m *MockConfigStore) CreateCloudAccount(ctx context.Context, account *config.CloudAccount) error {
	m.record("CreateCloudAccount", ctx, account)
	if m.CreateCloudAccountFn != nil {
		return m.CreateCloudAccountFn(ctx, account)
	}
	if !isExpected(&m.Mock, "CreateCloudAccount") {
		return nil
	}
	args := m.Called(ctx, account)
	return args.Error(0)
}

// GetCloudAccount defaults to returning a minimal stub {ID: id} when no Fn is
// set and no expectation is registered, so tests that don't care about account
// fields keep working without explicit setup.
func (m *MockConfigStore) GetCloudAccount(ctx context.Context, id string) (*config.CloudAccount, error) {
	m.record("GetCloudAccount", ctx, id)
	if m.GetCloudAccountFn != nil {
		return m.GetCloudAccountFn(ctx, id)
	}
	if !isExpected(&m.Mock, "GetCloudAccount") {
		return &config.CloudAccount{ID: id, Provider: "aws", AWSAuthMode: "access_keys"}, nil
	}
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.CloudAccount)
	if !ok {
		m.reporter.wrongReturnType("GetCloudAccount", "*config.CloudAccount", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) GetCloudAccountByExternalID(ctx context.Context, provider, externalID string) (*config.CloudAccount, error) {
	m.record("GetCloudAccountByExternalID", ctx, provider, externalID)
	if m.GetCloudAccountByExternalIDFn != nil {
		return m.GetCloudAccountByExternalIDFn(ctx, provider, externalID)
	}
	if !isExpected(&m.Mock, "GetCloudAccountByExternalID") {
		return nil, nil
	}
	args := m.Called(ctx, provider, externalID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.CloudAccount)
	if !ok {
		m.reporter.wrongReturnType("GetCloudAccountByExternalID", "*config.CloudAccount", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) UpdateCloudAccount(ctx context.Context, account *config.CloudAccount) error {
	m.record("UpdateCloudAccount", ctx, account)
	if !isExpected(&m.Mock, "UpdateCloudAccount") {
		return nil
	}
	args := m.Called(ctx, account)
	return args.Error(0)
}

func (m *MockConfigStore) DeleteCloudAccount(ctx context.Context, id string) error {
	m.record("DeleteCloudAccount", ctx, id)
	if m.DeleteCloudAccountFn != nil {
		return m.DeleteCloudAccountFn(ctx, id)
	}
	if !isExpected(&m.Mock, "DeleteCloudAccount") {
		return nil
	}
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockConfigStore) ListCloudAccounts(ctx context.Context, filter config.CloudAccountFilter) ([]config.CloudAccount, error) {
	m.record("ListCloudAccounts", ctx, filter)
	if m.ListCloudAccountsFn != nil {
		return m.ListCloudAccountsFn(ctx, filter)
	}
	if !isExpected(&m.Mock, "ListCloudAccounts") {
		return nil, nil
	}
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.CloudAccount)
	if !ok {
		m.reporter.wrongReturnType("ListCloudAccounts", "[]config.CloudAccount", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) SaveAccountCredential(ctx context.Context, accountID, credentialType, encryptedBlob string) error {
	m.record("SaveAccountCredential", ctx, accountID, credentialType, encryptedBlob)
	if !isExpected(&m.Mock, "SaveAccountCredential") {
		return nil
	}
	args := m.Called(ctx, accountID, credentialType, encryptedBlob)
	return args.Error(0)
}

func (m *MockConfigStore) GetAccountCredential(ctx context.Context, accountID, credentialType string) (string, error) {
	m.record("GetAccountCredential", ctx, accountID, credentialType)
	if !isExpected(&m.Mock, "GetAccountCredential") {
		return "", nil
	}
	args := m.Called(ctx, accountID, credentialType)
	return args.String(0), args.Error(1)
}

func (m *MockConfigStore) DeleteAccountCredentials(ctx context.Context, accountID string) error {
	m.record("DeleteAccountCredentials", ctx, accountID)
	if !isExpected(&m.Mock, "DeleteAccountCredentials") {
		return nil
	}
	args := m.Called(ctx, accountID)
	return args.Error(0)
}

func (m *MockConfigStore) HasAccountCredentials(ctx context.Context, accountID string) (bool, error) {
	m.record("HasAccountCredentials", ctx, accountID)
	if !isExpected(&m.Mock, "HasAccountCredentials") {
		return false, nil
	}
	args := m.Called(ctx, accountID)
	return args.Bool(0), args.Error(1)
}

func (m *MockConfigStore) GetAccountServiceOverride(ctx context.Context, accountID, provider, service string) (*config.AccountServiceOverride, error) {
	m.record("GetAccountServiceOverride", ctx, accountID, provider, service)
	if !isExpected(&m.Mock, "GetAccountServiceOverride") {
		return nil, nil
	}
	args := m.Called(ctx, accountID, provider, service)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.AccountServiceOverride)
	if !ok {
		m.reporter.wrongReturnType("GetAccountServiceOverride", "*config.AccountServiceOverride", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) SaveAccountServiceOverride(ctx context.Context, override *config.AccountServiceOverride) error {
	m.record("SaveAccountServiceOverride", ctx, override)
	if m.SaveAccountServiceOverrideFn != nil {
		return m.SaveAccountServiceOverrideFn(ctx, override)
	}
	if !isExpected(&m.Mock, "SaveAccountServiceOverride") {
		return nil
	}
	args := m.Called(ctx, override)
	return args.Error(0)
}

// SaveAccountServiceOverrideAuditedTx falls back to SaveAccountServiceOverride
// when no expectation is registered (see UpdatePurchasePlanTx), so tests that
// seed SaveAccountServiceOverrideFn keep observing the write.
func (m *MockConfigStore) SaveAccountServiceOverrideAuditedTx(ctx context.Context, tx pgx.Tx, override *config.AccountServiceOverride, audit config.AuditContext) error {
	m.record("SaveAccountServiceOverrideAuditedTx", ctx, tx, override, audit)
	if !isExpected(&m.Mock, "SaveAccountServiceOverrideAuditedTx") {
		return m.SaveAccountServiceOverride(ctx, override)
	}
	args := m.Called(ctx, tx, override, audit)
	return args.Error(0)
}
