package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/google/uuid"
)

func decodePlanEditorPlan(raw json.RawMessage) (*config.PlanScheduleIntent, error) {
	fields, err := planEditorObject(raw, "name enabled auto_purchase notification_days_before max_step_commitment target_accounts services ramp_schedule", "max_step_commitment")
	if err != nil {
		return nil, err
	}
	out := &config.PlanScheduleIntent{}
	if decodeErr := json.Unmarshal(raw, &out.Plan); decodeErr != nil {
		return nil, decodeErr
	}
	out.Plan.CapSupplied = fields["max_step_commitment"] != nil
	if fields["target_accounts"] != nil {
		var accounts []string
		if decodeErr := json.Unmarshal(fields["target_accounts"], &accounts); decodeErr != nil {
			return nil, decodeErr
		}
		accounts, err = planEditorUUIDs(accounts)
		if err != nil {
			return nil, err
		}
		out.AccountIDs = &accounts
	}
	if fields["services"] != nil {
		out.Services, err = decodePlanEditorServices(fields["services"])
		if err != nil {
			return nil, err
		}
	}
	if fields["ramp_schedule"] != nil {
		out.Ramp, err = decodePlanEditorRamp(fields["ramp_schedule"])
	}
	return out, err
}

func decodePlanEditorServices(raw json.RawMessage) (map[string]config.PlanScheduleServicePatch, error) {
	fields, err := planEditorObject(raw, "", "")
	if err != nil {
		return nil, err
	}
	out := make(map[string]config.PlanScheduleServicePatch, len(fields))
	for key, value := range fields {
		patch, patchErr := decodePlanEditorService(value)
		if patchErr != nil {
			return nil, patchErr
		}
		out[key] = patch
	}
	return out, nil
}

func decodePlanEditorService(raw json.RawMessage) (config.PlanScheduleServicePatch, error) {
	var patch config.PlanScheduleServicePatch
	fields, err := planEditorObject(raw, "provider service enabled term payment coverage ramp_schedule include_engines exclude_engines include_regions exclude_regions include_types exclude_types min_count", "include_engines exclude_engines include_regions exclude_regions include_types exclude_types")
	if err != nil {
		return patch, err
	}
	if decodeErr := json.Unmarshal(raw, &patch); decodeErr != nil {
		return patch, decodeErr
	}
	if !planEditorServiceSyntax(patch) {
		return patch, errors.New("unsupported service field")
	}
	for _, field := range []struct {
		name   string
		target **[]string
	}{
		{"include_engines", &patch.IncludeEngines}, {"exclude_engines", &patch.ExcludeEngines},
		{"include_regions", &patch.IncludeRegions}, {"exclude_regions", &patch.ExcludeRegions},
		{"include_types", &patch.IncludeTypes}, {"exclude_types", &patch.ExcludeTypes},
	} {
		if value, supplied := fields[field.name]; supplied {
			values, decodeErr := planEditorFilterValues(value)
			if decodeErr != nil {
				return patch, decodeErr
			}
			*field.target = values
		}
	}
	return patch, nil
}

func decodePlanEditorRamp(raw json.RawMessage) (config.PlanScheduleRampPatch, error) {
	var out config.PlanScheduleRampPatch
	fields, err := planEditorObject(raw, "type percent_per_step step_interval_days start_date", "")
	if err != nil {
		return out, err
	}
	date := fields["start_date"]
	delete(fields, "start_date")
	scalars, err := json.Marshal(fields)
	if err != nil {
		return out, err
	}
	if decodeErr := json.Unmarshal(scalars, &out); decodeErr != nil {
		return out, decodeErr
	}
	if date != nil {
		out.StartDate, err = planEditorDate(date)
	}
	if out.Type != nil && *out.Type != "" && !slices.Contains(config.ValidRampScheduleTypes, *out.Type) {
		return out, errors.New("unsupported ramp type")
	}
	return out, err
}

func planEditorDate(raw json.RawMessage) (*time.Time, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	date, err := time.Parse(time.DateOnly, value)
	if err != nil || date.Format(time.DateOnly) != value {
		return nil, errors.New("invalid UTC calendar date")
	}
	return &date, nil
}

func planEditorObject(raw json.RawMessage, allowed, nullable string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("expected an object")
	}
	out := make(map[string]json.RawMessage)
	for decoder.More() {
		if memberErr := planEditorObjectMember(decoder, out, allowed, nullable); memberErr != nil {
			return nil, memberErr
		}
	}
	if _, err = decoder.Token(); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("expected one editor object")
	}
	return out, nil
}

func planEditorObjectMember(decoder *json.Decoder, out map[string]json.RawMessage, allowed, nullable string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	key, ok := token.(string)
	if !ok {
		return errors.New("expected editor field name")
	}
	_, duplicate := out[key]
	if duplicate || allowed != "" && !slices.Contains(strings.Fields(allowed), key) {
		return errors.New("unknown or duplicate editor field")
	}
	var value json.RawMessage
	if decodeErr := decoder.Decode(&value); decodeErr != nil {
		return decodeErr
	}
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) && !slices.Contains(strings.Fields(nullable), key) {
		return errors.New("null editor field")
	}
	out[key] = value
	return nil
}

func planEditorCanonicalUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func planEditorUUIDs(values []string) ([]string, error) {
	for _, value := range values {
		if !planEditorCanonicalUUID(value) {
			return nil, errors.New("invalid editor UUID")
		}
	}
	slices.Sort(values)
	return slices.Compact(values), nil
}

func planEditorServiceSyntax(patch config.PlanScheduleServicePatch) bool {
	return (patch.Provider == nil || slices.Contains(config.ValidProviders, *patch.Provider)) &&
		(patch.Service == nil || *patch.Service != "") &&
		(patch.Term == nil || slices.Contains([]int{0, 1, 3}, *patch.Term)) && planEditorPaymentSyntax(patch.Payment)
}

func planEditorPaymentSyntax(value *string) bool {
	if value == nil || *value == "" {
		return true
	}
	for _, options := range config.ValidPaymentOptionsByProvider {
		if slices.Contains(options, *value) {
			return true
		}
	}
	return false
}

func planEditorFilterValues(raw json.RawMessage) (*[]string, error) {
	var elements []*string
	if err := json.Unmarshal(raw, &elements); err != nil {
		return nil, err
	}
	var values []string
	if elements != nil {
		values = make([]string, len(elements))
	}
	for i, element := range elements {
		if element == nil {
			return nil, errors.New("null filter element")
		}
		values[i] = *element
	}
	return &values, nil
}
