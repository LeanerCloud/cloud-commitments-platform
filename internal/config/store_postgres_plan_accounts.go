package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ==========================================
// PLAN ACCOUNTS
// ==========================================

// SetPlanAccounts replaces the full account list for a plan atomically.
func (s *PostgresStore) SetPlanAccounts(ctx context.Context, planID string, accountIDs []string) error {
	return s.SetPlanAccountsGated(ctx, planID, accountIDs, nil)
}

// SetPlanAccountsGated is SetPlanAccounts with gate (may be nil) run while the
// plan row is locked FOR UPDATE, before any write. Plan writers that decide on
// the plan's accounts (the auto-purchase arming gate) lock the same row, so the
// gate's decision cannot be overtaken by a concurrent arm or retarget. The plan
// is locked before the account rows the inserts touch, the order
// DeleteCloudAccount uses.
func (s *PostgresStore) SetPlanAccountsGated(ctx context.Context, planID string, accountIDs []string, gate PlanWriteGate) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer rollbackTx(ctx, tx, "SetPlanAccounts")

	managed, err := lockPlanManagedTx(ctx, tx, planID)
	if err != nil {
		return err
	}
	if gate != nil {
		locked, lockErr := s.LockPurchasePlanTx(ctx, tx, planID)
		if lockErr != nil {
			return lockErr
		}
		if locked == nil {
			return fmt.Errorf("%w: plan %s", ErrNotFound, planID)
		}
		if gateErr := s.runPlanGateTx(ctx, tx, locked, gate); gateErr != nil {
			return gateErr
		}
	}
	if guardErr := requireManagedAccountsTx(ctx, tx, planID, accountIDs, managed); guardErr != nil {
		return guardErr
	}
	if setErr := s.setPlanAccountsTx(ctx, tx, planID, accountIDs); setErr != nil {
		return setErr
	}

	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit plan accounts: %w", err)
	}
	return nil
}

func (s *PostgresStore) validatePlanAccountProvidersTx(ctx context.Context, tx pgx.Tx, planID string, accountIDs []string) error {
	services, err := s.getPlanServicesForShareTx(ctx, tx, planID)
	if err != nil {
		return err
	}
	if len(accountIDs) == 0 {
		return nil
	}
	expected, err := DerivePlanProviders(&PurchasePlan{Services: services})
	if err != nil {
		return fmt.Errorf("validate plan account providers: %w", err)
	}
	if len(expected) == 0 {
		return nil
	}

	mismatches, err := s.findPlanAccountProviderMismatchesTx(ctx, tx, accountIDs, expected)
	if err != nil {
		return err
	}
	if len(mismatches) == 0 {
		return nil
	}

	parts := make([]string, len(mismatches))
	for i, mismatch := range mismatches {
		parts[i] = fmt.Sprintf("account %q has provider=%q, expected one of %v",
			mismatch.Name, mismatch.Provider, expected)
	}
	return fmt.Errorf("%w: %s", ErrPlanProviderMismatch, strings.Join(parts, "; "))
}

func (s *PostgresStore) getPlanServicesForShareTx(ctx context.Context, tx pgx.Tx, planID string) (map[string]ServiceConfig, error) {
	var servicesJSON []byte
	if err := tx.QueryRow(ctx, `
		SELECT services
		FROM purchase_plans
		WHERE id = $1
		FOR SHARE
	`, planID).Scan(&servicesJSON); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: plan %s", ErrNotFound, planID)
		}
		return nil, fmt.Errorf("failed to get plan services: %w", err)
	}

	services := make(map[string]ServiceConfig)
	if err := json.Unmarshal(servicesJSON, &services); err != nil {
		return nil, fmt.Errorf("failed to decode plan services: %w", err)
	}
	return services, nil
}

type planAccountProviderMismatch struct {
	Name     string
	Provider string
}

func (s *PostgresStore) findPlanAccountProviderMismatchesTx(ctx context.Context, tx pgx.Tx, accountIDs, expected []string) ([]planAccountProviderMismatch, error) {
	expectedSet := make(map[string]struct{}, len(expected))
	for _, provider := range expected {
		expectedSet[provider] = struct{}{}
	}

	var mismatches []planAccountProviderMismatch
	for _, accountID := range accountIDs {
		var name, provider string
		if err := tx.QueryRow(ctx, `
			SELECT name, provider
			FROM cloud_accounts
			WHERE id = $1
			FOR SHARE
		`, accountID).Scan(&name, &provider); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("%w: account %s", ErrNotFound, accountID)
			}
			return nil, fmt.Errorf("failed to get account %s: %w", accountID, err)
		}
		if _, ok := expectedSet[provider]; !ok {
			mismatches = append(mismatches, planAccountProviderMismatch{Name: name, Provider: provider})
		}
	}
	return mismatches, nil
}
