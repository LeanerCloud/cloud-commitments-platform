package config

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// AccountStore holds the cloud account, override and registration methods of StoreInterface; StoreInterface embeds it.
type AccountStore interface {
	// Cloud accounts
	CreateCloudAccount(ctx context.Context, account *CloudAccount) error
	GetCloudAccount(ctx context.Context, id string) (*CloudAccount, error)
	// GetCloudAccountByExternalID looks up a cloud account by its
	// (provider, external_id) pair. Used by the scheduler's ambient
	// collector path to tag rec rows with the registered host-account's
	// UUID when the Lambda's STS identity matches a registered (but
	// possibly disabled) account — so the approve-modal Account column
	// shows the account name instead of `(ambient)`. Returns
	// (nil, nil) when no row matches (the caller treats this as the
	// genuine orphan case). The underlying
	// `UNIQUE(provider, external_id)` constraint on cloud_accounts
	// guarantees the lookup hits an index.
	GetCloudAccountByExternalID(ctx context.Context, provider, externalID string) (*CloudAccount, error)
	UpdateCloudAccount(ctx context.Context, account *CloudAccount) error
	DeleteCloudAccount(ctx context.Context, id string) error
	ListCloudAccounts(ctx context.Context, filter CloudAccountFilter) ([]CloudAccount, error)

	// Account credentials (encrypted blobs; never returned via API)
	SaveAccountCredential(ctx context.Context, accountID, credentialType, encryptedBlob string) error
	// GetAccountCredential returns ErrNotFound when the account has no
	// credential of credentialType.
	GetAccountCredential(ctx context.Context, accountID, credentialType string) (string, error)
	DeleteAccountCredentials(ctx context.Context, accountID string) error
	HasAccountCredentials(ctx context.Context, accountID string) (bool, error)

	// Account service overrides
	GetAccountServiceOverride(ctx context.Context, accountID, provider, service string) (*AccountServiceOverride, error)
	SaveAccountServiceOverride(ctx context.Context, override *AccountServiceOverride) error
	DeleteAccountServiceOverride(ctx context.Context, accountID, provider, service string) error
	// SaveAccountServiceOverrideAuditedTx upserts the override inside the
	// caller's WithTx transaction and records the change (the stored row
	// before and after) in config_audit_events, issue #1493. It serializes on
	// the override's key, so a caller writing several overrides in one tx must
	// write them in a consistent key order. A write that changes no setting
	// records nothing. override is updated to the stored row.
	SaveAccountServiceOverrideAuditedTx(ctx context.Context, tx pgx.Tx, override *AccountServiceOverride, audit AuditContext) error
	// UpdateAccountServiceOverrideAtomic serializes a read-merge-write of one
	// override under the same lock as SaveAccountServiceOverrideAuditedTx and
	// audits the write. merge receives the stored row (nil when none) and
	// returns the full row to write; its error aborts the write and is
	// propagated unchanged. Returns the saved row.
	UpdateAccountServiceOverrideAtomic(ctx context.Context, accountID, provider, service string, audit AuditContext, merge func(existing *AccountServiceOverride) (*AccountServiceOverride, error)) (*AccountServiceOverride, error)
	// DeleteAccountServiceOverrideAuditedTx deletes the override inside the
	// caller's transaction and records the deleted row in config_audit_events.
	// Deleting a missing override records nothing.
	DeleteAccountServiceOverrideAuditedTx(ctx context.Context, tx pgx.Tx, accountID, provider, service string, audit AuditContext) error
	// SaveConfigAuditEventTx records a config_audit_events row inside the
	// caller's transaction (the override import's own event). An empty ID is
	// filled in.
	SaveConfigAuditEventTx(ctx context.Context, tx pgx.Tx, event *AuditEvent) error
	ListAccountServiceOverrides(ctx context.Context, accountID string) ([]AccountServiceOverride, error)

	// Plan ↔ account association
	SetPlanAccounts(ctx context.Context, planID string, accountIDs []string) error
	// SetPlanAccountsGated is SetPlanAccounts with gate run under the plan's
	// row lock, before the write (nil gate: plain set).
	SetPlanAccountsGated(ctx context.Context, planID string, accountIDs []string, gate PlanWriteGate) error
	GetPlanAccounts(ctx context.Context, planID string) ([]CloudAccount, error)

	// Account registrations (self-service enrollment via federation IaC)
	CreateAccountRegistration(ctx context.Context, reg *AccountRegistration) error
	GetAccountRegistration(ctx context.Context, id string) (*AccountRegistration, error)
	GetAccountRegistrationByToken(ctx context.Context, token string) (*AccountRegistration, error)
	ListAccountRegistrations(ctx context.Context, filter AccountRegistrationFilter) ([]AccountRegistration, error)
	UpdateAccountRegistration(ctx context.Context, reg *AccountRegistration) error
	// TransitionRegistrationStatus atomically updates a registration's workflow fields.
	// actor is the UUID of the reviewer (nil for system-initiated transitions).
	TransitionRegistrationStatus(ctx context.Context, reg *AccountRegistration, fromStatus string, actor *string) error
	// ClearRegistrationCredentialPayload removes the encrypted credential kept
	// with a registration. A missing id is not an error.
	ClearRegistrationCredentialPayload(ctx context.Context, registrationID string) error
	// ApproveAccountRegistration atomically approves a pending registration,
	// creates its cloud account and links them. Returns ErrRegistrationConflict
	// when the registration is no longer pending; on any error nothing is saved.
	ApproveAccountRegistration(ctx context.Context, reg *AccountRegistration, account *CloudAccount, actor *string) error
	DeleteAccountRegistration(ctx context.Context, id string) error
}
