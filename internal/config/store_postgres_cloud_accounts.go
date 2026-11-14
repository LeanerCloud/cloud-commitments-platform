package config

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ==========================================
// CLOUD ACCOUNTS
// ==========================================

// CreateCloudAccount inserts a new cloud account record.
func (s *PostgresStore) CreateCloudAccount(ctx context.Context, account *CloudAccount) error {
	if account.ID == "" {
		account.ID = uuid.New().String()
	}
	now := time.Now()
	account.CreatedAt = now
	account.UpdatedAt = now

	query := `
		INSERT INTO cloud_accounts (
			id, name, description, contact_email, enabled,
			provider, external_id,
			aws_auth_mode, aws_role_arn, aws_external_id, aws_bastion_id, aws_web_identity_token_file, aws_is_org_root,
			azure_subscription_id, azure_tenant_id, azure_client_id, azure_auth_mode,
			gcp_project_id, gcp_client_email, gcp_auth_mode, gcp_wif_audience,
			created_at, updated_at, created_by
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7,
			$8, $9, $10, $11, $12, $13,
			$14, $15, $16, $17,
			$18, $19, $20, $21,
			$22, $23, $24
		)
	`

	_, err := s.db.Exec(ctx, query,
		account.ID,
		account.Name,
		nullStringFromString(account.Description),
		nullStringFromString(account.ContactEmail),
		account.Enabled,
		account.Provider,
		account.ExternalID,
		nullStringFromString(account.AWSAuthMode),
		nullStringFromString(account.AWSRoleARN),
		nullStringFromString(account.AWSExternalID),
		nullStringFromString(account.AWSBastionID),
		nullStringFromString(account.AWSWebIdentityTokenFile),
		account.AWSIsOrgRoot,
		nullStringFromString(account.AzureSubscriptionID),
		nullStringFromString(account.AzureTenantID),
		nullStringFromString(account.AzureClientID),
		nullStringFromString(account.AzureAuthMode),
		nullStringFromString(account.GCPProjectID),
		nullStringFromString(account.GCPClientEmail),
		nullStringFromString(account.GCPAuthMode),
		nullStringFromString(account.GCPWIFAudience),
		account.CreatedAt,
		account.UpdatedAt,
		nullStringFromString(account.CreatedBy),
	)
	if err != nil {
		return fmt.Errorf("failed to create cloud account: %w", err)
	}
	return nil
}

// GetCloudAccount returns a single cloud account by ID with credentials_configured derived.
func (s *PostgresStore) GetCloudAccount(ctx context.Context, id string) (*CloudAccount, error) {
	query := `
		SELECT
			ca.id, ca.name, COALESCE(ca.description,''), COALESCE(ca.contact_email,''),
			ca.enabled, ca.provider, ca.external_id,
			COALESCE(ca.aws_auth_mode,''), COALESCE(ca.aws_role_arn,''),
			COALESCE(ca.aws_external_id,''), COALESCE(ca.aws_bastion_id::text,''),
			COALESCE(ca.aws_web_identity_token_file,''),
			ca.aws_is_org_root,
			COALESCE(ca.azure_subscription_id,''), COALESCE(ca.azure_tenant_id,''),
			COALESCE(ca.azure_client_id,''), COALESCE(ca.azure_auth_mode,''),
			COALESCE(ca.gcp_project_id,''), COALESCE(ca.gcp_client_email,''), COALESCE(ca.gcp_auth_mode,''),
			COALESCE(ca.gcp_wif_audience,''),
			ca.created_at, ca.updated_at, COALESCE(ca.created_by::text,''),
			EXISTS(SELECT 1 FROM account_credentials ac WHERE ac.account_id = ca.id) AS credentials_configured
		FROM cloud_accounts ca
		WHERE ca.id = $1
	`

	var account CloudAccount
	err := s.db.QueryRow(ctx, query, id).Scan(
		&account.ID, &account.Name, &account.Description, &account.ContactEmail,
		&account.Enabled, &account.Provider, &account.ExternalID,
		&account.AWSAuthMode, &account.AWSRoleARN, &account.AWSExternalID, &account.AWSBastionID,
		&account.AWSWebIdentityTokenFile,
		&account.AWSIsOrgRoot,
		&account.AzureSubscriptionID, &account.AzureTenantID, &account.AzureClientID, &account.AzureAuthMode,
		&account.GCPProjectID, &account.GCPClientEmail, &account.GCPAuthMode,
		&account.GCPWIFAudience,
		&account.CreatedAt, &account.UpdatedAt, &account.CreatedBy,
		&account.CredentialsConfigured,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get cloud account: %w", err)
	}
	return &account, nil
}

// GetCloudAccountByExternalID returns a single cloud account matched by
// (provider, external_id). Used by the scheduler ambient-path tagging fix
// for issue #604 — when the Lambda's STS identity matches a registered
// account (regardless of enabled state), rec rows are stamped with that
// account's UUID so the approve modal shows the account name instead of
// `(ambient)`. Returns (nil, nil) when no row matches.
//
// The cloud_accounts table declares `UNIQUE(provider, external_id)`
// (migration 000011), which guarantees the lookup hits an index.
func (s *PostgresStore) GetCloudAccountByExternalID(ctx context.Context, provider, externalID string) (*CloudAccount, error) {
	query := `
		SELECT
			ca.id, ca.name, COALESCE(ca.description,''), COALESCE(ca.contact_email,''),
			ca.enabled, ca.provider, ca.external_id,
			COALESCE(ca.aws_auth_mode,''), COALESCE(ca.aws_role_arn,''),
			COALESCE(ca.aws_external_id,''), COALESCE(ca.aws_bastion_id::text,''),
			COALESCE(ca.aws_web_identity_token_file,''),
			ca.aws_is_org_root,
			COALESCE(ca.azure_subscription_id,''), COALESCE(ca.azure_tenant_id,''),
			COALESCE(ca.azure_client_id,''), COALESCE(ca.azure_auth_mode,''),
			COALESCE(ca.gcp_project_id,''), COALESCE(ca.gcp_client_email,''), COALESCE(ca.gcp_auth_mode,''),
			COALESCE(ca.gcp_wif_audience,''),
			ca.created_at, ca.updated_at, COALESCE(ca.created_by::text,''),
			EXISTS(SELECT 1 FROM account_credentials ac WHERE ac.account_id = ca.id) AS credentials_configured
		FROM cloud_accounts ca
		WHERE ca.provider = $1 AND ca.external_id = $2
	`

	var account CloudAccount
	err := s.db.QueryRow(ctx, query, provider, externalID).Scan(
		&account.ID, &account.Name, &account.Description, &account.ContactEmail,
		&account.Enabled, &account.Provider, &account.ExternalID,
		&account.AWSAuthMode, &account.AWSRoleARN, &account.AWSExternalID, &account.AWSBastionID,
		&account.AWSWebIdentityTokenFile,
		&account.AWSIsOrgRoot,
		&account.AzureSubscriptionID, &account.AzureTenantID, &account.AzureClientID, &account.AzureAuthMode,
		&account.GCPProjectID, &account.GCPClientEmail, &account.GCPAuthMode,
		&account.GCPWIFAudience,
		&account.CreatedAt, &account.UpdatedAt, &account.CreatedBy,
		&account.CredentialsConfigured,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get cloud account by external id: %w", err)
	}
	return &account, nil
}
