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
