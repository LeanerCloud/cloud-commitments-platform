package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/providerfactory"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// azureOptionsRefresher fills the Azure commitment-options probe row
// (commitmentopts.Service.RefreshAzure, bounded and a cheap no-op when the
// row exists).
type azureOptionsRefresher interface {
	RefreshAzure(ctx context.Context) error
}

// azureOptionsDeadlineMargin is left unused at the end of the run's deadline
// so a slow Azure probe cannot consume the time the caller still needs.
const azureOptionsDeadlineMargin = 5 * time.Second

// refreshAzureOptionsBestEffort runs the injected refresher only when Azure
// was among the successful providers of this run, last, so a slow probe never
// delays persistence or the notification email. A failure is logged once
// as a failure class only (see azureRefreshFailureClass): Azure SDK errors can
// carry the subscription ID in the request URL. It never fails the collection.
func (s *Scheduler) refreshAzureOptionsBestEffort(ctx context.Context, successfulProviders []string) {
	if s.azureOptions == nil || !slices.Contains(successfulProviders, "azure") {
		return
	}
	if dl, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl.Add(-azureOptionsDeadlineMargin))
		defer cancel()
	}
	if err := s.azureOptions.RefreshAzure(ctx); err != nil {
		logging.Warnf("Azure commitment options refresh failed (best effort, %s)", azureRefreshFailureClass(err))
	}
}

// azureRefreshFailureClass describes err without err.Error(), which for Azure
// SDK errors embeds the request URL and so the subscription ID. The status and
// error code of an *azcore.ResponseError carry no URL.
func azureRefreshFailureClass(err error) string {
	var respErr *azcore.ResponseError
	switch {
	case errors.As(err, &respErr):
		return fmt.Sprintf("status %d, error code %q", respErr.StatusCode, respErr.ErrorCode)
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	root := err
	for u := errors.Unwrap(root); u != nil; u = errors.Unwrap(root) {
		root = u
	}
	return fmt.Sprintf("root cause %T", root)
}

// providerOutcome bundles a single provider's collection outcome for the
// deterministic merge in collectAllProviders. Only one of recs/err is
// meaningful per outcome — err != nil means the provider failed entirely
// (mirrors the pre-fan-out `continue` branch); err == nil means the provider
// succeeded (possibly partially — partial-account-failure semantics live in
// fanOutPerAccount, not here).
//
// gap is non-nil when at least one of the provider's sweeps was incomplete;
// it is surfaced in recommendations_state.last_collection_partial (#87).
type providerOutcome struct {
	err                 error
	gap                 *sweepGap
	recs                []config.RecommendationRecord
	succeededAccountIDs []string
}

func (s *Scheduler) collectAWSAmbient(ctx context.Context, globalCfg *config.GlobalConfig) ([]config.RecommendationRecord, *sweepGap, error) {
	prov, err := s.providerFactory.CreateAndValidateProvider(ctx, "aws", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create AWS provider: %w", err)
	}
	return s.fetchAndConvert(ctx, prov, "aws", nil, s.hostAWSAccountID(ctx), globalCfg)
}

// hostAWSAccountID returns the Lambda's own AWS account ID via STS
// GetCallerIdentity, or "" when STS is unavailable or fails (logged at warn).
func (s *Scheduler) hostAWSAccountID(ctx context.Context) string {
	if s.stsClient == nil {
		return ""
	}
	stsCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := s.stsClient.GetCallerIdentity(stsCtx, &sts.GetCallerIdentityInput{})
	if err != nil {
		logging.Warnf("ambient host-account lookup: STS GetCallerIdentity failed: %v", err)
		return ""
	}
	if out == nil || out.Account == nil {
		return ""
	}
	return *out.Account
}

// resolveAmbientHostAccountID looks up the Lambda's own AWS account ID
// (via STS GetCallerIdentity) and, if it matches a registered
// cloud_accounts row by (provider="aws", external_id), returns that
// row's UUID. Returns "" when STS is unavailable, the lookup fails, or
// no registered account matches — callers fall back to the pre-fix
// nil tagging in that case, preserving the truly-orphan deployment.
//
// All errors are intentionally swallowed (logged at debug/warn) — this
// is a best-effort UX improvement on the ambient path and must not
// break the collection.
func (s *Scheduler) resolveAmbientHostAccountID(ctx context.Context) string {
	hostAcctID := s.hostAWSAccountID(ctx)
	if hostAcctID == "" {
		return ""
	}

	acct, err := s.config.GetCloudAccountByExternalID(ctx, "aws", hostAcctID)
	if err != nil {
		logging.Warnf("ambient host-account lookup: GetCloudAccountByExternalID(aws,%s) failed: %v", hostAcctID, err)
		return ""
	}
	if acct == nil {
		// Truly orphan deployment — preserve pre-fix nil tagging.
		return ""
	}
	return acct.ID
}

// resolveAmbientAccountID is the provider-agnostic counterpart to
// resolveAmbientHostAccountID: given the host's external identifier (subscription
// ID for Azure, project ID for GCP) it checks whether a registered cloud_accounts
// row exists for (provider, externalID) and returns its UUID. Returns "" on any
// error or when no row matches, preserving the pre-fix nil-tagging behavior so
// truly-orphan deployments are unaffected. All errors are intentionally swallowed
// (logged at warn) — this is a best-effort UX improvement on the ambient path
// and must not break the collection.
func (s *Scheduler) resolveAmbientAccountID(ctx context.Context, providerName, externalID string) string {
	if externalID == "" {
		return ""
	}
	acct, err := s.config.GetCloudAccountByExternalID(ctx, providerName, externalID)
	if err != nil {
		logging.Warnf("ambient host-account lookup: GetCloudAccountByExternalID(%s,%s) failed: %v", providerName, externalID, err)
		return ""
	}
	if acct == nil {
		// Truly orphan deployment — preserve pre-fix nil tagging.
		return ""
	}
	return acct.ID
}

// collectAzureAmbient collects Azure recommendations using the host's managed
// identity (DefaultAzureCredential / ambient credentials). Used by the ambient
// fallback path in collectAzureRecommendations when no accounts are registered.
// subscriptionID is passed explicitly to avoid an unnecessary Azure API round-trip
// to auto-discover subscriptions — the caller already resolved it from env.
func (s *Scheduler) collectAzureAmbient(ctx context.Context, subscriptionID string) ([]config.RecommendationRecord, *sweepGap, error) {
	prov, err := s.providerFactory.CreateAndValidateProvider(ctx, "azure", &provider.Config{
		AzureSubscriptionID: subscriptionID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create ambient Azure provider: %w", err)
	}
	recClient, err := prov.GetRecommendationsClient(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("get Azure recommendations client: %w", err)
	}
	recs, err := recClient.GetAllRecommendations(ctx)
	gap, err := tolerateIncompleteSweep("azure", err)
	if err != nil {
		return nil, nil, fmt.Errorf("get Azure recommendations: %w", err)
	}
	return s.convertRecommendations(recs, "azure"), gap, nil
}

// collectGCPAmbient collects GCP recommendations using Application Default
// Credentials. Used by the ambient fallback path in collectGCPRecommendations
// when no accounts are registered.
func (s *Scheduler) collectGCPAmbient(ctx context.Context, planTerm bool) ([]config.RecommendationRecord, *sweepGap, error) {
	prov, err := s.providerFactory.CreateAndValidateProvider(ctx, "gcp", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("create ambient GCP provider: %w", err)
	}
	recClient, err := prov.GetRecommendationsClient(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("get GCP recommendations client: %w", err)
	}
	recs, err := fetchGCPRecommendations(ctx, recClient, planTerm)
	gap, err := tolerateIncompleteSweep("gcp", err)
	if err != nil {
		return nil, nil, fmt.Errorf("get GCP recommendations: %w", err)
	}
	return s.convertRecommendations(recs, "gcp"), gap, nil
}

// fetchGCPRecommendations collects every GCP recommendation. With planTerm it
// passes RecommendationParams.ExperimentalGCPPlanTerm so Compute CUD terms come
// from the recommendation's root plan; otherwise it keeps GetAllRecommendations.
func fetchGCPRecommendations(ctx context.Context, rc provider.RecommendationsClient, planTerm bool) ([]common.Recommendation, error) {
	if !planTerm {
		return rc.GetAllRecommendations(ctx)
	}
	return rc.GetRecommendations(ctx, &common.RecommendationParams{ExperimentalGCPPlanTerm: true})
}

func (s *Scheduler) collectAWSForAccount(ctx context.Context, globalCfg *config.GlobalConfig, acct config.CloudAccount) ([]config.RecommendationRecord, *sweepGap, error) {
	// Self-account: role_arn mode with no role ARN means "use the CUDly
	// Lambda's own (ambient) credentials to access this account" -- see
	// credentials.resolveRoleARNProvider, which documents the identical
	// shape. Any OTHER auth mode with an empty AWSRoleARN -- including the
	// empty string org-discovery persists on newly-discovered member
	// accounts pending operator review (handler_accounts_discover.go,
	// persistDiscoveredMembers) -- must NOT take this branch: it must fall
	// through to ResolveAWSCredentialProvider below so its switch fails
	// loud ("unsupported aws_auth_mode") instead of silently collecting the
	// HOST account's data and tagging it with this account's UUID
	// (issue #107). A role_arn account that is not the host fails the
	// host-identity check instead (issue #402).
	if acct.AWSAuthMode == "role_arn" && acct.AWSRoleARN == "" {
		if err := credentials.VerifyHostAccount(ctx, &acct, s.stsClient); err != nil {
			return nil, nil, err
		}
		prov, err := s.providerFactory.CreateAndValidateProvider(ctx, "aws", nil)
		if err != nil {
			return nil, nil, fmt.Errorf("create ambient provider: %w", err)
		}
		return s.fetchAndConvert(ctx, prov, "aws", &acct.ID, acct.ExternalID, globalCfg)
	}
	awsCreds, err := credentials.ResolveAWSCredentialProviderWithOpts(ctx, &acct, s.credStore, s.assumeRoleSTS, s.awsResolveOptions())
	if err != nil {
		return nil, nil, fmt.Errorf("resolve credentials: %w", err)
	}
	cfg := &provider.Config{Name: "aws", AWSCredentialsProvider: awsCreds}
	prov, err := s.providerFactory.CreateAndValidateProvider(ctx, "aws", cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("create provider: %w", err)
	}
	return s.fetchAndConvert(ctx, prov, "aws", &acct.ID, acct.ExternalID, globalCfg)
}

// awsResolveOptions returns the resolver options for per-account AWS
// collection: the config-store bastion lookup and bastion STS factory, plus
// the host credentials for a bastion that is the host account itself.
func (s *Scheduler) awsResolveOptions() credentials.AWSResolveOptions {
	return credentials.AWSResolveOptions{
		AccountLookup:    credentials.AccountLookupFromStore(s.config),
		STSClientFactory: s.bastionSTS,
		AmbientProvider:  s.ambientAWSCreds,
		AmbientSTS:       s.stsClient,
	}
}

// collectAzureRecommendations fans out across all enabled Azure accounts,
// resolving per-account federated credentials via the KMS signer.
// When no accounts are registered but AZURE_SUBSCRIPTION_ID is set (CUDly
// running natively on Azure with managed identity), falls back to ambient
// credentials and tags recommendations with the registered subscription's
// UUID if found in cloud_accounts.
func (s *Scheduler) collectAzureRecommendations(ctx context.Context, _ *config.GlobalConfig) ([]config.RecommendationRecord, []string, *sweepGap, error) {
	accounts, err := s.enabledAccounts(ctx, "azure")
	if err != nil {
		return nil, nil, nil, err
	}
	if len(accounts) == 0 {
		// Issue #662: mirror the AWS ambient-path tagging fix for Azure.
		// When the host subscription matches a registered cloud_accounts row,
		// tag recs with that account's UUID instead of returning nil.
		// Best-effort: env lookup or store errors must not fail the collection.
		subscriptionID := os.Getenv("AZURE_SUBSCRIPTION_ID")
		if subscriptionID == "" {
			logging.Info("No enabled Azure accounts — skipping Azure recommendations")
			return nil, nil, nil, nil
		}
		recs, gap, err := s.collectAzureAmbient(ctx, subscriptionID)
		if err != nil {
			return nil, nil, nil, err
		}
		if acctID := s.resolveAmbientAccountID(ctx, "azure", subscriptionID); acctID != "" {
			recs = s.tagAccount(recs, acctID)
			return recs, ambientEvictionRoster(!gap.holdsEviction(), acctID), gap.forAccount().withHeld(acctID), nil
		}
		return recs, ambientEvictionRoster(!gap.holdsEviction(), ""), gap.forAccount(), nil
	}

	recs, outcome := fanOutPerAccount(ctx, "Azure", accounts, s.collectAzureForAccount)
	if outcome.FailedCount == len(accounts) && len(accounts) > 0 {
		return nil, nil, nil, errAllAccountsFailed("Azure", outcome)
	}
	return recs, outcome.SucceededAccountIDs, outcome.Gap, nil
}

func (s *Scheduler) collectAzureForAccount(ctx context.Context, acct config.CloudAccount) ([]config.RecommendationRecord, *sweepGap, error) {
	// Every recommendation returned below is tagged with THIS account's UUID
	// (see tagAccount at the end of this function), so the provider must be
	// pinned to this account's subscription. An empty AzureSubscriptionID
	// leaves the provider unpinned, and an unpinned provider now fans out
	// across every subscription the credential can see -- which would file
	// other subscriptions' recommendations under this account and expose them
	// to anyone authorized for it. Fail loud instead; the row is misconfigured.
	if acct.AzureSubscriptionID == "" {
		return nil, nil, fmt.Errorf("cloud account %s has no azure_subscription_id configured", acct.ID)
	}

	azCred, err := credentials.ResolveAzureTokenCredentialWithOpts(ctx, &acct, s.credStore, credentials.AzureResolveOptions{
		Signer:    s.oidcSigner,
		IssuerURL: s.oidcIssuerURL,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("resolve credentials: %w", err)
	}
	azProv, err := providerfactory.NewAzure(acct.AzureSubscriptionID, azCred)
	if err != nil {
		return nil, nil, fmt.Errorf("create provider: %w", err)
	}

	recClient, err := azProv.GetRecommendationsClient(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("get recommendations client: %w", err)
	}
	recs, err := recClient.GetAllRecommendations(ctx)
	gap, err := tolerateIncompleteSweep("azure", err)
	if err != nil {
		return nil, nil, fmt.Errorf("get recommendations: %w", err)
	}
	return s.tagAccount(s.convertRecommendations(recs, "azure"), acct.ID), gap, nil
}
