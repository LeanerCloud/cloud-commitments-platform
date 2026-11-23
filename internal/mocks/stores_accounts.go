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

// UpdateAccountServiceOverrideAtomic mocks the locked read-merge-write of one
// override. With an expectation it dispatches through m.Called; otherwise it
// emulates the store via the mocked WithTx, GetAccountServiceOverride, merge
// and SaveAccountServiceOverrideAuditedTx, so tests that seed those keep working.
func (m *MockConfigStore) UpdateAccountServiceOverrideAtomic(ctx context.Context, accountID, provider, service string, audit config.AuditContext, merge func(*config.AccountServiceOverride) (*config.AccountServiceOverride, error)) (*config.AccountServiceOverride, error) {
	m.record("UpdateAccountServiceOverrideAtomic", ctx, accountID, provider, service, audit, merge)
	if isExpected(&m.Mock, "UpdateAccountServiceOverrideAtomic") {
		args := m.Called(ctx, accountID, provider, service, audit, merge)
		if args.Get(0) == nil {
			return nil, args.Error(1)
		}
		v, ok := args.Get(0).(*config.AccountServiceOverride)
		if !ok {
			m.reporter.wrongReturnType("UpdateAccountServiceOverrideAtomic", "*config.AccountServiceOverride", args.Get(0))
		}
		return v, args.Error(1)
	}
	var next *config.AccountServiceOverride
	err := m.WithTx(ctx, func(tx pgx.Tx) error {
		existing, err := m.GetAccountServiceOverride(ctx, accountID, provider, service)
		if err != nil {
			return err
		}
		if next, err = merge(existing); err != nil {
			return err
		}
		return m.SaveAccountServiceOverrideAuditedTx(ctx, tx, next, audit)
	})
	if err != nil {
		return nil, err
	}
	return next, nil
}

// DeleteAccountServiceOverrideAuditedTx falls back to
// DeleteAccountServiceOverride when no expectation is registered.
func (m *MockConfigStore) DeleteAccountServiceOverrideAuditedTx(ctx context.Context, tx pgx.Tx, accountID, provider, service string, audit config.AuditContext) error {
	m.record("DeleteAccountServiceOverrideAuditedTx", ctx, tx, accountID, provider, service, audit)
	if !isExpected(&m.Mock, "DeleteAccountServiceOverrideAuditedTx") {
		return m.DeleteAccountServiceOverride(ctx, accountID, provider, service)
	}
	args := m.Called(ctx, tx, accountID, provider, service, audit)
	return args.Error(0)
}

// SaveConfigAuditEventTx records the event and returns nil when no
// expectation is registered.
func (m *MockConfigStore) SaveConfigAuditEventTx(ctx context.Context, tx pgx.Tx, event *config.AuditEvent) error {
	m.record("SaveConfigAuditEventTx", ctx, tx, event)
	if !isExpected(&m.Mock, "SaveConfigAuditEventTx") {
		return nil
	}
	args := m.Called(ctx, tx, event)
	return args.Error(0)
}

func (m *MockConfigStore) DeleteAccountServiceOverride(ctx context.Context, accountID, provider, service string) error {
	m.record("DeleteAccountServiceOverride", ctx, accountID, provider, service)
	if !isExpected(&m.Mock, "DeleteAccountServiceOverride") {
		return nil
	}
	args := m.Called(ctx, accountID, provider, service)
	return args.Error(0)
}

func (m *MockConfigStore) ListAccountServiceOverrides(ctx context.Context, accountID string) ([]config.AccountServiceOverride, error) {
	m.record("ListAccountServiceOverrides", ctx, accountID)
	if !isExpected(&m.Mock, "ListAccountServiceOverrides") {
		return nil, nil
	}
	args := m.Called(ctx, accountID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.AccountServiceOverride)
	if !ok {
		m.reporter.wrongReturnType("ListAccountServiceOverrides", "[]config.AccountServiceOverride", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) CreateAccountRegistration(ctx context.Context, reg *config.AccountRegistration) error {
	m.record("CreateAccountRegistration", ctx, reg)
	args := m.Called(ctx, reg)
	return args.Error(0)
}

func (m *MockConfigStore) GetAccountRegistration(ctx context.Context, id string) (*config.AccountRegistration, error) {
	m.record("GetAccountRegistration", ctx, id)
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.AccountRegistration)
	if !ok {
		m.reporter.wrongReturnType("GetAccountRegistration", "*config.AccountRegistration", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) GetAccountRegistrationByToken(ctx context.Context, token string) (*config.AccountRegistration, error) {
	m.record("GetAccountRegistrationByToken", ctx, token)
	args := m.Called(ctx, token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.AccountRegistration)
	if !ok {
		m.reporter.wrongReturnType("GetAccountRegistrationByToken", "*config.AccountRegistration", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) ListAccountRegistrations(ctx context.Context, filter config.AccountRegistrationFilter) ([]config.AccountRegistration, error) {
	m.record("ListAccountRegistrations", ctx, filter)
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.AccountRegistration)
	if !ok {
		m.reporter.wrongReturnType("ListAccountRegistrations", "[]config.AccountRegistration", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) UpdateAccountRegistration(ctx context.Context, reg *config.AccountRegistration) error {
	m.record("UpdateAccountRegistration", ctx, reg)
	args := m.Called(ctx, reg)
	return args.Error(0)
}

func (m *MockConfigStore) TransitionRegistrationStatus(ctx context.Context, reg *config.AccountRegistration, fromStatus string, actor *string) error {
	m.record("TransitionRegistrationStatus", ctx, reg, fromStatus, actor)
	args := m.Called(ctx, reg, fromStatus, actor)
	return args.Error(0)
}

func (m *MockConfigStore) ClearRegistrationCredentialPayload(ctx context.Context, registrationID string) error {
	m.record("ClearRegistrationCredentialPayload", ctx, registrationID)
	args := m.Called(ctx, registrationID)
	return args.Error(0)
}

func (m *MockConfigStore) ApproveAccountRegistration(ctx context.Context, reg *config.AccountRegistration, account *config.CloudAccount, actor *string) error {
	m.record("ApproveAccountRegistration", ctx, reg, account, actor)
	args := m.Called(ctx, reg, account, actor)
	return args.Error(0)
}

func (m *MockConfigStore) DeleteAccountRegistration(ctx context.Context, id string) error {
	m.record("DeleteAccountRegistration", ctx, id)
	args := m.Called(ctx, id)
	return args.Error(0)
}
