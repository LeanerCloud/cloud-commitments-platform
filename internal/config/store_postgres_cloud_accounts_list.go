package config

import (
	"context"
	"fmt"
	"strings"
)

// ListCloudAccounts returns accounts matching the filter, with credentials_configured derived.
func (s *PostgresStore) ListCloudAccounts(ctx context.Context, filter CloudAccountFilter) ([]CloudAccount, error) {
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
		WHERE 1=1
	`
	args := []any{}
	i := 1

	if filter.Provider != nil {
		query += fmt.Sprintf(" AND ca.provider = $%d", i)
		args = append(args, *filter.Provider)
		i++
	}
	if filter.Enabled != nil {
		query += fmt.Sprintf(" AND ca.enabled = $%d", i)
		args = append(args, *filter.Enabled)
		i++
	}
	if filter.Search != "" {
		// Escape ILIKE wildcards so user-supplied % and _ are treated as literals.
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(filter.Search)
		// Bind once and reference the same $N twice — Postgres allows parameter
		// reuse, and the single-bind form keeps future filter additions from
		// having to reason about "+2" offsets.
		query += fmt.Sprintf(" AND (ca.name ILIKE $%d ESCAPE '\\' OR ca.external_id ILIKE $%d ESCAPE '\\')", i, i)
		args = append(args, "%"+escaped+"%")
		i++
	}
	if filter.BastionID != nil {
		query += fmt.Sprintf(" AND ca.aws_bastion_id = $%d", i)
		args = append(args, *filter.BastionID)
		i++
	}
	_ = i // suppress "declared but not used" if no more conditions follow

	query += " ORDER BY ca.name"

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list cloud accounts: %w", err)
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
			return nil, fmt.Errorf("failed to scan cloud account: %w", err)
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}
