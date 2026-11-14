package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// buildListPlansQuery returns the SQL query and args for ListPurchasePlans.
//
// When accountIDs is empty the query returns all plans without an unassigned
// column (false by default for every plan).
//
// When accountIDs is non-empty the query uses a LEFT JOIN so that plans with
// zero rows in plan_accounts ("unassigned" legacy plans) are included
// alongside the matched-account plans. The boolean expression
// (NOT EXISTS (SELECT 1 FROM plan_accounts WHERE plan_id = pp.id)) is
// selected as the "unassigned" column: true for zero-account plans, false
// for every plan that has at least one plan_accounts row. This lets the
// caller bucket the two groups without a separate query.
//
// The WHERE clause retains the original account-filter semantics for
// assigned plans and adds OR NOT EXISTS ... to include unassigned ones only
// when includeUnassigned is set, so an account-scoped caller never receives
// plans no account owns (issue #29). DISTINCT prevents duplicates when a plan
// matches multiple account IDs.
func buildListPlansQuery(accountIDs []string, includeUnassigned bool) (query string, args []any) {
	if len(accountIDs) == 0 {
		return `
			SELECT id, name, enabled, auto_purchase, notification_days_before,
			       services, ramp_schedule, created_at, updated_at,
			       next_execution_date, last_execution_date, last_notification_sent,
			       max_step_commitment,
			       false AS unassigned
			FROM purchase_plans
			ORDER BY created_at DESC
		`, nil
	}
	placeholders := make([]string, len(accountIDs))
	args = make([]any, len(accountIDs))
	for i, id := range accountIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	unassignedClause := ""
	if includeUnassigned {
		unassignedClause = " OR NOT EXISTS (SELECT 1 FROM plan_accounts WHERE plan_id = pp.id)"
	}
	query = fmt.Sprintf(`
		SELECT DISTINCT pp.id, pp.name, pp.enabled, pp.auto_purchase, pp.notification_days_before,
		       pp.services, pp.ramp_schedule, pp.created_at, pp.updated_at,
		       pp.next_execution_date, pp.last_execution_date, pp.last_notification_sent,
		       pp.max_step_commitment,
		       (NOT EXISTS (SELECT 1 FROM plan_accounts WHERE plan_id = pp.id)) AS unassigned
		FROM purchase_plans pp
		LEFT JOIN plan_accounts pa ON pa.plan_id = pp.id
		WHERE pa.account_id IN (%s)%s
		ORDER BY pp.created_at DESC
	`, strings.Join(placeholders, ", "), unassignedClause)
	return query, args
}

// ListPurchasePlans lists purchase plans, optionally filtered by account IDs.
// When filter.AccountIDs is non-empty the result includes plans that
// reference at least one of the given accounts and, with
// filter.IncludeUnassigned, plans with zero plan_accounts rows (flagged with
// Unassigned=true). The no-filter case returns all plans with Unassigned=false.
func (s *PostgresStore) ListPurchasePlans(ctx context.Context, filter PurchasePlanFilter) ([]PurchasePlan, error) {
	query, args := buildListPlansQuery(filter.AccountIDs, filter.IncludeUnassigned)
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list purchase plans: %w", err)
	}
	defer rows.Close()

	plans := make([]PurchasePlan, 0)
	for rows.Next() {
		var plan PurchasePlan
		var servicesJSON, rampScheduleJSON []byte
		var nextExecDate, lastExecDate, lastNotifSent sql.NullTime

		err := rows.Scan(
			&plan.ID,
			&plan.Name,
			&plan.Enabled,
			&plan.AutoPurchase,
			&plan.NotificationDaysBefore,
			&servicesJSON,
			&rampScheduleJSON,
			&plan.CreatedAt,
			&plan.UpdatedAt,
			&nextExecDate,
			&lastExecDate,
			&lastNotifSent,
			&plan.MaxStepCommitment,
			&plan.Unassigned,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan purchase plan: %w", err)
		}

		// Unmarshal JSONB fields
		if err := json.Unmarshal(servicesJSON, &plan.Services); err != nil {
			return nil, fmt.Errorf("failed to unmarshal services: %w", err)
		}

		if err := json.Unmarshal(rampScheduleJSON, &plan.RampSchedule); err != nil {
			return nil, fmt.Errorf("failed to unmarshal ramp_schedule: %w", err)
		}

		// Handle nullable timestamps
		if nextExecDate.Valid {
			plan.NextExecutionDate = &nextExecDate.Time
		}
		if lastExecDate.Valid {
			plan.LastExecutionDate = &lastExecDate.Time
		}
		if lastNotifSent.Valid {
			plan.LastNotificationSent = &lastNotifSent.Time
		}

		plans = append(plans, plan)
	}

	return plans, rows.Err()
}
