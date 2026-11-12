package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// persistRetryExecution builds the successor PurchaseExecution and
// writes it + the predecessor linkage + suppressions in a single tx.
// Returns the populated newExecution so the caller can send the
// approval email and synthesize the response. Extracted from
// retryPurchase to keep that function under the cyclomatic-complexity
// ceiling; tx ordering and the slice-aliasing fix live here.
//
// Also returns the RAW approval token: newExecution.ApprovalToken holds
// only the SHA-256 hash that gets persisted (issue #103), so the caller's
// synchronous approval email (sendPurchaseApprovalEmail) must take the raw
// value from this second return instead.
func (h *Handler) persistRetryExecution(ctx context.Context, failedExec *config.PurchaseExecution, session *Session, totalUpfront, totalSavings float64) (*config.PurchaseExecution, string, error) {
	// Defensive deep copy of the recommendations slice. Sharing the
	// backing array with failedExec.Recommendations would let the
	// downstream purchase pipeline (purchase.Manager.purchaseRecommendations
	// at internal/purchase/execution.go) mutate per-element fields
	// (.Error / .Purchased / .PurchaseID) on the *original failed row's*
	// in-memory representation as well — corrupting the historical
	// record any caller still holding the failedExec pointer would see.
	// The DB rows are isolated (each row JSON-marshals its own copy),
	// but in-process aliasing across a "historical" row and its
	// "successor" is a footgun we want to remove at the source.
	copiedRecs := append([]config.RecommendationRecord(nil), failedExec.Recommendations...)

	// The successor is stamped with the retrying session's creator below. A
	// creator-less successor under 4-eyes could never be approved, and the
	// linkage written in the same tx would also block retrying the original
	// again, so refuse before anything is written (issue #677).
	if err := h.requireAttributablePurchaseCreator(ctx, session); err != nil {
		return nil, "", err
	}

	// Generate a crypto/rand-backed approval token for the retry execution
	// (issue #408). uuid.New().String() was used here previously, which only
	// provides 122 bits of entropy in a known format; common.GenerateApprovalToken
	// provides 256 bits of uniform randomness, matching every other creation site.
	approvalToken, err := common.GenerateApprovalToken()
	if err != nil {
		return nil, "", fmt.Errorf("generate approval token for retry: %w", err)
	}
	tokenExpiresAt := time.Now().Add(config.ApprovalTokenTTL)

	newExecutionID := uuid.New().String()
	// Carry the predecessor's stable idempotency lineage key onto the retry
	// successor VERBATIM (issue #1012) so the per-rec provider token is
	// reproduced and a re-drive of a "failed" execution whose commitment
	// actually landed short-circuits at the provider instead of double-buying.
	// Legacy predecessors (persisted before migration 000066) carry no key —
	// seed the successor's key from the predecessor's ExecutionID, which is what
	// the original attempt's token derived from, so the match still holds.
	idempotencyKey := failedExec.IdempotencyKey
	if idempotencyKey == "" {
		idempotencyKey = failedExec.ExecutionID
	}
	// PlanID + StepNumber propagate from the predecessor so a retried
	// planned execution stays attributed to its plan + ramp step (CR
	// #168 review). For ad-hoc executions PlanID is "" and StepNumber
	// is 0, so propagation is a no-op for the non-plan case.
	//
	// CloudAccountID propagates for the same reason, and it is load-bearing on
	// the money path (issue #1537). A multi-account plan fans out into one
	// purchase_executions row per cloud account, each stamped with
	// CloudAccountID plus a per-account idempotency key "<root-key>:<acctID>"
	// (purchase.executeForAccount). Those per-account rows reach
	// status="failed" on their own and are therefore retryable. Dropping
	// CloudAccountID here left the successor with a nil account scope, which
	// purchase.executePurchase reads as "root execution" and re-fans-out across
	// EVERY account on the plan: the accounts that already committed were
	// purchased a SECOND time, under re-suffixed keys
	// "<root-key>:<failedAcctID>:<acctID>" whose derived tokens miss the AWS
	// ClientToken, the EC2 RI tag-guard and the Azure two-step lookup alike.
	// Carrying the scope keeps a per-account retry a single-account re-drive
	// against the identical lineage key, which is the precondition for the
	// provider-side dedupe to engage at all.
	//
	// Whether it then DOES engage is per-provider and not universal: most AWS
	// services, Azure reservations and GCP CUDs reproduce the token and
	// dedupe, but AWS EC2 takes no client token and Azure savings-plans has no
	// tag-based lookup, so a re-drive of a landed order of either may still
	// duplicate it. Scope propagation is therefore necessary for dedupe
	// everywhere and sufficient nowhere for those two.
	// purchase.RetryRefusalReason gates this handler on that exclusion
	// (checkRetryEligibilityGates, issue #1668) and lifts it only for a
	// proven-unsent first attempt, which cannot have landed.
	//
	// Copied by value rather than by pointer so the successor and the
	// historical failed row never share a *string, matching the defensive
	// deep copy of Recommendations above.
	cloudAccountID := clonePtr(failedExec.CloudAccountID)
	newExecution := &config.PurchaseExecution{
		ExecutionID:            newExecutionID,
		IdempotencyKey:         idempotencyKey,
		PlanID:                 failedExec.PlanID,
		StepNumber:             failedExec.StepNumber,
		CloudAccountID:         cloudAccountID,
		Status:                 "pending",
		ScheduledDate:          time.Now(),
		Recommendations:        copiedRecs,
		TotalUpfrontCost:       totalUpfront,
		EstimatedSavings:       totalSavings,
		ApprovalToken:          config.HashApprovalToken(approvalToken),
		ApprovalTokenExpiresAt: &tokenExpiresAt,
		Source:                 common.PurchaseSourceWeb,
		CapacityPercent:        failedExec.CapacityPercent,
		CreatedByUserID:        resolveCreatorUserID(session),
		RetryAttemptN:          failedExec.RetryAttemptN + 1,
		FirstDispatchAt:        retrySuccessorFirstDispatchAt(failedExec),
	}

	graceCfg, err := h.gracePeriodConfig(ctx)
	if err != nil {
		return nil, "", err
	}
	suppressions := config.BuildSuppressions(failedExec.Recommendations, newExecutionID, graceCfg, time.Now())

	if err := h.persistRetrySuccessorTx(ctx, failedExec, newExecution, suppressions); err != nil {
		return nil, "", err
	}

	return newExecution, approvalToken, nil
}

// persistRetrySuccessorTx writes the three retry-tx changes: the new
// successor execution, the linkage pointer on the original failed row, and
// the successor's suppression rows. Extracted from persistRetryExecution to
// keep that function under the cyclomatic-complexity ceiling.
//
// Order matters: the FK on retry_execution_id requires the successor to
// exist before the original can point at it.
//
// The linkage write is an atomic CAS (LinkUnsentRetryExecutionAtomic when the
// no-guard refusal was lifted by the proven-unsent proof, LinkRetryExecutionAtomic
// otherwise; conditional on status='failed' AND retry_execution_id IS NULL), not a full-row
// upsert of a stale in-memory copy of the failed row. This is the
// authoritative, race-safe guard: the pre-tx already-retried check in
// loadAndValidateRetryRequest is only a fast-path optimization, since two
// concurrent retries of the same row can both read RetryExecutionID == nil
// before either commits. Whichever request's UPDATE runs second here
// matches zero rows and the tx rolls back (issue #220) instead of both
// persisting an approvable successor and reverting whatever the other
// writer changed on the original row.
//
// The successor is a web-source pending row for the retrying user, so it
// also goes through the submit dedupe (issue #760): see writeRetrySuccessorTx.
func (h *Handler) persistRetrySuccessorTx(ctx context.Context, failedExec, newExecution *config.PurchaseExecution, suppressions []config.PurchaseSuppression) error {
	if err := h.requireAZConfigSchema(ctx); err != nil {
		return err
	}
	err := h.config.WithTx(ctx, func(tx pgx.Tx) error {
		return h.writeRetrySuccessorTx(ctx, tx, failedExec, newExecution, suppressions)
	})
	if err == nil {
		return nil
	}
	var dup *retryDuplicateSubmitError
	switch {
	case errors.Is(err, errRetryLadderTrancheSettled):
		return NewClientErrorWithDetails(409,
			fmt.Sprintf("execution %s belongs to a ladder tranche that is already settled; retrying it could buy the same commitment twice", failedExec.ExecutionID),
			map[string]any{})
	case errors.Is(err, errRetryFamilyClaimed):
		return retryFamilyClaimedError(failedExec)
	case errors.Is(err, errRetryLinkRaceLost):
		return NewClientErrorWithDetails(409,
			fmt.Sprintf("execution %s was already retried or settled; reload it", failedExec.ExecutionID),
			map[string]any{})
	case errors.As(err, &dup):
		return NewClientErrorWithDetails(409,
			fmt.Sprintf("an identical purchase was submitted moments ago as execution %s; act on it instead of retrying %s", dup.existingID, failedExec.ExecutionID),
			map[string]any{"duplicate_execution_id": dup.existingID})
	}
	return fmt.Errorf("failed to save retry execution: %w", err)
}
