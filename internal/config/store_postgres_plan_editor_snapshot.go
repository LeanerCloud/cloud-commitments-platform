package config

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrPlanEditorUnsupportedSize = errors.New("plan editor history exceeds supported size")
	ErrPlanEditorAttribution     = errors.New("plan editor account attribution is unresolved")
)

// PlanEditorSnapshot is internal state for the editor projection, not an HTTP DTO.
type PlanEditorSnapshot struct {
	Plan              *PurchasePlan
	AccountIDs        []string
	Executions        []PurchaseExecution
	Baseline          PlanRampBaseline
	Committed         map[string]PlanRampQuantity
	Accounts          []PlanEditorAccount
	executionVersions []planEditorExecutionVersion
}

// PlanEditorAccount contains resolved provenance, without credentials or grants.
type PlanEditorAccount struct{ ID, Name, Provider string }

// ReadPlanEditorSnapshot resolves retained provenance; the caller must authorize it.
func (s *PostgresStore) ReadPlanEditorSnapshot(ctx context.Context, planID string) (*PlanEditorSnapshot, error) {
	out := &PlanEditorSnapshot{}
	err := s.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY`); err != nil {
			return err
		}
		var err error
		out.Plan, err = scanPurchasePlanRow(tx.QueryRow(ctx, purchasePlanSelectCols+` WHERE id=$1`, planID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT account_id::text FROM plan_accounts WHERE plan_id=$1 ORDER BY account_id`, planID)
		if err != nil {
			return err
		}
		out.AccountIDs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if stateErr := s.readPlanEditorExecutionStateTx(ctx, tx, out); stateErr != nil {
			return stateErr
		}
		ids, err := planEditorAccountIDs(out)
		if err != nil {
			return err
		}
		out.Accounts, err = resolvePlanEditorAccountsTx(ctx, tx, ids)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("read plan editor snapshot: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) readPlanEditorExecutionStateTx(ctx context.Context, tx pgx.Tx, out *PlanEditorSnapshot) error {
	rows, err := tx.Query(ctx, planExecutionSelectCols+` FROM purchase_executions WHERE plan_id=$1 ORDER BY execution_id LIMIT $2`, out.Plan.ID, MaxListLimit+1)
	if err != nil {
		return err
	}
	out.Executions, err = scanExecutionRows(rows)
	rows.Close()
	if err != nil {
		return err
	}
	if len(out.Executions) > MaxListLimit {
		return ErrPlanEditorUnsupportedSize
	}
	out.Baseline, err = s.GetPlanRampBaselineTx(ctx, tx, out.Plan.ID)
	if err != nil {
		return err
	}
	out.Committed, err = sumPlanCommitted(out.Executions)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPlanEditorAttribution, err)
	}
	rows, err = tx.Query(ctx, `SELECT execution_id::text, updated_at FROM purchase_executions WHERE plan_id=$1 ORDER BY execution_id LIMIT $2`, out.Plan.ID, MaxListLimit+1)
	if err != nil {
		return err
	}
	out.executionVersions, err = pgx.CollectRows(rows, pgx.RowToStructByPos[planEditorExecutionVersion])
	return err
}

func resolvePlanEditorAccountsTx(ctx context.Context, tx pgx.Tx, ids []string) ([]PlanEditorAccount, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, name, provider FROM cloud_accounts WHERE id::text=ANY($1) ORDER BY id`, ids)
	if err != nil {
		return nil, err
	}
	accounts, err := pgx.CollectRows(rows, pgx.RowToStructByPos[PlanEditorAccount])
	if err != nil {
		return nil, err
	}
	if len(accounts) != len(ids) {
		return nil, ErrPlanEditorAttribution
	}
	return accounts, nil
}

func planEditorAccountIDs(s *PlanEditorSnapshot) ([]string, error) {
	ids := make(map[string]struct{})
	for _, id := range s.AccountIDs {
		ids[id] = struct{}{}
	}
	for i := range s.Executions {
		accounts, err := planEditorExecutionAccounts(&s.Executions[i], s.AccountIDs)
		if err != nil {
			return nil, err
		}
		for _, id := range accounts {
			ids[id] = struct{}{}
		}
	}
	for _, quantities := range []map[string]PlanRampQuantity{s.Baseline, s.Committed} {
		for key := range quantities {
			id, err := planEditorIdentityAccount(key)
			if err != nil {
				return nil, err
			}
			ids[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	return out, nil
}

func planEditorExecutionAccounts(e *PurchaseExecution, associations []string) ([]string, error) {
	var ids []string
	if e.CloudAccountID != nil {
		ids = append(ids, *e.CloudAccountID)
	}
	if len(e.Recommendations) == 0 && e.CloudAccountID == nil {
		if len(associations) == 0 {
			return nil, ErrPlanEditorAttribution
		}
		return associations, nil
	}
	for i := range e.Recommendations {
		key, err := RecIdentityKey(&e.Recommendations[i])
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrPlanEditorAttribution, err)
		}
		id, err := planEditorIdentityAccount(key)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func planEditorIdentityAccount(key string) (string, error) {
	parts := strings.Split(key, "\x1f")
	if len(parts) != 11 {
		return "", ErrPlanEditorAttribution
	}
	id, err := uuid.Parse(parts[1])
	if err != nil || id.String() != parts[1] {
		return "", ErrPlanEditorAttribution
	}
	return parts[1], nil
}

type planEditorExecutionVersion struct {
	ID        string
	UpdatedAt time.Time
}
