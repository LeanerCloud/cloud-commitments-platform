package config

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/jackc/pgx/v5"
)

// DeleteCloudAccount deletes a cloud account. Cascades to credentials and overrides.
// Terminal purchase executions are kept as history with cloud_account_id set to
// NULL; a live execution still blocks the delete through the FK (issue #265).
// If an approved account_registrations row points at this account, it is reset to
// 'pending' in the same transaction so the admin can re-approve through the normal
// flow instead of being left with a dead-end "Approved (account pending link)" row.
// Enabled plans whose only target is this account are disabled in the same
// transaction (issue #264); see disableSoleAccountPlansTx.
func (s *PostgresStore) DeleteCloudAccount(ctx context.Context, id string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer rollbackTx(ctx, tx, "DeleteCloudAccount")

	if lockErr := lockAccountForDeleteTx(ctx, tx, id); lockErr != nil {
		return lockErr
	}

	// Reset any linked approved registration first (explicit NULL so we don't
	// rely on the FK's ON DELETE SET NULL behavior).
	if _, err = tx.Exec(ctx, `
		UPDATE account_registrations
		   SET status           = 'pending',
		       reviewed_by      = NULL,
		       reviewed_at      = NULL,
		       cloud_account_id = NULL
		 WHERE cloud_account_id = $1
		   AND status           = 'approved'
	`, id); err != nil {
		return fmt.Errorf("failed to reset linked registration: %w", err)
	}

	// Detach the account's terminal executions so they survive as history
	// (issue #265). Migration 000053's ON DELETE RESTRICT FK otherwise blocks
	// the delete for every referencing row whatever its status. Live rows are
	// left attached, so the DELETE below still fails on the FK (SQLSTATE
	// 23503) while any remain, and this UPDATE rolls back with it. Migration
	// 000128 keeps updated_at unchanged on these rows: detaching is not a
	// lifecycle event, and the plan health score and the retention sweep both
	// read updated_at as one.
	if _, err = tx.Exec(ctx, `
		UPDATE purchase_executions
		   SET cloud_account_id = NULL
		 WHERE cloud_account_id = $1
		   AND status = ANY($2)
	`, id, TerminalExecutionStatuses); err != nil {
		return fmt.Errorf("failed to detach terminal executions: %w", err)
	}

	disabled, err := disableSoleAccountPlansTx(ctx, tx, id)
	if err != nil {
		return err
	}

	tag, err := tx.Exec(ctx, `DELETE FROM cloud_accounts WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete cloud account: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("cloud account not found: %s", id)
	}

	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit cloud account deletion: %w", err)
	}
	if len(disabled) > 0 {
		logging.Infof("deleting cloud account %s disabled %d plan(s) left with no target accounts: %v", id, len(disabled), disabled)
	}
	return nil
}

// lockAccountForDeleteTx takes DeleteCloudAccount's locks (issues #264,
// #1072) in this order: the plans that target the account, then the account
// row, then any plan that gained the account meanwhile. The plan locks
// serialize the delete with a concurrent delete of another of the plan's
// accounts and with SetPlanAccounts (which locks the plan FOR UPDATE), so the
// plan disable decides on committed plan_accounts rows. The account lock waits
// for a writer still attaching the account to a plan (it holds KEY SHARE on
// the row through the plan_accounts FK until it commits); the second plan lock
// then takes the plans it attached.
func lockAccountForDeleteTx(ctx context.Context, tx pgx.Tx, id string) error {
	if err := lockAccountPlansTx(ctx, tx, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM cloud_accounts WHERE id = $1 FOR UPDATE`, id); err != nil {
		return fmt.Errorf("failed to lock cloud account: %w", err)
	}
	return lockAccountPlansTx(ctx, tx, id)
}

// lockAccountPlansTx locks, in id order, every plan with a plan_accounts row
// for accountID. A fixed order keeps two account deletes that share plans from
// deadlocking on each other.
func lockAccountPlansTx(ctx context.Context, tx pgx.Tx, accountID string) error {
	if _, err := tx.Exec(ctx, `
		SELECT p.id FROM purchase_plans p
		 WHERE p.id IN (SELECT pa.plan_id FROM plan_accounts pa WHERE pa.account_id = $1)
		 ORDER BY p.id
		   FOR UPDATE OF p
	`, accountID); err != nil {
		return fmt.Errorf("failed to lock plans of cloud account: %w", err)
	}
	return nil
}

// disableSoleAccountPlansTx disables every enabled plan whose only
// plan_accounts row is accountID, and returns their IDs (issue #264). The
// account delete cascades those rows away, and a plan with no accounts is the
// unscoped "universal plan" shape migration 000060 removed: the scheduler
// would keep creating executions for it with no account to buy in. Disabling
// rather than refusing the delete keeps the plan and its history for the
// admin to re-target and re-enable. Plans already disabled are not rewritten,
// so their updated_at (the plan edit token) does not move. The caller holds
// the plan rows locked (lockAccountPlansTx), so writers that lock the plan
// before changing its plan_accounts rows (SetPlanAccounts, another account
// delete) cannot change them under this statement's decision.
func disableSoleAccountPlansTx(ctx context.Context, tx pgx.Tx, accountID string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		UPDATE purchase_plans p
		   SET enabled = false
		 WHERE p.enabled
		   AND EXISTS (SELECT 1 FROM plan_accounts pa WHERE pa.plan_id = p.id AND pa.account_id = $1)
		   AND NOT EXISTS (SELECT 1 FROM plan_accounts pa WHERE pa.plan_id = p.id AND pa.account_id <> $1)
		RETURNING p.id::text
	`, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to disable plans targeting only this account: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("failed to disable plans targeting only this account: %w", err)
	}
	return ids, nil
}
