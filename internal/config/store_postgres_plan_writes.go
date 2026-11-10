package config

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) createPurchasePlan(ctx context.Context, executor globalConfigExecutor, plan *PurchasePlan) error {
	if plan.ID == "" {
		plan.ID = uuid.New().String()
	}

	now := time.Now()
	plan.CreatedAt = now
	plan.UpdatedAt = now

	servicesJSON, err := marshalPlanServices(plan.Services)
	if err != nil {
		return err
	}

	rampScheduleJSON, err := json.Marshal(plan.RampSchedule)
	if err != nil {
		return fmt.Errorf("failed to marshal ramp_schedule: %w", err)
	}

	query := `
		INSERT INTO purchase_plans (
			id, name, enabled, auto_purchase, notification_days_before,
			services, ramp_schedule, created_at, updated_at,
			next_execution_date, last_execution_date, last_notification_sent,
			max_step_commitment, ramp_ledger_state
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'new')
		RETURNING updated_at
	`

	var updatedAt time.Time
	err = executor.QueryRow(ctx, query,
		plan.ID,
		plan.Name,
		plan.Enabled,
		plan.AutoPurchase,
		plan.NotificationDaysBefore,
		servicesJSON,
		rampScheduleJSON,
		plan.CreatedAt,
		plan.UpdatedAt,
		plan.NextExecutionDate,
		plan.LastExecutionDate,
		plan.LastNotificationSent,
		plan.MaxStepCommitment,
	).Scan(&updatedAt)

	if err != nil {
		return fmt.Errorf("failed to create purchase plan: %w", err)
	}
	plan.UpdatedAt = updatedAt
	return nil
}

// planNameLockPrefix namespaces the advisory lock that serializes creates of
// the same plan name (same cudly: scheme as serviceConfigLockPrefix).
const planNameLockPrefix = "cudly:purchase-plan-name:"

// CreatePurchasePlanWithAccounts inserts plan and attaches accountIDs in one
// transaction, refusing the create when a plan the caller can see already has
// the same name (issue #1734).
//
// A name matches when lower(btrim(name, E' \t\r\n')) agrees, so a trailing tab or newline does not
// make a different name. The key is computed by the
// database once and used for both the advisory lock and the match, so Postgres
// alone defines the equivalence: an exact repeat always matches, whatever the
// database ctype. Case folding beyond ASCII follows that ctype. There is no
// unique index because plans in disjoint account scopes may share a name.
//
// Concurrent creates of one name queue on the lock. The plan and its accounts
// commit together, so a queued create always sees an account-bearing row.
//
// visible is called in the transaction with the accounts of each existing plan
// of that name, oldest first, and must be pure: it decides whether the caller
// may know about that plan. The first visible plan is reported as
// *PlanNameTakenError (errors.Is ErrPlanNameTaken). A match the caller cannot
// see is ignored and never disclosed.
func (s *PostgresStore) CreatePurchasePlanWithAccounts(ctx context.Context, plan *PurchasePlan, accountIDs []string, visible func(accounts []CloudAccount) bool) error {
	return s.WithTx(ctx, func(tx pgx.Tx) error {
		var key string
		if err := tx.QueryRow(ctx, `SELECT lower(btrim($1::text, E' \t\r\n'))`, plan.Name).Scan(&key); err != nil {
			return fmt.Errorf("failed to normalise plan name: %w", err)
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, planNameLockPrefix+key); err != nil {
			return fmt.Errorf("failed to acquire plan name lock: %w", err)
		}
		same, err := listPlansNamedTx(ctx, tx, key)
		if err != nil {
			return err
		}
		for _, existing := range same {
			accounts, err := getPlanAccountsFrom(ctx, tx, existing.ExistingID)
			if err != nil {
				return err
			}
			if visible(accounts) {
				return &existing
			}
		}
		if err := s.createPurchasePlan(ctx, tx, plan); err != nil {
			return err
		}
		return s.setPlanAccountsTx(ctx, tx, plan.ID, accountIDs)
	})
}

// listPlansNamedTx returns the plans whose normalised name is key, oldest
// first so the reported duplicate is deterministic.
func listPlansNamedTx(ctx context.Context, tx pgx.Tx, key string) ([]PlanNameTakenError, error) {
	rows, err := tx.Query(ctx, `SELECT id, name FROM purchase_plans WHERE lower(btrim(name, E' \t\r\n')) = $1 ORDER BY created_at, id`, key)
	if err != nil {
		return nil, fmt.Errorf("failed to look up plans by name: %w", err)
	}
	defer rows.Close()
	var out []PlanNameTakenError
	for rows.Next() {
		var p PlanNameTakenError
		if err := rows.Scan(&p.ExistingID, &p.ExistingName); err != nil {
			return nil, fmt.Errorf("failed to scan plan name: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PostgresStore) setPlanAccountsTx(ctx context.Context, tx pgx.Tx, planID string, accountIDs []string) error {
	if err := s.validatePlanAccountProvidersTx(ctx, tx, planID, accountIDs); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM plan_accounts WHERE plan_id = $1`, planID); err != nil {
		return fmt.Errorf("failed to clear plan accounts: %w", err)
	}
	for _, accountID := range accountIDs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO plan_accounts (plan_id, account_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			planID, accountID,
		); err != nil {
			return fmt.Errorf("failed to insert plan account: %w", err)
		}
	}
	return nil
}

// marshalPlanServices encodes a plan's services map for the JSONB column
// without the read-only service_configs stamps (issue #8). A plan's services
// map is a copy of settings, not a service_configs row, so a created_at or
// updated_at on an entry would be a caller-supplied value the database never
// wrote; it is dropped instead of stored. BoundCount is response-only and
// dropped the same way.
func marshalPlanServices(services map[string]ServiceConfig) ([]byte, error) {
	var stripped map[string]ServiceConfig
	if services != nil {
		stripped = make(map[string]ServiceConfig, len(services))
		for key := range services {
			svc := services[key]
			svc.CreatedAt = nil
			svc.UpdatedAt = nil
			svc.BoundCount = 0
			stripped[key] = svc
		}
	}
	servicesJSON, err := json.Marshal(stripped)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal services: %w", err)
	}
	return servicesJSON, nil
}
