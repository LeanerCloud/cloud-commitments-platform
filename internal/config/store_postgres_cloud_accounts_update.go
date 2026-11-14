package config

import (
	"context"
	"fmt"
	"time"
)

// UpdateCloudAccount updates mutable fields of a cloud account. An external_id
// change is refused with ErrAccountExternalIDHasHistory while history rows key
// on the stored value (purchase_history by provider, ri_exchange_history for
// AWS); the check is part of the UPDATE so it cannot race a separate read.
func (s *PostgresStore) UpdateCloudAccount(ctx context.Context, account *CloudAccount) error {
	account.UpdatedAt = time.Now()
	query := `
		UPDATE cloud_accounts SET
			name = $2,
			description = $3,
			contact_email = $4,
			enabled = $5,
			external_id = $6,
			aws_auth_mode = $7,
			aws_role_arn = $8,
			aws_external_id = $9,
			aws_bastion_id = $10,
			aws_web_identity_token_file = $11,
			aws_is_org_root = $12,
			azure_subscription_id = $13,
			azure_tenant_id = $14,
			azure_client_id = $15,
			azure_auth_mode = $16,
			gcp_project_id = $17,
			gcp_client_email = $18,
			gcp_auth_mode = $19,
			gcp_wif_audience = $20,
			updated_at = $21
		WHERE id = $1
		  AND (external_id = $6 OR NOT (
		    EXISTS (SELECT 1 FROM purchase_history ph
		            WHERE lower(ph.provider) = lower(cloud_accounts.provider) AND ph.account_id = cloud_accounts.external_id)
		    OR (cloud_accounts.provider = 'aws' AND EXISTS (
		            SELECT 1 FROM ri_exchange_history rh WHERE rh.account_id = cloud_accounts.external_id))
		  ))
	`
	tag, err := s.db.Exec(ctx, query,
		account.ID,
		account.Name,
		nullStringFromString(account.Description),
		nullStringFromString(account.ContactEmail),
		account.Enabled,
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
		account.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to update cloud account: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cloud_accounts WHERE id = $1)`, account.ID).Scan(&exists); err != nil {
			return fmt.Errorf("failed to check cloud account %s: %w", account.ID, err)
		}
		if exists {
			return ErrAccountExternalIDHasHistory
		}
		return fmt.Errorf("cloud account not found: %s", account.ID)
	}
	return nil
}
