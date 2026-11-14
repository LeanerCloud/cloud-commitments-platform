package config

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// GetPlanAccounts returns all cloud accounts associated with a plan.
func (s *PostgresStore) GetPlanAccounts(ctx context.Context, planID string) ([]CloudAccount, error) {
	return getPlanAccountsFrom(ctx, s.db, planID)
}

// planAccountsQuerier is satisfied by the pool and by a transaction.
type planAccountsQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func getPlanAccountsFrom(ctx context.Context, q planAccountsQuerier, planID string) ([]CloudAccount, error) {
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
		JOIN plan_accounts pa ON pa.account_id = ca.id
		WHERE pa.plan_id = $1
		ORDER BY ca.name
	`
	rows, err := q.Query(ctx, query, planID)
	if err != nil {
		return nil, fmt.Errorf("failed to get plan accounts: %w", err)
	}
	defer rows.Close()

	accounts := make([]CloudAccount, 0)
	for rows.Next() {
		var a CloudAccount
		if err := rows.Scan(
			&a.ID, &a.Name, &a.Description, &a.ContactEmail,
			&a.Enabled, &a.Provider, &a.ExternalID,
			&a.AWSAuthMode, &a.AWSRoleARN, &a.AWSExternalID, &a.AWSBastionID,
			&a.AWSWebIdentityTokenFile,
			&a.AWSIsOrgRoot,
			&a.AzureSubscriptionID, &a.AzureTenantID, &a.AzureClientID, &a.AzureAuthMode,
			&a.GCPProjectID, &a.GCPClientEmail, &a.GCPAuthMode,
			&a.GCPWIFAudience,
			&a.CreatedAt, &a.UpdatedAt, &a.CreatedBy,
			&a.CredentialsConfigured,
		); err != nil {
			return nil, fmt.Errorf("failed to scan plan account: %w", err)
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}
