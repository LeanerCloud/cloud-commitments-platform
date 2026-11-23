package config

import (
	"time"
)

// ConfigSetting represents a configuration setting for the defaults system.
type ConfigSetting struct { //nolint:revive // exported: doc comment style intentional
	UpdatedAt   time.Time `json:"updated_at"`
	Value       any       `json:"value"`
	Key         string    `json:"key"`
	Type        string    `json:"type"` // int, float, bool, string, json
	Category    string    `json:"category"`
	Description string    `json:"description"`
}

// CloudAccount represents a single managed cloud account/subscription/project.
type CloudAccount struct {
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Azure-specific
	AzureTenantID       string `json:"azure_tenant_id,omitempty"`
	AzureSubscriptionID string `json:"azure_subscription_id,omitempty"`
	AzureClientID       string `json:"azure_client_id,omitempty"`
	AzureAuthMode       string `json:"azure_auth_mode,omitempty"`
	CreatedBy           string `json:"created_by,omitempty"`
	Provider            string `json:"provider"`
	ExternalID          string `json:"external_id"`
	// AWS-specific
	AWSAuthMode             string `json:"aws_auth_mode,omitempty"`
	AWSRoleARN              string `json:"aws_role_arn,omitempty"`
	AWSExternalID           string `json:"aws_external_id,omitempty"`
	AWSBastionID            string `json:"aws_bastion_id,omitempty"`
	AWSWebIdentityTokenFile string `json:"aws_web_identity_token_file,omitempty"`
	Name                    string `json:"name"`
	ContactEmail            string `json:"contact_email,omitempty"`
	ID                      string `json:"id"`
	// GCPWIFAudience is the full Workload Identity Pool provider
	// resource used as the STS audience when exchanging a CUDly
	// KMS-signed JWT for a GCP access token. Only set for accounts
	// using the secret-free workload_identity_federation path.
	// Shape: //iam.googleapis.com/projects/<number>/locations/global/workloadIdentityPools/<pool>/providers/<provider>
	GCPWIFAudience string `json:"gcp_wif_audience,omitempty"`
	// GCP-specific
	GCPProjectID       string `json:"gcp_project_id,omitempty"`
	GCPClientEmail     string `json:"gcp_client_email,omitempty"`
	GCPAuthMode        string `json:"gcp_auth_mode,omitempty"`
	Description        string `json:"description,omitempty"`
	BastionAccountName string `json:"bastion_account_name,omitempty"` // derived, not stored in DB
	// Notices are warnings the API derives for the response, such as the
	// aws-cn Savings Plans limitation (issue #1194). Derived, not stored in DB.
	Notices               []AccountNotice `json:"notices,omitempty"`
	IsSelf                bool            `json:"is_self,omitempty"`         // derived, not stored in DB
	AWSIsOrgRoot          bool            `json:"aws_is_org_root,omitempty"` // AWS-specific
	CredentialsConfigured bool            `json:"credentials_configured"`    // derived, not stored in DB
	Enabled               bool            `json:"enabled"`
}

// CloudAccountFilter for ListCloudAccounts queries.
type CloudAccountFilter struct {
	Provider  *string
	Enabled   *bool
	BastionID *string // return accounts whose aws_bastion_id = *BastionID
	Search    string  // substring match on name or external_id
}

// AccountServiceOverride is a sparse per-account override on top of the global ServiceConfig.
// Nil pointer fields inherit the global value.
type AccountServiceOverride struct {
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	RampSchedule   *string   `json:"ramp_schedule,omitempty"`
	Enabled        *bool     `json:"enabled,omitempty"`
	Term           *int      `json:"term,omitempty"`
	Payment        *string   `json:"payment,omitempty"`
	Coverage       *float64  `json:"coverage,omitempty"`
	Provider       string    `json:"provider"`
	ID             string    `json:"id"`
	Service        string    `json:"service"`
	AccountID      string    `json:"account_id"`
	IncludeEngines []string  `json:"include_engines,omitempty"`
	ExcludeRegions []string  `json:"exclude_regions,omitempty"`
	IncludeTypes   []string  `json:"include_types,omitempty"`
	ExcludeTypes   []string  `json:"exclude_types,omitempty"`
	IncludeRegions []string  `json:"include_regions,omitempty"`
	ExcludeEngines []string  `json:"exclude_engines,omitempty"`
}

// AccountRegistration represents a self-service registration request from a
// target account owner. Submitted via POST /api/register during Terraform apply
// of the federation IaC, then approved or rejected by a CUDly admin.
type AccountRegistration struct {
	UpdatedAt            time.Time  `json:"updated_at"`
	CreatedAt            time.Time  `json:"created_at"`
	CloudAccountID       *string    `json:"cloud_account_id,omitempty"`
	ReviewedAt           *time.Time `json:"reviewed_at,omitempty"`
	ReviewedBy           *string    `json:"reviewed_by,omitempty"`
	AzureClientID        string     `json:"azure_client_id,omitempty"`
	GCPProjectID         string     `json:"gcp_project_id,omitempty"`
	Description          string     `json:"description,omitempty"`
	SourceProvider       string     `json:"source_provider,omitempty"`
	AWSRoleARN           string     `json:"aws_role_arn,omitempty"`
	AWSAuthMode          string     `json:"aws_auth_mode,omitempty"`
	AWSExternalID        string     `json:"aws_external_id,omitempty"`
	AzureSubscriptionID  string     `json:"azure_subscription_id,omitempty"`
	AzureTenantID        string     `json:"azure_tenant_id,omitempty"`
	ID                   string     `json:"id"`
	AzureAuthMode        string     `json:"azure_auth_mode,omitempty"`
	ContactEmail         string     `json:"contact_email"`
	GCPClientEmail       string     `json:"gcp_client_email,omitempty"`
	GCPAuthMode          string     `json:"gcp_auth_mode,omitempty"`
	GCPWIFAudience       string     `json:"gcp_wif_audience,omitempty"` // Full WIF provider resource; only set for federated path.
	RegCredentialType    string     `json:"reg_credential_type,omitempty"`
	RegCredentialPayload string     `json:"-"` // never returned in API responses (encrypted at rest)
	ReferenceToken       string     `json:"-"` // registrant's status credential; only submitRegistration returns it (#310)
	RejectionReason      string     `json:"rejection_reason,omitempty"`
	AccountName          string     `json:"account_name"`
	ExternalID           string     `json:"external_id"`
	Provider             string     `json:"provider"`
	Status               string     `json:"status"`                    // pending, approved, rejected
	HasCredentials       bool       `json:"has_credentials,omitempty"` // derived: true when reg_credential_type is set
}

// AccountRegistrationFilter for ListAccountRegistrations queries.
type AccountRegistrationFilter struct {
	Status   *string
	Provider *string
	Search   string
}
