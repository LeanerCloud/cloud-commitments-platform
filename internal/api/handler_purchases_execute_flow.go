package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// resolveCreatorUserID returns a pointer to the session user's UUID for
// stamping onto purchase_executions.created_by_user_id, or nil for
// non-user sessions whose UserID isn't a real UUID. This keeps the
// cancel-own RBAC check (issue #46) honest:
//   - Real human session → pointer to UUID; future cancel-own match works.
//   - Admin-API-key session → UserID is the literal "admin-api-key"
//     sentinel, which fails validateUUID; store NULL so the FK to users
//     stays valid and the row is reachable only via cancel-any/email-token.
//   - nil session (defensive — current callers gate on requirePermission
//     so this shouldn't happen) → NULL.
func resolveCreatorUserID(session *Session) *string {
	if session == nil {
		return nil
	}
	if validateUUID(session.UserID) != nil {
		return nil
	}
	uid := session.UserID
	return &uid
}

// requireAttributablePurchaseCreator refuses, while 4-eyes mode is on, any
// request that would mint a pending purchase execution with no recorded
// creator (issue #677). resolveCreatorUserID stores NULL for a session whose
// UserID is not a user UUID, which in practice is the stateless admin API
// key. Under 4-eyes, requireDifferentApprover and the manager's
// checkDifferentApprover deny a NULL-creator row that is not
// created_by_system for every approver, so such a row could only sit pending
// until it expired (and, on the direct-execute path, be refused after it was
// already written). Refusing up front gives the caller a clear error instead.
// The key is never stamped with a human creator on its behalf: that would let
// that human's colleague "approve" a purchase no human requested, or let the
// key holder approve their own request.
//
// Sessions with a user UUID (signed-in users and per-user API keys) pass
// without a config read. A config-load failure denies (fail closed), matching
// requireDifferentApprover. A nil config means 4-eyes is off, also matching it.
func (h *Handler) requireAttributablePurchaseCreator(ctx context.Context, session *Session) error {
	if resolveCreatorUserID(session) != nil {
		return nil
	}
	cfg, err := h.config.GetGlobalConfig(ctx)
	if err != nil {
		return fmt.Errorf("4-eyes policy check: failed to load global config: %w", err)
	}
	if cfg == nil || !cfg.RequireDifferentApprover {
		return nil
	}
	logging.Warnf("4-eyes: mode on; refusing to create a purchase execution with no attributable creator")
	return NewClientError(403, "4-eyes approval mode is enabled and this request has no attributable user (for example the admin API key), so the purchase it creates could never be approved; sign in or use a per-user API key instead")
}

// requirePurchaseCreatingPermission is requirePermission for endpoints that
// mint pending purchase executions: after the RBAC check it applies
// requireAttributablePurchaseCreator so no NULL-creator row is created while
// 4-eyes mode is on (issue #677).
func (h *Handler) requirePurchaseCreatingPermission(ctx context.Context, req *events.LambdaFunctionURLRequest, action, resource string) (*Session, error) {
	session, err := h.requirePermission(ctx, req, action, resource)
	if err != nil {
		return nil, err
	}
	if err := h.requireAttributablePurchaseCreator(ctx, session); err != nil {
		return nil, err
	}
	return session, nil
}

// fourEyesActorIdentity returns the identity string passed as the actor to
// purchase.Manager.ApproveAndExecute for the 4-eyes dual-control comparison
// (issue #1005 / PR #1500 adversarial review). Real human sessions always
// carry a non-empty Email. The stateless admin API key session
// (apiKeyAdminUserID sentinel) does not — falling back to session.Email alone
// there would make the manager's enforceFourEyesPolicy treat every API-key-
// driven approve/direct-execute as "actor identity unknown" and fail closed,
// even when the row was created by a different real user (a legitimate,
// non-self-approval case the RBAC layer already permits via the
// apiKeyAdminUserID short-circuit in authorizeSessionApprove /
// authorizeSessionExecuteDirect). Falling back to the sentinel itself is
// safe: it is never a valid email, so it can never collide with a real
// user's resolved email in the 4-eyes comparison, and a row CREATED via the
// API key (CreatedByUserID stays nil per resolveCreatorUserID above) is
// still denied through the existing NULL-creator fail-closed branch.
func fourEyesActorIdentity(session *Session) string {
	if session.Email != "" {
		return session.Email
	}
	return session.UserID
}

// executePurchase handles direct purchase execution from recommendations
// matchDuplicateInList scans a slice of recent executions for one that
// matches creatorID + idempotencyKey within the idempotency window.
// Returns the first match, or nil when there is no duplicate.
// Extracted from persistExecutionAndSuppressions to keep cyclomatic
// complexity under the project gocyclo threshold.
func matchDuplicateInList(pending []config.PurchaseExecution, creatorID, idempotencyKey string, now time.Time) *config.PurchaseExecution {
	cutoff := now.Add(-purchaseIdempotencyWindow)
	for i := range pending {
		ex := &pending[i]
		if ex.Source != common.PurchaseSourceWeb || ex.ScheduledDate.Before(cutoff) {
			continue
		}
		exCreator := ""
		if ex.CreatedByUserID != nil {
			exCreator = *ex.CreatedByUserID
		}
		if exCreator != creatorID {
			continue
		}
		exKey, err := purchaseIdempotencyKey(exCreator, ex.Recommendations, ex.CapacityPercent)
		if err != nil {
			logging.Errorf("purchase idempotency: skipping pending execution %s, cannot derive its key: %v", ex.ExecutionID, err)
			continue
		}
		if exKey == idempotencyKey {
			return ex
		}
	}
	return nil
}

// persistExecutionAndSuppressions saves the execution + its suppression
// records in a single transaction. It also performs the duplicate-execution
// check inside the same transaction, behind a per-creator advisory lock, so
// that the read and the insert are atomic: a plain SELECT ... FOR UPDATE
// cannot see a concurrent transaction's uncommitted insert (#643, MON-01).
//
// Return values:
//   - (nil, nil)          — no duplicate found; execution was inserted.
//   - (existing, nil)     — duplicate found; execution was NOT inserted; caller
//     should collapse onto existing.
//   - (nil, err)          — store error; caller should surface it.
func (h *Handler) persistExecutionAndSuppressions(
	ctx context.Context,
	execution *config.PurchaseExecution,
	suppressions []config.PurchaseSuppression,
	creatorID, idempotencyKey string,
) (dup *config.PurchaseExecution, err error) {
	if err := h.requireAZConfigSchema(ctx); err != nil {
		return nil, err
	}
	txErr := h.config.WithTx(ctx, func(tx pgx.Tx) error {
		// Duplicate check inside the tx, after taking the creator's submit
		// lock: a concurrent identical submit waits for this tx to commit,
		// then finds the row. Atomic with the insert below.
		now := time.Now()
		recent, err := h.config.ListRecentSubmitsTx(ctx, tx, creatorID, now.Add(-purchaseIdempotencyWindow))
		if err != nil {
			return err
		}
		if dup = matchDuplicateInList(recent, creatorID, idempotencyKey, now); dup != nil {
			return nil // found duplicate — skip insert, commit tx (read-only)
		}

		if err := h.config.SavePurchaseExecutionTx(ctx, tx, execution); err != nil {
			return err
		}
		for i := range suppressions {
			if err := h.config.CreateSuppressionTx(ctx, tx, &suppressions[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		return nil, fmt.Errorf("failed to save execution: %w", txErr)
	}
	return dup, nil
}

// purchaseIdempotencyWindow is how long a freshly-created pending execution
// "absorbs" an identical resubmission. A double-click or a retried network
// call within this window resolves to the original execution instead of
// minting a second approvable row (double-spend). Sized to comfortably cover
// a stuck request retry without masking a genuine intentional re-purchase.
const purchaseIdempotencyWindow = 2 * time.Minute

// purchaseIdempotencyKey derives a stable fingerprint of a submit so two
// identical submissions (same actor, same scaled rec set, same capacity)
// collapse to one execution. The recs are normalized and sorted so map/slice
// ordering can't change the hash. Account scope is implicit: each rec carries
// its CloudAccountID, so the same recs targeting a different account hash
// differently. Closes issue #644.
func purchaseIdempotencyKey(creatorID string, recs []config.RecommendationRecord, capacityPercent int) (string, error) {
	tuples := make([]string, 0, len(recs))
	for i := range recs {
		r := recs[i]
		acct := ""
		if r.CloudAccountID != nil {
			acct = *r.CloudAccountID
		}
		az, err := recDetailsAvailabilityZone(&r)
		if err != nil {
			return "", err
		}
		ci, err := r.ComputeIdentity()
		if err != nil {
			return "", err
		}
		// Single-AZ and Multi-AZ RDS buys of the same class are different
		// purchases and must not dedupe into one (issue #408).
		azConfig, err := r.IdentityAZConfig()
		if err != nil {
			return "", err
		}
		tuples = append(tuples, fmt.Sprintf("%s|%s|%s|%s|%s|%s|%d|%d|%s|%s|%s|%s|%s",
			strings.ToLower(strings.TrimSpace(r.Provider)),
			r.Service, r.Region, r.ResourceType, r.Engine, acct,
			r.Count, r.Term, strings.ToLower(strings.TrimSpace(r.Payment)), az,
			ci.Platform, ci.Tenancy, azConfig))
	}
	sort.Strings(tuples)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x1f%d\x1f%s", creatorID, capacityPercent, strings.Join(tuples, "\x1e"))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// recDetailsAvailabilityZone returns the zonal EC2 AZ carried in the rec's
// Details blob, "" for non-compute recs and for absent or legacy Details.
// The AZ is read from Details, not RecommendationRecord.AvailabilityZone,
// because pending executions written before issue #792 carry it only there
// and the idempotency key is recomputed from them (issue #792).
func recDetailsAvailabilityZone(r *config.RecommendationRecord) (string, error) {
	if r.Service != string(common.ServiceEC2) && r.Service != string(common.ServiceCompute) {
		return "", nil
	}
	details, err := common.DecodeServiceDetailsFor(r.Service, r.Details)
	if err != nil {
		return "", fmt.Errorf("decode %s details of %s/%s: %w", r.Service, r.Region, r.ResourceType, err)
	}
	if cd, ok := details.(*common.ComputeDetails); ok && cd != nil {
		return strings.TrimSpace(cd.AvailabilityZone), nil
	}
	return "", nil
}

// buildDuplicatePurchaseResponse returns the executePurchase response body for
// a submit that collapsed onto an existing pending execution (#644). It points
// at the original row so the client lands on the same approvable execution
// instead of a freshly-minted duplicate. duplicate=true lets the UI explain
// why no new approval email was sent.
func buildDuplicatePurchaseResponse(ex *config.PurchaseExecution) map[string]any {
	resp := map[string]any{
		"execution_id":         ex.ExecutionID,
		"status":               ex.Status,
		"recommendation_count": len(ex.Recommendations),
		"total_upfront_cost":   ex.TotalUpfrontCost,
		"estimated_savings":    ex.EstimatedSavings,
		"duplicate":            true,
		"message":              "Duplicate submission collapsed onto the existing execution (see status); no new execution or approval request was created.",
	}
	// An approval email only exists for a row still awaiting approval. A row
	// that already moved on (direct execute, approved, completed) never had
	// one, and email_sent=false would make the client report a failure.
	if ex.Status == "pending" || ex.Status == "notified" {
		resp["email_sent"] = ex.NotificationSent != nil
	}
	return resp
}

// newPendingExecution builds a fresh pending PurchaseExecution with a
// crypto/rand-backed approval token. Extracted from executePurchase to keep
// that function under the gocyclo threshold.
//
// Returns the RAW token alongside the execution: the struct's ApprovalToken
// field holds only the SHA-256 hash that gets persisted (issue #103), so the
// caller's synchronous approval email (sendPurchaseApprovalEmail) must take
// the raw value from the second return, not from execution.ApprovalToken.
func newPendingExecution(req *ExecutePurchaseRequest, totalUpfront, totalSavings float64) (*config.PurchaseExecution, string, error) {
	approvalToken, err := common.GenerateApprovalToken()
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate approval token: %w", err)
	}
	tokenExpiresAt := time.Now().Add(config.ApprovalTokenTTL)
	return &config.PurchaseExecution{
		ExecutionID:            uuid.New().String(),
		Status:                 "pending",
		ScheduledDate:          time.Now(),
		Recommendations:        req.Recommendations,
		TotalUpfrontCost:       totalUpfront,
		EstimatedSavings:       totalSavings,
		ApprovalToken:          config.HashApprovalToken(approvalToken),
		ApprovalTokenExpiresAt: &tokenExpiresAt,
		Source:                 common.PurchaseSourceWeb,
		CapacityPercent:        req.CapacityPercent,
	}, approvalToken, nil
}

func (h *Handler) executePurchase(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	execReq, session, paymentAdjustments, err := h.validateExecutePurchaseRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	totalUpfront, totalSavings, err := validateAndTotalRecommendations(execReq.Recommendations)
	if err != nil {
		return nil, err
	}

	// Submit-time idempotency (#644/#643): a double-click or retried POST with
	// an identical actor + scaled rec set + capacity within a short window must
	// resolve to the original pending execution rather than minting a second
	// approvable row (double-spend). The atomic guard lives inside
	// persistExecutionAndSuppressions (per-creator lock + read + INSERT in one tx)
	// which closes the TOCTOU race that a pre-tx read alone cannot prevent.
	creator := resolveCreatorUserID(session)
	creatorID := derefStringOrEmpty(creator)
	idempotencyKey, err := purchaseIdempotencyKey(creatorID, execReq.Recommendations, execReq.CapacityPercent)
	if err != nil {
		return nil, fmt.Errorf("derive purchase idempotency key: %w", err)
	}

	execution, rawApprovalToken, err := newPendingExecution(&execReq, totalUpfront, totalSavings)
	if err != nil {
		return nil, err
	}
	// CreatedByUserID is set after construction so newPendingExecution stays
	// signature-stable. The cancel-own RBAC path in cancelPurchase relies on
	// this stamp to identify the creator on later cancellation; legacy rows
	// pre-dating issue #46 can carry NULL here, so always go through the
	// resolveCreatorUserID helper rather than dereferencing the session.
	execution.CreatedByUserID = creator
	executionID := execution.ExecutionID

	// Load the grace-period config once before entering the tx.
	graceCfg, err := h.gracePeriodConfig(ctx)
	if err != nil {
		return nil, err
	}
	suppressions := config.BuildSuppressions(execReq.Recommendations, executionID, graceCfg, time.Now())
	dupExec, err := h.persistExecutionAndSuppressions(ctx, execution, suppressions, creatorID, idempotencyKey)
	if err != nil {
		return nil, err
	}
	if dupExec != nil {
		logging.Infof("concurrent duplicate purchase submit collapsed to existing execution %s", dupExec.ExecutionID)
		return withPaymentAdjustments(buildDuplicatePurchaseResponse(dupExec), paymentAdjustments), nil
	}

	// Direct-execute path (issue #289): a session with execute-any or
	// execute-own on purchases can request execute_mode="direct" to bypass
	// the approval email and commit the purchase immediately.
	//
	// authorizeSessionExecuteDirect is a hard gate — any check failure
	// returns a 403 rather than falling through to the email path. This
	// prevents a client that sets execute_mode="direct" but only holds the
	// base execute:purchases verb from silently degrading to the email flow.
	if execReq.ExecuteMode == "direct" {
		if err := h.authorizeSessionExecuteDirect(ctx, session, creatorID, execReq.Recommendations); err != nil {
			return nil, err
		}
		return h.directExecutePurchase(ctx, req, execution, session, paymentAdjustments)
	}

	// Send approval email synchronously so the response can surface the
	// actual outcome. The DB write above is the source of truth — email is
	// best-effort and never blocks the response body; the returned
	// email_sent / email_reason fields let the UI tell the user whether they
	// should wait for an inbox or cancel/retry manually. The one exception is
	// a send failure that cannot be recorded on the row: that answers 500
	// (issue #54).
	return h.sendApprovalAndFinalize(ctx, req, execution, rawApprovalToken, execReq.Recommendations, totalUpfront, totalSavings, paymentAdjustments)
}

// gracePeriodConfig loads the global config for BuildSuppressions' grace
// periods, read before any tx opens. A read error is returned and fails the
// purchase before anything is persisted (audit A01-016, issue #111): falling
// back to the default grace period would write suppressions for a provider
// the operator switched off (grace 0) or with the wrong expiry. This matches
// internal/purchase (AttachResolvedRecommendations) and the other config
// reads on these paths.
func (h *Handler) gracePeriodConfig(ctx context.Context) (*config.GlobalConfig, error) {
	g, err := h.config.GetGlobalConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to read grace periods for suppressions: %w", err)
	}
	return g, nil
}

// directExecutePurchase is the direct-execute branch of executePurchase
// (issue #289). It is called after the execution row has been persisted as
// "pending" and after authorizeSessionExecuteDirect has confirmed the session
// holds execute-any or execute-own on purchases.
//
// Steps:
//  1. Delegate to purchase.Manager.DirectExecute, which atomically
//     transitions the row to "approved" (stamping ApprovedBy), stamps the
//     three audit fields (executed_by_user_id, executed_at,
//     pre_approval_skip_reason) only once that claim is won, and then runs the
//     purchase synchronously. A refused or lost direct execute therefore
//     leaves no executed_at on a row that never ran, and failures map through
//     executeFailureError (403 four-eyes, 409 lost claim, 502 after the claim).
//  2. Send the best-effort post-execution notification email (issue #291).
//     This is the only email the direct-execute flow emits -- no approval
//     email precedes it -- so it is the path where the executed-notification
//     matters most.
//  3. Return a "completed" status to the caller, carrying any payment-option
//     coercion notices (paymentAdjustments, from the request validation) so
//     the direct-execute response surfaces them like the approval path does.
//
// The audit invariant is that a non-nil executed_by_user_id always co-occurs
// with a non-nil pre_approval_skip_reason; both are stamped in the same save.
func (h *Handler) directExecutePurchase(ctx context.Context, req *events.LambdaFunctionURLRequest, execution *config.PurchaseExecution, session *Session, paymentAdjustments []PaymentAdjustment) (any, error) {
	t0 := time.Now()
	executionID := execution.ExecutionID
	logging.Infof("purchase[%s]: directExecutePurchase entry (auth=session)", executionID)

	// Human session direct-execute: stamp the session user's UUID onto
	// transitioned_by (FK-safe via validUUIDPtrOrNil) so the audit trail
	// records who flipped the row to "approved".
	//
	// revocationToken is the raw token DirectExecute mints for this
	// approval (issue #103); only the token hash is stored, so this
	// return value is the only place a raw, emailable token exists after
	// the call returns.
	// The direct-execute Constraints ran on the request's recommendations; the
	// funnel re-runs them on the row its claim returns (issue #415).
	final, revocationToken, err := h.purchase.DirectExecute(ctx, executionID, fourEyesActorIdentity(session), validUUIDPtrOrNil(&session.UserID), h.directExecuteClaimCheck(session))
	if err != nil {
		logging.Errorf("purchase[%s]: directExecutePurchase failed after %s: %v",
			executionID, time.Since(t0), err)
		return nil, h.executeFailure(ctx, req, executionID, "direct-executed", final, revocationToken, session.Email, err)
	}

	logging.Infof("purchase[%s]: directExecutePurchase completed in %s", executionID, time.Since(t0))
	// Best-effort post-execution notification email (issue #291). The
	// direct-execute path sends no approval email (the whole point is to skip
	// the approval round-trip), so this is the only notification the recipients
	// receive -- making the executed-notification the most valuable here.
	// Mirrors the approve-path call sites (see approvePurchase and
	// approvePurchaseViaSession); recipient resolution and the nil-notifier
	// guard live inside sendPurchaseExecutedEmail. session.Email is the actor
	// who direct-executed, matching the actor passed to ApproveAndExecute above.
	h.sendPurchaseExecutedEmail(ctx, req, execution, revocationToken, session.Email)
	return withPaymentAdjustments(map[string]any{
		"execution_id":         executionID,
		"status":               "completed",
		"recommendation_count": len(execution.Recommendations),
		"total_upfront_cost":   execution.TotalUpfrontCost,
		"estimated_savings":    execution.EstimatedSavings,
		"direct_execute":       true,
		"message":              "Purchase executed immediately (direct-execute permission).",
	}, paymentAdjustments), nil
}
