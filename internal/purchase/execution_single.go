package purchase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// savePurchaseHistory persists one purchase_history row for a successful
// commitment. It returns the store error (rather than only logging it) so the
// caller can record an audit gap on the execution: a swallowed failure here
// used to leave the execution silently "completed" with no purchase_history
// row, making the purchase invisible in the History view (issue #621).
func (m *Manager) savePurchaseHistory(ctx context.Context, exec *config.PurchaseExecution, plan *config.PurchasePlan, rec config.RecommendationRecord, result common.PurchaseResult, accountID string, offeringClass ec2types.OfferingClassType) error {
	purchasedAt := time.Now()
	svc := m.mapServiceType(rec.Service)
	historyRecord := &config.PurchaseHistoryRecord{
		AccountID:        accountID,
		PurchaseID:       result.CommitmentID,
		Timestamp:        purchasedAt,
		Provider:         rec.Provider,
		Service:          rec.Service,
		Region:           rec.Region,
		ResourceType:     rec.ResourceType,
		Count:            rec.Count,
		Term:             rec.Term,
		Payment:          rec.Payment,
		UpfrontCost:      historyUpfrontCost(rec.Provider, svc, result),
		MonthlyCost:      rec.MonthlyCost,
		EstimatedSavings: rec.Savings,
		PlanID:           exec.PlanID,
		PlanName:         plan.Name,
		RampStep:         exec.StepNumber,
		CloudAccountID:   exec.CloudAccountID,
		Source:           exec.Source,
		// Stamp the in-app free-cancel window so the History UI can offer the
		// Revoke button (issue #290). Azure-only in Phase 1; nil for AWS/GCP.
		RevocationWindowClosesAt: config.RevocationWindowClosesAtFor(rec.Provider, purchasedAt),
	}
	// The marketplace-sell guard reads this column instead of asking AWS
	// (issue #292), so it must be the class of the recorded commitment: the
	// provider-reported class on an adopted re-drive, else the class the EC2
	// client was asked to buy.
	if rec.Provider == string(common.ProviderAWS) && (svc == common.ServiceEC2 || svc == common.ServiceCompute) {
		historyRecord.OfferingClass = historyOfferingClass(result, offeringClass)
	}
	if err := m.config.SavePurchaseHistory(ctx, historyRecord); err != nil {
		logging.Errorf("Failed to save history: %v", err)
		return err
	}
	return nil
}

func (m *Manager) sendPurchaseNotification(ctx context.Context, exec *config.PurchaseExecution, plan *config.PurchasePlan, totalSavings, totalUpfront float64) error {
	data := m.buildPurchaseConfirmationData(exec, plan, totalSavings, totalUpfront)
	return m.email.SendPurchaseConfirmation(ctx, data)
}

func (m *Manager) buildPurchaseConfirmationData(exec *config.PurchaseExecution, plan *config.PurchasePlan, totalSavings, totalUpfront float64) email.NotificationData {
	dashboardBase := strings.TrimRight(m.dashboardURL, "/")
	data := email.NotificationData{
		DashboardURL:     dashboardBase,
		TotalSavings:     totalSavings,
		TotalUpfrontCost: totalUpfront,
		PlanName:         plan.Name,
	}
	if dashboardBase != "" {
		data.ArcheraEducationURL = dashboardBase + "/archera-insurance"
	}

	for i := range exec.Recommendations {
		rec := exec.Recommendations[i]
		if rec.Purchased {
			data.Recommendations = append(data.Recommendations, email.RecommendationSummary{
				Service:        rec.Service,
				ResourceType:   rec.ResourceType,
				Engine:         rec.Engine,
				Region:         rec.Region,
				Count:          rec.Count,
				MonthlySavings: rec.Savings,
			})
		}
	}

	return data
}

// logRecCtxErr emits a diagnostic log line when a per-recommendation context has
// been canceled or timed out. It distinguishes DeadlineExceeded (the 30s per-rec
// budget fired) from Canceled (a parent context stopped the execution) so that
// CloudWatch filters can tell the two apart without parsing error strings.
// It is a no-op when recCtxErr is nil.
func logRecCtxErr(executionID, recTuple string, elapsed time.Duration, recCtxErr error) {
	if recCtxErr == nil {
		return
	}
	if errors.Is(recCtxErr, context.DeadlineExceeded) {
		logging.Errorf("purchase[%s]: per-rec deadline exceeded for %s (elapsed=%s)",
			executionID, recTuple, elapsed)
	} else if errors.Is(recCtxErr, context.Canceled) {
		logging.Errorf("purchase[%s]: recommendation context canceled for %s (elapsed=%s)",
			executionID, recTuple, elapsed)
	}
}

// preSubmitChecks runs what must hold before PurchaseCommitment: the AWS
// ElastiCache recent-reservation refusal (platform#1863), the GCP
// recent-commitment refusal (platform#682, a beforeSubmission error) and go's
// Redshift dispatch time, which bounds its DescribeReservedNodes match window
// by RequestedAt and must be the lineage's first dispatch time, never this
// call's.
func preSubmitChecks(ctx context.Context, client provider.ServiceClient, rec config.RecommendationRecord, recommendation common.Recommendation, opts common.PurchaseOptions, ownNames *purchaseOwnNames) (common.PurchaseOptions, error) {
	if err := elastiCacheRecentRefusal(ctx, client, recommendation, ownNames.elastiCacheNames(), ownElastiCacheID(opts)); err != nil {
		return opts, err
	}
	if err := gcpRecentCommitmentRefusal(ctx, client, recommendation, ownNames.gcpNames(), ownCommitmentName(opts)); err != nil {
		return opts, err
	}
	return withDispatchTime(rec, opts)
}

// ownCommitmentName is the GCP commitment name this rec's purchase call would
// create, or "" when the call carries no idempotency token.
func ownCommitmentName(opts common.PurchaseOptions) string {
	if opts.IdempotencyToken == "" {
		return ""
	}
	return gcpCommitmentName(opts.IdempotencyToken)
}

// purchaseCallError wraps a failed PurchaseCommitment. When the run context
// ended mid-call the request may have landed, so the error also carries
// ErrOutcomeUnknown: the row records an unknown outcome, never a plain failure.
func purchaseCallError(ctx context.Context, err error) error {
	if ctx.Err() != nil && !errors.Is(err, common.ErrOutcomeUnknown) {
		return fmt.Errorf("purchase failed: %w: %w", common.ErrOutcomeUnknown, err)
	}
	// The Azure client wraps a currency refusal with ErrOutcomeUnknown from
	// attempt 2 on and returns attempt 1 bare, so a bare one is a refusal.
	if errors.Is(err, common.ErrCurrencyNotAccepted) && !errors.Is(err, common.ErrOutcomeUnknown) {
		return beforeSubmission(fmt.Errorf("purchase refused: the provider does not accept the billing currency: %w", err))
	}
	return fmt.Errorf("purchase failed: %w", err)
}

// updatePlanProgress advances the ramp schedule after a purchase.
// Direct-execute purchases (Opportunities flow) have no plan to advance --
// PlanID is empty and the Postgres UUID column would reject the query
// with SQLSTATE 22P02, so short-circuit cleanly before hitting the store.
//
// The step being completed is read off the execution itself, never off the
// plan's current position: a multi-account step fans out into one execution
// per account, each retryable on its own, so the plan's position is exactly
// what a second completion of one step must not be trusted to imply (issue
// #1669). CompletePlanStep holds a SELECT FOR UPDATE lock for the duration,
// which also closes the lost-update race between overlapping Lambda
// invocations on the same plan (issue #1071).
func (m *Manager) updatePlanProgress(ctx context.Context, exec *config.PurchaseExecution) error {
	if exec.PlanID == "" {
		return nil
	}
	if exec.StepNumber <= 0 {
		// Reachable for a row stamped by pre-#1669 code, which wrote the COUNT
		// of completed steps and so wrote 0 for a plan's first step. Migration
		// 000098 retargets those rows, leaving only ones written during the
		// deploy overlap. Advancing the ramp from an unknown step would be a
		// guess on the money path; report instead.
		return fmt.Errorf("execution %s is attributed to plan %s but carries ramp step %d: refusing to advance the ramp",
			exec.ExecutionID, exec.PlanID, exec.StepNumber)
	}
	return m.config.CompletePlanStep(ctx, exec.PlanID, exec.StepNumber)
}

// getAWSAccountID retrieves the current AWS account ID using STS.
// It returns ("", error) on all failure paths so the caller decides how to
// handle the absent ID (log a sentinel, skip tagging, etc.) rather than
// silently recording "unknown" in the purchase history row (05-L2).
func (m *Manager) getAWSAccountID(ctx context.Context) (string, error) {
	if m.stsClient == nil {
		return "", fmt.Errorf("STS client not configured")
	}

	result, err := m.stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("STS GetCallerIdentity: %w", err)
	}

	if result.Account == nil {
		return "", fmt.Errorf("STS returned nil Account")
	}
	return *result.Account, nil
}

// executeSinglePurchase executes a single purchase using the appropriate provider.
// provCfg carries optional per-account credentials; pass nil to use ambient credentials.
// opts carries execution-level metadata (the source surface) that providers stamp
// onto the commitment they create.
func (m *Manager) executeSinglePurchase(ctx context.Context, rec config.RecommendationRecord, provCfg *provider.Config, opts common.PurchaseOptions, ownNames *purchaseOwnNames) (common.PurchaseResult, error) {
	// Per-purchase Info logs tagged with the owning execution ID so a
	// CloudWatch filter on the execution UUID surfaces every step of the
	// purchase attempt -- provider construction, service-client lookup,
	// details decode, the cloud SDK call, and the result. Issue #667
	// surfaced that the prior flow had ZERO observable signal between the
	// approve handler and the final aggregated error toast; a timed-out
	// SDK call left no trace in CloudWatch beyond the wrapped error.
	logging.Infof("purchase[%s]: starting rec %s/%s/%s/%s (count=%d term=%dyr payment=%s)",
		opts.ExecutionID, rec.Provider, rec.Service, rec.Region, rec.ResourceType, rec.Count, rec.Term, rec.Payment)

	// Cap this individual rec at 30s so one hung rec (SDK retry storm, STS
	// hang, pagination hang) cannot exhaust the Lambda budget and starve the
	// other recs running in parallel in the same execution. 30s matches the
	// purchasecfg hard limits (2 retries * 15s HTTP timeout) from issue #683.
	// The parent ctx governs the overall Lambda budget; this per-rec deadline
	// is always the shorter of the two.
	recTuple := fmt.Sprintf("%s/%s/%s/%s", rec.Provider, rec.Service, rec.Region, rec.ResourceType)
	recCtx, recCancel := context.WithTimeout(ctx, m.budgets.perRecTimeout())
	defer recCancel()

	// Create the provider
	t0 := time.Now()
	cloudProvider, err := m.providerFactory.CreateAndValidateProvider(recCtx, rec.Provider, provCfg)
	if err != nil {
		logging.Errorf("purchase[%s]: provider construction failed for %s after %s: %v",
			opts.ExecutionID, rec.Provider, time.Since(t0), err)
		return common.PurchaseResult{}, fmt.Errorf("failed to create %s provider: %w", rec.Provider, err)
	}
	logging.Infof("purchase[%s]: provider %s constructed in %s", opts.ExecutionID, rec.Provider, time.Since(t0))

	// Map service string to ServiceType
	serviceType := m.mapServiceType(rec.Service)

	// Get the service client for this region
	t0 = time.Now()
	serviceClient, err := cloudProvider.GetServiceClient(recCtx, purchaseClientService(rec.Provider, serviceType), rec.Region)
	if err != nil {
		logging.Errorf("purchase[%s]: service client lookup failed for %s/%s in %s after %s: %v",
			opts.ExecutionID, rec.Provider, serviceType, rec.Region, time.Since(t0), err)
		return common.PurchaseResult{}, fmt.Errorf("failed to get service client: %w", err)
	}
	logging.Infof("purchase[%s]: service client %s/%s/%s ready in %s",
		opts.ExecutionID, rec.Provider, serviceType, rec.Region, time.Since(t0))

	// Build the recommendation in common format
	recommendation := common.Recommendation{
		Provider:       common.ProviderType(rec.Provider),
		Service:        serviceType,
		Region:         rec.Region,
		ResourceType:   rec.ResourceType,
		Count:          rec.Count,
		Term:           common.Term(fmt.Sprintf("%dyr", rec.Term)),
		PaymentOption:  common.PaymentOption(rec.Payment),
		CommitmentCost: rec.UpfrontCost,
	}

	// Reconstruct the typed *Details pointer from the persisted JSON
	// payload (issue #453). The scheduler stores the full
	// common.ServiceDetails at collection time; this is the read-back
	// step that yields a *ComputeDetails / *DatabaseDetails /
	// *CacheDetails / *SavingsPlanDetails (etc.) keyed on rec.Service
	// so the cloud service client's findOfferingID type-assertion
	// succeeds with the full per-rec details (Platform, Tenancy,
	// Scope, AZConfig, HourlyCommitment, …), not just an Engine string.
	//
	// Legacy rows persisted before #453 carry an empty rec.Details —
	// DecodeServiceDetailsFor returns a zero-valued typed pointer for
	// those. The AWS EC2 client does not fill the gaps: an empty Platform
	// or an empty or unknown Tenancy or Scope is rejected before the request
	// is sent (providers/aws ec2/client.go buildEC2OfferingQuery), so the
	// purchase fails loudly instead of buying a default configuration. Engine
	// is preserved on the record column too, so we use it as a last-resort
	// fallback for DB/Cache services to avoid silently mis-purchasing a
	// legacy non-default-engine rec as the default engine.
	details, detailsErr := common.DecodeServiceDetailsFor(rec.Service, rec.Details)
	if detailsErr != nil {
		logging.Errorf("purchase[%s]: decode service details failed for %s rec %s: %v",
			opts.ExecutionID, rec.Service, rec.ResourceType, detailsErr)
		return common.PurchaseResult{}, fmt.Errorf("decode service details for %s rec %s: %w", rec.Service, rec.ResourceType, detailsErr)
	}
	if details != nil {
		applyEngineFallback(details, rec.Engine)
		recommendation.Details = details
	}
	// One log line capturing the details shape that's about to drive
	// findOfferingID. detailsAbsent=true is the legacy-row fallback
	// (issue #453) and a strong indicator that filters will fall back
	// to defaults — surface it so a "no offerings found" failure is
	// linkable to a stale rec rather than a transient AWS issue.
	logging.Infof("purchase[%s]: details ready for %s/%s (detailsAbsent=%v engine=%q idempotencyToken=%q)",
		opts.ExecutionID, rec.Provider, rec.Service, len(rec.Details) == 0, rec.Engine, opts.IdempotencyToken)

	// Execute the purchase. This is the call that hits the cloud SDK
	// (and ultimately the AWS / Azure / GCP API). Time it explicitly so
	// CloudWatch shows whether a failure came from the SDK call itself
	// (which the AWS SDK retries up to 3× × 30s by default) versus
	// something earlier in the flow — issue #667.
	opts, err = preSubmitChecks(recCtx, serviceClient, rec, recommendation, opts, ownNames)
	if err != nil {
		return common.PurchaseResult{}, err
	}
	tCall := time.Now()
	result, err := serviceClient.PurchaseCommitment(recCtx, recommendation, opts)
	elapsed := time.Since(tCall)
	if err != nil {
		logRecCtxErr(opts.ExecutionID, recTuple, elapsed, recCtx.Err())
		logging.Errorf("purchase[%s]: %s/%s/%s/%s PurchaseCommitment failed after %s: %v",
			opts.ExecutionID, rec.Provider, rec.Service, rec.Region, rec.ResourceType, elapsed, err)
		return result, purchaseCallError(ctx, err)
	}

	if !result.Success {
		if result.Error != nil {
			logging.Errorf("purchase[%s]: %s/%s/%s/%s PurchaseCommitment returned Success=false after %s: %v",
				opts.ExecutionID, rec.Provider, rec.Service, rec.Region, rec.ResourceType, elapsed, result.Error)
			return result, result.Error
		}
		logging.Errorf("purchase[%s]: %s/%s/%s/%s PurchaseCommitment returned Success=false after %s (no error detail)",
			opts.ExecutionID, rec.Provider, rec.Service, rec.Region, rec.ResourceType, elapsed)
		return result, fmt.Errorf("purchase was not successful")
	}

	cost := "unknown"
	if result.Cost != nil {
		cost = fmt.Sprintf("%.2f", *result.Cost)
	}
	logging.Infof("purchase[%s]: %s/%s/%s/%s PurchaseCommitment succeeded in %s (commitmentID=%s, cost=%s)",
		opts.ExecutionID, rec.Provider, rec.Service, rec.Region, rec.ResourceType, elapsed, result.CommitmentID, cost)
	return result, nil
}
