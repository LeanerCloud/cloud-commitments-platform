package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/accounts"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/google/uuid"
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

// buildOrgRootAWSConfig resolves the org-root account's stored credentials and
// returns an aws.Config configured to call AWS APIs as that account.
//
// Returns the resolver's error wrapped as a regular Go error (which the
// handler-default mapping surfaces as 500) — NOT a 400 ClientError — because
// ResolveAWSCredentialProvider mixes definite client-side validation
// failures (e.g., missing aws_role_arn for role_arn mode) with transient
// server-side failures (credential store unavailable, network errors during
// access-key load). Without a sentinel/typed error in the credentials
// package today, blanket-400 was misleading; 5xx is the safer default and
// retries are possible. Refining this to a proper 400/500 split is tracked
// in the credentials-package error-type cleanup (out of scope for this PR;
// see CR pass 1 on #212).
func (h *Handler) buildOrgRootAWSConfig(ctx context.Context, root *config.CloudAccount) (aws.Config, error) {
	baseCfg, err := h.getBaseAWSConfig(ctx)
	if err != nil {
		return aws.Config{}, fmt.Errorf("accounts: load base aws config: %w", err)
	}
	stsClient := sts.NewFromConfig(baseCfg)
	credProvider, err := credentials.ResolveAWSCredentialProviderWithOpts(ctx, root, h.credStore, stsClient, h.bastionResolveOptions(baseCfg))
	if err != nil {
		return aws.Config{}, fmt.Errorf("accounts: resolve credentials for org root %s: %w", root.ID, err)
	}
	cfg := baseCfg.Copy()
	cfg.Credentials = credProvider
	return cfg, nil
}

// runOrgDiscovery dispatches to the configured discovery function — the
// injectable seam Handler.discoverOrgFn for tests, falling back to the real
// accounts.DiscoverOrgAccounts in production.
func (h *Handler) runOrgDiscovery(ctx context.Context, cfg aws.Config) (*accounts.OrgDiscoveryResult, error) {
	discoverFn := h.discoverOrgFn
	if discoverFn == nil {
		discoverFn = accounts.DiscoverOrgAccounts
	}
	disco, err := discoverFn(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("accounts: org discovery failed: %w", err)
	}
	return disco, nil
}

// persistDiscoveredMembers dedupes the discovered list against existing aws
// rows and persists each new one with the spec-mandated defaults
// (enabled=false, aws_auth_mode="", aws_bastion_id=root.ID). Returns the
// {discovered, created, skipped} summary.
func (h *Handler) persistDiscoveredMembers(ctx context.Context, root *config.CloudAccount, members []config.CloudAccount) (DiscoverOrgResult, error) {
	awsProvider := "aws"
	existing, err := h.config.ListCloudAccounts(ctx, config.CloudAccountFilter{Provider: &awsProvider})
	if err != nil {
		return DiscoverOrgResult{}, fmt.Errorf("accounts: list existing aws accounts: %w", err)
	}
	knownExternal := make(map[string]struct{}, len(existing))
	for i := range existing {
		knownExternal[existing[i].ExternalID] = struct{}{}
	}

	result := DiscoverOrgResult{Discovered: len(members)}
	now := time.Now()
	for i := range members {
		member := members[i]
		if _, found := knownExternal[member.ExternalID]; found {
			result.Skipped++
			continue
		}
		// Defaults the spec mandates: persist disabled (operator review gate)
		// + bastion-id pre-filled with this org root's ID. AWSAuthMode is
		// LEFT EMPTY on purpose — pre-setting it to "bastion" while
		// AWSRoleARN is empty would leave the row in a half-configured
		// bastion shape (no role to assume) that the operator could
		// mistake for a usable account.
		// The operator's review step must set both AWSAuthMode="bastion"
		// AND a non-empty AWSRoleARN before flipping enabled=true; the
		// scheduler silently skips disabled rows in the meantime, and the
		// empty AWSAuthMode we persist here also fails
		// ResolveAWSCredentialProvider's switch with a clear
		// "unsupported aws_auth_mode" error if the row is enabled
		// prematurely. (CR pass 1 on PR #212.)
		member.Enabled = false
		member.ID = uuid.New().String()
		member.CreatedAt = now
		member.UpdatedAt = now
		member.AWSAuthMode = ""
		member.AWSBastionID = root.ID
		if err := h.config.CreateCloudAccount(ctx, &member); err != nil {
			if isDuplicateKeyError(err) {
				knownExternal[member.ExternalID] = struct{}{}
				result.Skipped++
				continue
			}
			return DiscoverOrgResult{}, fmt.Errorf("accounts: persist discovered %s: %w", member.ExternalID, err)
		}
		knownExternal[member.ExternalID] = struct{}{}
		result.Created++
	}
	return result, nil
}
