package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func preparePlanScheduleTargets(before *PlanEditorSnapshot, intent PlanScheduleIntent, plan *PurchasePlan, replaceRoots bool) (services, ramp []byte, err error) {
	services, ramp, err = mergePlanScheduleTargets(before, intent, replaceRoots)
	var storedServices map[string]ServiceConfig
	var storedRamp RampSchedule
	if err == nil {
		err = json.Unmarshal(services, &storedServices)
	}
	if err == nil {
		err = json.Unmarshal(ramp, &storedRamp)
	}
	if err != nil {
		return nil, nil, err
	}
	if !reflect.DeepEqual(plan.Services, storedServices) || !reflect.DeepEqual(plan.RampSchedule, storedRamp) {
		return nil, nil, fmt.Errorf("%w: authorized target differs from stored patch", ErrPurchasePlanConflict)
	}
	if before == nil {
		err = plan.Validate()
		if err != nil {
			return nil, nil, err
		}
	}
	return services, ramp, nil
}

// refusePlanScheduleStartedShapeChange: a percent-only reshape must never reach a plan with progress or a baseline (ledger demotion).
func refusePlanScheduleStartedShapeChange(before *PlanEditorSnapshot, intent PlanScheduleIntent) error {
	if before == nil {
		return nil
	}
	var captured RampSchedule
	if err := json.Unmarshal(before.rampScheduleJSON, &captured); err != nil {
		return err
	}
	if RederivesCumulativeSteps(captured, intent) && (captured.CurrentStep > 0 || before.Baseline != nil) {
		return fmt.Errorf("%w: percent per step cannot reshape a plan with progress or a baseline", ErrPurchasePlanConflict)
	}
	return nil
}

func mergePlanScheduleTargets(before *PlanEditorSnapshot, intent PlanScheduleIntent, replaceRoots bool) (services, ramp []byte, err error) {
	services, ramp = []byte(`{}`), []byte(`{}`)
	if before != nil {
		services = slices.Clone(before.servicesJSON)
		ramp = slices.Clone(before.rampScheduleJSON)
	}
	if refuseErr := refusePlanScheduleStartedShapeChange(before, intent); refuseErr != nil {
		return nil, nil, refuseErr
	}
	if services, err = mergePlanScheduleServices(services, intent.Services, before == nil); err != nil {
		return nil, nil, err
	}
	ramp, err = mergePlanScheduleRamp(ramp, intent, before == nil, replaceRoots)
	return services, ramp, err
}

func mergePlanScheduleServices(raw []byte, patches map[string]PlanScheduleServicePatch, create bool) ([]byte, error) {
	if len(patches) == 0 {
		return raw, nil
	}
	var services map[string]json.RawMessage
	if err := json.Unmarshal(raw, &services); err != nil || services == nil {
		return nil, fmt.Errorf("%w: services must be an object", ErrPurchasePlanConflict)
	}
	originalServices := maps.Clone(services)
	for _, key := range slices.Sorted(maps.Keys(patches)) {
		old, exists := originalServices[key]
		targetKey, target, err := mergePlanScheduleService(key, old, patches[key], create)
		if err != nil {
			return nil, err
		}
		_, inOriginal := originalServices[targetKey]
		_, inCurrent := services[targetKey]
		if (targetKey != key || !exists) && (inOriginal || inCurrent) {
			return nil, fmt.Errorf("%w: service identity collision", ErrPurchasePlanConflict)
		}
		delete(services, key)
		services[targetKey] = target
	}
	return marshalPlanScheduleRaw(services)
}

func mergePlanScheduleService(key string, old json.RawMessage, patch PlanScheduleServicePatch, create bool) (patchedKey string, target []byte, err error) {
	if old == nil {
		if !create {
			return "", nil, fmt.Errorf("%w: unknown service patch", ErrPurchasePlanConflict)
		}
		old = json.RawMessage(`{}`)
	}
	var original, service ServiceConfig
	err = json.Unmarshal(old, &original)
	if err != nil {
		return "", nil, err
	}
	target, err = mergePlanScheduleObject(old, patch)
	if err != nil {
		return "", nil, err
	}
	err = json.Unmarshal(target, &service)
	if err != nil {
		return "", nil, err
	}
	if service.Provider != original.Provider || service.Service != original.Service {
		key = service.Provider + "/" + service.Service
	}
	return key, target, nil
}

func mergePlanScheduleRamp(raw []byte, intent PlanScheduleIntent, create, replaceRoots bool) ([]byte, error) {
	if create && intent.StepCount == nil && intent.Ramp.PercentPerStep == nil {
		return nil, fmt.Errorf("%w: create requires a step count or percent per step", ErrPurchasePlanConflict)
	}
	var captured RampSchedule
	if err := json.Unmarshal(raw, &captured); err != nil {
		return nil, err
	}
	if invalidPlanScheduleRampCount(captured, intent) {
		return nil, fmt.Errorf("%w: invalid ramp step count or percent", ErrPurchasePlanConflict)
	}
	ramp, err := mergePlanScheduleObject(raw, intent.Ramp)
	if err != nil {
		return nil, err
	}
	cumulative := RederivesCumulativeSteps(captured, intent)
	fields := planScheduleRampFields(intent, captured, create, replaceRoots)
	merged, err := mergePlanScheduleObject(ramp, fields)
	if err != nil || !cumulative {
		return merged, err
	}
	return dropPlanScheduleAllocationMode(merged)
}

func planScheduleRampFields(intent PlanScheduleIntent, captured RampSchedule, create, replaceRoots bool) map[string]any {
	fields := map[string]any{}
	if create {
		fields["current_step"] = 0
	}
	if count := intent.StepCount; count != nil {
		fields["total_steps"], fields["percent_per_step"] = *count, 100/float64(*count)
		fields["allocation_mode"] = RampAllocationEqualCount
	} else if RederivesCumulativeSteps(captured, intent) {
		fields["total_steps"] = CumulativeStepCount(*intent.Ramp.PercentPerStep)
	}
	if replaceRoots {
		fields["dates_managed"] = true
	}
	return fields
}

func dropPlanScheduleAllocationMode(raw []byte) ([]byte, error) {
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, err
	}
	delete(stored, "allocation_mode")
	return marshalPlanScheduleRaw(stored)
}

// RederivesCumulativeSteps (also used by the API target): a percent-only intent reshapes the ramp unless it equals the stored percent of a cumulative plan.
func RederivesCumulativeSteps(captured RampSchedule, intent PlanScheduleIntent) bool {
	percent := intent.Ramp.PercentPerStep
	return intent.StepCount == nil && percent != nil &&
		(captured.AllocationMode == RampAllocationEqualCount || math.Abs(*percent-captured.PercentPerStep) > 1e-9)
}

func invalidPlanScheduleRampCount(captured RampSchedule, intent PlanScheduleIntent) bool {
	count, percent := intent.StepCount, intent.Ramp.PercentPerStep
	if count == nil && percent != nil {
		if derived := CumulativeStepCount(*percent); derived == 0 || RederivesCumulativeSteps(captured, intent) {
			count = &derived
		}
	} else if percent != nil {
		return true
	}
	return count != nil && (*count < max(1, captured.CurrentStep) || *count > MaxTotalSteps)
}

func mergePlanScheduleObject(raw []byte, patch any) ([]byte, error) {
	updates, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	if string(updates) == "{}" {
		return raw, nil
	}
	var stored, fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &stored); err != nil || stored == nil {
		return nil, fmt.Errorf("%w: stored schedule value must be an object", ErrPurchasePlanConflict)
	}
	err = json.Unmarshal(updates, &fields)
	if err != nil {
		return nil, err
	}
	maps.Copy(stored, fields)
	return marshalPlanScheduleRaw(stored)
}

func marshalPlanScheduleRaw(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(value)
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), err
}

type PlanScheduleRootChange struct {
	OldExecutionID *string
	StepNumber     int
	ScheduledDate  *time.Time
}

type planScheduleWindow struct {
	execution PurchaseExecution
	version   time.Time
}

type planScheduleWindows map[string]planScheduleWindow

func capturePlanScheduleWindows(before *PlanEditorSnapshot) planScheduleWindows {
	windows := planScheduleWindows{}
	if before != nil {
		for i := range before.Executions {
			window := planScheduleWindow{execution: before.Executions[i]}
			if p := window.execution.ScheduledExecutionAt; p != nil {
				fire := *p
				window.execution.ScheduledExecutionAt = &fire
			}
			windows[window.execution.ExecutionID] = window
		}
		for _, v := range before.executionVersions {
			if window, ok := windows[v.ID]; ok {
				windows[v.ID] = planScheduleWindow{window.execution, v.UpdatedAt}
			}
		}
	}
	return windows
}

// cancelPlanScheduleRootsTx cancels each replaced root under its captured updated_at; a lost
// CAS (or an old ID repeated in changes) fails so the caller's transaction rolls back.
func (s *PostgresStore) cancelPlanScheduleRootsTx(ctx context.Context, tx pgx.Tx, windows planScheduleWindows, changes []PlanScheduleRootChange, actor *string) error {
	for _, change := range changes {
		if change.OldExecutionID == nil {
			continue
		}
		old, exists := windows[*change.OldExecutionID]
		if !exists || old.execution.StepNumber != change.StepNumber {
			return ErrPurchasePlanConflict
		}
		//nolint:misspell // clears the legacy British column spelling, see migration 000089
		result, err := tx.Exec(ctx, `UPDATE purchase_executions e SET status='canceled', canceled_by=$5, cancelled_by=NULL,
			approval_token_hash='', approval_token_expires_at=NULL, updated_at=clock_timestamp()
			WHERE e.execution_id=$1 AND e.plan_id=$2 AND e.updated_at=$3 AND e.status=$4
			AND e.cloud_account_id IS NULL
			AND e.retry_attempt_n=0 AND e.retry_execution_id IS NULL AND COALESCE(e.idempotency_key,'') NOT LIKE '%:%'
			AND e.executed_at IS NULL AND e.completed_at IS NULL AND COALESCE(e.pre_approval_skip_reason,'')=''
			AND NOT EXISTS (SELECT 1 FROM purchase_executions r WHERE r.retry_execution_id=e.execution_id)
			AND e.scheduled_date>clock_timestamp() AND ((e.status='scheduled' AND e.scheduled_execution_at>clock_timestamp())
			 OR (e.status IN ('pending','notified') AND e.scheduled_execution_at IS NULL))`,
			old.execution.ExecutionID, old.execution.PlanID, old.version, old.execution.Status, actor)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrPurchasePlanConflict
		}
		err = s.DeleteSuppressionsByExecutionTx(ctx, tx, old.execution.ExecutionID)
		if err != nil {
			return err
		}
	}
	return nil
}

// insertPlanScheduleRootsTx inserts a pending root per dated change and returns the new
// execution IDs aligned with changes (nil where a change only retires a root).
func insertPlanScheduleRootsTx(ctx context.Context, tx pgx.Tx, planID string, changes []PlanScheduleRootChange, creator *string) ([]*string, error) {
	ids := make([]*string, len(changes))
	for i, change := range changes {
		if change.ScheduledDate == nil {
			continue
		}
		id := uuid.NewString()
		if change.StepNumber < 1 {
			return nil, ErrPurchasePlanConflict
		}
		if _, err := tx.Exec(ctx, `INSERT INTO purchase_executions
			(plan_id, execution_id, status, step_number, scheduled_date, approval_token_hash,
			 recommendations, capacity_percent, created_by_user_id, idempotency_key)
			VALUES ($1,$2,'pending',$3,$4,'','null'::jsonb,100,$5,$6)`,
			planID, id, change.StepNumber, *change.ScheduledDate, creator, uuid.NewString()); err != nil {
			return nil, err
		}
		ids[i] = &id
	}
	return ids, nil
}

// guardPlanScheduleClockTx re-checks every window against the database clock just before
// commit, so a slow command cannot commit a date that has gone stale meanwhile.
func guardPlanScheduleClockTx(ctx context.Context, tx pgx.Tx, windows planScheduleWindows, notice int, changes []PlanScheduleRootChange) error {
	var newDates, oldDates, fireTimes []time.Time
	for _, change := range changes {
		if change.ScheduledDate != nil {
			newDates = append(newDates, *change.ScheduledDate)
		}
		if change.OldExecutionID == nil {
			continue
		}
		old := windows[*change.OldExecutionID].execution
		oldDates = append(oldDates, old.ScheduledDate)
		if old.ScheduledExecutionAt != nil {
			fireTimes = append(fireTimes, *old.ScheduledExecutionAt)
		}
	}
	if len(newDates) > 0 && (notice < 0 || notice > MaxNotificationDaysBefore) {
		return ErrPurchasePlanConflict
	}
	var valid bool
	err := tx.QueryRow(ctx, `WITH final AS MATERIALIZED (SELECT clock_timestamp() AS instant)
		SELECT NOT EXISTS (SELECT 1 FROM unnest($1::timestamptz[]) AS d(value), final
		 WHERE value<=instant+($2::int*86400)*INTERVAL '1 second')
		AND NOT EXISTS (SELECT 1 FROM unnest($3::timestamptz[]) AS d(value), final WHERE value<=instant)
		AND NOT EXISTS (SELECT 1 FROM unnest($4::timestamptz[]) AS d(value), final WHERE value<=instant)`,
		newDates, notice, oldDates, fireTimes).Scan(&valid)
	if err == nil && !valid {
		err = ErrPurchasePlanConflict
	}
	return err
}

// persistPlanScheduleMutationTx writes one command's plan, accounts and roots. An existing plan is updated after
// its replaced roots are canceled and before the new roots exist, so the ramp ledger guard sees no retained
// capacity-holding root and an unstarted shape edit stays 'new' instead of degrading to 'legacy'.
func (s *PostgresStore) persistPlanScheduleMutationTx(ctx context.Context, tx pgx.Tx, before *PlanEditorSnapshot, windows planScheduleWindows, mutation *PlanScheduleMutation, actor *string, now time.Time) ([]PlanScheduleMapping, error) {
	services, ramp, err := preparePlanScheduleTargets(before, mutation.Intent, mutation.Plan, len(mutation.Roots) > 0)
	if err != nil {
		return nil, err
	}
	if before == nil {
		mutation.Plan.CreatedAt, mutation.Plan.UpdatedAt = now, now
		err = s.insertPurchasePlanJSON(ctx, tx, mutation.Plan, services, ramp)
		if err != nil {
			return nil, err
		}
	}
	err = s.persistPlanScheduleAccountsTx(ctx, tx, before, mutation)
	if err != nil {
		return nil, err
	}
	err = s.cancelAndUpdatePlanScheduleTx(ctx, tx, before, windows, mutation, actor, services, ramp)
	if err != nil {
		return nil, err
	}
	mapping, err := s.insertPlanScheduleMappingTx(ctx, tx, mutation, actor)
	if err != nil || (before != nil && len(mutation.Roots) == 0) {
		return mapping, err
	}
	return mapping, s.setPlanScheduleNextDateTx(ctx, tx, mutation.Plan)
}

func (s *PostgresStore) cancelAndUpdatePlanScheduleTx(ctx context.Context, tx pgx.Tx, before *PlanEditorSnapshot, windows planScheduleWindows, mutation *PlanScheduleMutation, actor *string, services, ramp []byte) error {
	err := s.cancelPlanScheduleRootsTx(ctx, tx, windows, mutation.Roots, actor)
	if err != nil || before == nil {
		return err
	}
	return s.updatePurchasePlanJSONTx(ctx, tx, mutation.Plan, services, ramp)
}

func (s *PostgresStore) setPlanScheduleNextDateTx(ctx context.Context, tx pgx.Tx, plan *PurchasePlan) error {
	next, err := s.planScheduleNextDateTx(ctx, tx, plan)
	if err != nil {
		return err
	}
	plan.NextExecutionDate = next
	_, err = tx.Exec(ctx, `UPDATE purchase_plans SET next_execution_date=$2 WHERE id=$1`, plan.ID, next)
	return err
}

func (s *PostgresStore) insertPlanScheduleMappingTx(ctx context.Context, tx pgx.Tx, mutation *PlanScheduleMutation, actor *string) ([]PlanScheduleMapping, error) {
	ids, err := insertPlanScheduleRootsTx(ctx, tx, mutation.Plan.ID, mutation.Roots, actor)
	if err != nil {
		return nil, err
	}
	mapping := make([]PlanScheduleMapping, len(ids))
	for i, change := range mutation.Roots {
		mapping[i] = PlanScheduleMapping{StepNumber: change.StepNumber, OldExecutionID: change.OldExecutionID, NewExecutionID: ids[i], ScheduledDate: change.ScheduledDate}
	}
	return mapping, nil
}

func (s *PostgresStore) persistPlanScheduleAccountsTx(ctx context.Context, tx pgx.Tx, before *PlanEditorSnapshot, mutation *PlanScheduleMutation) error {
	if before == nil || mutation.Intent.AccountIDs != nil {
		return s.setPlanAccountsTx(ctx, tx, mutation.Plan.ID, mutation.AccountIDs)
	}
	if !samePlanScheduleSet(before.AccountIDs, mutation.AccountIDs) {
		return ErrPurchasePlanConflict
	}
	return nil
}

// readPlanScheduleReceiptTx returns nil when the command has no receipt. A receipt for a different principal, plan, hash or base revision conflicts without revealing its result.
func readPlanScheduleReceiptTx(ctx context.Context, tx pgx.Tx, identity PlanScheduleCommandIdentity, authorize AuthorizePlanScheduleReplay) (*PlanScheduleCommandResult, error) {
	var matches bool
	var status int
	var body, evidence []byte
	err := tx.QueryRow(ctx, `SELECT plan_id=$2::uuid AND actor_kind=$3
		AND actor_user_id IS NOT DISTINCT FROM $4::uuid
		AND actor_key_id IS NOT DISTINCT FROM $5::uuid AND request_hash=$6
		AND base_revision IS NOT DISTINCT FROM $7::text,
		status_code, result_json::text, scope_evidence
		FROM plan_schedule_command_receipts WHERE command_id=$1 FOR SHARE`,
		identity.CommandID, identity.PlanID, identity.Actor.Kind, identity.Actor.UserID,
		identity.Actor.KeyID, identity.RequestHash, identity.ExpectedRevision).Scan(&matches, &status, &body, &evidence)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !matches {
		return nil, ErrPurchasePlanConflict
	}
	var scope PlanScheduleScope
	err = json.Unmarshal(evidence, &scope)
	if err != nil {
		return nil, err
	}
	accounts, err := lockPlanScheduleAccountsTx(ctx, tx, scope.AccountIDs)
	if err != nil {
		return nil, err
	}
	err = authorize(ctx, scope, accounts)
	if err != nil {
		return nil, err
	}
	return &PlanScheduleCommandResult{Status: status, Body: body, Replayed: true}, nil
}

func insertPlanScheduleReceiptTx(ctx context.Context, tx pgx.Tx, identity PlanScheduleCommandIdentity, after *PlanEditorSnapshot, scope PlanScheduleScope, body json.RawMessage) (*PlanScheduleCommandResult, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return nil, fmt.Errorf("invalid committed command response")
	}
	revision, err := after.Revision()
	if err != nil {
		return nil, err
	}
	evidence, err := json.Marshal(scope)
	if err != nil {
		return nil, err
	}
	status := 200
	if identity.ExpectedRevision == nil {
		status = 201
	}
	_, err = tx.Exec(ctx, `INSERT INTO plan_schedule_command_receipts
		(command_id,plan_id,actor_kind,actor_user_id,actor_key_id,request_hash,
		 base_revision,result_revision,status_code,result_json,scope_evidence)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::json,$11::jsonb)`,
		identity.CommandID, identity.PlanID, identity.Actor.Kind, identity.Actor.UserID,
		identity.Actor.KeyID, identity.RequestHash, identity.ExpectedRevision, revision, status, body, evidence)
	if err != nil {
		return nil, err
	}
	return &PlanScheduleCommandResult{Status: status, Body: slices.Clone(body)}, nil
}
