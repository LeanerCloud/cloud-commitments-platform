package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type PlanScheduleActorKind string

const (
	PlanScheduleSessionActor PlanScheduleActorKind = "session"
	PlanScheduleUserKeyActor PlanScheduleActorKind = "user-api-key"
	PlanScheduleAdminActor   PlanScheduleActorKind = "admin-api-key"
)

type PlanScheduleActor struct {
	Kind          PlanScheduleActorKind
	UserID, KeyID *string
}

type PlanScheduleCommandIdentity struct {
	CommandID, PlanID                     string
	Actor                                 PlanScheduleActor
	ExpectedRevision                      *string
	RequestHash                           string
	ProposedAccountIDs, ProposedProviders []string
}

type PlanScheduleOperationKind string

const (
	PlanScheduleCreatePlan PlanScheduleOperationKind = "create-plan"
	PlanScheduleUpdatePlan PlanScheduleOperationKind = "update-plan"
	PlanScheduleManageRoot PlanScheduleOperationKind = "manage-root"
	PlanScheduleCancelRoot PlanScheduleOperationKind = "cancel-root"
	PlanScheduleRevokeRoot PlanScheduleOperationKind = "revoke-root"
	PlanScheduleArmPlan    PlanScheduleOperationKind = "arm-plan"
)

type PlanScheduleScopeContext struct {
	AccountID, Provider, Service, Region *string
}

type PlanScheduleOperation struct {
	Kind                               PlanScheduleOperationKind
	ExecutionID, CreatorUserID, Status *string
	Contexts                           []PlanScheduleScopeContext
}

type PlanScheduleScope struct {
	AccountIDs []string
	Contexts   []PlanScheduleScopeContext
	Operations []PlanScheduleOperation
}

type PlanScheduleMutation struct {
	Plan       *PurchasePlan
	Intent     PlanScheduleIntent
	AccountIDs []string
	Roots      []PlanScheduleRootChange
	Diff       json.RawMessage
	Scope      PlanScheduleScope
}

type PlanScheduleMapping struct {
	StepNumber                     int
	OldExecutionID, NewExecutionID *string
	ScheduledDate                  *time.Time
}

type PlanScheduleCommandResult struct {
	Status   int
	Body     json.RawMessage
	Replayed bool
}

type BuildAuthorizedPlanScheduleMutation func(context.Context, *PlanEditorSnapshot, []PlanEditorAccount, time.Time) (*PlanScheduleMutation, error)
type AuthorizePlanScheduleReplay func(context.Context, PlanScheduleScope, []PlanEditorAccount) error
type ProjectCommittedPlanSchedule func(context.Context, *PlanEditorSnapshot, []PlanScheduleMapping, json.RawMessage) (json.RawMessage, error)

func lockPlanScheduleIdentityTx(ctx context.Context, tx pgx.Tx, kind, id string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "cudly:plan-schedule:"+kind+id)
	return err
}

func (s *PostgresStore) lockPlanScheduleStateTx(ctx context.Context, tx pgx.Tx, identity PlanScheduleCommandIdentity) (*PlanEditorSnapshot, []PlanEditorAccount, error) {
	providers, err := s.lockPlanScheduleHintsTx(ctx, tx, identity)
	if err != nil {
		return nil, nil, err
	}
	exists, err := lockPlanScheduleRowsTx(ctx, tx, identity)
	if err != nil {
		return nil, nil, err
	}
	accounts, err := s.lockPlanScheduleUnionTx(ctx, tx, identity, exists)
	// Create skips the recheck: cloud_accounts.provider is never updated (UpdateCloudAccount, store_postgres.go:3690).
	if err != nil || !exists {
		return nil, accounts, err
	}
	final, err := s.readPlanEditorSnapshotTx(ctx, tx, identity.PlanID)
	if err != nil {
		return nil, nil, err
	}
	actual, err := planScheduleRoutingHintsTx(ctx, tx, final, identity)
	if err != nil || !planScheduleContainsAll(providers, actual) {
		return nil, nil, ErrPurchasePlanConflict
	}
	return final, accounts, nil
}

func (s *PostgresStore) lockPlanScheduleHintsTx(ctx context.Context, tx pgx.Tx, identity PlanScheduleCommandIdentity) ([]string, error) {
	if err := lockPlanScheduleIdentityTx(ctx, tx, "plan:", identity.PlanID); err != nil {
		return nil, err
	}
	hints, err := s.readPlanEditorSnapshotTx(ctx, tx, identity.PlanID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	providers, err := planScheduleRoutingHintsTx(ctx, tx, hints, identity)
	if err == nil && len(providers) > 0 {
		err = s.LockPlanProvidersTx(ctx, tx, providers)
	}
	return providers, err
}

// lockPlanScheduleRowsTx strongly locks the plan and all its executions; false means the plan is absent (Create only).
func lockPlanScheduleRowsTx(ctx context.Context, tx pgx.Tx, identity PlanScheduleCommandIdentity) (bool, error) {
	err := tx.QueryRow(ctx, `SELECT 1 FROM purchase_plans WHERE id=$1 FOR UPDATE`, identity.PlanID).Scan(new(int))
	switch {
	case errors.Is(err, pgx.ErrNoRows) && identity.ExpectedRevision == nil:
		return false, nil
	case errors.Is(err, pgx.ErrNoRows):
		return false, ErrPurchasePlanConflict
	case err == nil:
		_, err = tx.Exec(ctx, `SELECT execution_id FROM purchase_executions WHERE plan_id=$1 ORDER BY execution_id FOR UPDATE`, identity.PlanID)
	}
	return err == nil, err
}

func (s *PostgresStore) lockPlanScheduleUnionTx(ctx context.Context, tx pgx.Tx, identity PlanScheduleCommandIdentity, exists bool) ([]PlanEditorAccount, error) {
	ids := slices.Clone(identity.ProposedAccountIDs)
	if exists {
		locked, err := s.readPlanEditorSnapshotTx(ctx, tx, identity.PlanID)
		if err != nil {
			return nil, err
		}
		current, err := planEditorAccountIDs(locked)
		if err != nil {
			return nil, err
		}
		ids = append(ids, current...)
	}
	slices.Sort(ids)
	return lockPlanScheduleAccountsTx(ctx, tx, slices.Compact(ids))
}

func planScheduleRoutingHintsTx(ctx context.Context, tx pgx.Tx, snapshot *PlanEditorSnapshot, identity PlanScheduleCommandIdentity) ([]string, error) {
	providers := slices.Clone(identity.ProposedProviders)
	accounts, err := resolvePlanEditorAccountsTx(ctx, tx, identity.ProposedAccountIDs, false)
	if err != nil {
		return nil, err
	}
	if snapshot != nil {
		providers = append(providers, DerivePlanProviders(snapshot.Plan)...)
		for key := range snapshot.Plan.Services {
			providers = append(providers, snapshot.Plan.Services[key].Provider)
		}
		accounts = append(accounts, snapshot.Accounts...)
	}
	for _, account := range accounts {
		providers = append(providers, account.Provider)
	}
	providers = slices.DeleteFunc(providers, func(provider string) bool { return provider == "" })
	slices.Sort(providers)
	return slices.Compact(providers), nil
}

func lockPlanScheduleAccountsTx(ctx context.Context, tx pgx.Tx, ids []string) ([]PlanEditorAccount, error) {
	return resolvePlanEditorAccountsTx(ctx, tx, ids, true)
}

func planScheduleContainsAll(locked, actual []string) bool {
	return !slices.ContainsFunc(actual, func(value string) bool { return !slices.Contains(locked, value) })
}

var planScheduleDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var errInvalidPlanSchedule = fmt.Errorf("%w: invalid command identity or mutation accounts", ErrPurchasePlanConflict)

func validPlanScheduleUUIDs(ids ...string) bool {
	return !slices.ContainsFunc(ids, func(id string) bool { parsed, err := uuid.Parse(id); return err != nil || parsed.String() != id })
}

func validPlanScheduleAccountSet(ids []string) bool {
	return validPlanScheduleUUIDs(ids...) && len(slices.Compact(slices.Sorted(slices.Values(ids)))) == len(ids)
}

// samePlanScheduleSet compares account sets ignoring order; nil equals empty.
func samePlanScheduleSet(a, b []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

func validatePlanScheduleIdentity(identity PlanScheduleCommandIdentity) error {
	if validPlanScheduleUUIDs(identity.CommandID, identity.PlanID) && validPlanScheduleActor(identity.Actor) &&
		planScheduleDigest.MatchString(identity.RequestHash) &&
		(identity.ExpectedRevision == nil || planScheduleDigest.MatchString(*identity.ExpectedRevision)) &&
		validPlanScheduleAccountSet(identity.ProposedAccountIDs) && !slices.ContainsFunc(identity.ProposedProviders, func(provider string) bool { _, err := planStepLockKeys([]string{provider}); return err != nil }) {
		return nil
	}
	return errInvalidPlanSchedule
}

func validPlanScheduleActor(actor PlanScheduleActor) bool {
	want, known := map[PlanScheduleActorKind][2]bool{PlanScheduleSessionActor: {true, false},
		PlanScheduleUserKeyActor: {true, true}, PlanScheduleAdminActor: {false, false}}[actor.Kind]
	return known && want == [2]bool{actor.UserID != nil, actor.KeyID != nil} &&
		(actor.UserID == nil || validPlanScheduleUUIDs(*actor.UserID)) && (actor.KeyID == nil || validPlanScheduleUUIDs(*actor.KeyID))
}

func validatePlanScheduleLockedTarget(accounts []PlanEditorAccount, mutation *PlanScheduleMutation) error {
	ids := make([]string, len(accounts))
	for i := range accounts {
		ids[i] = accounts[i].ID
	}
	if !validPlanScheduleAccountSet(mutation.AccountIDs) {
		return errInvalidPlanSchedule
	}
	if !planScheduleContainsAll(ids, mutation.AccountIDs) || !samePlanScheduleSet(ids, mutation.Scope.AccountIDs) {
		return ErrPurchasePlanConflict
	}
	return nil
}
