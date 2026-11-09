package config

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/jackc/pgx/v5"
)

// SaveRIExchangeDirectExecution inserts the audit row for a direct-API RI
// exchange as "submitted", before the provider call (issue #82). The status
// is not taken from the record: every row starts submitted, and created_at is
// stamped by the database clock after the guards ran.
//
// An AWS row is inserted under the same advisory lock as the history
// reservations, after refusing any source RI another exchange still holds
// (ErrRIExchangeSourceInFlight): a processing history row, or a direct row
// that is submitted or unknown (#1829). Azure rows are not guarded.
func (s *PostgresStore) SaveRIExchangeDirectExecution(ctx context.Context, record *RIExchangeDirectExecution) error {
	if err := record.Validate(); err != nil {
		return err
	}
	var sources *string
	if record.Sources != nil {
		raw := string(record.Sources)
		sources = &raw
	}
	// clock_timestamp(), not the transaction's now(): the lock wait must not
	// age the row, because the runbook treats an old submitted row as stale.
	query := `
		INSERT INTO ri_exchange_direct_executions (
			id, provider, cloud_account_id, provider_account_id, region,
			source_ids, sources, targets, max_payment_due, max_payment_currency,
			quoted_net_payable, provider_reference,
			executed_by_user_id, executed_by, executed_by_api_key_id, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9::numeric, $10, $11::numeric, $12, $13, $14, $15, clock_timestamp())
	`
	err := s.WithTx(ctx, func(tx pgx.Tx) error {
		if record.Provider == "aws" {
			if err := lockRIExchangeSpend(ctx, tx); err != nil {
				return err
			}
			if err := refuseRIExchangeSourcesInFlight(ctx, tx, nil, riExchangeHistoryGuardIDs(record.SourceIDs)); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, query,
			record.ID,
			record.Provider,
			record.CloudAccountID,
			record.ProviderAccountID,
			record.Region,
			record.SourceIDs,
			sources,
			string(record.Targets),
			record.MaxPaymentDue,
			record.MaxPaymentCurrency,
			record.QuotedNetPayable,
			nullStringFromString(record.ProviderReference),
			record.ExecutedByUserID,
			record.ExecutedBy,
			record.ExecutedByAPIKeyID,
		)
		return err
	})
	if errors.Is(err, ErrRIExchangeSourceInFlight) {
		return err
	}
	if err != nil {
		return fmt.Errorf("failed to save ri exchange direct execution: %w", err)
	}
	return nil
}

// riExchangeHistoryGuardIDs is the input the direct Save gives the history
// guard, which compares exactly: the trimmed raw ids plus their lower-cased
// forms, de-duplicated in order, so a history row stored in either spelling
// of a lower-case id still matches.
func riExchangeHistoryGuardIDs(sourceIDs []string) []string {
	seen := make(map[string]struct{}, 2*len(sourceIDs))
	out := make([]string, 0, 2*len(sourceIDs))
	for _, id := range sourceIDs {
		id = strings.TrimSpace(id)
		for _, v := range []string{id, strings.ToLower(id)} {
			if _, dup := seen[v]; v == "" || dup {
				continue
			}
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

// FinalizeRIExchangeDirectExecution records the provider outcome on a
// submitted audit row. It is a compare-and-set on status = 'submitted', so a
// row is finalized at most once: a second finalize, concurrent or later,
// updates nothing and returns ErrRIExchangeDirectExecutionNotSubmitted rather
// than overwriting the outcome already recorded.
func (s *PostgresStore) FinalizeRIExchangeDirectExecution(ctx context.Context, id string, outcome RIExchangeDirectOutcome) error {
	if err := outcome.Validate(); err != nil {
		return err
	}
	ready, err := directResolveSchemaReady(ctx, s.db)
	if err != nil {
		// The provider outcome must not be lost over a version read: the plain UPDATE is right on either schema.
		logging.Errorf("ri-exchange direct execute %s: schema version unreadable, finalizing without the resolution check: %v", id, err)
		ready = false
	}
	query := `
		UPDATE ri_exchange_direct_executions
		   SET status               = $2,
		       provider_reference   = COALESCE($3, provider_reference),
		       net_payable          = $4::numeric,
		       net_payable_currency = $5,
		       provider_status      = $6,
		       error                = $7,
		       completed_at         = now()
		 WHERE id = $1
		   AND status = 'submitted'`
	args := []any{
		id,
		outcome.Status,
		nullStringFromString(outcome.ProviderReference),
		outcome.NetPayable,
		nullStringFromString(outcome.NetPayableCurrency),
		nullStringFromString(outcome.ProviderStatus),
		nullStringFromString(outcome.Error),
	}
	if !ready {
		// No resolved_status column yet (or the schema version is unreadable): nothing to check against.
		return s.finalizeDirectPlain(ctx, id, query, args)
	}
	return s.finalizeDirectChecked(ctx, id, query, args, outcome)
}

func (s *PostgresStore) finalizeDirectPlain(ctx context.Context, id, query string, args []any) error {
	tag, err := s.db.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to finalize ri exchange direct execution %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("finalize ri exchange direct execution %s: %w", id, ErrRIExchangeDirectExecutionNotSubmitted)
	}
	return nil
}

func (s *PostgresStore) finalizeDirectChecked(ctx context.Context, id, query string, args []any, outcome RIExchangeDirectOutcome) error {
	var resolved *string
	err := s.db.QueryRow(ctx, query+"\n\t\tRETURNING resolved_status", args...).Scan(&resolved)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("finalize ri exchange direct execution %s: %w", id, ErrRIExchangeDirectExecutionNotSubmitted)
	}
	if err != nil {
		return fmt.Errorf("failed to finalize ri exchange direct execution %s: %w", id, err)
	}
	// A late finalize can outlive an operator's resolution. Both facts are
	// kept; a contradiction is logged because no reader lists resolved rows.
	if resolved != nil && outcome.Status != RIExchangeDirectStatusUnknown && *resolved != outcome.Status {
		logging.Errorf("ri-exchange direct execute %s: provider outcome %q contradicts operator resolution %q", id, outcome.Status, *resolved)
	}
	return nil
}
