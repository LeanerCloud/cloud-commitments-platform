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
