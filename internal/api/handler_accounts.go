package api

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

// listAccounts handles GET /api/accounts.
func (h *Handler) listAccounts(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "view", "accounts")
	if err != nil {
		return nil, err
	}

	filter, err := buildAccountFilter(req.QueryStringParameters)
	if err != nil {
		return nil, err
	}

	accts, err := h.config.ListCloudAccounts(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}

	if accts == nil {
		accts = []config.CloudAccount{}
	}

	// Filter by allowed accts if the user has restricted access.
	// An empty list or one containing "*" grants unrestricted access.
	// Otherwise each entry is matched against the account's ID, or its Name
	// when no other registered account shares it (issue #433).
	allowedAccounts, err := h.getAccountScope(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if !allowedAccounts.AllowsAll() {
		filtered := accts[:0]
		for i := range accts {
			acct := accts[i]
			if allowedAccounts.Allows(acct.ID, acct.Name) {
				filtered = append(filtered, acct)
			}
		}
		accts = filtered
	}

	// Mark the self-account (the account matching CUDly's own host identity)
	// and attach the derived notices (issue #1194).
	h.markSelfAccount(ctx, accts)

	return accts, nil
}

// AccountSummary is the minimal-disclosure projection of a cloud account used
// by the global topbar filter and the create-plan-from-commitment target
// prefill (issues #949, #951). It deliberately omits every credential/config
// field (role ARNs, subscription IDs, client emails, auth modes, bastion IDs,
// the self-account marker) so the endpoint can be gated on a low read verb that
// Standard / Read-Only users already hold, without leaking sensitive account
// configuration. The full CloudAccount object stays behind GET /api/accounts
// (view:accounts, admin-grade).
type AccountSummary struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ExternalID string `json:"external_id"`
	Provider   string `json:"provider"`
}

// listAccountsMinimal handles GET /api/accounts/list.
//
// Returns the minimal AccountSummary projection scoped by the session's
// allowed_accounts list. Gated on view:recommendations (held by both the
// Standard Users and Read-Only Users groups) rather than view:accounts, so the
// account dropdown in the global filter and the plan-target prefill work for
// non-admin users without exposing the credential/config metadata carried by
// the full GET /api/accounts response. See issues #949 / #951.
func (h *Handler) listAccountsMinimal(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "view", "recommendations")
	if err != nil {
		return nil, err
	}

	filter, err := buildAccountFilter(req.QueryStringParameters)
	if err != nil {
		return nil, err
	}

	accts, err := h.config.ListCloudAccounts(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}

	allowedAccounts, err := h.getAccountScope(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	unrestricted := allowedAccounts.AllowsAll()

	// Build the minimal projection in place, applying allowed_accounts scoping
	// during the copy so a restricted user only ever sees their entitled rows.
	summaries := make([]AccountSummary, 0, len(accts))
	for i := range accts {
		acct := &accts[i]
		if !unrestricted && !allowedAccounts.Allows(acct.ID, acct.Name) {
			continue
		}
		summaries = append(summaries, AccountSummary{
			ID:         acct.ID,
			Name:       acct.Name,
			ExternalID: acct.ExternalID,
			Provider:   acct.Provider,
		})
	}

	return summaries, nil
}

// annotateAccountNotices sets the derived notices on acct, such as the aws-cn
// Savings Plans limitation (issue #1194). The partition comes from the
// account's role ARN; a self-account with no role ARN falls back to
// hostPartition, CUDly's own partition ("" when unknown).
func annotateAccountNotices(acct *config.CloudAccount, hostPartition string) {
	partition := acct.AWSPartition()
	if partition == "" && acct.IsSelf && acct.Provider == "aws" {
		partition = hostPartition
	}
	acct.Notices = config.AWSPartitionNotices(partition)
}

// buildAccountFilter constructs a CloudAccountFilter from query parameters.
// An unknown provider or an enabled value other than "true"/"false" is a 400
// instead of being silently ignored (issue #312); an empty value or
// provider "all" means no filter.
func buildAccountFilter(params map[string]string) (config.CloudAccountFilter, error) {
	var filter config.CloudAccountFilter

	provider, err := providerFilterParam(params["provider"])
	if err != nil {
		return filter, err
	}
	if provider != "" {
		filter.Provider = &provider
	}

	switch params["enabled"] {
	case "":
	case "true":
		t := true
		filter.Enabled = &t
	case "false":
		f := false
		filter.Enabled = &f
	default:
		return filter, NewClientError(400, "invalid enabled: must be true or false")
	}

	filter.Search = params["search"]

	return filter, nil
}

// getAccount handles GET /api/accounts/:id.
func (h *Handler) getAccount(ctx context.Context, req *events.LambdaFunctionURLRequest, id string) (any, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	session, err := h.requirePermission(ctx, req, "view", "accounts")
	if err != nil {
		return nil, err
	}

	account, err := h.requireAccountAccess(ctx, session, id)
	if err != nil {
		return nil, err
	}

	annotateAccountNotices(account, "")
	return account, nil
}
