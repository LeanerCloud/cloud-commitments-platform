package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

// DiscoverOrgRequest is the request body for POST /api/accounts/discover-org.
// AccountID is the UUID of the org-root cloud account whose stored credentials
// will be used to call AWS Organizations.
type DiscoverOrgRequest struct {
	AccountID string `json:"account_id"`
}

// DiscoverOrgResult is the response shape for POST /api/accounts/discover-org.
// Discovered is the total number of member accounts the AWS Organizations API
// returned; Created is the number of new cloud_accounts rows persisted; Skipped
// is the number that already existed (matched by provider+external_id).
type DiscoverOrgResult struct {
	Discovered int `json:"discovered"`
	Created    int `json:"created"`
	Skipped    int `json:"skipped"`
}

// discoverOrgAccounts handles POST /api/accounts/discover-org. The endpoint
// (1) validates the named cloud account is an AWS org root, (2) resolves its
// stored credentials, (3) calls AWS Organizations ListAccounts via the
// credentials, (4) deduplicates against existing aws cloud_accounts rows by
// external_id, and (5) persists the new ones with enabled=false, an empty
// aws_auth_mode, and aws_bastion_id pointing at the org root, so an operator
// must review/approve each discovered account and explicitly choose the
// bastion auth mode plus role ARN before it'll be picked up by the scheduler.
// See issue #208 for the spec; see specs/multi-account-execution/
// acceptance.md F-1..F-3 for the acceptance criteria.
func (h *Handler) discoverOrgAccounts(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	// Admin-only: org discovery can create N cloud_accounts rows in one call
	// and may bring unfamiliar accounts into the roster. Even though those
	// rows boot disabled, the elevated privilege makes admin-scope the right
	// gate (CR pass 1 on PR #212).
	session, err := h.requireAdmin(ctx, req)
	if err != nil {
		return nil, err
	}

	root, err := h.parseDiscoverOrgRoot(ctx, session, req.Body)
	if err != nil {
		return nil, err
	}

	cfg, err := h.buildOrgRootAWSConfig(ctx, root)
	if err != nil {
		return nil, err
	}

	disco, err := h.runOrgDiscovery(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if disco == nil {
		return DiscoverOrgResult{}, nil
	}

	return h.persistDiscoveredMembers(ctx, root, disco.Accounts)
}

// parseDiscoverOrgRoot decodes the request body, loads the named account, and
// validates it's a usable AWS org root. Returns ClientErrors for the four
// failure modes the spec enumerates (bad JSON / bad UUID / not-aws / not-root)
// + 404 for missing-account. Pulled out of discoverOrgAccounts to keep that
// function under the gocyclo budget.
func (h *Handler) parseDiscoverOrgRoot(ctx context.Context, session *Session, rawBody string) (*config.CloudAccount, error) {
	var body DiscoverOrgRequest
	if err := json.Unmarshal([]byte(rawBody), &body); err != nil {
		return nil, NewClientError(400, "invalid JSON body")
	}
	if err := validateUUID(body.AccountID); err != nil {
		return nil, err
	}

	// The org root's credentials list the whole organization and every member
	// is persisted as a row, so the root must be in the caller's scope. Missing
	// and out-of-scope roots share one not-found refusal.
	root, err := h.requireAccountAccess(ctx, session, body.AccountID)
	if err != nil {
		return nil, err
	}
	if err := h.requireUnrestrictedDiscoveryScope(ctx, session); err != nil {
		return nil, err
	}
	if root.Provider != "aws" {
		return nil, NewClientError(400, "discover-org requires an aws account")
	}
	if !root.AWSIsOrgRoot {
		return nil, NewClientError(400, "account is not configured as an org root (set aws_is_org_root=true)")
	}
	return root, nil
}

// requireUnrestrictedDiscoveryScope refuses org discovery for account-scoped
// callers (issue #626). Discovery persists EVERY organization member as a new
// row, deduped against all aws rows regardless of scope, so a scoped caller
// would create rows outside their scope (or inside it via a name match) and
// learn from the created/skipped counts which external ids other tenants
// already registered. Filtering per member cannot help: the new rows get fresh
// UUIDs, so only a name match could ever place one in scope. Runs after
// requireAccountAccess so an out-of-scope root keeps its enumeration-safe 404,
// and before the root's credentials are loaded or AWS is called.
func (h *Handler) requireUnrestrictedDiscoveryScope(ctx context.Context, session *Session) error {
	scope, err := h.getAccountScope(ctx, session)
	if err != nil {
		return fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if !scope.AllowsAll() {
		return NewClientError(403, "permission denied: org discovery creates rows for every organization member and requires unrestricted account access")
	}
	return nil
}
