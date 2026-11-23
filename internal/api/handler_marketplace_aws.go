package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// marketplaceEC2Client is the narrow EC2 interface the marketplace handlers
// need. Using a minimal interface keeps test stubs small.
type marketplaceEC2Client interface {
	CreateMarketplaceListing(ctx context.Context, req ec2MarketplaceListingRequest) (ec2MarketplaceListingResult, error)
	DescribeMarketplaceListing(ctx context.Context, listingID string) (ec2MarketplaceListingResult, error)
	CancelMarketplaceListing(ctx context.Context, listingID string) (ec2MarketplaceListingResult, error)
	// FetchOfferingClass calls AWS DescribeReservedInstances to determine
	// whether a given Reserved Instance is 'standard' or 'convertible'. Used
	// when the purchase_history row has no offering_class stored (pre-migration
	// 000087 rows and externally-created Standard RIs).
	FetchOfferingClass(ctx context.Context, reservedInstancesID string) (string, error)
}

// buildMarketplaceEC2Client honors the injected factory for tests, falling
// back to the direct AWS SDK constructor in production.
func (h *Handler) buildMarketplaceEC2Client(cfg aws.Config) marketplaceEC2Client {
	if h.marketplaceEC2Factory != nil {
		return h.marketplaceEC2Factory(cfg)
	}
	return newMarketplaceEC2Client(cfg)
}

// loadMarketplaceAWSConfig returns an AWS config for the purchase's region that
// signs requests as the purchase's own cloud account, resolved through the same
// credential resolver the purchase path uses. A role_arn account without a role
// ARN is accepted only when it is the host account (credentials.ErrNotHostAccount
// otherwise). Rows without a cloud account (reachable only by sell-any callers
// with unrestricted scope) sign as the registered AWS account that owns the
// row's AWS account id, or else get the host's own credentials only when that
// id is confirmed as the host account; see
// loadMarketplaceNoCloudAccountAWSConfig.
func (h *Handler) loadMarketplaceAWSConfig(ctx context.Context, row *config.PurchaseHistoryRecord) (aws.Config, error) {
	base, err := h.loadAWSConfigWithRegion(ctx, row.Region)
	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to load AWS config: %w", err)
	}
	if row.CloudAccountID == nil || strings.TrimSpace(*row.CloudAccountID) == "" {
		return h.loadMarketplaceNoCloudAccountAWSConfig(ctx, row, base)
	}
	account, err := h.config.GetCloudAccount(ctx, *row.CloudAccountID)
	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to look up the purchase's cloud account: %w", err)
	}
	if account == nil {
		return aws.Config{}, NewClientError(400, "the purchase's cloud account no longer exists")
	}
	if account.Provider != "aws" {
		return aws.Config{}, NewClientError(400, "the purchase's cloud account is not an AWS account")
	}
	return h.marketplaceAccountAWSConfig(ctx, account, base)
}

// marketplaceAccountAWSConfig returns base signing as the given AWS cloud
// account, resolved through the purchase path's credential resolver.
func (h *Handler) marketplaceAccountAWSConfig(ctx context.Context, account *config.CloudAccount, base aws.Config) (aws.Config, error) {
	stsClient := sts.NewFromConfig(base)
	opts := h.bastionResolveOptions(base)
	opts.AmbientProvider, opts.AmbientSTS = base.Credentials, stsClient
	provider, err := credentials.ResolveAWSCredentialProviderWithOpts(ctx, account, h.credStore, stsClient, opts)
	if err != nil {
		if errors.Is(err, credentials.ErrNotHostAccount) {
			return aws.Config{}, NewClientError(400, err.Error())
		}
		return aws.Config{}, fmt.Errorf("failed to resolve AWS credentials for the purchase's account: %w", err)
	}
	cfg := base.Copy()
	cfg.Credentials = provider
	return cfg, nil
}

// marketplaceNoCloudAccountMessage is the tenant-visible refusal for a row with
// no cloud account whose AWS account is not the host account. Like
// credentials.ErrNotHostAccount it never carries the host account id.
const marketplaceNoCloudAccountMessage = "this purchase is not linked to a cloud account and its AWS account is not confirmed as the CUDly host account; it cannot be listed or canceled from here"

// loadMarketplaceNoCloudAccountAWSConfig handles a row with no cloud account.
// Its AWS account id is first resolved to the single registered AWS cloud
// account with that external id, the way revoke (#547) and RI exchange
// history (#553) match such rows; a match signs as that account
// (issue #560). An unregistered or ambiguous id falls back to the host-only
// check in loadMarketplaceHostAWSConfig. A failed lookup is an error, never a
// fallback to the host's credentials.
func (h *Handler) loadMarketplaceNoCloudAccountAWSConfig(ctx context.Context, row *config.PurchaseHistoryRecord, base aws.Config) (aws.Config, error) {
	accountID := strings.TrimSpace(row.AccountID)
	if accountID == "" {
		return loadMarketplaceHostAWSConfig(ctx, row, base)
	}
	accounts, err := h.config.ListCloudAccounts(ctx, config.CloudAccountFilter{})
	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to look up the purchase's AWS account: %w", err)
	}
	if account := uniqueAccountByExternalID(accounts, "aws", accountID); account != nil {
		return h.marketplaceAccountAWSConfig(ctx, account, base)
	}
	return loadMarketplaceHostAWSConfig(ctx, row, base)
}

// loadMarketplaceHostAWSConfig handles a row with no cloud account whose AWS
// account id matches no single registered AWS account: the ambient
// single-account deployment (purchase.SingleCloudAccountIDFromRecs returns
// nil) and legacy or account-deleted rows (cloud_account_id has no backfill
// and is ON DELETE SET NULL). The host's credentials can only act on
// the host account, so they are used only when the row's AWS account id
// passes the same host identity check the resolver applies to a role_arn
// account with no role ARN. Anything else, including an empty account id or a
// failed STS lookup, is refused before any EC2 client is built.
func loadMarketplaceHostAWSConfig(ctx context.Context, row *config.PurchaseHistoryRecord, base aws.Config) (aws.Config, error) {
	accountID := strings.TrimSpace(row.AccountID)
	if accountID == "" {
		logging.Warnf("marketplace: purchase %s has neither a cloud account nor an AWS account id; refusing to sign with host credentials", row.PurchaseID)
		return aws.Config{}, NewClientError(400, marketplaceNoCloudAccountMessage)
	}
	hostCheck := &config.CloudAccount{ID: "purchase " + row.PurchaseID, Provider: "aws", ExternalID: accountID}
	if err := credentials.VerifyHostAccount(ctx, hostCheck, sts.NewFromConfig(base)); err != nil {
		return aws.Config{}, NewClientError(400, marketplaceNoCloudAccountMessage)
	}
	return base, nil
}
