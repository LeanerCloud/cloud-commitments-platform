package scheduler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
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
