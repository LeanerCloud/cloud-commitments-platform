package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/jackc/pgx/v5"
)

// errRetryFamilyClaimed rolls back the retry tx when the fan-out's other kind
// (root versus account rows) was already retried. persistRetrySuccessorTx
// translates it into a 409 through retryFamilyClaimedError.
var errRetryFamilyClaimed = errors.New("fan-out retry family already claimed")

// retryFamilyKey is the fan-out key family of an execution: the idempotency key
// up to its first colon (a root key K, or an account row key K:<account>), or
// the execution ID for a legacy row that has no key.
func retryFamilyKey(exec *config.PurchaseExecution) string {
	key := exec.IdempotencyKey
	if key == "" {
		key = exec.ExecutionID
	}
	family, _, _ := strings.Cut(key, ":")
	return family
}

// retryLiftedByNoSubmission reports whether the retry gate let this row through
// only because it is a proven-unsent first attempt: a provider with no
// duplicate guard (EC2, Azure savings plans) whose refusal was lifted. Only
// such a row can buy twice when both the root and an account row are retried.
func retryLiftedByNoSubmission(exec *config.PurchaseExecution) bool {
	return purchase.RedriveRefusalReason(exec) != "" && purchase.RetryRefusalReason(exec) == ""
}

// guardRetryFamily refuses to retry a fan-out root after one of its account
// rows was retried, and the reverse. The per-row link CAS cannot see this: the
// root and each account row are different rows with their own retry link, yet
// the root's successor re-fans out across every account while an account
// row's successor re-runs one of them, so retrying both buys that account
// twice on a provider with no duplicate guard. Same-kind siblings (account A
// and account B) cover different accounts and stay allowed. It takes the
// family lock before reading, so two concurrent retries of the two kinds
// serialize and the second one sees the first one's committed link.
func (h *Handler) guardRetryFamily(ctx context.Context, tx pgx.Tx, failedExec *config.PurchaseExecution) error {
	if failedExec.PlanID == "" || !retryLiftedByNoSubmission(failedExec) {
		return nil
	}
	family := retryFamilyKey(failedExec)
	if err := h.config.LockRetryFamilyTx(ctx, tx, family); err != nil {
		return err
	}
	claimed, err := h.config.FamilyRetryClaimedTx(ctx, tx, family, failedExec.PlanID, failedExec.ExecutionID, failedExec.CloudAccountID == nil)
	if err != nil {
		return err
	}
	if claimed {
		return errRetryFamilyClaimed
	}
	return nil
}

// retryFamilyClaimedError is the 409 for errRetryFamilyClaimed. It names the
// kind that was already retried and is distinct from the "was already
// retried" text so the two refusals can be told apart.
func retryFamilyClaimedError(failedExec *config.PurchaseExecution) error {
	retried := "root"
	if failedExec.CloudAccountID == nil {
		retried = "account rows"
	}
	return NewClientErrorWithDetails(409,
		fmt.Sprintf("execution %s belongs to a fan-out whose %s was already retried; retrying it as well could buy the same commitment twice. Act on the retry successor instead", failedExec.ExecutionID, retried),
		map[string]any{"retry_family_claimed": true})
}
