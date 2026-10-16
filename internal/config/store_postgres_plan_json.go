package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) purchasePlanJSONForUpdateTx(ctx context.Context, tx pgx.Tx, next *PurchasePlan) (previous *PurchasePlan, mergedServices, mergedRamp []byte, mergeErr error) {
	old, err := s.LockPurchasePlanTx(ctx, tx, next.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	if old == nil {
		return nil, nil, nil, ErrNotFound
	}
	var services, ramp json.RawMessage
	if readErr := tx.QueryRow(ctx, `SELECT services,ramp_schedule FROM purchase_plans WHERE id=$1`, next.ID).Scan(&services, &ramp); readErr != nil {
		return nil, nil, nil, readErr
	}
	services, err = mergePlanServicesJSON(services, old.Services, next.Services)
	if err != nil {
		return nil, nil, nil, err
	}
	oldRamp, err := json.Marshal(old.RampSchedule)
	if err != nil {
		return nil, nil, nil, err
	}
	nextRamp, err := json.Marshal(next.RampSchedule)
	if err != nil {
		return nil, nil, nil, err
	}
	ramp, err = mergePlanJSONFields(ramp, oldRamp, nextRamp)
	if err != nil {
		return nil, nil, nil, err
	}
	return old, services, ramp, nil
}

func mergePlanServicesJSON(raw []byte, old, next map[string]ServiceConfig) ([]byte, error) {
	if len(old) == 0 && len(next) == 0 {
		return raw, nil
	}
	var objects map[string]json.RawMessage
	if err := json.Unmarshal(raw, &objects); err != nil {
		return nil, err
	}
	if objects == nil {
		objects = make(map[string]json.RawMessage)
	}
	for key := range old {
		if _, ok := next[key]; ok {
			continue
		}
		delete(objects, key)
	}
	for key := range next {
		svc := next[key]
		previous, exists := old[key]
		updated, err := mergePlanServiceJSON(objects[key], previous, svc, exists)
		if err != nil {
			return nil, err
		}
		objects[key] = updated
	}
	return json.Marshal(objects)
}

func mergePlanServiceJSON(raw []byte, old, next ServiceConfig, exists bool) ([]byte, error) {
	updated, err := marshalPlanService(next)
	if err != nil || !exists {
		return updated, err
	}
	previous, err := marshalPlanService(old)
	if err != nil {
		return nil, err
	}
	return mergePlanJSONFields(raw, previous, updated)
}

func marshalPlanService(svc ServiceConfig) ([]byte, error) {
	return json.Marshal(struct {
		ServiceConfig
		IncludeEngines []string `json:"include_engines"`
		ExcludeEngines []string `json:"exclude_engines"`
		IncludeRegions []string `json:"include_regions"`
		ExcludeRegions []string `json:"exclude_regions"`
		IncludeTypes   []string `json:"include_types"`
		ExcludeTypes   []string `json:"exclude_types"`
	}{svc, svc.IncludeEngines, svc.ExcludeEngines, svc.IncludeRegions, svc.ExcludeRegions, svc.IncludeTypes, svc.ExcludeTypes})
}

func mergePlanJSONFields(raw, old, next []byte) ([]byte, error) {
	if bytes.Equal(old, next) {
		return raw, nil
	}
	var original, before, after map[string]json.RawMessage
	for _, entry := range []struct {
		data   []byte
		target *map[string]json.RawMessage
	}{{raw, &original}, {old, &before}, {next, &after}} {
		if err := json.Unmarshal(entry.data, entry.target); err != nil {
			return nil, fmt.Errorf("decode stored plan fields: %w", err)
		}
	}
	if original == nil {
		original = make(map[string]json.RawMessage)
	}
	for key, value := range before {
		if replacement, ok := after[key]; !ok {
			delete(original, key)
		} else if !bytes.Equal(value, replacement) {
			original[key] = replacement
		}
	}
	for key, value := range after {
		if _, exists := before[key]; !exists {
			original[key] = value
		}
	}
	return json.Marshal(original)
}
