package api

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// authorizeAWSExchangeExecution applies the gates on the direct AWS execute
// endpoint that follow session permission and scope: the permission
// Constraints, then the 4-eyes policy. It returns the deployment's cloud
// account ID for the idempotency key.
func (h *Handler) authorizeAWSExchangeExecution(ctx context.Context, session *Session, region string, maxRat *big.Rat) (string, error) {
	// Enforce the per-permission Constraints configured on the granting
	// execute:ri-exchange permission (SEC-01, issue #1141). RI exchanges
	// are AWS EC2 only and region-scoped, and operate on the RIs of the
	// deployment's own AWS account, so AccountIDs carries the registered
	// cloud account the running deployment resolves to (fail closed on a
	// resolution error; unattributedAccountConstraint when the deployment
	// maps to no registered account, so an AccountIDs-constrained
	// permission denies). The amount cap is checked against the caller's
	// max_payment_due_usd guardrail, which ExecuteExchange independently
	// enforces against the actual quoted payment due.
	maxPayment, _ := maxRat.Float64()
	cloudAccountID, err := h.resolveReshapeCloudAccountIDOrUnattributed(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to resolve cloud account scope: %w", err)
	}
	err = h.requirePermissionConstraints(ctx, session, "execute", "ri-exchange", []auth.PermissionConstraints{{
		AccountIDs:        []string{cloudAccountID},
		Providers:         []string{string(common.ProviderAWS)},
		Services:          []string{string(common.ServiceEC2)},
		Regions:           []string{region},
		MaxPurchaseAmount: maxPayment,
	}})
	if err != nil {
		return "", err
	}

	err = h.requireNoDirectExchangeUnderFourEyes(ctx)
	if err != nil {
		return "", err
	}
	return cloudAccountID, nil
}

// requireNoDirectExchangeUnderFourEyes refuses the direct execute endpoints
// while 4-eyes mode is on. A direct execute quotes and runs an irreversible
// exchange in one request, so there is no pending record and no second person
// to approve it; the approval flow (pending record + different approver) is the
// only path that satisfies the policy.
func (h *Handler) requireNoDirectExchangeUnderFourEyes(ctx context.Context) error {
	cfg, err := h.config.GetGlobalConfig(ctx)
	if err != nil {
		return fmt.Errorf("4-eyes policy check: failed to load global config: %w", err)
	}
	if cfg != nil && cfg.RequireDifferentApprover {
		return NewClientError(403, "direct RI exchange execution is disabled while 4-eyes approval mode is on; use the approval flow")
	}
	return nil
}

// requireDifferentRIExchangeApprover applies the purchase 4-eyes check when a creator
// is recorded. Every exchange row today is written by the scheduler or the ladder
// engine with no creator (pkg/exchange.ExchangeRecord carries none), so there is
// no human requester to differ from: under 4-eyes such a row stays approvable by
// any identified approver, but never by an anonymous token holder (issue #518).
func (h *Handler) requireDifferentRIExchangeApprover(ctx context.Context, session *Session, record *config.RIExchangeRecord) error {
	if record.CreatedByUserID != nil {
		return h.requireDifferentApprover(ctx, session, record.ID, record.CreatedByUserID, false)
	}
	if session != nil {
		return nil
	}
	cfg, err := h.config.GetGlobalConfig(ctx)
	if err != nil {
		return fmt.Errorf("4-eyes policy check: failed to load global config: %w", err)
	}
	if cfg == nil || !cfg.RequireDifferentApprover {
		return nil
	}
	logging.Warnf("4-eyes[%s]: mode on; system-created exchange approved with no session; denying (fail-closed)", record.ID)
	return NewClientError(500, "4-eyes approval mode is enabled but no session could be resolved; sign in before approving")
}

// getRIExchangeRecord loads an exchange record for the approve and reject
// routes. A missing record is (nil, nil) whether the store reports it as a
// nil record or as a wrapped config.ErrNotFound (PostgresStore), so callers
// answer it as a client error and not a 500 (issue #601). Any other store
// error stays a server error.
func (h *Handler) getRIExchangeRecord(ctx context.Context, id string) (*config.RIExchangeRecord, error) {
	record, err := h.config.GetRIExchangeRecord(ctx, id)
	if errors.Is(err, config.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to look up exchange record: %w", err)
	}
	return record, nil
}

// validateExchangeApproval validates ID, token, and record state for an exchange approval.
func (h *Handler) validateExchangeApproval(ctx context.Context, id, token string) (*config.RIExchangeRecord, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}
	if token == "" {
		return nil, NewClientError(400, "approval token is required")
	}

	record, err := h.getRIExchangeRecord(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := checkExchangeToken(record, token, "approval"); err != nil {
		return nil, err
	}
	return record, nil
}

const (
	riExchangeApproveConflictMsg = "exchange already processed or was canceled by a newer analysis run"
	riExchangeRejectConflictMsg  = "exchange already processed or was canceled"
	riExchangeExpiredMsg         = "exchange has expired"
)

// checkExchangeToken applies the emailed-token status mapping shared with the
// purchase approve, cancel and revoke links (issue #587): a token that does
// not match is 403, and a matching token on an exchange past its expires_at
// is 410. The match is checked first, so only the token holder learns that
// the exchange expired. kind is "approval" or "rejection" and only shapes the
// 403 text.
//
// A missing record (nil) and a record without a token get the same 403 as a
// wrong token (issue #601): the token alone authorizes these routes, so they
// are open to callers with no session, and a distinct answer would tell such
// a caller which exchange ids exist.
//
// record.ApprovalToken is the SHA-256 hex digest stored at rest (issue #103);
// config.ApprovalTokenMatches hashes the supplied token and compares digests
// in constant time, as everywhere else that validates one of these tokens,
// and never matches an empty stored digest.
func checkExchangeToken(record *config.RIExchangeRecord, token, kind string) error {
	if record == nil || !config.ApprovalTokenMatches(record.ApprovalToken, token) {
		return NewClientError(403, "invalid "+kind+" token")
	}
	if record.ExpiresAt != nil && time.Now().After(*record.ExpiresAt) {
		return NewClientError(410, riExchangeExpiredMsg)
	}
	return nil
}
