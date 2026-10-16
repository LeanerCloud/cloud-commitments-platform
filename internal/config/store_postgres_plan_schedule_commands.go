package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
)

func preparePlanScheduleTargets(before *PlanEditorSnapshot, intent PlanScheduleIntent, plan *PurchasePlan, replaceRoots bool) (services, ramp []byte, err error) {
	services, ramp = []byte(`{}`), []byte(`{}`)
	if before != nil {
		services = slices.Clone(before.servicesJSON)
		ramp = slices.Clone(before.rampScheduleJSON)
	}
	services, err = mergePlanScheduleServices(services, intent.Services, before == nil)
	if err == nil {
		ramp, err = mergePlanScheduleRamp(ramp, intent, before == nil, replaceRoots)
	}
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
	if create && intent.StepCount == nil {
		return nil, fmt.Errorf("%w: create requires step count", ErrPurchasePlanConflict)
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
	fields := map[string]any{}
	if create {
		fields["current_step"] = 0
	}
	if count := intent.StepCount; count != nil {
		fields["total_steps"], fields["percent_per_step"] = *count, 100/float64(*count)
		fields["allocation_mode"] = RampAllocationEqualCount
	}
	if replaceRoots {
		fields["dates_managed"] = true
	}
	return mergePlanScheduleObject(ramp, fields)
}

func invalidPlanScheduleRampCount(captured RampSchedule, intent PlanScheduleIntent) bool {
	count := intent.StepCount
	return intent.Ramp.PercentPerStep != nil && (count != nil || captured.AllocationMode == RampAllocationEqualCount) ||
		count != nil && (*count < max(1, captured.CurrentStep) || *count > MaxTotalSteps)
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
