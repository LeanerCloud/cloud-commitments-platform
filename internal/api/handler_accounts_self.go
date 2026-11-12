package api

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"
)

// markSelfAccount sets IsSelf=true on the account matching the source
// identity, then attaches each account's derived notices. The self-account
// has no role ARN, so its notices use CUDly's own partition from STS.
func (h *Handler) markSelfAccount(ctx context.Context, accts []config.CloudAccount) {
	si := h.resolveSourceIdentity(ctx)
	var hostPartition string
	if si != nil {
		hostPartition = si.Partition
	}
	for i := range accts {
		if si != nil && si.ExternalID() != "" && accts[i].Provider == si.Provider && accts[i].ExternalID == si.ExternalID() {
			accts[i].IsSelf = true
		}
		annotateAccountNotices(&accts[i], hostPartition)
	}
}

// createSelfAccount handles POST /api/accounts/self.
// Auto-creates an account for CUDly's own host cloud using ambient credentials.
func (h *Handler) createSelfAccount(ctx context.Context, httpReq *events.LambdaFunctionURLRequest) (any, error) {
	if _, err := h.requirePermission(ctx, httpReq, "create", "accounts"); err != nil {
		return nil, err
	}

	si := h.resolveSourceIdentity(ctx)
	if si == nil || si.ExternalID() == "" {
		return nil, NewClientError(400, "source identity not available — CUDly cannot detect its own cloud account")
	}

	req := buildSelfAccountRequest(si)
	if err := validateCloudAccountRequest(req); err != nil {
		return nil, err
	}

	now := time.Now()
	account := cloudAccountFromRequest(req)
	account.ID = uuid.New().String()
	account.CreatedAt = now
	account.UpdatedAt = now
	account.Enabled = true

	if err := h.config.CreateCloudAccount(ctx, account); err != nil {
		return nil, classifyStoreError(err, "self-account already exists")
	}

	account.IsSelf = true
	annotateAccountNotices(account, si.Partition)
	return account, nil
}

func buildSelfAccountRequest(si *sourceIdentity) CloudAccountRequest {
	enabled := true
	req := CloudAccountRequest{
		Name:       "CUDly host",
		Provider:   si.Provider,
		ExternalID: si.ExternalID(),
		Enabled:    &enabled,
	}
	switch si.Provider {
	case "aws":
		req.AWSAuthMode = "role_arn"
	case "azure":
		req.AzureAuthMode = "managed_identity"
		req.AzureSubscriptionID = si.SubscriptionID
		req.AzureTenantID = si.TenantID
		req.AzureClientID = si.ClientID
	case "gcp":
		req.GCPAuthMode = "application_default"
		req.GCPProjectID = si.ProjectID
	}
	return req
}
